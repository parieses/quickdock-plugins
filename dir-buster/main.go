// 目录探测 — 对指定目标用内置常见路径字典进行轻量探测（自用）
//
// 并发受限、可配扩展名；采用「start 立即返回 + 前端轮询」的异步会话模型，
// 扫描过程中实时返回命中的非 404 路径。仅探测用户授权的目标，内置字典、不递归。
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// 内置常见路径字典（自用，非攻击用途）。覆盖管理后台、API、配置文件、备份等典型暴露面。
var wordlist = []string{
	"admin", "admin.php", "admin/login", "admin/login.php", "admin/index.php", "admin.html",
	"login", "login.php", "login.html", "signin", "signin.php", "auth", "auth/login",
	"wp-admin", "wp-login.php", "xmlrpc.php", "wp-content", "wp-includes", "wp-config.php",
	"phpmyadmin", "phpMyAdmin", "pma", "adminer", "sqladmin", "myadmin",
	"dashboard", "panel", "cpanel", "webadmin", "manager", "manage", "console",
	"api", "api/v1", "api/v2", "api/v3", "apis", "graphql", "swagger", "swagger-ui", "swagger.json", "openapi.json", "docs", "doc", "redoc",
	"robots.txt", "sitemap.xml", "sitemap_index.xml", "favicon.ico", "crossdomain.xml",
	".env", ".env.backup", ".env.local", ".env.dev", "config", "config.php", "config.json", "config.yaml", "config.ini",
	"settings", "settings.php", "setup", "setup.php", "install", "install.php", "installer", "upgrade", "phpinfo.php", "info.php",
	"backup", "backups", "backup.zip", "backup.sql", "db", "db.sql", "database", "database.sql", "data.sql", "dump.sql", "sql", "sql.bak",
	"test", "tests", "dev", "development", "staging", "beta", "old", "new", "tmp", "temp", "cache", "tmp/", "temp/",
	"upload", "uploads", "uploader", "files", "file", "download", "downloads", "assets", "static", "images", "img", "css", "js", "media", "public",
	"vendor", "storage", "storage/logs", "logs", "log", "logs/", "var", "var/log", "app", "src", "server-status", "status", "health", "healthz", "ping", "metrics",
	"debug", "debugbar", "trace", " error", "errors", "exception", ".git/config", ".git/HEAD", ".svn/entries", ".hg/", ".well-known/security.txt", ".well-known/ai.txt",
	"web.config", ".htaccess", ".htpasswd", "nginx.conf", "users", "user", "account", "profile", "member", "members", "user/login", "register", "registration",
	"mail", "mailer", "smtp", "ftp", "ssh", "rdp", "vpn", "proxy", "jenkins", "gitlab", "grafana", "kibana", "prometheus", "elasticsearch", "solr", "redis", "rabbitmq",
	"actuator", "actuator/health", "jmx", "console/", "vendor/phpunit", "composer.json", "package.json", "yarn.lock", "readme", "readme.html", "readme.md", "LICENSE", "CHANGELOG",
}

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

func strFrom(m map[string]interface{}, k string) string {
	if v, ok := m[k].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func intFrom(m map[string]interface{}, k string, def int) int {
	switch v := m[k].(type) {
	case float64:
		n := int(v)
		if n > 0 {
			return n
		}
	case string:
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 0 {
			return n
		}
	}
	return def
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

/* ==================== 会话 ==================== */

type finding struct {
	Path     string `json:"path"`
	Status   int    `json:"status"`
	Size     int64  `json:"size"`
	Location string `json:"location,omitempty"`
	Method   string `json:"method"`
}

type session struct {
	ID   string
	mu   sync.Mutex
	running bool
	stopCh  chan struct{}
	ctx     context.Context
	cancel  context.CancelFunc

	BaseURL  string
	Method   string
	total    int
	scanned  int
	found    []finding
	errMsg   string
}

var (
	sessMu   sync.Mutex
	sessions = map[string]*session{}
	seqID    int
)

func (s *session) isRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

func (s *session) snapshot() map[string]interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	found := make([]finding, len(s.found))
	copy(found, s.found)
	return map[string]interface{}{
		"sessionId": s.ID,
		"running":   s.running,
		"baseUrl":   s.BaseURL,
		"total":     s.total,
		"scanned":   s.scanned,
		"found":     found,
		"error":     s.errMsg,
	}
}

func (s *session) run(candidates []string, timeoutMs int) {
	sem := make(chan struct{}, 24)
	var wg sync.WaitGroup
	client := &http.Client{
		Timeout: time.Duration(timeoutMs) * time.Millisecond,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse // 不自动跟随，记录 3xx 作为命中
		},
	}

	for _, p := range candidates {
		if !s.isRunning() {
			break
		}
		wg.Add(1)
		go func(path string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			s.probe(client, path)
			s.mu.Lock()
			s.scanned++
			s.mu.Unlock()
		}(p)
	}
	wg.Wait()
	s.mu.Lock()
	s.running = false
	s.mu.Unlock()
}

func (s *session) probe(client *http.Client, path string) {
	url := strings.TrimRight(s.BaseURL, "/") + path
	req, err := http.NewRequestWithContext(s.ctx, s.Method, url, nil)
	if err != nil {
		return
	}
	req.Header.Set("User-Agent", "QuickDock-DirBuster/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 512*1024))

	// 404/400 视为未命中；其余（2xx/3xx/403/401/500 等）视为暴露
	if resp.StatusCode == 404 || resp.StatusCode == 400 {
		return
	}

	location := resp.Header.Get("Location")
	f := finding{
		Path:     path,
		Status:   resp.StatusCode,
		Size:     resp.ContentLength,
		Location: location,
		Method:   s.Method,
	}

	s.mu.Lock()
	for _, x := range s.found {
		if x.Path == path {
			s.mu.Unlock()
			return // 去重
		}
	}
	s.found = append(s.found, f)
	s.mu.Unlock()
}

/* ==================== 命令处理 ==================== */

func handleStart(id int64, input map[string]interface{}) {
	base := strFrom(input, "url")
	if base == "" {
		respondError(id, -32602, "请输入目标 URL（如 https://example.com）")
		return
	}
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		respondError(id, -32602, "URL 必须以 http:// 或 https:// 开头")
		return
	}

	method := strings.ToUpper(strFrom(input, "method"))
	if method == "" {
		method = "GET"
	}
	timeoutMs := intFrom(input, "timeout", 4000)
	if timeoutMs > 15000 {
		timeoutMs = 15000
	}

	// 扩展名
	extInput := strFrom(input, "extensions")
	var exts []string
	if extInput == "" {
		exts = []string{"", "php", "html", "asp", "jsp"}
	} else {
		for _, e := range strings.Split(extInput, ",") {
			exts = append(exts, strings.TrimSpace(e))
		}
	}

	// 构建候选路径
	candidates := []string{}
	for _, w := range wordlist {
		base2 := w
		if !strings.HasPrefix(base2, "/") {
			base2 = "/" + base2
		}
		for _, ext := range exts {
			p := base2
			if ext != "" {
				p = p + "." + ext
			}
			candidates = append(candidates, p)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	sessMu.Lock()
	seqID++
	sid := fmt.Sprintf("d%d", seqID)
	s := &session{
		ID:       sid,
		running:  true,
		stopCh:   make(chan struct{}),
		ctx:      ctx,
		cancel:   cancel,
		BaseURL:  base,
		Method:   method,
		total:    len(candidates),
		scanned:  0,
		found:    []finding{},
	}
	sessions[sid] = s
	sessMu.Unlock()

	go s.run(candidates, timeoutMs)

	respond(id, map[string]interface{}{
		"sessionId": sid,
		"baseUrl":   base,
		"total":     len(candidates),
		"running":   true,
	})
}

func getSession(id int64, input map[string]interface{}) *session {
	sid := strFrom(input, "sessionId")
	if sid == "" {
		respondError(id, -32602, "缺少 sessionId")
		return nil
	}
	sessMu.Lock()
	s := sessions[sid]
	sessMu.Unlock()
	if s == nil {
		respondError(id, -32002, "会话不存在或已结束")
		return nil
	}
	return s
}

func handlePoll(id int64, input map[string]interface{}) {
	s := getSession(id, input)
	if s == nil {
		return
	}
	respond(id, s.snapshot())
}

func handleStop(id int64, input map[string]interface{}) {
	s := getSession(id, input)
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.running {
		s.running = false
		close(s.stopCh)
	}
	s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
	}

	snap := s.snapshot()
	sessMu.Lock()
	delete(sessions, s.ID)
	sessMu.Unlock()
	snap["stopped"] = true
	respond(id, snap)
}

func dispatch(raw string) {
	var req rpcRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		respondError(0, -32700, "parse error: "+err.Error())
		return
	}
	switch req.Method {
	case "initialize":
		respond(req.ID, map[string]interface{}{"status": "ready", "name": "QuickDock Directory Buster"})
	case "host.ping":
		respond(req.ID, map[string]interface{}{"pong": true})
	case "plugin.execute":
		var params executeParams
		if len(req.Params) > 0 {
			_ = json.Unmarshal(req.Params, &params)
		}
		switch strings.ToLower(strings.TrimSpace(params.Command)) {
		case "start":
			handleStart(req.ID, params.Input)
		case "poll":
			handlePoll(req.ID, params.Input)
		case "stop":
			handleStop(req.ID, params.Input)
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
