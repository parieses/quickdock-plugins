package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"runtime/debug"
	"strings"

	"system-tools/sysutil"
)

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

var nameRe = regexp.MustCompile(`^[A-Za-z0-9\-_.]+$`)

func main() {
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
		var req RPCRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			continue
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					fmt.Fprintf(os.Stderr, "service-manager panic: %v\n%s\n", r, debug.Stack())
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
		respond(req.ID, map[string]interface{}{"status": "ready", "name": "QuickDock Service Manager"})
	case "host.ping":
		respond(req.ID, map[string]interface{}{"pong": true})
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
	if t, ok := input["text"].(string); ok && t != "" {
		if strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[") {
			var nested map[string]interface{}
			if err := json.Unmarshal([]byte(t), &nested); err == nil {
				for k, v := range nested {
					if _, e := input[k]; !e {
						input[k] = v
					}
				}
			}
		}
	}
	switch {
	case strings.HasPrefix(p.Command, "service-"):
		handleService(req.ID, p.Command, input)
	default:
		respondError(req.ID, -32601, "unknown command: "+p.Command)
	}
}

func getStr(input map[string]interface{}, key string) string {
	if v, ok := input[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func handleService(id int64, cmd string, input map[string]interface{}) {
	switch cmd {
	case "service-list":
		handleServiceList(id, input)
	case "service-action":
		handleServiceAction(id, input)
	default:
		respondError(id, -32601, "unknown command: "+cmd)
	}
}

func handleServiceList(id int64, input map[string]interface{}) {
	filter := strings.ToLower(getStr(input, "filter"))
	// 显式输出小驼峰属性名，与前端读取的 s.name / s.displayName / s.status / s.startType 对齐。
	script := `
Get-Service | Select-Object @{Name='name';Expression={$_.Name}},@{Name='displayName';Expression={$_.DisplayName}},@{Name='status';Expression={$_.Status.ToString()}},@{Name='startType';Expression={$_.StartType.ToString()}} | ConvertTo-Json -Compress -Depth 2
`
	out, err := sysutil.PowerShell(script)
	if err != nil {
		respondError(id, -1, "枚举失败: "+err.Error())
		return
	}
	out = strings.TrimSpace(out)
	var all []map[string]interface{}
	if err := json.Unmarshal([]byte(out), &all); err != nil {
		respondError(id, -1, "解析失败")
		return
	}
	if filter != "" {
		kept := all[:0]
		for _, s := range all {
			name, _ := s["name"].(string)
			disp, _ := s["displayName"].(string)
			if strings.Contains(strings.ToLower(name), filter) || strings.Contains(strings.ToLower(disp), filter) {
				kept = append(kept, s)
			}
		}
		all = kept
	}
	respond(id, map[string]interface{}{"services": all})
}

func handleServiceAction(id int64, input map[string]interface{}) {
	name := getStr(input, "name")
	action := getStr(input, "action")
	if !nameRe.MatchString(name) {
		respondError(id, -32602, "非法的服务名")
		return
	}
	if action != "start" && action != "stop" && action != "restart" {
		respondError(id, -32602, "非法的操作")
		return
	}
	script := fmt.Sprintf(`
$name = '%s'
$action = '%s'
try {
  $svc = Get-Service -Name $name -ErrorAction Stop
  switch ($action) {
    'start'   { $svc.Start() }
    'stop'    { $svc.Stop() }
    'restart' { $svc.Stop(); Start-Sleep -Seconds 1; $svc.Start() }
  }
  Start-Sleep -Seconds 1
  $svc.Refresh()
  [PSCustomObject]@{ name = $svc.Name; displayName = $svc.DisplayName; status = $svc.Status.ToString() } | ConvertTo-Json -Compress
} catch {
  Write-Error $_.Exception.Message
  exit 1
}
`, name, action)
	out, err := sysutil.PowerShell(script)
	if err != nil {
		respondError(id, -1, "操作失败: "+err.Error())
		return
	}
	out = strings.TrimSpace(out)
	var res map[string]interface{}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		respondError(id, -1, "解析失败")
		return
	}
	respond(id, res)
}

func respond(id int64, result interface{}) {
	data, _ := json.Marshal(RPCResponse{JSONRPC: "2.0", ID: id, Result: mustMarshal(result)})
	data = append(data, '\n')
	os.Stdout.Write(data)
}
func respondError(id int64, code int, msg string) {
	data, _ := json.Marshal(RPCResponse{JSONRPC: "2.0", ID: id, Error: &RPCError{Code: code, Message: msg}})
	data = append(data, '\n')
	os.Stdout.Write(data)
}
func mustMarshal(v interface{}) json.RawMessage {
	d, _ := json.Marshal(v)
	return d
}
