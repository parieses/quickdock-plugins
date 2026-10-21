package main

// app-manager —— 已安装应用管理（查看 / 定位 / 卸载）
//
// 数据来自注册表的 Uninstall 项（HKLM / HKCU / WOW6432Node 三处），这是
// Windows「应用和功能」与老版「程序和功能」的共同数据源。
//
// 卸载这件事刻意分成两类处理（权限模型决定的，不是偷懒）：
//   · MSI 安装的程序：卸载串是 MsiExec /X{GUID}，可执行文件是固定的 msiexec，
//     能进 process 白名单，插件直接调用；
//   · 自带卸载器的程序：卸载串是某个具体路径的 unins000.exe 之类，插件不可能
//     把「任意路径的可执行文件」写进白名单（那等于声明 process:true 全放行）。
//     这类只给命令复制 / 打开安装目录 / 跳转系统设置页，让用户自己按一下。
// 后端对执行入口做了二次校验：只认 MsiExec /X|/I {GUID} 这一种形态，
// 其余一律拒绝执行，避免任何形式的命令注入。

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

const pluginID = "io.github.parieses.app-manager"

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
					qdLogPanic("app-manager panic", r)
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
		respond(req.ID, map[string]interface{}{"status": "ready", "name": "QuickDock App Manager", "pluginId": pluginID})
	case "host.ping":
		respond(req.ID, map[string]interface{}{"pong": true})
	case "shutdown":
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
	input := p.Input
	// 命令面板带文本进来时（/app xxx），文本也要能当搜索词用
	if t, ok := input["text"].(string); ok && t != "" && input["keyword"] == nil {
		input["keyword"] = t
	}
	switch {
	case strings.HasPrefix(p.Command, "apps."):
		handleApps(req.ID, p.Command, input)
	case p.Command == "app-manager":
		respond(req.ID, map[string]interface{}{"ok": true})
	default:
		respondError(req.ID, -32601, "unknown command: "+p.Command)
	}
}

func handleApps(id int64, cmd string, input map[string]interface{}) {
	switch cmd {
	case "apps.list":
		handleList(id, input)
	case "apps.uninstall":
		handleUninstall(id, input)
	case "apps.openSettings":
		handleOpenSettings(id)
	case "apps.openDir":
		handleOpenDir(id, input)
	default:
		respondError(id, -32601, "unknown command: "+cmd)
	}
}

func getStr(input map[string]interface{}, key string) string {
	if v, ok := input[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}
func getBool(input map[string]interface{}, key string) bool {
	if v, ok := input[key].(bool); ok {
		return v
	}
	return false
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
