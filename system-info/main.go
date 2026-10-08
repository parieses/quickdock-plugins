package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
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

func main() {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 256*1024), 256*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
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
					fmt.Fprintf(os.Stderr, "system-info panic: %v\n%s\n", r, debug.Stack())
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
		respond(req.ID, map[string]interface{}{"status": "ready", "name": "QuickDock System Info"})
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
	case strings.HasPrefix(p.Command, "system-info-"):
		handleSystemInfo(req.ID, p.Command, input)
	default:
		respondError(req.ID, -32601, "unknown command: "+p.Command)
	}
}

func handleSystemInfo(id int64, cmd string, input map[string]interface{}) {
	if cmd != "system-info-collect" {
		respondError(id, -32601, "unknown command")
		return
	}
	script := `
$os = Get-CimInstance Win32_OperatingSystem
$cpu = Get-CimInstance Win32_Processor | Select-Object -First 1
$mem = Get-CimInstance Win32_PhysicalMemory
$gpu = Get-CimInstance Win32_VideoController | Select-Object -First 1
$boot = $os.LastBootUpTime
$up = (Get-Date) - $boot
$totalRam = ($mem | Measure-Object -Property Capacity -Sum).Sum
$disks = Get-CimInstance Win32_LogicalDisk -Filter "DriveType=3" | ForEach-Object {
  [PSCustomObject]@{ device = $_.DeviceID; size = $_.Size; free = $_.FreeSpace }
}
$obj = [PSCustomObject]@{
  hostname = $env:COMPUTERNAME
  os = ($os.Caption).Trim()
  osVersion = $os.Version
  osBuild = $os.BuildNumber
  arch = $env:PROCESSOR_ARCHITECTURE
  uptimeDays = [math]::Round($up.TotalDays, 2)
  cpu = ($cpu.Name).Trim()
  cpuCores = $cpu.NumberOfCores
  cpuThreads = $cpu.NumberOfLogicalProcessors
  totalRam = $totalRam
  gpu = ($gpu.Name).Trim()
  disks = $disks
}
$obj | ConvertTo-Json -Depth 3 -Compress
`
	out, err := sysutil.PowerShell(script)
	if err != nil {
		respondError(id, -1, "采集失败: "+err.Error())
		return
	}
	out = strings.TrimSpace(out)
	var data map[string]interface{}
	if err := json.Unmarshal([]byte(out), &data); err != nil {
		msg := out
		if len(msg) > 200 {
			msg = msg[:200]
		}
		respondError(id, -1, "解析失败: "+msg)
		return
	}
	respond(id, data)
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
