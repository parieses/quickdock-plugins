// API Mock - 本地 HTTP 接口 Mock 服务（native 插件，真实监听端口）
// JSON-RPC 2.0 over stdin/stdout（与 package-check 同协议）
// 命令：
//   start       {port}            启动本地 mock 服务（默认 8787），返回监听地址
//   stop                         停止服务
//   status                       返回 {running, address, routeCount}
//   get-routes                   返回当前路由表
//   set-routes   {routes:[...]}   整体替换路由表（来自前端编辑）
//   get-logs                     返回近期请求日志（环形缓冲，最多 200 条）

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

// ---- 数据模型 ----

type Route struct {
	ID          string `json:"id"`
	Method      string `json:"method"`      // GET/POST/PUT/DELETE/PATCH/ANY
	Path        string `json:"path"`        // 精确匹配，或以 /* 结尾表示前缀匹配，或 * 匹配全部
	StatusCode  int    `json:"status"`      // 默认 200
	ContentType string `json:"contentType"` // 默认 application/json
	Body        string `json:"body"`
	DelayMs     int    `json:"delay"`
	Enabled     bool   `json:"enabled"`
}

type LogEntry struct {
	Time    string `json:"time"`
	Method  string `json:"method"`
	Path    string `json:"path"`
	Matched string `json:"matched"` // 命中的路由 ID，未命中为 "-"
	Status  int    `json:"status"`
}

// ---- 全局状态（长驻子进程）----

var mu sync.RWMutex
var routes []Route
var srv *http.Server
var logBuf []LogEntry
var logSeq int
var listenAddr string

const maxLog = 200

func init() {
	routes = []Route{
		{ID: "demo-get", Method: "GET", Path: "/api/hello", StatusCode: 200, ContentType: "application/json", Body: `{"message":"hello from mock","endpoint":"/api/hello"}`, Enabled: true},
		{ID: "demo-post", Method: "POST", Path: "/api/echo", StatusCode: 200, ContentType: "application/json", Body: `{"received":true,"hint":"请求体原样透传请改用代理模式"}`, Enabled: true},
	}
}

// ---- HTTP 服务 ----

func pathMatch(pattern, path string) bool {
	if pattern == "*" {
		return true
	}
	if strings.HasSuffix(pattern, "/*") {
		prefix := strings.TrimSuffix(pattern, "/*")
		if prefix == "" {
			return true
		}
		return strings.HasPrefix(path, prefix+"/") || path == prefix
	}
	return pattern == path
}

func handler(w http.ResponseWriter, r *http.Request) {
	mu.RLock()
	cp := make([]Route, len(routes))
	copy(cp, routes)
	mu.RUnlock()

	var matched *Route
	for i := range cp {
		rt := &cp[i]
		if !rt.Enabled {
			continue
		}
		if rt.Method != "ANY" && !strings.EqualFold(rt.Method, r.Method) {
			continue
		}
		if pathMatch(rt.Path, r.URL.Path) {
			matched = rt
			break
		}
	}

	status := 404
	ct := "application/json"
	body := `{"error":"no matching route","path":"` + r.URL.Path + `"}`
	if matched != nil {
		if matched.DelayMs > 0 {
			time.Sleep(time.Duration(matched.DelayMs) * time.Millisecond)
		}
		status = matched.StatusCode
		if status == 0 {
			status = 200
		}
		ct = matched.ContentType
		if ct == "" {
			ct = "application/json"
		}
		body = matched.Body
	}

	w.Header().Set("Content-Type", ct)
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "*")
	w.Header().Set("Access-Control-Allow-Headers", "*")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))

	mid := "-"
	if matched != nil {
		mid = matched.ID
	}
	appendLog(r.Method, r.URL.Path, mid, status)
}

func appendLog(method, path, matched string, status int) {
	mu.Lock()
	defer mu.Unlock()
	logSeq++
	entry := LogEntry{
		Time:    time.Now().Format("15:04:05"),
		Method:  method,
		Path:    path,
		Matched: matched,
		Status:  status,
	}
	logBuf = append(logBuf, entry)
	if len(logBuf) > maxLog {
		logBuf = logBuf[len(logBuf)-maxLog:]
	}
}

func startServer(port int) {
	mu.Lock()
	if srv != nil {
		_ = srv.Close()
		srv = nil
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", handler)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	srv = &http.Server{Addr: addr, Handler: mux}
	listenAddr = "http://" + addr
	mu.Unlock()

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("[api-mock] server error: %v", err)
		}
	}()
}

func stopServer() {
	mu.Lock()
	if srv != nil {
		_ = srv.Close()
		srv = nil
	}
	listenAddr = ""
	mu.Unlock()
}

func isRunning() bool {
	mu.RLock()
	defer mu.RUnlock()
	return srv != nil
}

// ---- JSON-RPC ----

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

func strFrom(input map[string]interface{}, key string) string {
	if v, ok := input[key].(string); ok {
		return v
	}
	return ""
}

func handleExec(id int64, cmd string, input map[string]interface{}) {
	switch cmd {
	case "start":
		port := 8787
		if p, ok := input["port"].(float64); ok && p > 0 {
			port = int(p)
		}
		startServer(port)
		mu.RLock()
		addr := listenAddr
		mu.RUnlock()
		respond(id, map[string]interface{}{"running": true, "address": addr, "port": port})
	case "stop":
		stopServer()
		respond(id, map[string]interface{}{"stopped": true})
	case "status":
		mu.RLock()
		addr := listenAddr
		n := len(routes)
		mu.RUnlock()
		respond(id, map[string]interface{}{"running": isRunning(), "address": addr, "routeCount": n})
	case "get-routes":
		mu.RLock()
		cp := make([]Route, len(routes))
		copy(cp, routes)
		mu.RUnlock()
		respond(id, map[string]interface{}{"routes": cp})
	case "set-routes":
		var newRoutes []Route
		if arr, ok := input["routes"].([]interface{}); ok {
			for _, item := range arr {
				if m, ok := item.(map[string]interface{}); ok {
					rt := Route{Enabled: true, StatusCode: 200, ContentType: "application/json"}
					if v, ok := m["id"].(string); ok {
						rt.ID = v
					}
					if v, ok := m["method"].(string); ok {
						rt.Method = v
					}
					if v, ok := m["path"].(string); ok {
						rt.Path = v
					}
					if v, ok := m["status"].(float64); ok {
						rt.StatusCode = int(v)
					}
					if v, ok := m["contentType"].(string); ok {
						rt.ContentType = v
					}
					if v, ok := m["body"].(string); ok {
						rt.Body = v
					}
					if v, ok := m["delay"].(float64); ok {
						rt.DelayMs = int(v)
					}
					if v, ok := m["enabled"].(bool); ok {
						rt.Enabled = v
					}
					if rt.ID == "" {
						rt.ID = fmt.Sprintf("r%d", time.Now().UnixNano())
					}
					newRoutes = append(newRoutes, rt)
				}
			}
		}
		mu.Lock()
		routes = newRoutes
		mu.Unlock()
		respond(id, map[string]interface{}{"ok": true, "count": len(newRoutes)})
	case "get-logs":
		mu.RLock()
		cp := make([]LogEntry, len(logBuf))
		copy(cp, logBuf)
		mu.RUnlock()
		respond(id, map[string]interface{}{"logs": cp})
	default:
		respondError(id, -32601, "unknown command: "+cmd)
	}
}

func dispatch(raw string) {
	var req rpcRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		respondError(0, -32700, "parse error: "+err.Error())
		return
	}
	switch req.Method {
	case "initialize":
		respond(req.ID, map[string]interface{}{"status": "ready", "name": "QuickDock API Mock"})
	case "host.ping":
		respond(req.ID, map[string]interface{}{"pong": true})
	case "plugin.execute":
		var params executeParams
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &params); err != nil {
				respondError(req.ID, -32602, "invalid params: "+err.Error())
				return
			}
		}
		handleExec(req.ID, strings.ToLower(strings.TrimSpace(params.Command)), params.Input)
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
			defer func() {
				if r := recover(); r != nil {
					fmt.Fprintf(os.Stderr, "[api-mock][panic:dispatch] %v\n%s\n", r, debug.Stack())
					var req rpcRequest
					if json.Unmarshal([]byte(raw), &req) == nil {
						respondError(req.ID, -32603, fmt.Sprintf("internal error: %v", r))
					}
				}
			}()
			dispatch(raw)
		}(data)
	}
	wg.Wait()
}
