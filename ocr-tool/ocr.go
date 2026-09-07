package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// OcrLine 单行识别结果，携带文本、坐标框与置信度（供前端画框）。
type OcrLine struct {
	Text  string `json:"text"`
	Box   [4]int `json:"box"` // [x1, y1, x2, y2]，原图像素坐标
	Score float32 `json:"score"`
}

// OcrResult 完整的 OCR 识别结果（对前端友好）。
type OcrResult struct {
	Text      string    `json:"text"`
	Lines     []OcrLine `json:"lines"`
	Engine    string    `json:"engine"`
	Width     int       `json:"width"`
	Height    int       `json:"height"`
	LineCount int       `json:"lineCount"`
	ElapsedMs int64     `json:"elapsedMs"`
	LogPath   string    `json:"logPath,omitempty"`
}

// ocrTask 异步识别任务：ocr-image 提交后返回 taskId，前端用 ocr-task 轮询结果。
// 这样单次 plugin.execute 不会超过 20s 超时（识别本身可能耗时数秒）。
type ocrTask struct {
	mu       sync.Mutex
	state    string // pending | done | error
	phase    string // preparing | loading-engine | detecting | recognizing
	progress int
	total    int
	result   OcrResult
	errMsg   string
	created  time.Time // 用于 LRU 回收
}

const (
	taskStatePending = "pending"
	taskStateDone    = "done"
	taskStateError   = "error"
)

const (
	// 任务超出此数时按 created 升序淘汰最旧的，避免 tasks map 无限增长。
	maxOcrTasks = 32
)

var (
	tasks   = map[string]*ocrTask{}
	taskMu  sync.Mutex
	taskSeq int64
)

func handleOcrCommand(id int64, cmd string, input map[string]interface{}) {
	switch cmd {
	case "ocr-status":
		ocrStatus(id)
	case "ocr-prepare":
		ocrPrepare(id)
	case "ocr-image":
		ocrImage(id, input)
	case "ocr-task":
		ocrTaskPoll(id, input)
	default:
		respondError(id, -32601, "unknown ocr command: "+cmd)
	}
}

func ocrStatus(id int64) {
	ready := allAssetsPresent(modelsDir())
	dl.mu.Lock()
	download := map[string]interface{}{
		"active":  dl.active,
		"current": dl.current,
		"total":   dl.total,
		"files":   dl.files,
		"errMsg":  dl.errMsg,
	}
	dl.mu.Unlock()

	respond(id, map[string]interface{}{
		"ready":        ready,
		"engineLoaded": eng.loaded,
		"modelsDir":    modelsDir(),
		"downloading":  isDownloading(),
		"download":     download,
	})
}

func ocrPrepare(id int64) {
	if allAssetsPresent(modelsDir()) {
		respond(id, map[string]interface{}{"accepted": true, "alreadyReady": true, "modelsDir": modelsDir()})
		return
	}
	startDownload()
	respond(id, map[string]interface{}{"accepted": true, "alreadyReady": false, "modelsDir": modelsDir()})
}

// ocrImage 解码图片（base64 或路径）后提交异步识别任务，返回 taskId。
// 未就绪时返回明确错误码，引导用户先下载模型。
//
// 注意：handleExecute 已经在 main.go 里把 input.text 自动解包到 input 顶层，
// 此处不需要再次解包，避免外层 path/data 被覆盖的歧义。
func ocrImage(id int64, input map[string]interface{}) {
	if !allAssetsPresent(modelsDir()) {
		respondError(id, -32001, "模型未就绪：请先在界面点击「准备模型」下载 PaddleOCR 权重（约 178MB）")
		return
	}

	var raw []byte
	if s, ok := input["data"].(string); ok && s != "" {
		if idx := strings.Index(s, ","); idx >= 0 && strings.HasPrefix(s, "data:") {
			s = s[idx+1:]
		}
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
		if err != nil {
			respondError(id, -1, "base64 解码失败: "+err.Error())
			return
		}
		raw = decoded
	} else if p, ok := input["path"].(string); ok && p != "" {
		data, err := os.ReadFile(p)
		if err != nil {
			respondError(id, -1, "读取图片失败: "+err.Error())
			return
		}
		raw = data
	} else {
		respondError(id, -1, "需要 data(base64) 或 path(图片路径) 参数")
		return
	}

	if len(raw) == 0 {
		respondError(id, -1, "图片内容为空")
		return
	}

	ext := detectImageExt(raw)
	tmp, err := os.CreateTemp("", "qd-ocr-*"+ext)
	if err != nil {
		respondError(id, -1, "创建临时文件失败: "+err.Error())
		return
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		respondError(id, -1, "写入临时文件失败: "+err.Error())
		return
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		respondError(id, -1, "关闭临时文件失败: "+err.Error())
		return
	}

	taskID := newTaskID()
	t := &ocrTask{
		state:   taskStatePending,
		phase:   "preparing",
		created: time.Now(),
	}
	taskMu.Lock()
	tasks[taskID] = t
	taskMu.Unlock()

	go runOcrTask(t, tmpPath)

	respond(id, map[string]interface{}{"taskId": taskID, "accepted": true})
}

// runOcrTask 后台执行识别，更新任务状态供轮询。
// 用 defer-recover 兜住 engine panic：标记 task 为 error 让前端停止轮询，
// 同时确保 tmp 文件一定被清理。
func runOcrTask(t *ocrTask, imgPath string) {
	defer os.Remove(imgPath)
	defer func() {
		if r := recover(); r != nil {
			msg := fmt.Sprintf("OCR panic: %v", r)
			t.mu.Lock()
			t.state = taskStateError
			t.errMsg = msg
			t.mu.Unlock()
			logf("runOcrTask: %s", msg)
			hostLog("error", "%s", msg)
		}
	}()

	start := time.Now()
	t.setPhase("loading-engine")

	res, err := eng.recognize(imgPath)
	elapsed := time.Since(start).Milliseconds()

	t.mu.Lock()
	if err != nil {
		t.state = taskStateError
		t.errMsg = err.Error()
		t.mu.Unlock()
		// 同时记录到本地日志与宿主日志，方便用户两种方式都能查到
		logf("runOcrTask: OCR 失败 path=%s err=%v", imgPath, err)
		hostLog("error", "OCR 失败: %v", err)
		return
	}
	res.ElapsedMs = elapsed
	res.LogPath = backendLogPath
	t.result = res
	t.state = taskStateDone
	t.mu.Unlock()
	logf("runOcrTask: OCR 完成 path=%s 行数=%d 耗时=%dms", imgPath, res.LineCount, elapsed)
	hostLog("info", "OCR 完成 行数=%d 耗时=%dms", res.LineCount, elapsed)
}

func ocrTaskPoll(id int64, input map[string]interface{}) {
	tid, _ := input["taskId"].(string)
	if tid == "" {
		respondError(id, -1, "缺少 taskId")
		return
	}
	taskMu.Lock()
	t := tasks[tid]
	taskMu.Unlock()
	if t == nil {
		respondError(id, -1, "任务不存在或已过期")
		return
	}

	t.mu.Lock()
	resp := map[string]interface{}{
		"taskId":   tid,
		"state":    t.state,
		"phase":    t.phase,
		"progress": t.progress,
		"total":    t.total,
	}
	if t.state == taskStateDone {
		resp["result"] = t.result
	}
	if t.state == taskStateError {
		resp["error"] = t.errMsg
	}
	t.mu.Unlock()
	respond(id, resp)
}

// newTaskID 分配新 taskId（自增 + ns 后缀），任务 map 容量超出 LRU 时淘汰最旧。
func newTaskID() string {
	taskMu.Lock()
	defer taskMu.Unlock()
	taskSeq++
	id := fmt.Sprintf("t%d-%d", taskSeq, time.Now().UnixNano())
	evictOldTasksLocked()
	return id
}

func evictOldTasksLocked() {
	if len(tasks) <= maxOcrTasks {
		return
	}
	type kv struct {
		id string
		ts time.Time
	}
	all := make([]kv, 0, len(tasks))
	for id, t := range tasks {
		all = append(all, kv{id: id, ts: t.created})
	}
	// 从最旧开始淘汰，保留 maxOcrTasks 个
	sort.Slice(all, func(i, j int) bool { return all[i].ts.Before(all[j].ts) })
	toEvict := len(all) - maxOcrTasks
	for i := 0; i < toEvict; i++ {
		delete(tasks, all[i].id)
	}
	logf("evictOldTasksLocked: 淘汰 %d 个旧任务", toEvict)
}

// detectImageExt 根据文件头判断图片格式，决定临时文件扩展名。
func detectImageExt(b []byte) string {
	if len(b) >= 4 && b[0] == 0x89 && b[1] == 0x50 && b[2] == 0x4E && b[3] == 0x47 {
		return ".png"
	}
	if len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF {
		return ".jpg"
	}
	if len(b) >= 3 && b[0] == 0x47 && b[1] == 0x49 && b[2] == 0x46 { // GIF
		return ".gif"
	}
	if len(b) >= 2 && b[0] == 0x42 && b[1] == 0x4D { // BMP
		return ".bmp"
	}
	return ".png"
}

func (t *ocrTask) setPhase(p string) {
	t.mu.Lock()
	t.phase = p
	t.mu.Unlock()
}
