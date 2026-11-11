// 服务器自查 — 只读暴露面体检（QuickDock native 插件）
//
// JSON-RPC 2.0 over stdin/stdout。与宿主约定：initialize 握手 / host.ping 健康检查 /
// plugin.execute 业务入口。日志走 qdlog.go 的 qdLog*（落 plugin-YYYYMMDD.log）。
// 命令名统一小写（宿主会 strings.ToLower 后再分发），此处再兜底一次。
//
// 设计原则（硬约束，改代码前先读）：
//   - 只做 TCP connect 扫描，不用 ICMP / raw socket / SYN 半开（Windows 防火墙与杀软会拦）
//   - 只做 GET/HEAD 与单次协议探测，不利用漏洞、不写入、不上传、不删改远程内容
//   - 认证类检查只允许「单次」：匿名 FTP 一次、无认证探测一次；绝不轮询口令、绝不高并发认证
//   - 拒绝内网 / 回环 / 链路本地 / 组播目标，防止误扫他人内网
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
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
					qdLogPanic("server-audit dispatch panic", r)
					respondError(req.ID, -32000, fmt.Sprintf("internal error: %v", r))
				}
			}()
			handleRequest(req)
		}()
	}
}

func handleRequest(req rpcRequest) {
	switch req.Method {
	case "initialize":
		respond(req.ID, map[string]interface{}{"status": "ready", "name": "服务器自查"})
	case "host.ping":
		respond(req.ID, map[string]interface{}{"pong": true})
	case "plugin.execute":
		var p executeParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			respondError(req.ID, -32602, "invalid params: "+err.Error())
			return
		}
		result, errMsg := execute(p)
		if errMsg != "" {
			respondError(req.ID, -32603, errMsg)
			return
		}
		respond(req.ID, result)
	default:
		respondError(req.ID, -32601, "method not found: "+req.Method)
	}
}

// execute 分发命令（命令名统一小写；前端也用小写发送）。
func execute(p executeParams) (map[string]interface{}, string) {
	cmd := strings.ToLower(strings.TrimSpace(p.Command))
	cmd = strings.ReplaceAll(cmd, ".", "-")
	switch cmd {
	case "audit-start":
		return handleAuditStart(p.Input)
	case "audit-poll":
		return handleAuditPoll(p.Input)
	case "audit-stop":
		return handleAuditStop(p.Input)
	case "audit-report":
		return handleAuditReport(p.Input)
	case "ip-lookup":
		return handleIPLookup(p.Input)
	case "audit":
		return map[string]interface{}{
			"ok":   true,
			"hint": "面板入口：发起巡检请用 audit.start（参数 host）",
		}, ""
	case "backend-ping":
		return map[string]interface{}{"ok": true}, ""
	default:
		return nil, "unknown command: " + p.Command
	}
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
