// Junk Cleaner - Windows 垃圾清理工具
// JSON-RPC 2.0 over stdin/stdout (native 插件协议)
//
// 命令：
//   categories  获取可清理的分类清单（含默认开关）
//   scan        扫描各分类占用（只读，计算大小/文件数）
//   clean       清理指定分类（删除文件，先 UI 确认）
//   task-status 查询异步任务进度
//
// 安全原则：
//   - 仅清理白名单内的系统垃圾目录，绝不触碰用户文档/桌面/下载
//   - 扫描阶段只读；删除必须经前端显式选中分类后发起
//   - 删除跳过被占用的文件（锁定的临时文件），不致命

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"sort"
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

// ---- 垃圾分类白名单 ----

type junkCategory struct {
	Key       string   // 稳定标识，前端与后端共用
	Name      string   // 中文显示名
	NameEn    string   // 英文显示名
	Desc      string   // 说明
	Group     string   // cache | system | temp | danger（前端分组）
	DefaultOn bool     // 默认是否勾选清理
	Dangerous bool     // 高危：删除不可逆/影响范围大，默认不勾选且需二次警告
	Roots     []string // 根目录（支持 %ENV% 展开）
	SubGlob   string   // 可选：仅在 Root 下的此子目录通配下操作（如 *\Cache）
	Exts      []string // 可选：仅匹配这些扩展名（小写，含点）；空=全部文件
	Files     []string // 可选：显式单文件路径（如 %WINDIR%\memory.dmp）
}

// 仅收录明确为「系统垃圾」的位置，避免误删任何用户数据。
// 分类参考 ZyperWinOptimize 的 Clean.cs（纯文件清理项），并保留安全默认：
// 日志/浏览器缓存/Cookies/回收站 默认不勾选；回收站标记为高危。
var junkCategories = []junkCategory{
	// ---------- 缓存文件 ----------
	{
		Key: "windows-update", Name: "Windows 更新缓存", NameEn: "Windows Update Cache",
		Group: "cache", Desc: "SoftwareDistribution\\Download 中已下载的更新安装包",
		DefaultOn: true,
		Roots:     []string{`%WINDIR%\SoftwareDistribution\Download`},
	},
	{
		Key: "delivery-optimization", Name: "传递优化缓存", NameEn: "Delivery Optimization",
		Group: "cache", Desc: "SoftwareDistribution\\DeliveryOptimization（P2P 更新分发缓存）",
		DefaultOn: true,
		Roots:     []string{`%WINDIR%\SoftwareDistribution\DeliveryOptimization`},
	},
	{
		Key: "thumbnails", Name: "缩略图缓存", NameEn: "Thumbnail Cache",
		Group: "cache", Desc: "资源管理器缩略图缓存数据库（thumbcache_*.db / IconCache.db）",
		DefaultOn: true,
		Roots:     []string{`%LOCALAPPDATA%\Microsoft\Windows\Explorer`},
		Exts:      []string{".db"},
	},
	{
		Key: "inetcache", Name: "网页缓存", NameEn: "IE/Edge Legacy Cache",
		Group: "cache", Desc: "INetCache 临时 Internet 文件（IE / 旧版 Edge）",
		DefaultOn: true,
		Roots:     []string{`%LOCALAPPDATA%\Microsoft\Windows\INetCache`},
	},
	{
		Key: "cookies", Name: "Cookies", NameEn: "Cookies",
		Group: "cache", Desc: "INetCookies 浏览器 Cookie（清理后需重新登录网站，默认不勾选）",
		DefaultOn: false,
		Roots:     []string{`%LOCALAPPDATA%\Microsoft\Windows\INetCookies`},
	},
	{
		Key: "d3d-cache", Name: "D3D 着色器缓存", NameEn: "D3D Shader Cache",
		Group: "cache", Desc: "D3DSCache 中编译后的着色器缓存（游戏/显卡，清理后自动重建）",
		DefaultOn: true,
		Roots:     []string{`%LOCALAPPDATA%\Local\D3DSCache`},
	},
	{
		Key: "dotnet-nc", Name: ".NET 程序集缓存", NameEn: ".NET Native Image Cache",
		Group: "cache", Desc: "assembly\\NativeImages_* 原生映像缓存（清理后首次运行略慢）",
		DefaultOn: true,
		Roots: []string{
			`%WINDIR%\assembly\NativeImages_v4.0.30319_32`,
			`%WINDIR%\assembly\NativeImages_v4.0.30319_64`,
		},
	},
	{
		Key: "rds-cache", Name: "远程桌面缓存", NameEn: "RDS Client Cache",
		Group: "cache", Desc: "Terminal Server Client\\Cache 远程桌面位图缓存",
		DefaultOn: true,
		Roots:     []string{`%LOCALAPPDATA%\Microsoft\Terminal Server Client\Cache`},
	},
	{
		Key: "browser-cache", Name: "浏览器缓存 (Edge/Chrome)", NameEn: "Browser Cache",
		Group: "cache", Desc: "Edge / Chrome 的 User Data\\*\\Cache 目录（清理后浏览器自动重建）",
		DefaultOn: false,
		Roots: []string{
			`%LOCALAPPDATA%\Microsoft\Edge\User Data`,
			`%LOCALAPPDATA%\Google\Chrome\User Data`,
		},
		SubGlob: `*\Cache`,
	},
	// ---------- 系统文件 ----------
	{
		Key: "crash-dumps", Name: "崩溃转储与错误报告", NameEn: "Crash Dumps & WER",
		Group: "system", Desc: "程序崩溃转储（CrashDumps/Minidump）、Windows 错误报告队列与 memory.dmp",
		DefaultOn: true,
		Roots: []string{
			`%LOCALAPPDATA%\CrashDumps`,
			`%LOCALAPPDATA%\Minidump`,
			`%WINDIR%\Minidump`,
			`%ProgramData%\Microsoft\Windows\WER\ReportQueue`,
			`%ProgramData%\Microsoft\Windows\WER\ReportArchive`,
		},
		Exts:  []string{".dmp"},
		Files: []string{`%WINDIR%\memory.dmp`},
	},
	{
		Key: "diagnosis-data", Name: "诊断数据缓存", NameEn: "Diagnostics Data",
		Group: "system", Desc: "ProgramData\\Microsoft\\Diagnosis 诊断数据缓存",
		DefaultOn: true,
		Roots:     []string{`%ProgramData%\Microsoft\Diagnosis`},
	},
	{
		Key: "defender-scans", Name: "Defender 扫描缓存", NameEn: "Defender Scan Cache",
		Group: "system", Desc: "Windows Defender\\Scans 历史扫描缓存",
		DefaultOn: true,
		Roots:     []string{`%ProgramData%\Microsoft\Windows Defender\Scans`},
	},
	{
		Key: "winsxs-temp", Name: "WinSxS 临时文件", NameEn: "WinSxS Temp",
		Group: "system", Desc: "WinSxS\\Temp 组件存储临时文件",
		DefaultOn: true,
		Roots:     []string{`%WINDIR%\WinSxS\Temp`},
	},
	{
		Key: "windows-logs", Name: "系统日志与调试文件", NameEn: "System Logs",
		Group: "system", Desc: "Windows\\Logs 下的 .log / .etl 日志（默认不清理，日志有排障价值）",
		DefaultOn: false,
		Roots:     []string{`%WINDIR%\Logs`, `%WINDIR%\Debug`},
		Exts:      []string{".log", ".etl"},
	},
	{
		Key: "store-cache", Name: "Microsoft Store 缓存", NameEn: "Store Cache",
		Group: "system", Desc: "UWP 应用缓存（Packages\\*\\AC\\Temp）",
		DefaultOn: false,
		Roots:     []string{`%LOCALAPPDATA%\Packages`},
		SubGlob:   `*\AC\Temp`,
	},
	// ---------- 临时文件 ----------
	{
		Key: "system-temp", Name: "系统临时文件", NameEn: "System Temp",
		Group: "temp", Desc: "Windows\\Temp 与用户 Temp 目录下的临时文件",
		DefaultOn: true,
		Roots:     []string{`%WINDIR%\Temp`, `%TEMP%`},
	},
	{
		Key: "prefetch", Name: "预读取文件", NameEn: "Prefetch",
		Group: "temp", Desc: "Windows\\Prefetch 下的 .pf 预读取加速文件",
		DefaultOn: true,
		Roots:     []string{`%WINDIR%\Prefetch`},
		Exts:      []string{".pf"},
	},
	// ---------- 高危（默认不勾选）----------
	{
		Key: "recycle-bin", Name: "回收站", NameEn: "Recycle Bin",
		Group: "danger", Desc: "彻底清空所有用户的回收站（$Recycle.bin），删除不可逆，请谨慎",
		DefaultOn: false, Dangerous: true,
		Roots: []string{`%SystemDrive%\$Recycle.bin`},
	},
}

func categoryByKey(key string) (junkCategory, bool) {
	for _, c := range junkCategories {
		if c.Key == key {
			return c, true
		}
	}
	return junkCategory{}, false
}

// ---- 后台任务注册表 ----

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

func startTask() *pdfTask {
	tasksMu.Lock()
	defer tasksMu.Unlock()
	now := time.Now()
	for id, t := range tasks {
		if t.Status != "running" && now.Sub(t.finished) > taskTTL {
			delete(tasks, id)
		}
	}
	taskSeq++
	t := &pdfTask{ID: fmt.Sprintf("jc-%d", taskSeq), Status: "running"}
	tasks[t.ID] = t
	return t
}

func getTask(id string) (*pdfTask, bool) {
	tasksMu.Lock()
	defer tasksMu.Unlock()
	t, ok := tasks[id]
	return t, ok
}

func updateTaskMessage(t *pdfTask, msg string) {
	tasksMu.Lock()
	t.Message = msg
	tasksMu.Unlock()
}

func finishTask(t *pdfTask, result map[string]interface{}, err error) {
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

// ---- 诊断日志（静默降级）----

var (
	diagFile *os.File
	diagMu   sync.Mutex
)

func diagLogf(format string, args ...interface{}) {
	diagMu.Lock()
	defer diagMu.Unlock()
	if diagFile == nil {
		return
	}
	fmt.Fprintf(diagFile, "%s %s\n", time.Now().Format("2006-01-02 15:04:05.000"), fmt.Sprintf(format, args...))
	_ = diagFile.Sync()
}

func setupDiagLog() {
	f, err := os.OpenFile(filepath.Join(filepath.Dir(os.Args[0]), "junk-cleaner.log"),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	diagFile = f
}

// ---- 文件遍历 ----

// envRe 匹配 Windows 风格环境变量 %VAR%
var envRe = regexp.MustCompile(`%([^%]+)%`)

// expandPath 展开路径中的环境变量。Go 的 os.ExpandEnv 仅支持 $VAR/${VAR}，
// 不支持 Windows 的 %VAR% 写法，故 Windows 下手动替换。
func expandPath(p string) string {
	if gOS != "windows" {
		return os.ExpandEnv(p)
	}
	return envRe.ReplaceAllStringFunc(p, func(m string) string {
		name := m[1 : len(m)-1]
		if v := os.Getenv(name); v != "" {
			return v
		}
		return m
	})
}

// collectFiles 遍历分类下所有匹配文件，回调 path 与 size；只读，不计修改。
func collectFiles(cat junkCategory, onFile func(path string, size int64)) {
	for _, r := range cat.Roots {
		root := expandPath(r)
		info, err := os.Stat(root)
		if err != nil || !info.IsDir() {
			continue
		}
		if cat.SubGlob != "" {
			matches, _ := filepath.Glob(filepath.Join(root, cat.SubGlob))
			for _, m := range matches {
				if fi, e := os.Stat(m); e == nil && fi.IsDir() {
					walkJunk(m, cat, onFile)
				}
			}
		} else {
			walkJunk(root, cat, onFile)
		}
	}
	// 显式单文件（如 %WINDIR%\memory.dmp）
	for _, f := range cat.Files {
		fp := expandPath(f)
		if fi, err := os.Stat(fp); err == nil && !fi.IsDir() {
			onFile(fp, fi.Size())
		}
	}
}

func walkJunk(dir string, cat junkCategory, onFile func(path string, size int64)) {
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // 跳过不可读项，不致命
		}
		if d.IsDir() {
			return nil // 继续遍历
		}
		if len(cat.Exts) > 0 {
			ext := strings.ToLower(filepath.Ext(path))
			hit := false
			for _, e := range cat.Exts {
				if e == ext {
					hit = true
					break
				}
			}
			if !hit {
				return nil
			}
		}
		if fi, e := d.Info(); e == nil {
			onFile(path, fi.Size())
		}
		return nil
	})
}

// pruneEmptyDirs 自底向上删除已清空的子目录（尽力而为，失败忽略）。
func pruneEmptyDirs(root string) {
	var dirs []string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			dirs = append(dirs, path)
		}
		return nil
	})
	sort.SliceStable(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, d := range dirs {
		if d == root {
			continue
		}
		_ = os.Remove(d) // 非空则失败，忽略
	}
}

// ---- 主循环 ----

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
		// 每请求独立 goroutine：任何 handler 都不阻塞 stdin 读循环，host.ping 永远秒回
		wg.Add(1)
		go func(raw string) {
			defer wg.Done()
			dispatchWithDiag(raw)
		}(data)
	}
	wg.Wait()
}

func dispatchWithDiag(raw string) {
	defer func() {
		if r := recover(); r != nil {
			diagLogf("PANIC %v\n%s", r, debug.Stack())
		}
	}()
	var req rpcRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		respondError(0, -32700, "parse error: "+err.Error())
		return
	}
	handleRequest(req)
}

func handleRequest(req rpcRequest) {
	switch req.Method {
	case "initialize":
		respond(req.ID, map[string]interface{}{"status": "ready", "name": "QuickDock Junk Cleaner"})
	case "host.ping":
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
	case "categories":
		handleCategories(req.ID)
	case "scan":
		handleScan(req.ID, params.Input)
	case "clean":
		handleClean(req.ID, params.Input)
	case "task-status":
		handleTaskStatus(req.ID, params.Input)
	default:
		respondError(req.ID, -32601, "unknown command: "+params.Command)
	}
}

func handleCategories(id int64) {
	list := make([]map[string]interface{}, 0, len(junkCategories))
	for _, c := range junkCategories {
		list = append(list, map[string]interface{}{
			"key":       c.Key,
			"name":      c.Name,
			"nameEn":    c.NameEn,
			"desc":      c.Desc,
			"group":     c.Group,
			"defaultOn": c.DefaultOn,
			"dangerous": c.Dangerous,
		})
	}
	respond(id, map[string]interface{}{"categories": list})
}

func handleScan(id int64, input map[string]interface{}) {
	_ = input // 扫描全部分类，供前端勾选
	t := startTask()
	go func() {
		results := make([]map[string]interface{}, 0, len(junkCategories))
		var totalSize int64
		var totalFiles int64
		for _, c := range junkCategories {
			var size int64
			var count int64
			collectFiles(c, func(_ string, s int64) {
				size += s
				count++
			})
			results = append(results, map[string]interface{}{
				"key":       c.Key,
				"name":      c.Name,
				"nameEn":    c.NameEn,
				"desc":      c.Desc,
				"group":     c.Group,
				"defaultOn": c.DefaultOn,
				"dangerous": c.Dangerous,
				"sizeBytes": size,
				"fileCount": count,
			})
			totalSize += size
			totalFiles += count
		}
		finishTask(t, map[string]interface{}{
			"categories": results,
			"totalSize":  totalSize,
			"totalFiles": totalFiles,
		}, nil)
	}()
	respond(id, map[string]interface{}{"async": true, "taskId": t.ID})
}

// parseKeys 解析前端传入的分类选择。
// 支持 map{key:bool} 或 array[key]；未提供返回 (nil,false)。
func parseKeys(input map[string]interface{}) ([]string, bool) {
	if input == nil {
		return nil, false
	}
	cats, ok := input["categories"]
	if !ok {
		return nil, false
	}
	switch v := cats.(type) {
	case []interface{}:
		keys := []string{}
		for _, x := range v {
			if s, ok := x.(string); ok {
				keys = append(keys, s)
			}
		}
		return keys, true
	case map[string]interface{}:
		keys := []string{}
		for k, val := range v {
			if b, ok := val.(bool); ok && b {
				keys = append(keys, k)
			}
		}
		return keys, true
	}
	return nil, false
}

func handleClean(id int64, input map[string]interface{}) {
	keys, specified := parseKeys(input)
	if !specified || len(keys) == 0 {
		respondError(id, -32602, "未选择要清理的分类")
		return
	}
	// 校验 key 合法性
	valid := make([]junkCategory, 0, len(keys))
	for _, k := range keys {
		if c, ok := categoryByKey(k); ok {
			valid = append(valid, c)
		}
	}
	if len(valid) == 0 {
		respondError(id, -32602, "没有有效的清理分类")
		return
	}

	t := startTask()
	go func() {
		perCat := make([]map[string]interface{}, 0, len(valid))
		var totalFreed int64
		var totalDeleted int64
		for _, c := range valid {
			var freed int64
			var deleted int64
			var skipped int64
			updateTaskMessage(t, "正在清理："+c.Name)
			collectFiles(c, func(path string, size int64) {
				if err := os.Remove(path); err != nil {
					skipped++ // 文件被占用等，跳过不致命
					return
				}
				freed += size
				deleted++
			})
			// 尽力删除已清空的目录
			for _, r := range c.Roots {
				root := expandPath(r)
				if c.SubGlob != "" {
					if matches, _ := filepath.Glob(filepath.Join(root, c.SubGlob)); len(matches) > 0 {
						for _, m := range matches {
							pruneEmptyDirs(m)
						}
					}
				} else {
					pruneEmptyDirs(root)
				}
			}
			perCat = append(perCat, map[string]interface{}{
				"key":      c.Key,
				"name":     c.Name,
				"freed":    freed,
				"deleted":  deleted,
				"skipped":  skipped,
			})
			totalFreed += freed
			totalDeleted += deleted
		}
		finishTask(t, map[string]interface{}{
			"results":       perCat,
			"totalFreed":    totalFreed,
			"totalDeleted":  totalDeleted,
		}, nil)
	}()
	respond(id, map[string]interface{}{"async": true, "taskId": t.ID})
}

func handleTaskStatus(id int64, input map[string]interface{}) {
	taskID := strFrom(input, "taskId")
	if taskID == "" {
		respondError(id, -32602, "缺少 taskId")
		return
	}
	t, ok := getTask(taskID)
	if !ok {
		respondError(id, -32602, "任务不存在或已过期")
		return
	}
	out := map[string]interface{}{"id": t.ID, "status": t.Status}
	if t.Message != "" {
		out["message"] = t.Message
	}
	if t.Status == "done" {
		out["result"] = t.Result
	} else if t.Status == "error" {
		out["error"] = t.Error
	}
	respond(id, out)
}

// ---- 辅助 ----

func strFrom(m map[string]interface{}, key string, def ...string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	if len(def) > 0 {
		return def[0]
	}
	return ""
}

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
		diagLogf("FLUSH 阻塞 %.2fs id=%d", el.Seconds(), resp.ID)
	}
}
