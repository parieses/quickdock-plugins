// 数据粉碎 - 彻底删除敏感文件，让数据无法被恢复：多次覆写文件内容后重命名并删除；另可覆写磁盘空闲空间，清理此前已删除文件留下的数据残片
// JSON-RPC 2.0 over stdin/stdout（QuickDock native 插件骨架，由 plugin-scaffold 生成）
//
// 与宿主约定：initialize 握手 / host.ping 健康检查 / plugin.execute 业务入口。
// 日志走 templates/qdlog.go 的 qdLog*（落 plugin-YYYYMMDD.log），不要自己 fmt.Println。
// 命令名统一小写（宿主会 strings.ToLower 后再分发）。
//
// 命令一览：
//   volumes      列出本机卷（容量 / 文件系统 / SSD 与否 / 可写性）
//   preview      干跑预览：展开目标、算总量与预计耗时、给出风险提示
//   task-start   启动覆写任务（异步，立即返回 sessionId）
//   task-poll    拉进度
//   task-cancel  取消（已覆写的不会回滚，这一点 UI 必须说明）
//
// task-start 要求调用方带上 confirm：
//   粉碎文件 → "DELETE"；空闲空间覆写 → 目标盘符或卷路径。
// 这是后端自己的闸门，不依赖前端有没有弹确认框。

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
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

var stdout = bufio.NewWriter(os.Stdout)
var stdoutMu sync.Mutex
var cbSeq int64 = 1_000_000_000

// 单任务上限：超过就拒绝，避免一次把整个盘的文件全选进来
const maxFilesPerTask = 20000

func main() {
	qdLogStart()
	defer qdLogExit()
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
		if qdObserveLine(data) {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal([]byte(data), &req); err != nil {
			respondError(json.RawMessage("0"), -32700, "parse error: "+err.Error())
			continue
		}
		if req.Method == "" {
			continue
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					qdLogPanic("disk-shredder dispatch panic", r)
					respondError(req.ID, -32000, fmt.Sprintf("internal error: %v", r))
				}
			}()
			handleRequest(req)
		}()
	}
}

// hostCall 向宿主发起回调（如 db.set / host.notify），fire-and-forget。
func hostCall(method string, params map[string]interface{}) {
	cbSeq++
	payload, err := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0", "id": cbSeq, "method": method, "params": params,
	})
	if err != nil {
		return
	}
	stdoutMu.Lock()
	defer stdoutMu.Unlock()
	stdout.Write(payload)
	stdout.WriteByte('\n')
	stdout.Flush()
}

func handleRequest(req rpcRequest) {
	switch req.Method {
	case "initialize":
		respond(req.ID, map[string]interface{}{"status": "ready", "name": "数据粉碎"})
	case "host.ping":
		respond(req.ID, map[string]interface{}{"pong": true})
	case "plugin.execute":
		var p executeParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			respondError(req.ID, -32602, "invalid params: "+err.Error())
			return
		}
		dispatchCommand(req.ID, p)
	default:
		respondError(req.ID, -32601, "method not found: "+req.Method)
	}
}

// dispatchCommand 分发前端命令。命令名统一小写，点号与连字符等价。
func dispatchCommand(id json.RawMessage, p executeParams) {
	cmd := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(p.Command), ".", "-"))
	in := p.Input
	if in == nil {
		in = map[string]interface{}{}
	}
	switch cmd {
	case "volumes":
		handleVolumes(id)
	case "preview":
		handlePreview(id, in)
	case "task-start":
		handleTaskStart(id, in)
	case "task-poll":
		handleTaskPoll(id, in)
	case "task-cancel":
		handleTaskCancel(id, in)
	case "backend-ping":
		respond(id, map[string]interface{}{"ok": true})
	default:
		respondError(id, -32601, "unknown command: "+p.Command)
	}
}

/* ==================== 参数读取 ==================== */

func strFrom(in map[string]interface{}, k string) string {
	if v, ok := in[k].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func strListFrom(in map[string]interface{}, k string) []string {
	raw, ok := in[k].([]interface{})
	if !ok {
		if s, ok2 := in[k].(string); ok2 && s != "" {
			return []string{s}
		}
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, strings.TrimSpace(s))
		}
	}
	return out
}

func intFrom(in map[string]interface{}, k string, def int) int {
	switch v := in[k].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		n := 0
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			return n
		}
	}
	return def
}

func uint64From(in map[string]interface{}, k string, def uint64) uint64 {
	switch v := in[k].(type) {
	case float64:
		return uint64(v)
	case int:
		return uint64(v)
	case string:
		var n uint64
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			return n
		}
	}
	return def
}

/* ==================== 命令实现 ==================== */

func handleVolumes(id json.RawMessage) {
	vs := listVolumesSorted()
	qdLogInfo("列出卷 %d 个", len(vs))
	respond(id, map[string]interface{}{"volumes": vs})
}

// handlePreview 干跑：不写任何字节，只回答「要动哪些东西、多大、多久、有什么风险」。
func handlePreview(id json.RawMessage, in map[string]interface{}) {
	mode := strFrom(in, "mode")
	passes := intFrom(in, "passes", 1)
	if passes < 1 {
		passes = 1
	}
	if passes > 7 && passes != 7 {
		passes = 7
	}
	plan := passPlan(passes)
	planNames := make([]string, 0, len(plan))
	for _, k := range plan {
		planNames = append(planNames, passName(k))
	}

	if mode == "wipe-free" {
		handlePreviewWipe(id, in, passes, planNames)
		return
	}
	if mode != "" && mode != "shred" {
		respondError(id, -32602, "未知模式："+mode)
		return
	}

	paths := strListFrom(in, "paths")
	if len(paths) == 0 {
		respondError(id, -32602, "没有选择任何文件或文件夹")
		return
	}

	items := make([]map[string]interface{}, 0, len(paths))
	rejected := make([]map[string]interface{}, 0)
	var totalBytes int64
	var totalFiles int
	sample := make([]map[string]interface{}, 0, 30)
	mediaSeen := map[string]bool{}

	for _, raw := range paths {
		p := strings.TrimSpace(raw)
		if p == "" {
			continue
		}
		if reason := guardReject(p); reason != "" {
			rejected = append(rejected, map[string]interface{}{"path": p, "reason": reason})
			continue
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			rejected = append(rejected, map[string]interface{}{"path": p, "reason": "路径无法解析"})
			continue
		}
		st, err := os.Lstat(abs)
		if err != nil {
			rejected = append(rejected, map[string]interface{}{"path": p, "reason": "无法访问：" + err.Error()})
			continue
		}
		if st.Mode()&os.ModeSymlink != 0 {
			rejected = append(rejected, map[string]interface{}{"path": p, "reason": "是符号链接，不跟随"})
			continue
		}

		entry := map[string]interface{}{
			"path":   abs,
			"isDir":  st.IsDir(),
			"size":   st.Size(),
			"note":   guardNote(abs),
			"mtime":  st.ModTime().Format("2006-01-02 15:04"),
			"volume": volumeSummary(abs),
		}
		files := 0
		bytes := int64(0)
		if st.IsDir() {
			cnt, sum := statDir(abs, &sample, mediaSeen)
			files, bytes = cnt, sum
			entry["fileCount"] = cnt
		} else {
			files, bytes = 1, st.Size()
			if len(sample) < 30 {
				sample = append(sample, map[string]interface{}{"path": abs, "size": st.Size()})
			}
		}
		entry["files"] = files
		entry["bytes"] = bytes
		totalBytes += bytes
		totalFiles += files
		items = append(items, entry)
	}

	if len(items) == 0 {
		respond(id, map[string]interface{}{
			"mode": "shred", "passes": passes, "plan": planNames,
			"items": []map[string]interface{}{}, "rejected": rejected,
			"totalBytes": int64(0), "totalFiles": 0, "estSec": 0,
			"warnings": []map[string]interface{}{{"level": "error", "text": "没有可处理的目标"}},
		})
		return
	}

	work := totalBytes * int64(len(plan))
	est := float64(work) / float64(estimateBytesPerSec(mediaSeen))
	est += float64(totalFiles) * 0.004 * float64(len(plan)) // 每个文件每遍的开合 + Sync 开销

	respond(id, map[string]interface{}{
		"mode":       "shred",
		"passes":     passes,
		"plan":       planNames,
		"items":      items,
		"rejected":   rejected,
		"sample":     sample,
		"totalBytes": totalBytes,
		"workBytes":  work,
		"totalFiles": totalFiles,
		"estSec":     est,
		"warnings":   shredWarnings(mediaSeen, totalFiles, totalBytes),
	})
}

// handlePreviewWipe 空闲空间覆写的预览。语义必须讲清：覆写的是整个卷。
func handlePreviewWipe(id json.RawMessage, in map[string]interface{}, passes int, planNames []string) {
	volPath := strFrom(in, "volume")
	if volPath == "" {
		respondError(id, -32602, "没有选择卷")
		return
	}
	var vol *Volume
	for _, v := range listVolumesSorted() {
		if strings.EqualFold(strings.TrimSpace(v.Path), strings.TrimSpace(volPath)) {
			vv := v
			vol = &vv
			break
		}
	}
	if vol == nil {
		respondError(id, -32602, "卷不存在或已拔出："+volPath)
		return
	}
	if vol.Kind == "cdrom" || vol.Kind == "network" {
		respondError(id, -32602, kindText(vol.Kind)+"不支持空闲空间覆写")
		return
	}
	reserve := uint64From(in, "reserveBytes", 0)
	if reserve == 0 {
		reserve = defaultReserve(vol.TotalBytes)
	}
	// 不做「reserve 超过可用一半就截断」这类兜底：预览必须与 task-start 算出的
	// 结果完全一致，否则用户看到的「将写入 X」和实际写入量对不上
	target := uint64(0)
	if vol.FreeBytes > reserve {
		target = vol.FreeBytes - reserve
	}
	tempDir := strFrom(in, "tempDir")
	if tempDir == "" {
		if d, err := pickTempDir(vol); err == nil {
			tempDir = d
		}
	}
	work := target * uint64(len(planNames))
	speed := float64(estimateBytesPerSec(map[string]bool{vol.Media: true}))

	warns := []map[string]interface{}{}
	warns = append(warns, map[string]interface{}{
		"level": "warn",
		"text":  fmt.Sprintf("覆写的是 %s 卷的整个空闲空间（约 %s），不是你选的某个文件夹——这一点与 Windows cipher /w 一致", vol.Path, humanBytes(target)),
	})
	warns = append(warns, map[string]interface{}{"level": "info", "text": mediaRisk(vol.Media)})
	if vol.FileSystem != "" && !strings.EqualFold(vol.FileSystem, "NTFS") {
		warns = append(warns, map[string]interface{}{
			"level": "info",
			"text":  "文件系统是 " + vol.FileSystem + "：cipher /w 只支持 NTFS，这一步是自研实现，FAT32 / exFAT 同样可用",
		})
	}
	warns = append(warns, map[string]interface{}{
		"level": "info",
		"text":  "卷影副本 / 系统还原点里的旧版本不会被覆写；需要的话请另行执行 vssadmin delete shadows",
	})
	if tempDir == "" {
		warns = append(warns, map[string]interface{}{
			"level": "error",
			"text":  "没找到可写目录来放临时文件（卷根通常需要管理员权限），请手动指定该卷上的一个文件夹",
		})
	}
	if target == 0 {
		warns = append(warns, map[string]interface{}{
			"level": "error",
			"text":  fmt.Sprintf("可用空间 %s 已不足保留余量 %s，没有可覆写的空间（可以调小保留余量）",
				humanBytes(vol.FreeBytes), humanBytes(reserve)),
		})
	}

	respond(id, map[string]interface{}{
		"mode":       "wipe-free",
		"passes":     passes,
		"plan":       planNames,
		"volume":     vol,
		"freeBytes":  vol.FreeBytes,
		"reserve":    reserve,
		"target":     target,
		"workBytes":  work,
		"tempDir":    tempDir,
		"estSec":     float64(work) / speed,
		"warnings":   warns,
	})
}

// handleTaskStart 启动任务。必须先过 confirm 闸门。
func handleTaskStart(id json.RawMessage, in map[string]interface{}) {
	mode := strFrom(in, "mode")
	passes := intFrom(in, "passes", 1)
	if passes < 1 {
		passes = 1
	}
	if passes > 7 {
		passes = 7
	}
	renames := intFrom(in, "renames", 6)
	if renames < 0 {
		renames = 0
	}
	if renames > 32 {
		renames = 32
	}
	confirm := strFrom(in, "confirm")

	switch mode {
	case "shred":
		paths := strListFrom(in, "paths")
		if len(paths) == 0 {
			respondError(id, -32602, "没有选择任何文件或文件夹")
			return
		}
		if !strings.EqualFold(confirm, "DELETE") {
			respondError(id, -32602, "需要确认：confirm 必须是 DELETE（不可撤销操作）")
			return
		}
		t := newTask("shred")
		go runShred(t, paths, passPlan(passes), renames)
		qdLogInfo("启动粉碎任务 id=%s 目标=%d 遍数=%d", t.ID, len(paths), passes)
		respond(id, map[string]interface{}{"sessionId": t.ID, "mode": "shred"})
	case "wipe-free":
		volPath := strFrom(in, "volume")
		if volPath == "" {
			respondError(id, -32602, "没有选择卷")
			return
		}
		if !strings.EqualFold(strings.TrimRight(confirm, "\\"), strings.TrimRight(volPath, "\\")) {
			respondError(id, -32602, fmt.Sprintf("需要确认：请输入卷名 %s 后重试（覆写的是整个卷的空闲空间）", volPath))
			return
		}
		var vol *Volume
		for _, v := range listVolumesSorted() {
			if strings.EqualFold(strings.TrimSpace(v.Path), strings.TrimSpace(volPath)) {
				vv := v
				vol = &vv
				break
			}
		}
		if vol == nil {
			respondError(id, -32602, "卷不存在或已拔出："+volPath)
			return
		}
		reserve := uint64From(in, "reserveBytes", 0)
		if reserve == 0 {
			reserve = defaultReserve(vol.TotalBytes)
		}
		tempDir := strFrom(in, "tempDir")
		t := newTask("wipe-free")
		go runWipe(t, vol, passPlan(passes), reserve, tempDir)
		qdLogInfo("启动空闲空间覆写 id=%s 卷=%s 遍数=%d 余量=%s", t.ID, vol.Path, passes, humanBytes(reserve))
		respond(id, map[string]interface{}{"sessionId": t.ID, "mode": "wipe-free"})
	default:
		respondError(id, -32602, "未知模式："+mode)
	}
}

func handleTaskPoll(id json.RawMessage, in map[string]interface{}) {
	sid := strFrom(in, "sessionId")
	if sid == "" {
		respondError(id, -32602, "缺少 sessionId")
		return
	}
	t := getTask(sid)
	if t == nil {
		respondError(id, -32602, "任务不存在或已过期："+sid)
		return
	}
	respond(id, t.snapshot(200))
}

func handleTaskCancel(id json.RawMessage, in map[string]interface{}) {
	sid := strFrom(in, "sessionId")
	if sid == "" {
		respondError(id, -32602, "缺少 sessionId")
		return
	}
	t := getTask(sid)
	if t == nil {
		respondError(id, -32602, "任务不存在或已过期："+sid)
		return
	}
	t.cancel()
	qdLogInfo("取消任务 id=%s（已覆写的部分不会回滚）", sid)
	respond(id, map[string]interface{}{"ok": true, "sessionId": sid})
}

/* ==================== 任务执行 ==================== */

func runShred(t *task, paths []string, plan []passKind, renames int) {
	t.setPhase("展开目标")
	files, dirs, skipped, truncated := collectFiles(paths, maxFilesPerTask)
	for _, s := range skipped {
		t.addNote(s)
	}
	if truncated {
		t.addNote(fmt.Sprintf("目标文件超过 %d 个，只处理了前 %d 个", maxFilesPerTask, len(files)))
	}

	var total int64
	for _, f := range files {
		if st, err := os.Lstat(f); err == nil {
			total += st.Size()
		}
	}
	t.TotalBytes = total * int64(len(plan))
	t.TotalItems = len(files) + len(dirs)

	rnd := newPRNG()
	done := 0
	for _, f := range files {
		if t.canceled() {
			break
		}
		done++
		t.setPhase(fmt.Sprintf("覆写文件 %d/%d", done, len(files)))
		t.setCurrent(f)

		st, err := os.Lstat(f)
		size := int64(0)
		if err == nil {
			size = st.Size()
		}
		note := ""
		if size == 0 {
			note = "空文件：没有数据区可覆写（内容常驻 MFT，文件名已随机化）"
		} else if size < 900 {
			note = "小于 900 字节：NTFS 上内容可能常驻 MFT 记录，覆写数据流未必擦得掉"
		}

		err = shredFile(f, plan, renames, rnd, func(n int64) { t.addBytes(n) })
		if err != nil {
			qdLogWarn("粉碎失败 path=%s err=%v", f, err)
			t.addItem(taskItem{Path: f, Size: size, OK: false, Err: readableErr(err)})
			continue
		}
		t.addItem(taskItem{Path: f, Size: size, OK: true, Note: note})
	}

	// 目录本体：文件都清完后删空壳，同样先随机改名
	for _, d := range dirs {
		if t.canceled() {
			break
		}
		cur := d
		for i := 0; i < renames && i < 8; i++ {
			next, err := randomNameIn(filepath.Dir(cur))
			if err != nil {
				break
			}
			if err := os.Rename(cur, next); err != nil {
				break
			}
			cur = next
		}
		if err := os.Remove(cur); err != nil {
			t.addItem(taskItem{Path: d, OK: false, Err: "删除目录失败：" + err.Error()})
		} else {
			t.addItem(taskItem{Path: d, OK: true, Note: "目录已随机改名后删除"})
		}
	}

	if t.canceled() {
		t.finish("canceled")
		qdLogInfo("粉碎任务取消 id=%s 已完成 %d/%d", t.ID, t.DoneItems, t.TotalItems)
		return
	}
	if t.Failed > 0 {
		t.ErrText = fmt.Sprintf("%d 项失败（详见列表）", t.Failed)
		t.finish("error")
	} else {
		t.finish("done")
	}
	qdLogInfo("粉碎任务结束 id=%s 文件=%d 目录=%d 失败=%d 耗时=%s",
		t.ID, len(files), len(dirs), t.Failed, time.Since(t.StartedAt).Round(time.Millisecond))
}

func runWipe(t *task, vol *Volume, plan []passKind, reserve uint64, tempDir string) {
	t.setPhase("准备")
	if tempDir == "" {
		d, err := pickTempDir(vol)
		if err != nil {
			t.ErrText = err.Error()
			t.finish("error")
			qdLogError("空闲空间覆写准备失败 vol=%s err=%v", vol.Path, err)
			return
		}
		tempDir = d
	}
	if _, err := os.Stat(tempDir); err != nil {
		t.ErrText = "临时目录不可用：" + err.Error()
		t.finish("error")
		return
	}

	cur, err := currentVolume(vol.Path)
	if err != nil {
		t.ErrText = err.Error()
		t.finish("error")
		return
	}
	target := uint64(0)
	if cur.FreeBytes > reserve {
		target = cur.FreeBytes - reserve
	}
	t.TotalBytes = int64(target * uint64(len(plan)))
	t.TotalItems = len(plan)

	written, notes := wipeFree(t, cur, len(plan), reserve, tempDir)
	for _, n := range notes {
		t.addNote(n)
	}
	t.addItem(taskItem{
		Path: vol.Path,
		Size: int64(written),
		OK:   true,
		Note: fmt.Sprintf("覆写 %s，临时文件放在 %s", humanBytes(written), tempDir),
	})

	switch {
	case t.canceled():
		t.finish("canceled")
	case written == 0:
		t.ErrText = "没有写入任何数据"
		t.finish("error")
	default:
		t.finish("done")
	}
	qdLogInfo("空闲空间覆写结束 id=%s 卷=%s 写入=%s 耗时=%s",
		t.ID, vol.Path, humanBytes(written), time.Since(t.StartedAt).Round(time.Millisecond))
}

/* ==================== 预览辅助 ==================== */

// statDir 统计目录下的文件数与总字节，顺手采前 30 个样本和涉及的介质类型。
func statDir(root string, sample *[]map[string]interface{}, mediaSeen map[string]bool) (int, int64) {
	count := 0
	var sum int64
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() || d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		count++
		if info, e := d.Info(); e == nil {
			sum += info.Size()
			if len(*sample) < 30 {
				*sample = append(*sample, map[string]interface{}{"path": p, "size": info.Size()})
			}
		}
		return nil
	})
	if v := volumeOf(root); v != nil {
		mediaSeen[v.Media] = true
	}
	return count, sum
}

func volumeSummary(path string) map[string]interface{} {
	v := volumeOf(path)
	if v == nil {
		return map[string]interface{}{"path": "", "media": "unknown", "fs": ""}
	}
	return map[string]interface{}{
		"path":  v.Path,
		"media": v.Media,
		"fs":    v.FileSystem,
		"kind":  v.Kind,
	}
}

// estimateBytesPerSec 吞吐估算，数值取自本机实测（HDD 上含 Sync 与删除开销约 33 MB/s，
// 纯写入段更高），取中间值：宁可让用户等得比预期短，也别报一个做不到的乐观数字。
func estimateBytesPerSec(mediaSeen map[string]bool) float64 {
	const mb = 1024.0 * 1024.0
	if mediaSeen["ssd"] {
		return 220 * mb
	}
	if mediaSeen["hdd"] {
		return 60 * mb
	}
	return 90 * mb
}

// shredWarnings 覆写前必须让用户知道的四件事，按介质与内容动态裁剪。
func shredWarnings(mediaSeen map[string]bool, files int, totalBytes int64) []map[string]interface{} {
	w := make([]map[string]interface{}, 0, 6)
	if mediaSeen["ssd"] {
		w = append(w, map[string]interface{}{"level": "warn", "text": mediaRisk("ssd")})
	} else if mediaSeen["hdd"] {
		w = append(w, map[string]interface{}{"level": "info", "text": mediaRisk("hdd")})
	} else {
		w = append(w, map[string]interface{}{"level": "warn", "text": mediaRisk("unknown")})
	}
	w = append(w, map[string]interface{}{
		"level": "info",
		"text":  "文件名会先随机改名多次再删除，用来清掉 MFT 记录里的旧名字；但文件的大小、时间戳等元数据仍可能残留。",
	})
	if totalBytes > 0 && int64(totalBytes)/int64(maxI(1, files)) < 4096 {
		w = append(w, map[string]interface{}{
			"level": "warn",
			"text":  "这批文件普遍偏小：NTFS 上小于约 900 字节的内容存在 MFT 记录里（cipher 文档说的是 4 KB），覆写数据流未必能擦掉它们。",
		})
	}
	w = append(w, map[string]interface{}{
		"level": "info",
		"text":  "卷影副本 / 系统还原点可能仍保留旧版本；要一并清掉请另行执行 vssadmin delete shadows。",
	})
	w = append(w, map[string]interface{}{
		"level": "info",
		"text":  "多遍覆写（Gutmann 35 遍之类）对现代硬盘没有额外收益，多出来的只是等待时间。",
	})
	return w
}

func maxI(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// readableErr 把 Go 的系统错误翻成人话（Windows 共享冲突最常见）。
func readableErr(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "being used by another process") || strings.Contains(msg, "另一个程序正在使用"):
		return "文件被其他程序占用，无法覆写（先关闭占用它的程序）"
	case strings.Contains(msg, "Access is denied") || strings.Contains(msg, "拒绝访问"):
		return "没有权限（可能需要管理员，或文件是只读/系统文件）"
	case strings.Contains(msg, "no such file") || strings.Contains(msg, "系统找不到指定"):
		return "文件已不存在"
	}
	return msg
}

/* ==================== 响应 ==================== */

// idToInt 把请求 id 归一化为 int64 供日志追踪使用；字符串 id（前端桥接常用）无法转数字时返回 0。
func idToInt(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	var n float64
	if err := json.Unmarshal(raw, &n); err == nil {
		return int64(n)
	}
	return 0
}

func respond(id json.RawMessage, result interface{}) {
	defer qdFinishRequest(idToInt(id), 0, "")
	payload, err := json.Marshal(result)
	if err != nil {
		respondError(json.RawMessage("0"), -32603, "marshal result failed: "+err.Error())
		return
	}
	writeResponse(rpcResponse{JSONRPC: "2.0", ID: id, Result: payload})
}

func respondError(id json.RawMessage, code int, msg string) {
	defer qdFinishRequest(idToInt(id), code, msg)
	writeResponse(rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}})
}

func writeResponse(resp rpcResponse) {
	data, err := json.Marshal(resp)
	if err != nil {
		return
	}
	stdoutMu.Lock()
	defer stdoutMu.Unlock()
	stdout.Write(data)
	stdout.WriteByte('\n')
	stdout.Flush()
}
