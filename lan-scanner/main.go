// 局域网设备扫描 — 扫描你所在局域网里的在线设备
//
// 自动识别网段 → 读邻居缓存 → 并发探存活 → 扫端口 → 问 NetBIOS 名 → 认厂商，
// 用来回答「家里这个网里到底连了些什么」和「NAS / 打印机到底分到了哪个 IP」。
// 采用「start 立即返回 + 前端轮询」的异步会话模型，避免撞上 20 秒命令超时。
//
// 只扫你自己的局域网：网段默认来自本机网卡，不落盘、不写文件、不做任何修改。
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
)

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

var stdout = bufio.NewWriter(os.Stdout)
var stdoutMu sync.Mutex
var cbSeq int64 = 1_000_000_000

func main() {
	qdLogStart()
	defer qdLogExit()
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
		if qdObserveLine(data) {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal([]byte(data), &req); err != nil {
			respondError(0, -32700, "parse error: "+err.Error())
			continue
		}
		if req.Method == "" {
			continue
		}
		// 每个请求单独 goroutine：poll 与 start 会同时打进来，串行的話会互相堵死
		wg.Add(1)
		go func(r rpcRequest) {
			defer wg.Done()
			defer func() {
				if p := recover(); p != nil {
					qdLogPanic("lan-scanner dispatch panic", p)
					respondError(r.ID, -32000, fmt.Sprintf("internal error: %v", p))
				}
			}()
			handleRequest(r)
		}(req)
	}
	wg.Wait()
}

// hostCall 向宿主发起回调（db.* / host.clipboard.write 等），fire-and-forget。
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
		respond(req.ID, map[string]interface{}{"status": "ready", "name": "局域网设备扫描"})
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

// dispatchCommand 分发前端命令。命令名统一小写，点号与连字符等价（历史写法兼容）。
func dispatchCommand(id int64, p executeParams) {
	cmd := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(p.Command), ".", "-"))
	in := p.Input
	if in == nil {
		in = map[string]interface{}{}
	}
	switch cmd {
	case "net-list":
		handleNetList(id)
	case "scan-start":
		handleScanStart(id, in)
	case "scan-poll":
		handleScanPoll(id, in)
	case "scan-stop":
		handleScanStop(id, in)
	case "clip-copy":
		handleClipCopy(id, in)
	case "lan-scan":
		// 面板入口：这条命令只负责打开扫描面板（宿主行为），不发起扫描。
		// 显式拦下而不是落到 default，是为了给 MCP/AI 一句能照做的指路。
		respondError(id, -32602,
			"命令 lan-scan 只打开扫描面板（不发起扫描）；开始扫描请用 scan-start，取结果用 scan-poll")
	default:
		respondError(id, -32601, "unknown command: "+p.Command)
	}
}

/* ==================== 命令实现 ==================== */

func handleNetList(id int64) {
	subs := localSubnets()
	if len(subs) == 0 {
		qdLogWarn("未识别到私网网段（可能没连网或只有公网 IP）")
	}
	respond(id, map[string]interface{}{
		"subnets": subs,
		"selfIPs": selfIPs(),
	})
}

func handleScanStart(id int64, in map[string]interface{}) {
	cidr := strFrom(in, "cidr")
	if cidr == "" {
		if subs := localSubnets(); len(subs) > 0 {
			cidr = subs[0].CIDR
		}
	}
	if cidr == "" {
		respondError(id, -32602, "没识别到本机网段，请手动填写（如 192.168.1.0/24）")
		return
	}
	ips, err := expandCIDR(cidr)
	if err != nil {
		respondError(id, -32602, err.Error())
		return
	}
	if len(ips) == 0 {
		respondError(id, -32602, "网段内没有可扫描的地址")
		return
	}

	ports := parsePorts(in)
	probeMS := intFrom(in, "probeMs", 300)
	portMS := intFrom(in, "portMs", 600)
	conc := intFrom(in, "concurrency", 128)
	doNames := boolFrom(in, "names", true)
	if probeMS < 100 {
		probeMS = 100
	}
	if probeMS > 2000 {
		probeMS = 2000
	}
	if portMS < 100 {
		portMS = 100
	}
	if portMS > 3000 {
		portMS = 3000
	}
	if conc < 8 {
		conc = 8
	}
	if conc > 512 {
		conc = 512
	}

	s := newSession(cidr, ports, probeMS, portMS, conc, doNames)
	s.setPhase("准备中")
	qdLogInfo("扫描开始 cidr=%s 地址=%d 端口=%d 并发=%d 超时=%d/%dms",
		cidr, len(ips), len(ports), conc, probeMS, portMS)

	go s.run(ips)

	respond(id, map[string]interface{}{
		"sessionId": s.ID,
		"cidr":      cidr,
		"total":     len(ips),
		"ports":     len(ports),
		"running":   true,
	})
}

func handleScanPoll(id int64, in map[string]interface{}) {
	sid := strFrom(in, "sessionId")
	sessMu.Lock()
	s := sessions[sid]
	sessMu.Unlock()
	if s == nil {
		respondError(id, -32002, "会话不存在或已结束")
		return
	}
	respond(id, s.snapshot())
}

func handleScanStop(id int64, in map[string]interface{}) {
	sid := strFrom(in, "sessionId")
	sessMu.Lock()
	s := sessions[sid]
	delete(sessions, sid)
	sessMu.Unlock()
	if s == nil {
		respond(id, map[string]interface{}{"stopped": true})
		return
	}
	s.mu.Lock()
	if s.running {
		s.running = false
		close(s.stopCh)
	}
	s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
	}
	qdLogInfo("[%s] 用户停止扫描", sid)
	snap := s.snapshot()
	snap["stopped"] = true
	respond(id, snap)
}

// handleClipCopy 把文本写进系统剪贴板（前端 iframe 沙箱里拿不到 navigator.clipboard）。
func handleClipCopy(id int64, in map[string]interface{}) {
	text := strFrom(in, "text")
	if text == "" {
		respondError(id, -32602, "没有可复制的内容")
		return
	}
	hostCall("host.clipboard.write", map[string]interface{}{"text": text})
	respond(id, map[string]interface{}{"ok": true})
}

/* ==================== 参数解析 ==================== */

func parsePorts(in map[string]interface{}) []int {
	preset := strFrom(in, "preset")
	base, ok := portPresets[preset]
	if !ok {
		base = portPresets["common"]
	}
	var user []int
	if raw := strFrom(in, "ports"); raw != "" {
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if n, err := strconv.Atoi(part); err == nil && n > 0 && n < 65536 {
				user = append(user, n)
			}
		}
	} else {
		user = base
	}
	return mergePorts(user)
}

func strFrom(m map[string]interface{}, k string) string {
	if v, ok := m[k].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func intFrom(m map[string]interface{}, k string, def int) int {
	switch v := m[k].(type) {
	case float64:
		if n := int(v); n > 0 {
			return n
		}
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func boolFrom(m map[string]interface{}, k string, def bool) bool {
	switch v := m[k].(type) {
	case bool:
		return v
	case string:
		if v == "true" || v == "1" {
			return true
		}
		if v == "false" || v == "0" {
			return false
		}
	case float64:
		return v != 0
	}
	return def
}

/* ==================== 输出 ==================== */

func respond(id int64, result interface{}) {
	defer qdFinishRequest(id, 0, "")
	payload, err := json.Marshal(result)
	if err != nil {
		respondError(id, -32603, "marshal result failed: "+err.Error())
		return
	}
	writeResponse(rpcResponse{JSONRPC: "2.0", ID: id, Result: payload})
}

func respondError(id int64, code int, msg string) {
	defer qdFinishRequest(id, code, msg)
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
