// Duplicate File Finder - 按内容哈希查找重复文件（原生 JSON-RPC 子进程）
// 命令：
//   scan    input {path, hidden?}  异步：递归扫描，按 size 预分组再算 sha256，返回重复分组
//   delete  input {remove:[path]}  删除指定冗余副本（保留其一）
//   task-status 轮询扫描进度
package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type executeParams struct {
	Command string                 `json:"command"`
	Input   map[string]interface{} `json:"input"`
}

func strFrom(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func strSliceFrom(m map[string]interface{}, key string) []string {
	if v, ok := m[key].([]interface{}); ok {
		out := []string{}
		for _, x := range v {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func respond(id int64, result interface{}) {
	out, _ := json.Marshal(map[string]interface{}{"jsonrpc": "2.0", "id": id, "result": result})
	fmt.Println(string(out))
}

func respondError(id int64, code int, msg string) {
	out, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0", "id": id,
		"error": map[string]interface{}{"code": code, "message": msg},
	})
	fmt.Println(string(out))
}

// ---- 异步任务 ----

type asyncTask struct {
	ID       string                 `json:"id"`
	Status   string                 `json:"status"`
	Result   map[string]interface{} `json:"result,omitempty"`
	Error    string                 `json:"error,omitempty"`
	Progress map[string]interface{} `json:"progress,omitempty"`
	finished time.Time
}

var (
	tasksMu sync.Mutex
	tasks   = make(map[string]*asyncTask)
	taskSeq int64
)

func startTask() *asyncTask {
	tasksMu.Lock()
	defer tasksMu.Unlock()
	taskSeq++
	t := &asyncTask{ID: fmt.Sprintf("df-%d", taskSeq), Status: "running"}
	tasks[t.ID] = t
	return t
}

func getTask(id string) (*asyncTask, bool) {
	tasksMu.Lock()
	defer tasksMu.Unlock()
	t, ok := tasks[id]
	return t, ok
}

func finishTask(t *asyncTask, result map[string]interface{}, err error) {
	tasksMu.Lock()
	defer tasksMu.Unlock()
	t.finished = time.Now()
	if err != nil {
		t.Status = "error"
		t.Error = err.Error()
	} else {
		t.Status = "done"
		t.Result = result
	}
}

func updateProgress(t *asyncTask, p map[string]interface{}) {
	tasksMu.Lock()
	t.Progress = p
	tasksMu.Unlock()
}

func handleTaskStatus(id int64, input map[string]interface{}) {
	taskID := strFrom(input, "taskId")
	t, ok := getTask(taskID)
	if !ok {
		respond(id, map[string]interface{}{"status": "missing", "taskId": taskID})
		return
	}
	resp := map[string]interface{}{"status": t.Status, "taskId": t.ID}
	if t.Error != "" {
		resp["error"] = t.Error
	}
	if t.Progress != nil {
		resp["progress"] = t.Progress
	}
	if t.Result != nil {
		resp["result"] = t.Result
	}
	respond(id, resp)
}

// ---- 扫描逻辑 ----

// workerCount 计算并行度：CPU 核数，钳制在 [2,12]，兼顾大目录遍历与磁盘 IO 队列深度。
func workerCount() int {
	n := runtime.GOMAXPROCS(0)
	if n < 2 {
		n = 2
	}
	if n > 12 {
		n = 12
	}
	return n
}

// fileHash 计算文件 SHA-256；1MB 拷贝缓冲减少大文件系统调用次数。
func fileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, 1<<20)
	if _, err := io.CopyBuffer(h, f, buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// scanProgress 扫描进度快照（并发安全，供前端轮询展示）。
// phase: 0=遍历目录, 1=计算哈希；其余为各阶段计数。
type scanProgress struct {
	phase   int32
	files   int64
	dirs    int64
	hashed  int64
	toHash  int64
	current atomic.Value // 当前正在遍历的目录
}

func (sp *scanProgress) snapshot() map[string]interface{} {
	m := map[string]interface{}{
		"phase": atomic.LoadInt32(&sp.phase),
		"files": atomic.LoadInt64(&sp.files),
		"dirs":  atomic.LoadInt64(&sp.dirs),
	}
	if atomic.LoadInt32(&sp.phase) == 1 {
		m["hashed"] = atomic.LoadInt64(&sp.hashed)
		m["toHash"] = atomic.LoadInt64(&sp.toHash)
	}
	if c, ok := sp.current.Load().(string); ok && c != "" {
		m["current"] = c
	}
	return m
}

// scanDuplicates 并发扫描重复文件：
//  1) 并行目录树遍历（worker 池 + pending 计数），按文件大小分组（跳过 0 字节与符号链接）
//  2) 仅对 size 相同组内文件并行计算 sha256，按 hash 再分组
//  3) 保留 >1 的重复组，按大小倒序
//  sp 接收进度计数（文件/目录/已哈希数、当前目录、阶段），供前端实时展示。
func scanDuplicates(root string, includeHidden bool, sp *scanProgress) []map[string]interface{} {
	sizeMap := map[int64][]string{}
	var mu sync.Mutex
	workers := workerCount()
	atomic.StoreInt32(&sp.phase, 0)

	// ---- 1) 并行目录遍历 ----
	dirs := make(chan string, 4096)
	var pending sync.WaitGroup
	pending.Add(1)
	go func() { dirs <- root }()

	for i := 0; i < workers; i++ {
		go func() {
		for dir := range dirs {
			sp.current.Store(dir)
			atomic.AddInt64(&sp.dirs, 1)
			entries, err := os.ReadDir(dir)
				if err == nil {
					for _, d := range entries {
						name := d.Name()
						if !includeHidden && strings.HasPrefix(name, ".") {
							continue
						}
						child := filepath.Join(dir, name)
						if d.IsDir() {
							// 独立 goroutine 发送子目录，避免队列满时阻塞把 pending 计数卡死
							pending.Add(1)
							go func(c string) { dirs <- c }(child)
							continue
						}
						if d.Type()&os.ModeSymlink != 0 {
							continue // 跳过符号链接，防循环引用
						}
						fi, e := d.Info()
						if e != nil || fi.Size() == 0 {
							continue
						}
						mu.Lock()
						sizeMap[fi.Size()] = append(sizeMap[fi.Size()], child)
						mu.Unlock()
						atomic.AddInt64(&sp.files, 1)
					}
				}
				pending.Done()
			}
		}()
	}
	pending.Wait()
	close(dirs)

	// 进入哈希阶段前记录待哈希总数（前端的百分比分母）
	var toHash int64
	for _, files := range sizeMap {
		if len(files) >= 2 {
			toHash += int64(len(files))
		}
	}
	atomic.StoreInt64(&sp.toHash, toHash)
	atomic.StoreInt32(&sp.phase, 1)

	// ---- 2) 并行哈希（仅 size ≥2 的组）----
	hashGroups := map[string][]string{}
	var hmu sync.Mutex
	jobs := make(chan string, 1024)
	var hwg sync.WaitGroup
	for i := 0; i < workers; i++ {
		hwg.Add(1)
		go func() {
			defer hwg.Done()
			for f := range jobs {
				h, e := fileHash(f)
				if e != nil {
					continue
				}
				atomic.AddInt64(&sp.hashed, 1)
				hmu.Lock()
				hashGroups[h] = append(hashGroups[h], f)
				hmu.Unlock()
			}
		}()
	}
	for _, files := range sizeMap {
		if len(files) >= 2 {
			for _, f := range files {
				jobs <- f
			}
		}
	}
	close(jobs)
	hwg.Wait()

	// ---- 3) 仅保留重复组（>1 个文件）----
	groups := []map[string]interface{}{}
	for h, files := range hashGroups {
		if len(files) < 2 {
			continue
		}
		sort.Strings(files)
		var sz int64
		if fi, e := os.Stat(files[0]); e == nil {
			sz = fi.Size()
		}
		groups = append(groups, map[string]interface{}{
			"hash":  h,
			"size":  sz,
			"count": len(files),
			"files": files,
		})
	}
	sort.Slice(groups, func(i, j int) bool {
		return groups[i]["size"].(int64) > groups[j]["size"].(int64)
	})
	return groups
}

func handleScan(id int64, input map[string]interface{}) {
	path := strings.TrimSpace(strFrom(input, "path"))
	if path == "" {
		respondError(id, -32602, "请选择要扫描的目录")
		return
	}
	if fi, err := os.Stat(path); err != nil || !fi.IsDir() {
		respondError(id, -32602, "目录不存在或不是文件夹")
		return
	}
	includeHidden := false
	if v, ok := input["hidden"].(bool); ok {
		includeHidden = v
	}
	t := startTask()
	sp := &scanProgress{}

	// 进度上报：每 150ms 把当前计数写进任务，前端轮询时展示，避免界面"一动不动"
	done := make(chan struct{})
	go func() {
		tick := time.NewTicker(150 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-done:
				updateProgress(t, sp.snapshot())
				return
			case <-tick.C:
				updateProgress(t, sp.snapshot())
			}
		}
	}()

	go func() {
		groups := scanDuplicates(path, includeHidden, sp)
		// 先把结果落库（status=done），再通知进度 ticker 退出，
		// 保证前端轮询能立即读到完成态，避免 100% 后再空等一个轮询周期。
		totalFiles := 0
		wasted := int64(0)
		for _, g := range groups {
			files := g["files"].([]string)
			totalFiles += len(files)
			wasted += g["size"].(int64) * int64(len(files)-1)
		}
		finishTask(t, map[string]interface{}{
			"groups":     groups,
			"groupCount": len(groups),
			"totalFiles": totalFiles,
			"wasted":     wasted,
		}, nil)
		close(done)
	}()
	respond(id, map[string]interface{}{"async": true, "taskId": t.ID})
}

func handleDelete(id int64, input map[string]interface{}) {
	remove := strSliceFrom(input, "remove")
	if len(remove) == 0 {
		respondError(id, -32602, "未指定要删除的文件")
		return
	}
	var deleted, skipped int
	var failed []string
	for _, p := range remove {
		if err := os.Remove(p); err != nil {
			skipped++
			failed = append(failed, p)
		} else {
			deleted++
		}
	}
	respond(id, map[string]interface{}{
		"deleted": deleted,
		"skipped": skipped,
		"failed":  failed,
	})
}

func dispatch(raw string) {
	var req rpcRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		respondError(0, -32700, "parse error: "+err.Error())
		return
	}
	switch req.Method {
	case "initialize":
		respond(req.ID, map[string]interface{}{"status": "ready", "name": "QuickDock Dup Finder"})
	case "host.ping":
		respond(req.ID, map[string]interface{}{"pong": true})
	case "plugin.execute":
		var params executeParams
		if len(req.Params) > 0 {
			_ = json.Unmarshal(req.Params, &params)
		}
		switch strings.ToLower(strings.TrimSpace(params.Command)) {
		case "scan":
			handleScan(req.ID, params.Input)
		case "delete":
			handleDelete(req.ID, params.Input)
		case "task-status":
			handleTaskStatus(req.ID, params.Input)
		default:
			respondError(req.ID, -32601, "unknown command: "+params.Command)
		}
	default:
		respondError(req.ID, -32601, "unknown method: "+req.Method)
	}
}

func main() {
	reader := bufio.NewReader(os.Stdin)
	var wg sync.WaitGroup
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		data := strings.TrimSpace(line)
		if data == "" {
			continue
		}
		wg.Add(1)
		go func(raw string) {
			defer wg.Done()
			dispatch(raw)
		}(data)
	}
	wg.Wait()
}
