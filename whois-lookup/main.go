// WHOIS Lookup - 查询域名/IP 的 WHOIS 注册信息（原生 JSON-RPC 子进程，标准库 net）
// 命令：lookup  input {query}   （支持域名或 IP；域名自动经 IANA 定位权威 whois 服务器）
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"time"
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type executeParams struct {
	Command string                 `json:"command"`
	Input   map[string]interface{} `json:"input"`
}

func strFrom(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func respond(id int64, result interface{}) {
	out, _ := json.Marshal(map[string]interface{}{"jsonrpc": "2.0", "id": id, "result": result})
	fmt.Println(string(out))
}

func respondError(id int64, code int, msg string) {
	out, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0", "id": id,
		"error": map[string]interface{}{"code": code, "message": msg},
	})
	fmt.Println(string(out))
}

// whoisQuery 连接 whois 服务器（TCP 43）并发送查询，返回原始文本
func whoisQuery(server, query string) (string, error) {
	conn, err := net.DialTimeout("tcp", server+":43", 10*time.Second)
	if err != nil {
		return "", fmt.Errorf("连接 %s 失败: %w", server, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(12 * time.Second))

	if _, err := conn.Write([]byte(query + "\r\n")); err != nil {
		return "", fmt.Errorf("发送查询失败: %w", err)
	}
	buf, err := io.ReadAll(conn)
	if err != nil && len(buf) == 0 {
		return "", fmt.Errorf("读取响应失败: %w", err)
	}
	return string(buf), nil
}

// parseReferral 从 IANA 响应中提取权威 whois 服务器（whois: 行）
func parseReferral(text string) string {
	for _, line := range strings.Split(text, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToLower(t), "whois:") {
			val := strings.TrimSpace(t[len("whois:"):])
			if val != "" {
				return val
			}
		}
	}
	return ""
}

func normalizeQuery(raw string) string {
	q := strings.TrimSpace(raw)
	q = strings.TrimSuffix(q, ".")
	q = strings.TrimRight(q, "/")
	if i := strings.Index(q, "://"); i >= 0 {
		q = q[i+3:]
	}
	if i := strings.Index(q, "/"); i >= 0 {
		q = q[:i]
	}
	if strings.HasPrefix(strings.ToLower(q), "www.") {
		q = q[4:]
	}
	return strings.ToLower(q)
}

func handleLookup(id int64, input map[string]interface{}) {
	raw := strings.TrimSpace(strFrom(input, "query"))
	if raw == "" {
		respondError(id, -32602, "请输入域名或 IP")
		return
	}
	query := normalizeQuery(raw)

	var server string
	if net.ParseIP(query) != nil {
		server = "whois.arin.net"
	} else {
		// 1) 先问 IANA 拿到权威 whois 服务器
		iana, err := whoisQuery("whois.iana.org", query)
		if err != nil {
			respondError(id, -1, err.Error())
			return
		}
		if ref := parseReferral(iana); ref != "" {
			server = ref
		} else {
			// 部分 ccTLD 无薄 WHOIS，直接返回 IANA 信息
			respond(id, map[string]interface{}{
				"ok":       true,
				"query":    query,
				"server":   "whois.iana.org",
				"raw":      strings.TrimSpace(iana),
				"truncated": false,
			})
			return
		}
	}

	text, err := whoisQuery(server, query)
	if err != nil {
		respondError(id, -1, err.Error())
		return
	}
	text = strings.TrimSpace(text)
	truncated := false
	const maxLen = 16000
	if len(text) > maxLen {
		text = text[:maxLen] + "\n…（结果已截断）"
		truncated = true
	}

	respond(id, map[string]interface{}{
		"ok":        true,
		"query":     query,
		"server":    server,
		"raw":       text,
		"truncated": truncated,
	})
}

func dispatch(raw string) {
	var req rpcRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		respondError(0, -32700, "parse error: "+err.Error())
		return
	}
	switch req.Method {
	case "initialize":
		respond(req.ID, map[string]interface{}{"status": "ready", "name": "QuickDock WHOIS Lookup"})
	case "host.ping":
		respond(req.ID, map[string]interface{}{"pong": true})
	case "plugin.execute":
		var params executeParams
		if len(req.Params) > 0 {
			_ = json.Unmarshal(req.Params, &params)
		}
		switch strings.ToLower(strings.TrimSpace(params.Command)) {
		case "lookup":
			handleLookup(req.ID, params.Input)
		default:
			respondError(req.ID, -32601, "unknown command: "+params.Command)
		}
	default:
		respondError(req.ID, -32601, "unknown method: "+req.Method)
	}
}

func main() {
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
		wg.Add(1)
		go func(raw string) {
			defer wg.Done()
			dispatch(raw)
		}(data)
	}
	wg.Wait()
}
