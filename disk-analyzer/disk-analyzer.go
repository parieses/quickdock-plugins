// Disk Analyzer - 磁盘空间分析工具
// JSON-RPC 2.0 over stdin/stdout
//
// 协议对齐 QuickDock 宿主约定（参考外部插件的 system-tools 源码）：
//   - initialize      宿主启动插件后的握手，必须响应，否则 15s 超时判定加载失败
//   - host.ping       健康检查
//   - plugin.execute  唯一的业务入口，params = {command, input}
//
// 注意：宿主 Manager.ExecuteCommand 固定发送 "plugin.execute"，
// 前端 pluginExec(cmd, data) 的 cmd 落在 params.command，data 被打包进 params.input.text。
//
// 扫描架构（对齐 SpaceSniffer 的"后台全量扫描 + 渐进渲染"）：
//   - disk-scan-root   枚举本机盘符（来自磁盘统计 API，瞬时返回，永不过时）
//   - disk-scan-full   启动一个【后台】goroutine 对指定路径做整棵子树全量扫描，
//                      立即返回 {started:true}，不阻塞 RPC（宿主 20s 超时无关）
//   - disk-scan-status 轮询接口：返回当前已扫出的【部分树快照】+ 进度，毫秒级响应；
//                      用 version 做增量，未变化时只回 {unchanged:true}，避免重复传大树
//   - disk-scan        旧式"按需单层/浅层边界扫描"，仅用于扫描中临时展开某个还没扫到的节点
// 前端点击盘符后轮询 disk-scan-status，方块图随快照逐帧长大——即 SpaceSniffer 体验。

package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

// ---- 目录节点 ----

type dirNode struct {
	Path string `json:"path"`
	Name string `json:"name"`
	Size int64  `json:"size"`
	// IsFile 为 true 表示叶子文件，前端据此决定是否可展开
	IsFile bool `json:"isFile,omitempty"`
	// IsDrive 为 true 表示这是一个盘符节点（如 C:），其体积来自磁盘统计 API
	// 而非目录遍历，因此几乎瞬间返回，不会触发宿主 RPC 超时
	IsDrive bool `json:"isDrive,omitempty"`
	// Scanning 为 true 表示这个目录的体积还在后台统计中（占位方块，size 暂为 0）
	// 前端据此把它画成半透明/带省略号的"扫描中"方块，体积算完后再长大
	Scanning bool `json:"scanning,omitempty"`
	// Truncated 表示该目录体积统计因超时被截断，显示值偏小
	Truncated bool `json:"truncated,omitempty"`
	// Children 在扫描结果中填充（指针切片，便于后台并发写同一棵树）
	Children []*dirNode `json:"children,omitempty"`
	// Partial 表示本层中至少有一个子目录统计被截断
	Partial bool `json:"partial,omitempty"`
	// Elapsed 本次扫描耗时（毫秒），便于前端提示
	ElapsedMs int64 `json:"elapsedMs,omitempty"`
	// Total/Free 仅盘符节点使用：磁盘总容量 / 剩余容量（字节）
	Total uint64 `json:"total,omitempty"`
	Free  uint64 `json:"free,omitempty"`
	// UsagePct 仅盘符节点使用：已用百分比（0-100）
	UsagePct float64 `json:"usagePct,omitempty"`
}

// driveInfo 描述一个本地盘符
type driveInfo struct {
	Letter   string  // "C:"
	Path     string  // "C:\\"
	Label    string  // 卷标（可能为空）
	Total    uint64  // 总字节
	Free     uint64  // 剩余字节
	Used     uint64  // 已用字节
	UsagePct float64 // 已用百分比
	Ready    bool    // 是否成功取到容量（光驱空盘等会失败）
}

// errBudgetExceeded 用于从 WalkDir 回调中提前中断遍历
var errBudgetExceeded = errors.New("budget exceeded")

// 旧式按需扫描的预算（disk-scan 用）。后台全量扫描（disk-scan-full）不使用此预算，
// 它有自己的整体 deadline（jobOverall）。
const defaultBudget = 10 * time.Second

// 后台全量扫描的全局上限：避免极端情况下把内存吃爆或扫到天荒地老。
const (
	maxJobNodes = 20000    // 单任务返回节点数硬上限
	maxJobDepth = 4        // 最大扫描深度
	jobOverall  = 3 * time.Minute // 单任务整体耗时上限
	perDirBudget = 20 * time.Second // 单个目录体积统计的预算（后台，宽松）
	walkSemSize = 64       // 并发 walk 的目录数上限
)

var stdout = bufio.NewWriter(os.Stdout)

// walkSem 限制同时进行的目录遍历 goroutine 数量，防止扇出爆炸把句柄/内存打爆。
var walkSem = make(chan struct{}, walkSemSize)

// ---- 后台扫描任务 ----

type scanJob struct {
	mu          sync.Mutex
	path        string
	maxDepth    int
	startedAt   time.Time
	deadline    time.Time
	done        bool
	truncated   bool
	root        *dirNode
	scannedDirs int64
	nodeCount   int64
	version     int64 // 每次树结构变更自增，前端据此判定是否需要拉新快照
}

var (
	jobsMu sync.Mutex
	jobs   = map[string]*scanJob{}
)

func (j *scanJob) setTrunc() {
	j.mu.Lock()
	j.truncated = true
	j.mu.Unlock()
}

func (j *scanJob) isDone() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.done
}

func main() {
	reader := bufio.NewReader(os.Stdin)

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		data := strings.TrimSpace(line)
		if data == "" {
			continue
		}

		var req rpcRequest
		if err := json.Unmarshal([]byte(data), &req); err != nil {
			respondError(0, -32700, "parse error: "+err.Error())
			continue
		}
		handleRequest(req)
	}
}

func handleRequest(req rpcRequest) {
	switch req.Method {
	case "initialize":
		respond(req.ID, map[string]interface{}{
			"status": "ready",
			"name":   "QuickDock Disk Analyzer",
		})
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

	// 兼容前端 pluginExec 打包格式：input = {text: JSON.stringify(实际参数)}
	// 自动解包，使 handler 能直接访问 input["path"] / input["limit"]
	input := params.Input
	if input == nil {
		input = map[string]interface{}{}
	}
	if textRaw, ok := input["text"].(string); ok && textRaw != "" {
		trimmed := strings.TrimSpace(textRaw)
		if strings.HasPrefix(trimmed, "{") {
			var nested map[string]interface{}
			if err := json.Unmarshal([]byte(trimmed), &nested); err == nil {
				for k, v := range nested {
					if _, exists := input[k]; !exists {
						input[k] = v
					}
				}
			}
		} else {
			// 命令面板直接传入路径字符串的场景
			if _, exists := input["path"]; !exists {
				input["path"] = trimmed
			}
		}
	}

	// 命令名同时兼容 kebab-case（plugin.json 声明）与点号写法（历史前端调用）
	cmd := strings.ToLower(strings.TrimSpace(params.Command))
	cmd = strings.ReplaceAll(cmd, ".", "-")
	cmd = strings.TrimPrefix(cmd, "disk-")

	switch cmd {
	case "scan", "":
		handleScan(req.ID, input)
	case "scan-root", "root":
		// 根层不再扫描整盘（会超时），改为列出盘符，瞬时返回
		handleScanRoot(req.ID, input)
	case "scan-full", "full":
		handleScanFull(req.ID, input)
	case "scan-status", "status":
		handleScanStatus(req.ID, input)
	case "list":
		handleList(req.ID, input)
	case "info":
		handleInfo(req.ID, input)
	case "open":
		handleOpen(req.ID, input)
	default:
		respondError(req.ID, -32601, "unknown command: "+params.Command)
	}
}

// ---- 业务处理 ----

// handleScan 旧式按需扫描：对单个路径做边界（depth 层）扫描并立即返回。
// 仅用于扫描过程中临时展开某个还没被后台任务覆盖到的节点，不建议用来扫整盘。
func handleScan(id int64, input map[string]interface{}) {
	path := strFrom(input, "path")
	if path == "" {
		path = rootPath()
	}
	limit := intFrom(input, "limit", 60)
	depth := intFrom(input, "depth", 1)
	if depth < 1 {
		depth = 1
	}
	if depth > 4 {
		depth = 4
	}
	budget := time.Duration(intFrom(input, "budgetMs", int(defaultBudget/time.Millisecond))) * time.Millisecond
	if budget <= 0 || budget > 18*time.Second {
		budget = defaultBudget
	}

	node, err := scanLevel(path, depth, limit, budget)
	if err != nil {
		respondError(id, -32603, "扫描失败: "+err.Error())
		return
	}
	respond(id, node)
}

func handleList(id int64, input map[string]interface{}) {
	path := strFrom(input, "path")
	if path == "" {
		path = rootPath()
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		respondError(id, -32603, err.Error())
		return
	}
	items := make([]map[string]interface{}, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		items = append(items, map[string]interface{}{
			"name": e.Name(),
			"path": filepath.Join(path, e.Name()),
		})
	}
	respond(id, items)
}

func handleInfo(id int64, input map[string]interface{}) {
	path := strFrom(input, "path")
	if path == "" {
		path = rootPath()
	}
	stat, err := getDiskStats(path)
	if err != nil {
		respondError(id, -32603, err.Error())
		return
	}
	respond(id, map[string]interface{}{
		"total":     formatSize(int64(stat.Total)),
		"used":      formatSize(int64(stat.Used)),
		"free":      formatSize(int64(stat.Free)),
		"usage":     formatPct(stat.UsagePct),
		"usagePct":  stat.UsagePct,
		"totalRaw":  stat.Total,
		"usedRaw":   stat.Used,
		"freeRaw":   stat.Free,
		"mount":     path,
		"platform":  runtime.GOOS,
	})
}

// handleOpen 用系统文件管理器打开给定路径（右键方块/列表项时调用）。
// 文件夹：直接打开该目录；文件：在父目录中选中该文件（Windows /select，macOS open -R）。
func handleOpen(id int64, input map[string]interface{}) {
	path := strFrom(input, "path")
	if path == "" {
		respondError(id, -32602, "缺少 path 参数")
		return
	}
	isFile := boolFrom(input, "isFile")

	var err error
	switch runtime.GOOS {
	case "windows":
		if isFile {
			// /select,"C:\foo\bar.txt" —— 打开父目录并高亮该文件
			err = exec.Command("explorer.exe", "/select,"+strconv.Quote(path)).Start()
		} else {
			err = exec.Command("explorer.exe", path).Start()
		}
	case "darwin":
		if isFile {
			err = exec.Command("open", "-R", path).Start()
		} else {
			err = exec.Command("open", path).Start()
		}
	default:
		err = exec.Command("xdg-open", path).Start()
	}
	if err != nil {
		respondError(id, -32603, "打开文件管理器失败: "+err.Error())
		return
	}
	respond(id, map[string]interface{}{"ok": true, "path": path})
}

// handleScanRoot 返回本机所有盘符（固定盘 / 可移动盘 / RAM 盘），
// 每个盘符的体积来自磁盘统计 API（GetDiskFreeSpaceExW / Statfs），
// 完全不遍历目录，因此永远瞬时返回，不会再出现"根目录扫描超时"。
// 自定义要扫描哪些盘符由前端过滤展示（被选中的盘符才会被展开扫描）。
func handleScanRoot(id int64, _ map[string]interface{}) {
	started := time.Now()
	drives, err := listDrives()
	if err != nil {
		respondError(id, -32603, "枚举盘符失败: "+err.Error())
		return
	}

	children := make([]*dirNode, 0, len(drives))
	var aggTotal, aggUsed uint64
	for _, d := range drives {
		used := d.Used
		if !d.Ready {
			used = 0
		}
		label := d.Letter
		if d.Label != "" {
			label = d.Letter + " (" + d.Label + ")"
		}
		children = append(children, &dirNode{
			Path:     d.Path,
			Name:     label,
			Size:     int64(used),
			IsFile:   false,
			IsDrive:  true,
			Total:    d.Total,
			Free:     d.Free,
			UsagePct: d.UsagePct,
		})
		aggTotal += d.Total
		aggUsed += used
	}

	respond(id, &dirNode{
		Path:      "",
		Name:      "此电脑",
		Size:      int64(aggUsed),
		Children:  children,
		Partial:   false,
		ElapsedMs: time.Since(started).Milliseconds(),
		Total:     aggTotal,
		Free:      aggTotal - aggUsed,
	})
}

// handleScanFull 启动后台全量扫描任务，立即返回（不阻塞 RPC）。
// 真正的遍历在 goroutine 里跑，前端通过 disk-scan-status 轮询进度。
func handleScanFull(id int64, input map[string]interface{}) {
	path := strFrom(input, "path")
	if path == "" {
		path = rootPath()
	}
	maxDepth := intFrom(input, "maxDepth", 3)
	if maxDepth < 1 {
		maxDepth = 1
	}
	if maxDepth > maxJobDepth {
		maxDepth = maxJobDepth
	}

	jobsMu.Lock()
	if j, ok := jobs[path]; ok && !j.isDone() {
		jobsMu.Unlock()
		respond(id, map[string]interface{}{
			"jobId":         path,
			"started":       false,
			"alreadyRunning": true,
			"maxDepth":      maxDepth,
		})
		return
	}
	j := &scanJob{
		path:      path,
		maxDepth:  maxDepth,
		startedAt: time.Now(),
		deadline:  time.Now().Add(jobOverall),
	}
	jobs[path] = j
	jobsMu.Unlock()

	go j.run()
	respond(id, map[string]interface{}{
		"jobId":    path,
		"started":  true,
		"maxDepth": maxDepth,
	})
}

// handleScanStatus 轮询接口：返回当前已扫出的部分树快照 + 进度，毫秒级。
// 用 since(version) 做增量——未变化时不回传大树，省带宽。
func handleScanStatus(id int64, input map[string]interface{}) {
	path := strFrom(input, "path")
	if path == "" {
		path = rootPath()
	}
	jobsMu.Lock()
	j, ok := jobs[path]
	jobsMu.Unlock()
	if !ok {
		respondError(id, -32603, "没有该路径的活动扫描，请先调用 disk-scan-full")
		return
	}
	since := int64(intFrom(input, "since", -1))

	j.mu.Lock()
	defer j.mu.Unlock()
	// 实时刷新根节点耗时，前端统计栏"耗时"随之走动
	if j.root != nil {
		j.root.ElapsedMs = time.Since(j.startedAt).Milliseconds()
	}
	if since == j.version {
		respond(id, map[string]interface{}{
			"jobId":       path,
			"done":        j.done,
			"truncated":   j.truncated,
			"scannedDirs": atomic.LoadInt64(&j.scannedDirs),
			"elapsedMs":   time.Since(j.startedAt).Milliseconds(),
			"version":     j.version,
			"unchanged":   true,
		})
		return
	}
	raw, err := json.Marshal(j.root)
	if err != nil {
		respondError(id, -32603, "序列化失败: "+err.Error())
		return
	}
	respond(id, map[string]interface{}{
		"jobId":       path,
		"done":        j.done,
		"truncated":   j.truncated,
		"scannedDirs": atomic.LoadInt64(&j.scannedDirs),
		"elapsedMs":   time.Since(j.startedAt).Milliseconds(),
		"version":     j.version,
		"unchanged":   false,
		"root":        json.RawMessage(raw),
	})
}

// ---- 后台扫描实现 ----

func (j *scanJob) run() {
	root := &dirNode{Path: j.path, Name: displayName(j.path), Size: 0}
	// 若是卷内路径，附加所在卷的容量（总/已用/剩余/使用率），供前端统计栏显示。
	// getDiskStats 对任意卷内路径都有效（返回所在卷容量），非卷根也适用。
	if st, err := getDiskStats(j.path); err == nil {
		root.Total = st.Total
		root.Free = st.Free
		root.UsagePct = st.UsagePct
		root.Size = int64(st.Used)
	}
	j.mu.Lock()
	j.root = root
	j.mu.Unlock()
	j.walk(j.path, root, 0)
	j.mu.Lock()
	j.done = true
	j.version++
	j.root.ElapsedMs = time.Since(j.startedAt).Milliseconds()
	j.mu.Unlock()
}

// walk 递归扫描 path（depth 层），把结果直接写入 node.Children。
// 渐进式（对齐 SpaceSniffer）：
//  1. 刚 ReadDir 完，立刻把所有子项作为"占位节点"挂上 node.Children（目录 size=0
//     且 Scanning=true），并 version++ —— 前端此时就能画出整版方块，而非干等。
//  2. 各目录体积在后台并发统计，算完一个就把对应占位节点的 Size 就地更新、
//     Scanning 置否、再 version++ —— 前端方块一个接一个"长大"。
//  3. 未被截断的子目录继续递归下钻，过程同上，逐层渐进铺开。
// 全局 j.mu 保护：占位节点挂载、子节点 Size/Scanning/Children 更新、version 自增、
// 以及 disk-scan-status 的快照序列化，全部在同一把锁下，避免读到半更新状态。
// 重活（os.ReadDir / dirSize）都在锁外完成。
func (j *scanJob) walk(path string, node *dirNode, depth int) {
	walkSem <- struct{}{}
	defer func() { <-walkSem }()

	if time.Now().After(j.deadline) {
		j.setTrunc()
		return
	}
	if depth >= j.maxDepth {
		return
	}
	if atomic.LoadInt64(&j.nodeCount) > maxJobNodes {
		j.setTrunc()
		return
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return
	}
	atomic.AddInt64(&j.scannedDirs, 1)

	var files, dirs []os.DirEntry
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e)
		} else {
			files = append(files, e)
		}
	}

	// 立刻创建占位子节点：文件带真实大小（零成本），目录先 size=0 并标记 Scanning。
	// 注意：文件条目可能因 Info() 失败或非普通文件（junction/符号链接/残留）被跳过，
	// 所以"childNodes 里文件的个数"可能 < len(files)，不能拿 len(files) 当目录区起点，
	// 否则目录占位节点下标整体偏移、并发统计时越界 panic。用 fileNodes 精确计数。
	childNodes := make([]*dirNode, 0, len(files)+len(dirs))
	fileNodes := 0
	for _, f := range files {
		info, ie := f.Info()
		if ie != nil {
			continue
		}
		if !info.Mode().IsRegular() {
			continue
		}
		childNodes = append(childNodes, &dirNode{
			Path:   filepath.Join(path, f.Name()),
			Name:   f.Name(),
			Size:   info.Size(),
			IsFile: true,
		})
		fileNodes++
	}
	for _, d := range dirs {
		childNodes = append(childNodes, &dirNode{
			Path:     filepath.Join(path, d.Name()),
			Name:     d.Name(),
			Size:     0,
			Scanning: true,
		})
	}

	// 占位节点先上树，版本号自增 —— 前端秒出方块
	j.mu.Lock()
	node.Children = childNodes
	atomic.AddInt64(&j.nodeCount, int64(len(childNodes)))
	j.version++
	j.mu.Unlock()

	// 并发统计每个目录体积，完成后就地更新对应的占位节点
	dirStart := fileNodes // 占位切片里目录从下标 dirStart 开始（精确对齐实际追加的文件数）
	var wg sync.WaitGroup
	sem := make(chan struct{}, scanWorkers())
	for i, d := range dirs {
		wg.Add(1)
		go func(i int, d os.DirEntry) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			p2 := filepath.Join(path, d.Name())
			size, trunc := dirSize(p2, j.deadline, perDirBudget)
			if trunc {
				j.setTrunc()
			}
			ph := childNodes[dirStart+i] // 占位节点，复用同一对象，前端方块保持稳定
			j.mu.Lock()
			ph.Size = size
			ph.Truncated = trunc
			ph.Scanning = false
			j.version++
			j.mu.Unlock()
			if !trunc && depth+1 < j.maxDepth {
				go j.walk(p2, ph, depth+1)
			}
		}(i, d)
	}
	wg.Wait()

	// 本层所有目录体积已确定，按体积从大到小排序（大的方块更醒目），
	// 再 version++ 让前端按最终顺序重绘这一层。
	sort.Slice(node.Children, func(a, b int) bool {
		if node.Children[a].Size != node.Children[b].Size {
			return node.Children[a].Size > node.Children[b].Size
		}
		return node.Children[a].Name < node.Children[b].Name
	})
	j.mu.Lock()
	j.version++
	j.mu.Unlock()
}

// scanWorkers 返回扫描工作池大小：CPU 核数 * 2，封顶 32，保底 4。
func scanWorkers() int {
	n := runtime.NumCPU() * 2
	if n < 4 {
		n = 4
	}
	if n > 32 {
		n = 32
	}
	return n
}

// scanLevel 展开 path 的直接子项（单层），并对每个子目录并行统计体积。
// 旧式按需扫描用（disk-scan）。depth>1 时递归下钻，共享 deadline 预算。
func scanLevel(path string, depth, limit int, budget time.Duration) (*dirNode, error) {
	started := time.Now()
	deadline := started.Add(budget)
	partial := &atomic.Bool{}
	budgetCnt := int64(maxJobNodes)
	return scanLevelR(path, depth, limit, deadline, started, partial, &budgetCnt)
}

// scanLevelR 是 scanLevel 的递归实现。所有层级共享同一 deadline 与 partial 标记，
// 用带缓冲 channel 做信号量限制并发 WalkDir 的 goroutine 数量。
func scanLevelR(path string, depth, limit int, deadline time.Time, rootStarted time.Time, partial *atomic.Bool, nodeBudget *int64) (*dirNode, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	sem := make(chan struct{}, scanWorkers())
	var (
		mu    sync.Mutex
		nodes []*dirNode
	)
	var wg sync.WaitGroup
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() {
			info, ie := e.Info()
			if ie != nil {
				continue
			}
			if !info.Mode().IsRegular() {
				continue
			}
			mu.Lock()
			nodes = append(nodes, &dirNode{
				Path:   filepath.Join(path, name),
				Name:   name,
				Size:   info.Size(),
				IsFile: true,
			})
			atomic.AddInt64(nodeBudget, -1)
			mu.Unlock()
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			childPath := filepath.Join(path, name)
			size, truncated := dirSize(childPath, deadline, defaultBudget)
			if truncated {
				partial.Store(true)
			}
			node := &dirNode{
				Path:      childPath,
				Name:      name,
				Size:      size,
				Truncated: truncated,
			}
			if depth > 1 && !truncated && time.Now().Before(deadline) && atomic.LoadInt64(nodeBudget) > 0 {
				sub, serr := scanLevelR(childPath, depth-1, limit, deadline, rootStarted, partial, nodeBudget)
				if serr == nil && sub != nil {
					node.Children = sub.Children
				}
			}
			mu.Lock()
			nodes = append(nodes, node)
			atomic.AddInt64(nodeBudget, -1)
			mu.Unlock()
		}()
	}
	wg.Wait()

	var total int64
	for _, n := range nodes {
		total += n.Size
	}

	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].Size != nodes[j].Size {
			return nodes[i].Size > nodes[j].Size
		}
		return nodes[i].Name < nodes[j].Name
	})
	if limit > 0 && len(nodes) > limit {
		nodes = nodes[:limit]
	}

	return &dirNode{
		Path:      path,
		Name:      displayName(path),
		Size:      total,
		Children:  nodes,
		Partial:   partial.Load(),
		ElapsedMs: time.Since(rootStarted).Milliseconds(),
	}, nil
}

// dirSize 递归统计目录体积。用 WalkDir（不跟随符号链接）而非 Walk，
// 每 256 个条目检查一次 deadline，超时立刻返回已累计值并置 truncated。
// perDir 为该目录单独的预算（后台任务用 job.deadline，按需扫描用 defaultBudget）。
func dirSize(root string, deadline time.Time, perDir time.Duration) (int64, bool) {
	perDirDeadline := time.Now().Add(perDir)
	if perDirDeadline.After(deadline) {
		perDirDeadline = deadline
	}
	var (
		total     int64
		counter   int
		truncated bool
	)
	walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		counter++
		if counter&255 == 0 && (time.Now().After(deadline) || time.Now().After(perDirDeadline)) {
			truncated = true
			return errBudgetExceeded
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return nil
		}
		total += info.Size()
		return nil
	})
	if errors.Is(walkErr, errBudgetExceeded) {
		truncated = true
	}
	return total, truncated
}

// ---- 响应 ----

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

// writeResponse 统一出口：成功与失败都必须 Flush，
// 否则响应滞留在 bufio 缓冲区，宿主收不到任何字节，只会等到超时。
func writeResponse(resp rpcResponse) {
	data, err := json.Marshal(resp)
	if err != nil {
		return
	}
	stdout.Write(data)
	stdout.WriteByte('\n')
	stdout.Flush()
}

// ---- 工具函数 ----

func strFrom(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func boolFrom(m map[string]interface{}, key string) bool {
	if v, ok := m[key].(bool); ok {
		return v
	}
	return false
}

func intFrom(m map[string]interface{}, key string, def int) int {
	switch v := m[key].(type) {
	case float64:
		if v > 0 {
			return int(v)
		}
	case int:
		if v > 0 {
			return v
		}
	case string:
		var n int
		if _, err := fmtSscan(v, &n); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func fmtSscan(s string, n *int) (int, error) {
	val := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errors.New("not a number")
		}
		val = val*10 + int(c-'0')
	}
	*n = val
	return 1, nil
}

func formatSize(bytes int64) string {
	if bytes < 0 {
		bytes = 0
	}
	units := []string{"B", "KB", "MB", "GB", "TB", "PB"}
	size := float64(bytes)
	idx := 0
	for size >= 1024 && idx < len(units)-1 {
		size /= 1024
		idx++
	}
	if idx == 0 {
		return itoa(int64(size)) + " B"
	}
	return trimFloat(size) + " " + units[idx]
}

func formatPct(p float64) string {
	return trimFloat(p) + "%"
}

func trimFloat(f float64) string {
	scaled := int64(f*10 + 0.5)
	whole := scaled / 10
	frac := scaled % 10
	return itoa(whole) + "." + itoa(frac)
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [24]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func rootPath() string {
	if runtime.GOOS == "windows" {
		if sd := os.Getenv("SystemDrive"); sd != "" {
			return sd + "\\"
		}
		return "C:\\"
	}
	return "/"
}

// displayName 让 "C:\" / "/" 这类根路径显示得体，而不是 filepath.Base 返回的 "\"
func displayName(path string) string {
	cleaned := strings.TrimRight(path, `\/`)
	if cleaned == "" {
		return path
	}
	if runtime.GOOS == "windows" && len(cleaned) == 2 && cleaned[1] == ':' {
		return cleaned + `\`
	}
	base := filepath.Base(cleaned)
	if base == "." || base == string(filepath.Separator) {
		return path
	}
	return base
}
