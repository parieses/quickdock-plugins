package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime/debug"
	"strings"
	"time"
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
					fmt.Fprintf(os.Stderr, "geoip panic: %v\n%s\n", r, debug.Stack())
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
		respond(req.ID, map[string]interface{}{"status": "ready", "name": "QuickDock GeoIP"})
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
	case strings.HasPrefix(p.Command, "geoip-"):
		handleGeoip(req.ID, p.Command, input)
	default:
		respondError(req.ID, -32601, "unknown command: "+p.Command)
	}
}

func httpGet(url string) ([]byte, error) {
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func handleGeoip(id int64, cmd string, input map[string]interface{}) {
	if cmd != "geoip-lookup" {
		respondError(id, -32601, "unknown geoip command")
		return
	}
	ip := ""
	if v, ok := input["ip"].(string); ok {
		ip = strings.TrimSpace(v)
	}
	url := "http://ip-api.com/json"
	if ip != "" {
		url = "http://ip-api.com/json/" + ip
	}
	data, err := httpGet(url)
	if err != nil {
		respondError(id, -1, "查询失败: "+err.Error())
		return
	}
	var res map[string]interface{}
	if err := json.Unmarshal(data, &res); err != nil {
		respondError(id, -1, "解析失败")
		return
	}
	if s, _ := res["status"].(string); s == "fail" {
		respondError(id, -1, fmt.Sprintf("%v", res["message"]))
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
