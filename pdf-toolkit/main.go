// PDF Toolkit - PDF 处理工具箱
// JSON-RPC 2.0 over stdin/stdout
//
// 命令：
//   merge       合并多个 PDF
//   split       拆分 PDF
//   compress    压缩 PDF
//   watermark   添加水印
//   extract-img 提取图片
//   info        获取 PDF 信息

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

// ---- JSON-RPC 结构 ----

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type executeParams struct {
	Command string                 `json:"command"`
	Input   map[string]interface{} `json:"input"`
}

// ---- 运行时 ----

var (
	writeMu sync.Mutex
	stdout  = bufio.NewWriter(os.Stdout)
	gOS     string
)

func init() {
	gOS = runtime.GOOS
}

// ---- 后台任务注册表 ----
// 长任务（merge/split/compress/watermark/extract-img）必须异步执行：
// 宿主对 plugin.execute 有 20s 超时、ping 5s，同步跑 pdfcpu 大文件会超时被标记 unresponsive。
// 方案：handler 校验入参后立即返回 {async:true, taskId}，goroutine 后台执行，前端轮询 task-status。

type pdfTask struct {
	ID       string                 `json:"id"`
	Status   string                 `json:"status"` // running | done | error
	Message  string                 `json:"message,omitempty"`
	Result   map[string]interface{} `json:"result,omitempty"`
	Error    string                 `json:"error,omitempty"`
	finished time.Time
}

var (
	tasksMu sync.Mutex
	tasks   = make(map[string]*pdfTask)
	taskSeq int64
)

const taskTTL = 30 * time.Minute

// startPDFTask 注册一个新任务并返回；调用方随后启动 goroutine 执行，
// 执行完必须调用 finishPDFTask 收尾。
func startPDFTask() *pdfTask {
	tasksMu.Lock()
	defer tasksMu.Unlock()
	// 顺手清理已结束且过期的任务，避免注册表无限增长
	now := time.Now()
	for id, t := range tasks {
		if t.Status != "running" && now.Sub(t.finished) > taskTTL {
			delete(tasks, id)
		}
	}
	taskSeq++
	t := &pdfTask{ID: fmt.Sprintf("pdf-%d", taskSeq), Status: "running"}
	tasks[t.ID] = t
	return t
}

func getPDFTask(id string) (*pdfTask, bool) {
	tasksMu.Lock()
	defer tasksMu.Unlock()
	t, ok := tasks[id]
	return t, ok
}

// finishPDFTask 结束任务：err 非 nil 记 error，否则记结果。
func finishPDFTask(t *pdfTask, result map[string]interface{}, err error) {
	tasksMu.Lock()
	defer tasksMu.Unlock()
	t.finished = time.Now()
	if err != nil {
		t.Status = "error"
		t.Error = err.Error()
		t.Message = "处理失败"
	} else {
		t.Status = "done"
		t.Result = result
	}
}

func exeSuffix() string {
	if gOS == "windows" {
		return ".exe"
	}
	return ""
}

// ---- 诊断日志（v0.1.7）----
// 写入插件目录下 pdf-toolkit.log（打开失败静默降级，不干扰主流程）。
// 用途：宿主弹「连续 N 次无响应」时，本文件可还原插件侧时间轴——
// 收到哪些请求、ping 是否处理、哪个 handler 慢、Flush 是否卡、有无 panic。
var (
	diagLogFile *os.File
	diagMu      sync.Mutex
)

func diagLogf(format string, args ...interface{}) {
	diagMu.Lock()
	defer diagMu.Unlock()
	if diagLogFile == nil {
		return
	}
	fmt.Fprintf(diagLogFile, "%s %s\n", time.Now().Format("2006-01-02 15:04:05.000"), fmt.Sprintf(format, args...))
	_ = diagLogFile.Sync()
}

func setupDiagLog() {
	f, err := os.OpenFile(filepath.Join(filepath.Dir(os.Args[0]), "pdf-toolkit.log"),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return // 只读目录/权限问题：静默降级
	}
	diagLogFile = f
}

// dispatchWithDiag 带诊断的 dispatch：记录收包、panic（防崩溃+留证）、慢处理
func dispatchWithDiag(raw string) {
	defer func() {
		if r := recover(); r != nil {
			diagLogf("PANIC %v\n%s", r, debug.Stack())
		}
	}()
	method := ""
	var probe struct {
		Method string `json:"method"`
	}
	if len(raw) > 0 && raw[0] == '{' {
		if err := json.Unmarshal([]byte(raw), &probe); err == nil && probe.Method != "" {
			method = probe.Method
		}
	}
	diagLogf("recv method=%s len=%d", method, len(raw))
	start := time.Now()
	dispatch(raw)
	if el := time.Since(start); el > 3*time.Second {
		diagLogf("SLOW method=%s 耗时 %.2fs", method, el.Seconds())
	} else if method == "host.ping" && el > 500*time.Millisecond {
		// ping 都慢？说明 respond 链路上有停顿（写锁/Flush 阻塞）
		diagLogf("PING 偏慢 %.2fs", el.Seconds())
	}
}

func main() {
	setupDiagLog()
	diagLogf("进程启动 pid=%d", os.Getpid())
	defer diagLogf("进程退出")
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
		// 每个请求独立 goroutine：任何 handler（含同步慢操作：info 最长 15s、剪贴板
		// PowerShell、打开目录）都不阻塞 stdin 读循环，host.ping 永远秒回——
		// 杜绝「同步 handler 卡住 → 健康检查连续 3 轮 ping 超时 → 标记 unresponsive
		// → 第 6 轮强杀重启」的整类死法（2026-08-24 v0.1.6 结构根治）。
		// EOF（宿主停止/升级杀进程）后 wg.Wait() 等在途响应写完再退出，
		// 避免"插件不回话"的表象——此前为规避该表象改成同步 dispatch，
		// 反而引入任意 handler 卡死即被误杀的风险。
		wg.Add(1)
		go func(raw string) {
			defer wg.Done()
			dispatchWithDiag(raw)
		}(data)
	}
	wg.Wait()
}

func dispatch(data string) {
	var req rpcRequest
	if err := json.Unmarshal([]byte(data), &req); err != nil {
		respondError(0, -32700, "parse error: "+err.Error())
		return
	}
	handleRequest(req)
}

func handleRequest(req rpcRequest) {
	switch req.Method {
	case "initialize":
		respond(req.ID, map[string]interface{}{"status": "ready", "name": "QuickDock PDF Toolkit"})
	case "host.ping":
		// 协议统一为 host.ping（宿主 manager.pingOne 固定发送此方法）。
		// 若版本混杂导致回 unknown method，每轮 ping 都 -32601 → 90s 后被误标 unresponsive。
		diagLogf("ping id=%d", req.ID)
		respond(req.ID, map[string]interface{}{"pong": true})
	case "plugin.execute":
		handleExecute(req)
	default:
		respondError(req.ID, -32601, "unknown method: "+req.Method)
	}
}

func handleExecute(req rpcRequest) {
	var params executeParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			respondError(req.ID, -32602, "invalid params: "+err.Error())
			return
		}
	}

	cmd := strings.ToLower(strings.TrimSpace(params.Command))
	switch cmd {
	case "merge", "合并":
		handleMerge(req.ID, params.Input)
	case "split", "拆分":
		handleSplit(req.ID, params.Input)
	case "compress", "压缩":
		handleCompress(req.ID, params.Input)
	case "watermark", "水印":
		handleWatermark(req.ID, params.Input)
	case "extract-img", "extract_images", "提取图片":
		handleExtractImages(req.ID, params.Input)
	case "info", "信息":
		handleInfo(req.ID, params.Input)
	case "clipboard", "复制":
		handleClipboard(req.ID, params.Input)
	case "open-folder", "打开目录":
		handleOpenFolder(req.ID, params.Input)
	case "task-status", "任务状态":
		handleTaskStatus(req.ID, params.Input)
	default:
		respondError(req.ID, -32601, "unknown command: "+params.Command)
	}
}

// ---- 命令处理 ----

func handleMerge(id int64, input map[string]interface{}) {
	files := input["files"]
	outputDir := strFrom(input, "outputDir")
	if files == nil || outputDir == "" {
		respondError(id, -32602, "缺少 files 或 outputDir 参数")
		return
	}

	fileList, ok := files.([]interface{})
	if !ok {
		respondError(id, -32602, "files 必须是数组")
		return
	}
	if len(fileList) < 2 {
		respondError(id, -32602, "合并至少需要 2 个 PDF 文件")
		return
	}

	paths := make([]string, len(fileList))
	for i, f := range fileList {
		if s, ok := f.(string); ok {
			paths[i] = s
		} else {
			respondError(id, -32602, "file 必须是字符串路径")
			return
		}
	}

	// 输出为目录 + 自动命名（避免让用户手输文件名）
	os.MkdirAll(outputDir, 0755)
	filename := fmt.Sprintf("合并文件_%s.pdf", time.Now().Format("20060102_150405"))
	outPath := filepath.Join(outputDir, filename)

	// 异步执行：立即返回 taskId，避免宿主 20s 超时判定 unresponsive
	t := startPDFTask()
	go func() {
		err := mergePDFs(paths, outPath)
		if err != nil {
			finishPDFTask(t, nil, fmt.Errorf("合并失败: %w", err))
			return
		}
		finishPDFTask(t, map[string]interface{}{
			"ok":      true,
			"output":  outPath,
			"count":   len(paths),
			"message": fmt.Sprintf("成功合并 %d 个 PDF → %s", len(paths), outPath),
		}, nil)
	}()

	respond(id, map[string]interface{}{"ok": true, "async": true, "taskId": t.ID})
}

func handleSplit(id int64, input map[string]interface{}) {
	inputPath := strFrom(input, "input")
	outputDir := strFrom(input, "outputDir")
	pages := input["pages"]

	if inputPath == "" || outputDir == "" {
		respondError(id, -32602, "缺少 input 或 outputDir 参数")
		return
	}

	outDir := ensureExt(outputDir, "")
	os.MkdirAll(outDir, 0755)

	var pageRanges []string
	if p, ok := pages.([]interface{}); ok {
		for _, pg := range p {
			if s, ok := pg.(string); ok {
				pageRanges = append(pageRanges, s)
			}
		}
	}
	if len(pageRanges) == 0 {
		pageRanges = []string{"all"}
	}

	// 异步执行：立即返回 taskId，避免宿主 20s 超时判定 unresponsive
	t := startPDFTask()
	go func() {
		outPaths, err := splitPDF(inputPath, outDir, pageRanges)
		if err != nil {
			finishPDFTask(t, nil, fmt.Errorf("拆分失败: %w", err))
			return
		}
		finishPDFTask(t, map[string]interface{}{
			"ok":     true,
			"output": outPaths,
			"count":  len(outPaths),
		}, nil)
	}()
	respond(id, map[string]interface{}{"ok": true, "async": true, "taskId": t.ID})
}

func handleCompress(id int64, input map[string]interface{}) {
	inputPath := strFrom(input, "input")
	outputDir := strFrom(input, "outputDir")

	if inputPath == "" || outputDir == "" {
		respondError(id, -32602, "缺少 input 或 outputDir 参数")
		return
	}

	// 输出到目录，自动命名（压缩产物为单文件，无需让用户手输文件名）
	os.MkdirAll(outputDir, 0755)
	outPath := filepath.Join(outputDir, "compressed.pdf")

	// 异步执行：立即返回 taskId，避免宿主 20s 超时判定 unresponsive
	t := startPDFTask()
	go func() {
		err := compressPDF(inputPath, outPath)
		if err != nil {
			finishPDFTask(t, nil, fmt.Errorf("压缩失败: %w", err))
			return
		}
		inInfo, _ := os.Stat(inputPath)
		outInfo, _ := os.Stat(outPath)
		saved := "0 B"
		if inInfo != nil && outInfo != nil && inInfo.Size() > outInfo.Size() {
			saved = formatSize(inInfo.Size() - outInfo.Size())
		}
		finishPDFTask(t, map[string]interface{}{
			"ok":     true,
			"output": outPath,
			"saved":  saved,
		}, nil)
	}()
	respond(id, map[string]interface{}{"ok": true, "async": true, "taskId": t.ID})
}

func handleWatermark(id int64, input map[string]interface{}) {
	inputPath := strFrom(input, "input")
	outputDir := strFrom(input, "outputDir")
	text := strFrom(input, "text", "水印")
	opacity := float64(0.3)
	size := float64(48)

	if inputPath == "" || outputDir == "" {
		respondError(id, -32602, "缺少 input 或 outputDir 参数")
		return
	}

	if v, ok := input["opacity"].(float64); ok {
		opacity = v
	}
	if v, ok := input["size"].(float64); ok {
		size = v
	}

	// 输出到目录，自动命名
	os.MkdirAll(outputDir, 0755)
	outPath := filepath.Join(outputDir, "watermarked.pdf")

	// 异步执行：立即返回 taskId，避免宿主 20s 超时判定 unresponsive
	t := startPDFTask()
	go func() {
		err := addWatermark(inputPath, outPath, text, opacity, size)
		if err != nil {
			finishPDFTask(t, nil, fmt.Errorf("添加水印失败: %w", err))
			return
		}
		finishPDFTask(t, map[string]interface{}{
			"ok":     true,
			"output": outPath,
		}, nil)
	}()
	respond(id, map[string]interface{}{"ok": true, "async": true, "taskId": t.ID})
}

func handleExtractImages(id int64, input map[string]interface{}) {
	inputPath := strFrom(input, "input")
	outputDir := strFrom(input, "outputDir")

	if inputPath == "" || outputDir == "" {
		respondError(id, -32602, "缺少 input 或 outputDir 参数")
		return
	}

	outDir := ensureExt(outputDir, "")
	os.MkdirAll(outDir, 0755)

	// 异步执行：立即返回 taskId，避免宿主 20s 超时判定 unresponsive
	t := startPDFTask()
	go func() {
		imgPaths, err := extractImages(inputPath, outDir)
		if err != nil {
			finishPDFTask(t, nil, fmt.Errorf("提取图片失败: %w", err))
			return
		}
		finishPDFTask(t, map[string]interface{}{
			"ok":     true,
			"output": outDir,
			"count":  len(imgPaths),
		}, nil)
	}()
	respond(id, map[string]interface{}{"ok": true, "async": true, "taskId": t.ID})
}

func handleInfo(id int64, input map[string]interface{}) {
	inputPath := strFrom(input, "input")
	if inputPath == "" {
		respondError(id, -32602, "缺少 input 参数")
		return
	}

	info, err := getPDFInfo(inputPath)
	if err != nil {
		respondError(id, -32603, "获取信息失败: "+err.Error())
		return
	}

	respond(id, map[string]interface{}{
		"ok":   true,
		"info": info,
	})
}

func handleClipboard(id int64, input map[string]interface{}) {
	text := strFrom(input, "text")
	if text == "" {
		respondError(id, -32602, "缺少 text 参数")
		return
	}

	result, err := copyToClipboard(text)
	if err != nil {
		respond(id, map[string]interface{}{
			"ok":       false,
			"fallback": true,
			"text":     text,
		})
		return
	}

	respond(id, map[string]interface{}{
		"ok":     true,
		"copied": result,
	})
}

func handleOpenFolder(id int64, input map[string]interface{}) {
	path := strFrom(input, "path")
	if path == "" {
		respondError(id, -32602, "缺少 path 参数")
		return
	}

	err := openFolder(path)
	if err != nil {
		respondError(id, -32603, "打开文件夹失败: "+err.Error())
		return
	}

	respond(id, map[string]interface{}{"ok": true})
}

// handleTaskStatus 查询后台任务状态（前端轮询用）。
func handleTaskStatus(id int64, input map[string]interface{}) {
	taskID := strFrom(input, "taskId")
	if taskID == "" {
		respondError(id, -32602, "缺少 taskId 参数")
		return
	}
	t, ok := getPDFTask(taskID)
	if !ok {
		respondError(id, -32604, "任务不存在或已过期: "+taskID)
		return
	}
	resp := map[string]interface{}{
		"ok":      true,
		"id":      t.ID,
		"status":  t.Status,
		"message": t.Message,
	}
	if t.Status == "done" {
		resp["result"] = t.Result
	}
	if t.Status == "error" {
		resp["error"] = t.Error
	}
	respond(id, resp)
}


// ---- PDF 操作实现 ----

func mergePDFs(paths []string, outputPath string) error {
	// pdfcpu v0.15.0: merge outFile inFile...（输出在前，无 -f；--force 为全局长标志）
	args := []string{"merge", "--force", outputPath}
	args = append(args, paths...)
	_, err := runPDFCommand(args)
	return err
}

func splitPDF(inputPath, outputDir string, pageRanges []string) ([]string, error) {
	// pdfcpu v0.15.0 拆分/提取：
	//  - 整本拆单页：split --mode span，span=1
	//  - 指定页码：extract -m page -p <pages>
	// 产物先落独立临时子目录再移出：直接 glob 输出目录会把其中的历史 PDF
	// （含输入文件自身）一并计入 count，造成「拆 3 个提示 24 个」的虚报。
	tmpDir := filepath.Join(outputDir, fmt.Sprintf("_split_%d", time.Now().UnixNano()))
	defer os.RemoveAll(tmpDir)
	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		return nil, err
	}

	var outPaths []string

	if len(pageRanges) == 0 || (len(pageRanges) == 1 && pageRanges[0] == "all") {
		if _, err := runPDFCommand([]string{"split", "--force", inputPath, tmpDir, "1"}); err != nil {
			return nil, err
		}
	} else {
		pages := strings.Join(pageRanges, ",")
		if _, err := runPDFCommand([]string{"extract", "-m", "page", "-p", pages, "--force", inputPath, tmpDir}); err != nil {
			return nil, err
		}
	}

	matches, err := filepath.Glob(filepath.Join(tmpDir, "*.pdf"))
	if err != nil {
		return nil, err
	}
	for _, src := range matches {
		dst := filepath.Join(outputDir, filepath.Base(src))
		if err := os.Rename(src, dst); err != nil {
			// 跨卷/占用时退化为拷贝
			data, rerr := os.ReadFile(src)
			if rerr != nil {
				continue
			}
			if werr := os.WriteFile(dst, data, 0644); werr != nil {
				continue
			}
		}
		outPaths = append(outPaths, dst)
	}
	return outPaths, nil
}

func compressPDF(inputPath, outputPath string) error {
	// pdfcpu v0.15.0 中旧 compress 命令已移除，改用 optimize（无 quality 参数）
	_, err := runPDFCommand([]string{"optimize", "--force", inputPath, outputPath})
	return err
}

func addWatermark(inputPath, outputPath, text string, opacity, size float64) error {
	// pdfcpu v0.15.0: watermark add "文本" "描述" --mode text inFile outFile
	// ⚠️ 中文必须指定 CJK 字体：pdfcpu 默认字体不含中文字形，会把汉字静默替换成
	// 空格（命令成功、Watermarked:Yes，但页面上一片空白）。desc 的字体参数在
	// v0.15.0 中叫 font:（旧版的 fontKey 已移除），需先 fonts install 安装 TTF。
	ensureCJKFont()
	desc := fmt.Sprintf("font:SimHei, points:%d, opacity:%g, position:c", int(size), opacity)
	args := []string{
		"watermark", "add", text, desc,
		"--mode", "text", "--force",
		inputPath, outputPath,
	}
	_, err := runPDFCommand(args)
	return err
}

// cjkFontOnce 保证每个插件进程只尝试安装一次 CJK 字体（幂等：已安装时重复 install 开销极小）
var cjkFontOnce sync.Once

// ensureCJKFont 安装 Windows 自带黑体供水印使用；失败不阻断流程（退化为旧行为）
func ensureCJKFont() {
	if gOS != "windows" {
		return
	}
	cjkFontOnce.Do(func() {
		fontPath := filepath.Join(os.Getenv("WINDIR"), "Fonts", "simhei.ttf")
		if _, err := os.Stat(fontPath); err != nil {
			return // 无黑体可用（精简系统），保持原状
		}
		_, _ = runPDFCommand([]string{"fonts", "install", fontPath})
	})
}

func extractImages(inputPath, outputDir string) ([]string, error) {
	// pdfcpu v0.15.0: images extract inFile outDir
	if _, err := runPDFCommand([]string{"images", "extract", "--force", inputPath, outputDir}); err != nil {
		return nil, err
	}
	// 必须返回真实图片清单（此前返回目录名导致 count 恒为 1）
	var imgs []string
	entries, err := os.ReadDir(outputDir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		switch ext {
		case ".jpg", ".jpeg", ".png", ".gif", ".tif", ".tiff", ".bmp", ".webp":
			imgs = append(imgs, filepath.Join(outputDir, e.Name()))
		}
	}
	return imgs, nil
}

func getPDFInfo(path string) (map[string]interface{}, error) {
	// info 是同步命令（宿主 20s 超时），必须限时强杀防 pdfcpu 异常挂起拖死 RPC
	out, err := runPDFCommandT([]string{"info", path}, 15*time.Second)
	if err != nil {
		return nil, err
	}

	info := map[string]interface{}{
		"path":  path,
		"raw":   string(out),
		"size":  0,
		"pages": 0,
	}

	if fi, err := os.Stat(path); err == nil {
		info["size"] = fi.Size()
		info["sizeFormatted"] = formatSize(fi.Size())
	}

	return info, nil
}

// ---- PDF 命令执行 ----

func runPDFCommand(args []string) ([]byte, error) {
	// 异步任务（merge/split 等在 goroutine 中跑大文件）给足 15 分钟，与前端 waitTask 超时对齐
	return runPDFCommandT(args, 15*time.Minute)
}

func runPDFCommandT(args []string, timeout time.Duration) ([]byte, error) {
	path, err := findPDFCPU()
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return out, fmt.Errorf("pdfcpu 执行超时（%s），已强制终止", timeout)
	}
	if err != nil {
		return out, fmt.Errorf("pdfcpu 执行失败: %w\n输出: %s", err, string(out))
	}
	return out, nil
}

func findPDFCPU() (string, error) {
	// 1) 优先使用插件同目录自带的 pdfcpu（随插件锁定版本，避免依赖用户 PATH 中飘忽的版本）
	if exe, err := os.Executable(); err == nil {
		sibling := filepath.Join(filepath.Dir(exe), "pdfcpu"+exeSuffix())
		if _, err := os.Stat(sibling); err == nil {
			return sibling, nil
		}
	}
	// 2) 回退到 PATH
	if p, err := exec.LookPath("pdfcpu" + exeSuffix()); err == nil {
		return p, nil
	}
	// 3) Go bin 目录
	goPath := os.Getenv("GOPATH")
	if goPath == "" {
		goPath = filepath.Join(os.Getenv("HOME"), "go")
	}
	candidate := filepath.Join(goPath, "bin", "pdfcpu"+exeSuffix())
	if _, err := os.Stat(candidate); err == nil {
		return candidate, nil
	}
	return "", fmt.Errorf("未找到 pdfcpu，请将 pdfcpu.exe 放在插件目录下，或确保其在 PATH 中")
}

// ---- 系统操作 ----

func copyToClipboard(text string) (bool, error) {
	switch gOS {
	case "windows":
		// 剪贴板被其他进程（Word/Excel/浏览器大块复制）锁定时 Set-Clipboard 可能
		// 无限期阻塞，必须限时强杀——goroutine dispatch 虽已不阻塞 ping，但
		// 限时能让前端在宿主 20s 超时前拿到明确错误而非干等。
		escaped := strings.ReplaceAll(text, "'", "''")
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-Command", fmt.Sprintf("Set-Clipboard -Value '%s'", escaped))
		return true, cmd.Run()
	case "darwin":
		// pbcopy
		cmd := exec.Command("pbcopy")
		stdin, _ := cmd.StdinPipe()
		cmd.Start()
		stdin.Write([]byte(text))
		stdin.Close()
		return true, cmd.Wait()
	default:
		// Linux: xclip 或 xsel
		cmd := exec.Command("xclip", "-selection", "clipboard")
		stdin, _ := cmd.StdinPipe()
		cmd.Start()
		stdin.Write([]byte(text))
		stdin.Close()
		if err := cmd.Wait(); err != nil {
			// 尝试 xsel
			cmd = exec.Command("xsel", "--clipboard", "--input")
			stdin, _ = cmd.StdinPipe()
			cmd.Start()
			stdin.Write([]byte(text))
			stdin.Close()
			return true, cmd.Wait()
		}
		return true, nil
	}
}

func openFolder(path string) error {
	switch gOS {
	case "windows":
		return exec.Command("explorer", path).Start()
	case "darwin":
		return exec.Command("open", path).Run()
	default:
		return exec.Command("xdg-open", path).Run()
	}
}

// ---- 工具函数 ----

func strFrom(m map[string]interface{}, key string, def ...string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	if len(def) > 0 {
		return def[0]
	}
	return ""
}

func intFrom(m map[string]interface{}, key string, def int) int {
	if v, ok := m[key].(float64); ok {
		return int(v)
	}
	return def
}

func ensureExt(path, ext string) string {
	if ext == "" {
		return path
	}
	if strings.HasSuffix(path, ext) {
		return path
	}
	return path + ext
}

func formatSize(bytes int64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%d B", bytes)
	}
	if bytes < 1024*1024 {
		return fmt.Sprintf("%.1f KB", float64(bytes)/1024)
	}
	if bytes < 1024*1024*1024 {
		return fmt.Sprintf("%.1f MB", float64(bytes)/(1024*1024))
	}
	return fmt.Sprintf("%.1f GB", float64(bytes)/(1024*1024*1024))
}

// ---- 响应函数 ----

func respond(id int64, result interface{}) {
	payload, err := json.Marshal(result)
	if err != nil {
		respondError(id, -32603, "marshal result failed: "+err.Error())
		return
	}
	writeResponse(rpcResponse{JSONRPC: "2.0", ID: id, Result: payload})
}

func respondError(id int64, code int, msg string) {
	writeResponse(rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}})
}

func writeResponse(resp rpcResponse) {
	data, err := json.Marshal(resp)
	if err != nil {
		return
	}
	writeMu.Lock()
	defer writeMu.Unlock()
	start := time.Now()
	stdout.Write(data)
	stdout.WriteByte('\n')
	stdout.Flush()
	if el := time.Since(start); el > 500*time.Millisecond {
		// stdout 管道满（宿主不消费）或磁盘/控制台问题 → 响应阻塞，宿主 ping 必然超时
		diagLogf("FLUSH 阻塞 %.2fs id=%d", el.Seconds(), resp.ID)
	}
}

// io 占位
var _ = io.Discard
