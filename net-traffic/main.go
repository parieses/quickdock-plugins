package main

// net-traffic —— 实时流量监控
//
// 后端常驻采样：每秒读一次网卡字节计数（iphlpapi GetIfTable），每 3 秒读一次
// TCP/UDP 连接表（GetExtendedTcpTable / GetExtendedUdpTable）并解析 PID → 进程名。
// 前端只做一件事：定时取快照（traffic.snapshot），把曲线与列表画出来。
//
// 为什么不在请求时临时采样：速率必须由「两次采样的差值 ÷ 间隔」得到，
// 采样器必须常驻；同时常驻采样也让前端无论何时打开都立刻有历史曲线。

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

const pluginID = "io.github.parieses.net-traffic"

type RPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}
type RPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func main() {
	qdLogStart()
	defer qdLogExit()

	startSampler()

	reader := bufio.NewReader(os.Stdin)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if qdObserveLine(line) {
			continue
		}
		var req RPCRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			continue
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					qdLogPanic("net-traffic panic", r)
					respondError(req.ID, -32000, fmt.Sprintf("internal error: %v", r))
				}
			}()
			handleRequest(req)
		}()
	}
}

func handleRequest(req RPCRequest) {
	switch req.Method {
	case "initialize":
		respond(req.ID, map[string]interface{}{"status": "ready", "name": "QuickDock Net Traffic", "pluginId": pluginID})
	case "host.ping":
		respond(req.ID, map[string]interface{}{"pong": true})
	case "shutdown":
		// 通知类消息不回包（与 database 一致：回 id=0 的 unknown method 会干扰宿主判定）
		return
	case "plugin.execute":
		handleExecute(req)
	default:
		respondError(req.ID, -32601, "unknown method: "+req.Method)
	}
}

func handleExecute(req RPCRequest) {
	var p struct {
		Command string                 `json:"command"`
		Input   map[string]interface{} `json:"input"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil {
		respondError(req.ID, -32602, "invalid params")
		return
	}
	switch {
	case strings.HasPrefix(p.Command, "traffic."):
		handleTraffic(req.ID, p.Command)
	case p.Command == "net-traffic":
		// 命令面板入口：宿主会直接拉起前端窗口，这里只需确认命令可用。
		respond(req.ID, map[string]interface{}{"ok": true})
	default:
		respondError(req.ID, -32601, "unknown command: "+p.Command)
	}
}

func handleTraffic(id int64, cmd string) {
	switch cmd {
	case "traffic.snapshot":
		respond(id, buildSnapshot())
	case "traffic.reset":
		resetStats()
		respond(id, map[string]interface{}{"ok": true})
	default:
		respondError(id, -32601, "unknown command: "+cmd)
	}
}

func respond(id int64, result interface{}) {
	defer qdFinishRequest(id, 0, "")
	data, _ := json.Marshal(RPCResponse{JSONRPC: "2.0", ID: id, Result: mustMarshal(result)})
	data = append(data, '\n')
	os.Stdout.Write(data)
}

func respondError(id int64, code int, msg string) {
	defer qdFinishRequest(id, code, msg)
	data, _ := json.Marshal(RPCResponse{JSONRPC: "2.0", ID: id, Error: &RPCError{Code: code, Message: msg}})
	data = append(data, '\n')
	os.Stdout.Write(data)
}

func mustMarshal(v interface{}) json.RawMessage {
	d, _ := json.Marshal(v)
	return d
}
