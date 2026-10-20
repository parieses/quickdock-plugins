// 目录探测 — 对指定目标用内置常见路径字典进行轻量探测（自用）
//
// 并发受限、可配扩展名；采用「start 立即返回 + 前端轮询」的异步会话模型，
// 扫描过程中实时返回命中的非 404 路径。仅探测用户授权的目标，内置字典、不递归。
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
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
	"debug", "debugbar", "trace", "errors", "exception", ".git/config", ".git/HEAD", ".svn/entries", ".hg/", ".well-known/security.txt", ".well-known/ai.txt",
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

// outMu 序列化 stdout 写入：每个请求在独立 goroutine 中 dispatch，
// 并发 fmt.Println 会交错输出、破坏 JSON-RPC 逐行协议。
var outMu sync.Mutex

func respond(id int64, result interface{}) {
	defer qdFinishRequest(id, 0, "")
	out, _ := json.Marshal(map[string]interface{}{"jsonrpc": "2.0", "id": id, "result": result})
	outMu.Lock()
	fmt.Println(string(out))
	outMu.Unlock()
}

func respondError(id int64, code int, msg string) {
	defer qdFinishRequest(id, code, msg)
	out, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0", "id": id,
		"error": map[string]interface{}{"code": code, "message": msg},
	})
	outMu.Lock()
	fmt.Println(string(out))
	outMu.Unlock()
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
	ID      string
	mu      sync.Mutex
	running bool
	stopCh  chan struct{}
	ctx     context.Context
	cancel  context.CancelFunc

	BaseURL string
	Method  string
	total   int
	scanned int
	found   []finding
	errMsg  string
	base    *baseLine // 软 404 基线（nil = 未检出，按严格状态码判断）
}

// ---- 软 404 基线 ----
//
// 现代站点（SPA / Next.js / 统一错误页 / 前置 WAF）对不存在的路径照样返回 200 + 同一张
// 页面。此时「非 404/400 即暴露」会把整个字典都判成命中，输出全是噪音，用户还得手工
// 一条条验。所以正式扫描前先打 3 个几乎不可能存在的随机路径，把 (状态码, 长度, 内容指纹)
// 记为基线；之后凡与基线同状态码且同指纹/同量级长度的响应，一律按「不存在」丢弃。
type baseLine struct {
	status int
	length int64
	fp     string
}

var numRe = regexp.MustCompile(`[0-9]+`)

// fingerprint 把响应体压成可比较的指纹：抹掉被回显的请求路径、把数字折叠成 0
// （时间戳 / 计数器 / 随机 ID 都会变）、去掉空白，取前 400 字符。
// 目标只有一个：同一张「软 404 页面」在不同随机路径下必须落到同一个指纹上。
func fingerprint(body []byte, path string) string {
	s := string(body)
	s = strings.ReplaceAll(s, path, "~P~")
	s = strings.ReplaceAll(s, strings.TrimPrefix(path, "/"), "~P~")
	s = numRe.ReplaceAllString(s, "0")
	s = strings.Join(strings.Fields(s), "")
	if len(s) > 400 {
		s = s[:400]
	}
	return s
}

// randPath 生成一个基本不可能真实存在的探测路径。
func randPath() string {
	b := make([]byte, 10)
	_, _ = rand.Read(b)
	return "/qd-probe-" + hex.EncodeToString(b)
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
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
		// soft404=true 表示识别出了「不存在的路径也返回 2xx/3xx」的统一错误页并据此过滤，
		// 前端据此提示用户「结果已剔除软 404」。基线本身就是 404/400 的站点不算软 404。
		"soft404": s.base != nil && s.base.status != 404 && s.base.status != 400,
	}
}

// fetchResult 一次探测请求的结果（body 只保留前缀，够做指纹即可）。
type fetchResult struct {
	status   int
	length   int64
	body     []byte
	location string
}

// doGet 发起一次探测请求，最多读 maxBytes 字节。
// length 优先取 Content-Length（chunked 时退回已读长度）。
func (s *session) doGet(client *http.Client, path string, maxBytes int64) (*fetchResult, error) {
	url := strings.TrimRight(s.BaseURL, "/") + path
	req, err := http.NewRequestWithContext(s.ctx, s.Method, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "QuickDock-DirBuster/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBytes))
	n := resp.ContentLength
	if n < 0 {
		n = int64(len(body)) // chunked / 无 Content-Length：用已读长度
	}
	return &fetchResult{
		status:   resp.StatusCode,
		length:   n,
		body:     body,
		location: resp.Header.Get("Location"),
	}, nil
}

// detectBaseline 用 3 个随机路径探测软 404 基线。
// 只有至少 2 个样本落到同一指纹时才启用（单个样本可能是偶发错误页/限流页，不足采信）。
// 任何一个样本网络失败都直接放弃基线：宁可多报几条，也不能因为基线错了把真命中吞掉。
func (s *session) detectBaseline(client *http.Client) *baseLine {
	type sample struct {
		status int
		length int64
		fp     string
	}
	var samples []sample
	for i := 0; i < 3; i++ {
		if !s.isRunning() {
			return nil
		}
		p := randPath()
		r, err := s.doGet(client, p, 64*1024)
		if err != nil {
			qdLogWarn("软404基线探测失败，按严格状态码判断 path=%s err=%v", p, err)
			return nil
		}
		fp := fingerprint(r.body, p)
		qdLogInfo("软404基线样本 path=%s status=%d len=%d fp=%s", p, r.status, r.length, fp[:minInt(len(fp), 60)])
		samples = append(samples, sample{r.status, r.length, fp})
	}
	if len(samples) < 2 {
		return nil
	}
	for _, a := range samples {
		same := 0
		for _, b := range samples {
			if a.status == b.status && a.fp == b.fp {
				same++
			}
		}
		if same >= 2 {
			qdLogInfo("软404基线 status=%d len=%d（%d/%d 随机路径指纹一致）",
				a.status, a.length, same, len(samples))
			return &baseLine{status: a.status, length: a.length, fp: a.fp}
		}
	}
	return nil
}

func (s *session) baseLine() *baseLine {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.base
}

func (s *session) run(candidates []string, timeoutMs int) {
	defer func() {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
	}()

	client := &http.Client{
		Timeout: time.Duration(timeoutMs) * time.Millisecond,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse // 不自动跟随，记录 3xx 作为命中
		},
	}

	// 先校准软 404，再开扫：基线错了整轮结果都是废的
	if b := s.detectBaseline(client); b != nil {
		s.mu.Lock()
		s.base = b
		s.mu.Unlock()
	}

	// 固定 worker 池消费候选队列。原实现是「每个候选起一个 goroutine 再抢信号量」，
	// 1 万候选等于 1 万个协程同时挂着等锁；这里改成 24 个 worker + channel，内存恒定。
	const workers = 24
	jobs := make(chan string)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					qdLogPanic("[panic] dir-buster worker", r)
				}
			}()
			for path := range jobs {
				if !s.isRunning() {
					return
				}
				s.probe(client, path)
				s.mu.Lock()
				s.scanned++
				s.mu.Unlock()
			}
		}()
	}

feed:
	for _, p := range candidates {
		select {
		case jobs <- p:
		case <-s.stopCh:
			break feed
		}
	}
	close(jobs)
	wg.Wait()
}

func (s *session) probe(client *http.Client, path string) {
	r, err := s.doGet(client, path, 64*1024)
	if err != nil {
		return
	}

	// 404/400 视为未命中
	if r.status == 404 || r.status == 400 {
		return
	}
	// 软 404：与基线同状态码，且指纹一致或长度几乎一样 → 这是「统一错误页」，不是命中。
	// 长度容差取 max(32 字节, 基线的 2%)，兜住页面里回显的路径长度差异。
	if b := s.baseLine(); b != nil && r.status == b.status {
		tol := b.length / 50
		if tol < 32 {
			tol = 32
		}
		if fingerprint(r.body, path) == b.fp || (b.length > 0 && abs64(r.length-b.length) <= tol) {
			return
		}
	}

	f := finding{
		Path:     path,
		Status:   r.status,
		Size:     r.length,
		Location: r.location,
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
		ID:      sid,
		running: true,
		stopCh:  make(chan struct{}),
		ctx:     ctx,
		cancel:  cancel,
		BaseURL: base,
		Method:  method,
		total:   len(candidates),
		scanned: 0,
		found:   []finding{},
	}
	sessions[sid] = s
	sessMu.Unlock()

	go func() {
		defer func() {
			if r := recover(); r != nil {
				qdLogPanic("[panic] dir-buster run", r)
				s.mu.Lock()
				s.errMsg = fmt.Sprintf("panic: %v", r)
				s.running = false
				s.mu.Unlock()
			}
		}()
		s.run(candidates, timeoutMs)
	}()

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
	qdLogStart()
	defer qdLogExit()
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
		if qdObserveLine(data) {
			continue
		}
		wg.Add(1)
		go func(raw string) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					qdLogPanic("[panic] dir-buster dispatch", r)
					var req rpcRequest
					if err := json.Unmarshal([]byte(raw), &req); err == nil && req.ID != 0 {
						respondError(req.ID, -32603, "internal error")
					}
				}
			}()
			dispatch(raw)
		}(data)
	}
	wg.Wait()
}
