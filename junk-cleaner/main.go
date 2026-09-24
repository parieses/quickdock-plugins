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
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
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
	Detect    string   // 可选：检测该程序是否在 PATH；为空=始终显示，非空=仅在可找到时显示（用于语言缓存）
	CleanCmd  string   // 可选：清理命令（cmd /c 执行）；设置后清理走该命令而非逐文件 os.Remove
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
	// ---------- 语言工具链缓存（仅在该工具已安装时显示）----------
	{
		Key: "go-build-cache", Name: "Go 构建缓存", NameEn: "Go Build Cache",
		Group: "lang", Desc: "go build 缓存（GOCACHE），`go clean -cache` 清理，重编译自动重建",
		DefaultOn: true, Detect: "go", CleanCmd: "go clean -cache",
	},
	{
		Key: "go-mod-cache", Name: "Go 模块缓存", NameEn: "Go Module Cache",
		Group: "lang", Desc: "go 下载的依赖模块（GOMODCACHE），`go clean -modcache` 清理，下次构建需重新下载",
		DefaultOn: false, Detect: "go", CleanCmd: "go clean -modcache",
	},
	{
		Key: "npm-cache", Name: "npm 缓存", NameEn: "npm Cache",
		Group: "lang", Desc: "npm 包缓存，`npm cache clean --force` 清理，清理后首次安装略慢",
		DefaultOn: true, Detect: "npm", CleanCmd: `powershell -NoProfile -Command "npm cache clean --force"`,
		Roots: []string{`%LOCALAPPDATA%\npm-cache`},
	},
	{
		Key: "pnpm-store", Name: "pnpm 存储", NameEn: "pnpm Store",
		Group: "lang", Desc: "pnpm 内容寻址存储，`pnpm store prune` 清理未被引用的包",
		DefaultOn: true, Detect: "pnpm", CleanCmd: `powershell -NoProfile -Command "pnpm store prune"`,
		Roots: []string{`%LOCALAPPDATA%\pnpm\store`},
	},
	{
		Key: "pip-cache", Name: "pip 缓存", NameEn: "pip Cache",
		Group: "lang", Desc: "Python pip 下载缓存，`pip cache purge` 清理，清理后重装需重新下载",
		DefaultOn: true, Detect: "pip", CleanCmd: "pip cache purge",
		Roots: []string{`%LOCALAPPDATA%\pip\cache`},
	},
	{
		Key: "gradle-cache", Name: "Gradle 缓存", NameEn: "Gradle Cache",
		Group: "lang", Desc: "Gradle 依赖缓存（~/.gradle/caches），直接删除缓存文件，下次构建重新解析",
		DefaultOn: false, Detect: "gradle",
		Roots: []string{`%USERPROFILE%\.gradle\caches`},
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

// runTask 在独立 goroutine 内执行异步任务体 fn；recover 捕获 panic，
// 将任务标记为 error 并记录堆栈，避免 goroutine panic 直接杀死 native 进程
// （此前 handleScan/handleClean/handleScanEmpty 的 goroutine 裸奔无保护，是
// 「扫描中插件进程突然退出」的头号嫌疑）。
func runTask(t *pdfTask, name string, fn func(t *pdfTask)) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				diagLogf("TASK PANIC [%s] %v\n%s", name, r, debug.Stack())
				finishTask(t, nil, fmt.Errorf("任务内部错误: %v", r))
			}
		}()
		fn(t)
	}()
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
	for _, r := range resolveRoots(cat) {
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

// ---- 语言缓存：检测 / 路径解析 / 命令式清理 ----

// catAvailable 判断分类是否应展示：Detect 为空始终可用；非空则要求该程序可找到。
// 部分工具以 .ps1 形式存在（如 npm/pnpm），exec.LookPath 默认找不到，
// 用 PowerShell 的 Get-Command 兜底探测，避免已安装却隐藏。
func catAvailable(c junkCategory) bool {
	if c.Detect == "" {
		return true
	}
	if _, err := exec.LookPath(c.Detect); err == nil {
		return true
	}
	return probePowerShell("if(Get-Command " + c.Detect + " -ErrorAction SilentlyContinue){exit 0}else{exit 1}")
}

// probePowerShell 运行一段 PowerShell 脚本，按退出码返回是否成功。
func probePowerShell(script string) bool {
	cmd := exec.Command("powershell", "-NoProfile", "-Command", script)
	return cmd.Run() == nil
}

// shOut 运行命令并返回 trim 后的 stdout；失败返回空串。
func shOut(name string, args ...string) string {
	cmd := exec.Command(name, args...)
	var b bytes.Buffer
	cmd.Stdout = &b
	if err := cmd.Run(); err != nil {
		return ""
	}
	return strings.TrimSpace(b.String())
}

// resolveRoots 返回分类实际要扫描/清理的根目录。
// 语言缓存路径往往由工具自身决定（如 GOCACHE），用 `go env` / `npm config` 等动态解析；
// 解析失败则回退到静态 Roots（默认路径），保证总能给出可用路径。
func resolveRoots(c junkCategory) []string {
	switch c.Key {
	case "go-build-cache":
		if s := shOut("go", "env", "GOCACHE"); s != "" {
			return []string{s}
		}
	case "go-mod-cache":
		if s := shOut("go", "env", "GOMODCACHE"); s != "" {
			return []string{s}
		}
	case "npm-cache":
		if s := shOut("powershell", "-NoProfile", "-Command", "npm config get cache"); s != "" {
			return []string{s}
		}
	case "pnpm-store":
		if s := shOut("powershell", "-NoProfile", "-Command", "pnpm store path"); s != "" {
			return []string{s}
		}
	case "pip-cache":
		if s := shOut("pip", "cache", "dir"); s != "" {
			return []string{s}
		}
	}
	return c.Roots
}

// runCleanCmd 以 cmd /c 执行清理命令，返回合并输出（供诊断）。
func runCleanCmd(cmdStr string) (string, error) {
	cmd := exec.Command("cmd", "/c", cmdStr)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	diagLogf("clean cmd [%s] err=%v out=%s", cmdStr, err, out.String())
	return out.String(), err
}

// fileDeleteCategory 逐文件删除分类下匹配的文件（跳过被占用项），并尽力清理空目录。
// 命令式清理失败时的兜底，也用于无 CleanCmd 的分类（如 gradle）。
func fileDeleteCategory(c junkCategory) (freed, deleted, skipped int64) {
	collectFiles(c, func(path string, size int64) {
		if err := os.Remove(path); err != nil {
			skipped++ // 文件被占用等，跳过不致命
			return
		}
		freed += size
		deleted++
	})
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
	return
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
	diagLogf("RPC recv method=%s id=%d", req.Method, req.ID)
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
	diagLogf("execute command=%s id=%d", cmd, req.ID)
	switch cmd {
	case "categories":
		handleCategories(req.ID)
	case "scan":
		handleScan(req.ID, params.Input)
	case "clean":
		handleClean(req.ID, params.Input)
	case "scan-empty":
		handleScanEmpty(req.ID, params.Input)
	case "clean-empty":
		handleCleanEmpty(req.ID, params.Input)
	case "task-status":
		handleTaskStatus(req.ID, params.Input)
	default:
		respondError(req.ID, -32601, "unknown command: "+params.Command)
	}
}

func handleCategories(id int64) {
	list := make([]map[string]interface{}, 0, len(junkCategories))
	for _, c := range junkCategories {
		if !catAvailable(c) {
			continue
		}
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
	t := startTask()
	runTask(t, "scan", func(t *pdfTask) {
		results := make([]map[string]interface{}, 0, len(junkCategories))
		var totalSize int64
		var totalFiles int64
		for _, c := range junkCategories {
			if !catAvailable(c) {
				continue
			}
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
	})
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
	runTask(t, "clean", func(t *pdfTask) {
		perCat := make([]map[string]interface{}, 0, len(valid))
		var totalFreed int64
		var totalDeleted int64
		for _, c := range valid {
			updateTaskMessage(t, "正在清理："+c.Name)
			var freed, deleted, skipped int64
			if c.CleanCmd != "" && catAvailable(c) {
				// 命令式清理：先统计占用，再执行对应工具原生命令（如 go clean -cache）
				collectFiles(c, func(_ string, s int64) { freed += s; deleted++ })
				if out, err := runCleanCmd(c.CleanCmd); err != nil {
					diagLogf("clean cmd 失败 [%s] err=%v out=%s，回退文件删除", c.CleanCmd, err, out)
					freed, deleted, skipped = fileDeleteCategory(c)
				}
			} else {
				freed, deleted, skipped = fileDeleteCategory(c)
			}
			perCat = append(perCat, map[string]interface{}{
				"key":     c.Key,
				"name":    c.Name,
				"freed":   freed,
				"deleted": deleted,
				"skipped": skipped,
			})
			totalFreed += freed
			totalDeleted += deleted
		}
		finishTask(t, map[string]interface{}{
			"results":      perCat,
			"totalFreed":   totalFreed,
			"totalDeleted": totalDeleted,
		}, nil)
	})
	respond(id, map[string]interface{}{"async": true, "taskId": t.ID})
}

// ---- 空目录 / 空文件 清理（用户自选目录）----
// 与普通系统垃圾白名单不同：这里由用户在界面自选目录，扫描其中的空目录（无子项）
// 与 0 字节空文件，可勾选后删除。删除时对路径按长度降序（深者优先）逐个移除。

func emptyItemsFrom(input map[string]interface{}) []string {
	if v, ok := input["items"].([]interface{}); ok {
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

func findEmptyDirs(root string) []string {
	var result []string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if entries, e := os.ReadDir(path); e == nil && len(entries) == 0 {
				result = append(result, path)
			}
		}
		return nil
	})
	return result
}

func findEmptyFiles(root string) []string {
	var result []string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			if fi, e := d.Info(); e == nil && fi.Size() == 0 {
				result = append(result, path)
			}
		}
		return nil
	})
	return result
}

func handleScanEmpty(id int64, input map[string]interface{}) {
	root := strings.TrimSpace(strFrom(input, "root"))
	if root == "" {
		respondError(id, -32602, "请选择目录")
		return
	}
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		respondError(id, -32602, "目录不存在或不是文件夹")
		return
	}
	mode := strings.ToLower(strFrom(input, "mode"))
	if mode == "" {
		mode = "both"
	}
	t := startTask()
	runTask(t, "scan-empty", func(t *pdfTask) {
		var dirs, files []string
		if mode == "dir" || mode == "both" {
			dirs = findEmptyDirs(root)
		}
		if mode == "file" || mode == "both" {
			files = findEmptyFiles(root)
		}
		finishTask(t, map[string]interface{}{
			"dirs":      dirs,
			"files":     files,
			"dirCount":  len(dirs),
			"fileCount": len(files),
		}, nil)
	})
	respond(id, map[string]interface{}{"async": true, "taskId": t.ID})
}

func handleCleanEmpty(id int64, input map[string]interface{}) {
	items := emptyItemsFrom(input)
	if len(items) == 0 {
		respondError(id, -32602, "未选择要清理的项目")
		return
	}
	// 深路径优先删除：父空目录需等子项先删
	sort.Slice(items, func(i, j int) bool { return len(items[i]) > len(items[j]) })
	var deleted, skipped int
	var failed []string
	for _, p := range items {
		if err := os.Remove(p); err != nil {
			skipped++
			failed = append(failed, p)
		} else {
			deleted++
		}
	}
	respond(id, map[string]interface{}{"deleted": deleted, "skipped": skipped, "failed": failed})
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
