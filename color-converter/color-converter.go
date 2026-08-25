// Color Converter - 颜色转换 + 屏幕取色
// JSON-RPC 2.0 over stdin/stdout
//
// 协议对齐 QuickDock 宿主约定：
//   - initialize      启动握手，必须尽快响应，否则超时判定加载失败
//   - host.ping       健康检查
//   - plugin.execute  唯一业务入口，params = {command, input}
//
// 命令：
//   - screen-pick     采样鼠标光标当前位置的屏幕像素颜色，返回 {hex, r, g, b}
//                     倒计时由前端控制：点按钮 → 3 秒倒数 → 调本命令，
//                     用户利用间隙把鼠标移到目标颜色上。
//
// 注意：宿主 Manager.ExecuteCommand 固定发送 "plugin.execute"，
// 前端 postMessage 的 command 落在 params.command。

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
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

var stdout = bufio.NewWriter(os.Stdout)

// stdoutMu 保护 stdout：宿主请求处理与插件主动回调可能并发写出，
// JSON-RPC 每行一个对象，交错写入会损坏协议
var stdoutMu sync.Mutex

// cbSeq 插件主动回调的自增 id，从大数起步与宿主请求 id 空间隔离
var cbSeq int64 = 1_000_000_000

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
		if req.Method == "" {
			// 无 method 的帧 = 宿主对插件回调请求（host.*）的响应，fire-and-forget，静默丢弃
			continue
		}
		handleRequest(req)
	}
}

// hostCall 向宿主发起回调请求（如 host.clipboard.write / host.notify）。
// 不等待响应：结果帧由主循环静默丢弃。
func hostCall(method string, params map[string]interface{}) {
	payload, err := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      cbSeq,
		"method":  method,
		"params":  params,
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
		respond(req.ID, map[string]interface{}{"status": "ready", "name": "QuickDock Color Tools"})
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

// execute 分发命令。command 兼容点号写法（内部统一替换为 -）。
func execute(p executeParams) (map[string]interface{}, string) {
	command := strings.ReplaceAll(p.Command, ".", "-")

	switch command {
	case "screen-pick", "pick-color":
		// 同步取色：立即采样鼠标当前位置（保留用于冒烟测试）
		r, g, b, err := pickScreenColor()
		if err != nil {
			return nil, err.Error()
		}
		return pickResult(r, g, b, false), ""
	case "screen-pick-start":
		// 进入热键等待模式：后台 goroutine 监听 F8 取色 / ESC 取消，60s 自动超时。
		// epoch 防竞态：重复 start 时旧 goroutine 检测到代数变化自动退出。
		pickMu.Lock()
		pickEpoch++
		myEpoch := pickEpoch
		pickState = pickSession{Status: "running"}
		pickMu.Unlock()
		go waitPickLoop(myEpoch)
		return map[string]interface{}{"started": true}, ""
	case "pick-status":
		pickMu.Lock()
		defer pickMu.Unlock()
		switch pickState.Status {
		case "done":
			return map[string]interface{}{
				"status": "done",
				"hex":    fmt.Sprintf("#%02x%02x%02x", pickState.R, pickState.G, pickState.B),
				"r":      pickState.R,
				"g":      pickState.G,
				"b":      pickState.B,
				"copied": pickState.Copied,
			}, ""
		default:
			return map[string]interface{}{"status": pickState.Status}, ""
		}
	case "screen-pick-cancel":
		pickMu.Lock()
		epoch := pickEpoch
		pickMu.Unlock()
		markPick(epoch, "cancelled", 0, 0, 0, false)
		return map[string]interface{}{"ok": true}, ""
	case "backend-ping": // 供前端探测后端可用性
		return map[string]interface{}{"ok": true}, ""
	default:
		return nil, "unknown command: " + p.Command
	}
}

// ---- 热键等待模式的状态 ----

type pickSession struct {
	Status string // running | done | cancelled | error
	R, G, B uint8
	Copied bool
	ErrMsg string
}

var (
	pickMu     sync.Mutex
	pickState  pickSession
	pickEpoch  int
)

// markPick 终态写入；epoch 不匹配说明已被新一轮 start 重置，丢弃本次结果
func markPick(epoch int, status string, r, g, b uint8, copied bool) {
	pickMu.Lock()
	defer pickMu.Unlock()
	if epoch != pickEpoch {
		return
	}
	pickState = pickSession{Status: status, R: r, G: g, B: b, Copied: copied}
}

func markPickErr(epoch int, msg string) {
	pickMu.Lock()
	defer pickMu.Unlock()
	if epoch != pickEpoch {
		return
	}
	pickState = pickSession{Status: "error", ErrMsg: msg}
}

func pickResult(r, g, b uint8, copied bool) map[string]interface{} {
	return map[string]interface{}{
		"hex":    fmt.Sprintf("#%02x%02x%02x", r, g, b),
		"r":      r,
		"g":      g,
		"b":      b,
		"copied": copied,
	}
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
// writeResponse 统一出口：成功与失败都必须 Flush，
// 否则响应滞留在 bufio 缓冲区，宿主收不到任何字节，只会等到超时。
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
