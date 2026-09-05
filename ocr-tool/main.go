package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
)

// ---- JSON-RPC structures ----

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

// ---- Execute params ----

type ExecuteParams struct {
	Command string                 `json:"command"`
	Input   map[string]interface{} `json:"input"`
}

// modelsDirCache 由 initialize 解析的 pluginDir 拼接 /models 得到，模型存储于此。
var modelsDirCache string

func main() {
	initLog()
	scanner := bufio.NewScanner(os.Stdin)
	// 图片经 base64 传入时可能很大（截图可达数 MB），放大缓冲到 64MB。
	scanner.Buffer(make([]byte, 1024*1024), 64*1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var req RPCRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			continue
		}

		handleRequest(req)
	}
	// stdin 关闭（宿主退出/卸载）时主循环结束，进程自然退出，避免成为孤儿进程。
}

func handleRequest(req RPCRequest) {
	logf("handleRequest method=%s paramsLen=%d", req.Method, len(req.Params))
	switch req.Method {
	case "initialize":
		handleInitialize(req)
	case "host.ping":
		respond(req.ID, map[string]interface{}{"pong": true})
	case "plugin.execute":
		handleExecute(req)
	default:
		respondError(req.ID, -32601, "unknown method: "+req.Method)
	}
}

// handleInitialize 握手：解析主程序传入的 pluginDir，派生模型目录。
func handleInitialize(req RPCRequest) {
	var p struct {
		PluginDir string `json:"pluginDir"`
	}
	if len(req.Params) > 0 {
		_ = json.Unmarshal(req.Params, &p)
	}
	if p.PluginDir != "" {
		modelsDirCache = filepath.Join(p.PluginDir, "models")
	} else if e, err := os.Executable(); err == nil {
		modelsDirCache = filepath.Join(filepath.Dir(e), "models")
	}
	logf("initialize: pluginDir=%q modelsDir=%q", p.PluginDir, modelsDirCache)
	respond(req.ID, map[string]interface{}{
		"status":   "ready",
		"name":     "QuickDock OCR Tool",
		"engine":   "paddle-ocr",
		"platform": runtime.GOOS,
	})
}

func handleExecute(req RPCRequest) {
	var params ExecuteParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		respondError(req.ID, -32602, "invalid params: "+err.Error())
		return
	}

	cmd := params.Command
	input := params.Input
	logf("handleExecute command=%s inputKeys=%v", cmd, keysOf(input))

	// 兼容前端 pluginExec 可能的打包格式：input = {text: JSON.stringify(实际参数)}
	// 自动解包，使各 handler 能直接访问 input["data"]、input["path"]、input["taskId"] 等
	if textRaw, ok := input["text"].(string); ok && textRaw != "" {
		var nested map[string]interface{}
		if strings.HasPrefix(textRaw, "{") || strings.HasPrefix(textRaw, "[") {
			if err := json.Unmarshal([]byte(textRaw), &nested); err == nil {
				for k, v := range nested {
					if _, exists := input[k]; !exists {
						input[k] = v
					}
				}
			}
		}
	}

	switch {
	case strings.HasPrefix(cmd, "ocr-"):
		handleOcrCommand(req.ID, cmd, input)
	default:
		respondError(req.ID, -32601, "unknown command: "+cmd)
	}
}

func respond(id int64, result interface{}) {
	data, _ := json.Marshal(RPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  mustMarshal(result),
	})
	logf("respond id=%d resultLen=%d", id, len(data))
	data = append(data, '\n')
	os.Stdout.Write(data)
}

func respondError(id int64, code int, msg string) {
	logf("respondError id=%d code=%d msg=%s", id, code, msg)
	data, _ := json.Marshal(RPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &RPCError{Code: code, Message: msg},
	})
	data = append(data, '\n')
	os.Stdout.Write(data)
}

func mustMarshal(v interface{}) json.RawMessage {
	data, _ := json.Marshal(v)
	return data
}

// keysOf 返回 map 的键列表（用于日志，避免打印敏感/过大内容）。
func keysOf(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// ---- hostLog：通过 stdout 向主程序发起 log.* 回调，由宿主落盘到 plugin-*.log ----

var hostReqID int64

func hostLog(level, format string, args ...interface{}) {
	id := atomic.AddInt64(&hostReqID, 1)
	msg, _ := json.Marshal(map[string]string{"message": fmt.Sprintf(format, args...)})
	sendJSON(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  "log." + level,
		"params":  json.RawMessage(msg), // 必须是 RawMessage，否则会被二次编码
	})
}

// sendJSON 写一行 JSON 到 stdout（JSON-RPC 主通道）。
func sendJSON(v interface{}) {
	data, _ := json.Marshal(v)
	data = append(data, '\n')
	os.Stdout.Write(data)
}
