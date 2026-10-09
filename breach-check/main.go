package main

import (
	"bufio"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime/debug"
	"strings"
	"time"
	"net/url"
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
					fmt.Fprintf(os.Stderr, "breach-check panic: %v\n%s\n", r, debug.Stack())
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
		respond(req.ID, map[string]interface{}{"status": "ready", "name": "QuickDock Breach Check"})
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
	case strings.HasPrefix(p.Command, "breach-"):
		handleBreach(req.ID, p.Command, input)
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

func httpGetUA(target string, headers map[string]string) ([]byte, int, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest("GET", target, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", "QuickDock-BreachCheck")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return body, resp.StatusCode, nil
}

func handleBreach(id int64, cmd string, input map[string]interface{}) {
	switch cmd {
	case "breach-password":
		handleBreachPassword(id, input)
	case "breach-email":
		handleBreachEmail(id, input)
	default:
		respondError(id, -32601, "unknown command: "+cmd)
	}
}

func handleBreachPassword(id int64, input map[string]interface{}) {
	pw := getStr(input, "password")
	if pw == "" {
		respondError(id, -32602, "请输入要检查的密码")
		return
	}
	sum := sha1.Sum([]byte(pw))
	hash := strings.ToUpper(hex.EncodeToString(sum[:]))
	prefix := hash[:5]
	suffix := hash[5:]

	// Pwned Passwords 的 range 接口在 api.pwnedpasswords.com（不是 api.haveibeenpwned.com），后者无此路径会返回 404。
	body, status, err := httpGetUA("https://api.pwnedpasswords.com/range/"+prefix, nil)
	if err != nil {
		respondError(id, -1, "查询失败: "+err.Error())
		return
	}
	if status != 200 {
		respondError(id, -1, fmt.Sprintf("查询失败 (HTTP %d)", status))
		return
	}
	breached := false
	count := 0
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(strings.ToUpper(line))
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		if parts[0] == suffix {
			breached = true
			fmt.Sscanf(parts[1], "%d", &count)
			break
		}
	}
	respond(id, map[string]interface{}{
		"breached": breached,
		"count":    count,
		"note":     "采用 k-匿名性：仅发送哈希前 5 位，密码原文不出本机",
	})
}

func handleBreachEmail(id int64, input map[string]interface{}) {
	email := getStr(input, "email")
	if email == "" {
		respondError(id, -32602, "请输入要检查的邮箱")
		return
	}
	apiKey := getStr(input, "apiKey")
	if apiKey == "" {
		respondError(id, -32602, "邮箱查询需要 HIBP API Key（haveibeenpwned.com 免费申请）。密码泄露可使用「密码检查」无需 Key。")
		return
	}
	target := "https://haveibeenpwned.com/api/v3/breachedaccount/" + url.PathEscape(email) + "?truncateResponse=false"
	body, status, err := httpGetUA(target, map[string]string{"hibp-api-key": apiKey})
	if err != nil {
		respondError(id, -1, "查询失败: "+err.Error())
		return
	}
	switch status {
	case 200:
		var breaches []map[string]interface{}
		if err := json.Unmarshal(body, &breaches); err != nil {
			respondError(id, -1, "解析失败")
			return
		}
		respond(id, map[string]interface{}{"breached": true, "breaches": breaches})
	case 404:
		respond(id, map[string]interface{}{"breached": false, "breaches": []interface{}{}})
	case 401:
		respondError(id, -1, "API Key 无效（HTTP 401）")
	case 403:
		respondError(id, -1, "需要有效的 API Key（HTTP 403）")
	case 429:
		respondError(id, -1, "请求过于频繁，请稍后再试（HTTP 429）")
	default:
		respondError(id, -1, fmt.Sprintf("查询失败 (HTTP %d)", status))
	}
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
