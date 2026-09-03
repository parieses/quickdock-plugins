// Login Tester — 登录爆破/撞库安全自检（原生 JSON-RPC 子进程，标准库）
//
// 对自身网站登录接口进行密码库测试，验证是否存在弱口令 / 缺少限速 / 无账号锁定等风险。
// 仅用于你拥有或已授权的站点。
//
// 命令（均走 plugin.execute，params.command 路由）：
//
//	test.start     校验配置并启动后台 worker 池，立即返回 sessionId + 总数（宿主 execute 有 20s 超时，故用异步会话模型）
//	test.poll      按 sessionId 返回进度快照（已完成/总数/命中/速率/锁定检测）
//	test.stop      停止指定会话
//	meta.builtin   返回内置常见密码库数量与示例
//
// 成功判定（successMode）：
//
//	failKeyword     响应体「不包含」某关键词即视为成功（最常用，填登录失败文案如"密码错误"）
//	successKeyword  响应体「包含」某关键词即视为成功
//	statusEquals    状态码等于指定值（如 302 跳转成功页）
//	redirectContains Location 头包含指定字符串
//	cookieContains  Set-Cookie 包含指定字符串（如 sessionid）
package main

import (
	"context"
	"bufio"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

/* ==================== RPC 基础 ==================== */

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

func strSliceFrom(m map[string]interface{}, key string) []string {
	if v, ok := m[key].([]interface{}); ok {
		out := []string{}
		for _, x := range v {
			if s, ok := x.(string); ok {
				s = strings.TrimSpace(s)
				if s != "" {
					out = append(out, s)
				}
			}
		}
		return out
	}
	return nil
}

func strMapFrom(m map[string]interface{}, key string) map[string]string {
	out := map[string]string{}
	if v, ok := m[key].(map[string]interface{}); ok {
		for k, val := range v {
			if s, ok := val.(string); ok {
				out[k] = s
			}
		}
	}
	return out
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

func boolFrom(m map[string]interface{}, k string, def bool) bool {
	if v, ok := m[k].(bool); ok {
		return v
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

/* ==================== 内置密码库 ==================== */

func builtinPasswords() []string {
	return []string{
		"123456", "password", "12345678", "qwerty", "123456789", "12345", "1234", "111111",
		"1234567", "dragon", "123123", "baseball", "abc123", "football", "monkey", "letmein",
		"696969", "shadow", "master", "666666", "qwertyuiop", "123321", "mustang", "1234567890",
		"michael", "654321", "superman", "1qaz2wsx", "7777777", "121212", "000000", "qazwsx",
		"123qwe", "killer", "trustno1", "jordan", "jennifer", "zxcvbnm", "asdfgh", "hunter",
		"buster", "soccer", "harley", "batman", "andrew", "tigger", "sunshine", "iloveyou",
		"2000", "charlie", "robert", "thomas", "hockey", "ranger", "daniel", "starwars",
		"klaster", "112233", "george", "computer", "michelle", "jessica", "pepper", "1111",
		"zxcvbn", "555555", "11111111", "131313", "freedom", "777777", "pass", "maggie",
		"159357", "aaaaaa", "ginger", "princess", "joshua", "cheese", "amanda", "summer",
		"love", "ashley", "nicole", "chelsea", "biteme", "matthew", "access", "yankees",
		"987654321", "dallas", "austin", "thunder", "taylor", "matrix", "mobilemail", "mom",
		"monitor", "monitoring", "montana", "moon", "moscow", "admin", "administrator", "root",
		"toor", "user", "test", "guest", "info", "adm", "mysql", "oracle", "postgres", "ftp",
		"pw123", "password1", "password123", "qwerty123", "abc123456", "1q2w3e4r", "aa123456",
		"woaini", "123abc", "a123456", "qq123456", "wang123", "liwei", "zhangsan", "123456a",
		"1qazxsw2", "qweasd", "passw0rd", "p@ssw0rd", "P@ssw0rd", "Passw0rd", "Password1",
		"Password123", "Abcd1234", "abcd1234", "Qwerty123", "1Q2W3E4R", "q1w2e3r4",
	}
}

/* ==================== 会话状态 ==================== */

type attemptResult struct {
	User     string `json:"user"`
	Pass     string `json:"pass"`
	Status   int    `json:"status"`
	Success  bool   `json:"success"`
	Location  string `json:"location,omitempty"`
	SetCookie string `json:"setCookie,omitempty"`
	Body      string `json:"body,omitempty"`
	Note      string `json:"note,omitempty"`
	Ms        int64  `json:"ms"`
}

type csrfCfg struct {
	Enabled     bool   `json:"enabled"`
	URL         string `json:"url"`
	Regex       string `json:"regex"`
	Field       string `json:"field"`
	Header      string `json:"header"`
	PerRequest  bool   `json:"perRequest"`
}

type testConfig struct {
	URL             string            `json:"url"`
	Method          string            `json:"method"`
	UserField       string            `json:"userField"`
	PassField       string            `json:"passField"`
	Users           []string          `json:"users"`
	PassList        []string          `json:"passList"`
	UseBuiltin      bool              `json:"useBuiltin"`
	ExtraFields     map[string]string `json:"extraFields"`
	Headers         map[string]string `json:"headers"`
	Cookie          string            `json:"cookie"`
	UserAgent       string            `json:"userAgent"`
	SuccessMode     string            `json:"successMode"`
	FailKeyword     string            `json:"failKeyword"`
	SuccessKeyword  string            `json:"successKeyword"`
	SuccessStatus   int               `json:"successStatus"`
	SuccessLocation string            `json:"successLocation"`
	SuccessCookie   string            `json:"successCookie"`
	Threads         int               `json:"threads"`
	DelayMs         int               `json:"delayMs"`
	MaxRequests     int               `json:"maxRequests"`
	TimeoutMs       int               `json:"timeoutMs"`
	FollowRedirects bool              `json:"followRedirects"`
	CSRF            csrfCfg           `json:"csrf"`
}

type session struct {
	mu          sync.Mutex
	id          string
	total       int
	done        int
	startedAt   time.Time
	status      string // running | done | stopped | error
	found       []attemptResult
	lastResults []attemptResult
	lockout     bool
	lockoutNote string
	failStreak  int
	reqErrStreak int
	errMsg      string
	stop        chan struct{}
	stopOnce    sync.Once
	cfg         testConfig
}

var (
	sessions   = map[string]*session{}
	sessionsMu sync.Mutex
	seq        int64
)

func newSessionID() string {
	seq++
	return fmt.Sprintf("lt_%d_%d", time.Now().UnixNano(), seq)
}

/* ==================== 测试执行 ==================== */

func fetchCSRFToken(cfg testConfig) (string, error) {
	if cfg.CSRF.URL == "" || cfg.CSRF.Regex == "" {
		return "", fmt.Errorf("csrf 配置不完整")
	}
	client := &http.Client{Timeout: 8 * time.Second}
	req, err := http.NewRequestWithContext(context.Background(), "GET", cfg.CSRF.URL, nil)
	if err != nil {
		return "", err
	}
	if cfg.UserAgent != "" {
		req.Header.Set("User-Agent", cfg.UserAgent)
	}
	if cfg.Cookie != "" {
		req.Header.Set("Cookie", cfg.Cookie)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	re, err := regexp.Compile(cfg.CSRF.Regex)
	if err != nil {
		return "", err
	}
	m := re.FindStringSubmatch(string(body))
	if len(m) >= 2 {
		return m[1], nil
	}
	if len(m) == 1 {
		return m[0], nil
	}
	return "", fmt.Errorf("未匹配到 CSRF token")
}

func splitKeywords(s string) []string {
	parts := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\t'
	})
	out := []string{}
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func evalSuccess(cfg testConfig, status int, body, location, setCookie string) bool {
	switch cfg.SuccessMode {
	case "successKeyword":
		for _, k := range splitKeywords(cfg.SuccessKeyword) {
			if strings.Contains(body, k) {
				return true
			}
		}
		return false
	case "statusEquals":
		return status == cfg.SuccessStatus
	case "redirectContains":
		return strings.Contains(location, cfg.SuccessLocation)
	case "cookieContains":
		return strings.Contains(setCookie, cfg.SuccessCookie)
	case "failKeyword":
		fallthrough
	default:
		ks := splitKeywords(cfg.FailKeyword)
		if len(ks) == 0 {
			return false
		}
		for _, k := range ks {
			if strings.Contains(body, k) {
				return false
			}
		}
		return true
	}
}

func (s *session) attempt(user, pass string) attemptResult {
	cfg := s.cfg
	fields := map[string]string{}
	for k, v := range cfg.ExtraFields {
		fields[k] = v
	}
	fields[cfg.UserField] = user
	fields[cfg.PassField] = pass

	var csrfToken string
	if cfg.CSRF.Enabled {
		if t, err := fetchCSRFToken(cfg); err == nil {
			csrfToken = t
			if cfg.CSRF.Field != "" {
				fields[cfg.CSRF.Field] = csrfToken
			}
		}
	}

	client := &http.Client{
		Timeout: time.Duration(cfg.TimeoutMs) * time.Millisecond,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if !cfg.FollowRedirects {
				return http.ErrUseLastResponse
			}
			if len(via) >= 10 {
				return fmt.Errorf("stopped after 10 redirects")
			}
			return nil
		},
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
	}

	var req *http.Request
	var err error
	if strings.EqualFold(cfg.Method, "GET") {
		q := url.Values{}
		for k, v := range fields {
			q.Set(k, v)
		}
		u := cfg.URL
		if strings.Contains(u, "?") {
			u += "&"
		} else {
			u += "?"
		}
		u += q.Encode()
		req, err = http.NewRequestWithContext(context.Background(), "GET", u, nil)
	} else {
		form := url.Values{}
		for k, v := range fields {
			form.Set(k, v)
		}
		req, err = http.NewRequestWithContext(context.Background(), "POST", cfg.URL, strings.NewReader(form.Encode()))
		if err == nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
	}
	if err != nil {
		return attemptResult{User: user, Pass: pass, Success: false, Note: "构造请求失败: " + err.Error(), Ms: 0}
	}

	for k, v := range cfg.Headers {
		req.Header.Set(k, v)
	}
	if cfg.CSRF.Enabled && cfg.CSRF.Header != "" && csrfToken != "" {
		req.Header.Set(cfg.CSRF.Header, csrfToken)
	}
	if cfg.Cookie != "" {
		req.Header.Set("Cookie", cfg.Cookie)
	}
	if cfg.UserAgent != "" {
		req.Header.Set("User-Agent", cfg.UserAgent)
	}

	start := time.Now()
	resp, rerr := client.Do(req)
	ms := time.Since(start).Milliseconds()
	if rerr != nil {
		return attemptResult{User: user, Pass: pass, Success: false, Status: 0, Note: rerr.Error(), Ms: ms}
	}
	defer resp.Body.Close()
	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	bodyStr := string(bodyBytes)
	if runes := []rune(bodyStr); len(runes) > 500 {
		bodyStr = string(runes[:500]) + "…"
	}
	location := resp.Header.Get("Location")
	setCookie := strings.Join(resp.Header.Values("Set-Cookie"), "; ")

	success := evalSuccess(cfg, resp.StatusCode, bodyStr, location, setCookie)
	return attemptResult{
		User: user, Pass: pass, Status: resp.StatusCode, Success: success,
		Location: location, SetCookie: setCookie, Body: bodyStr, Ms: ms,
	}
}

func (s *session) record(r attemptResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.done++
	s.lastResults = append(s.lastResults, r)
	if len(s.lastResults) > 50 {
		s.lastResults = s.lastResults[len(s.lastResults)-50:]
	}
	if r.Success {
		s.found = append(s.found, r)
		s.failStreak = 0
		s.reqErrStreak = 0
	} else {
		if r.Status == 0 {
			s.reqErrStreak++
		} else {
			s.reqErrStreak = 0
		}
		s.failStreak++
		// 疑似账号锁定 / 限流：连续 20 次失败且出现限流特征
		lower := strings.ToLower(r.Note + " " + r.Location)
		if strings.Contains(lower, "429") || strings.Contains(lower, "locked") ||
			strings.Contains(lower, "lock") || strings.Contains(lower, "too many") ||
			strings.Contains(lower, "频率") || strings.Contains(lower, "锁定") ||
			strings.Contains(lower, "频繁") || strings.Contains(lower, "验证码") {
			if !s.lockout {
				s.lockout = true
				s.lockoutNote = fmt.Sprintf("第 %d 次尝试出现限流/锁定特征：%s", s.done, r.Note)
			}
		}
	}
}

func (s *session) snapshot() map[string]interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	elapsed := int64(time.Since(s.startedAt).Milliseconds())
	rate := 0.0
	if elapsed > 0 {
		rate = float64(s.done) / (float64(elapsed) / 1000.0)
	}
	last := make([]attemptResult, len(s.lastResults))
	copy(last, s.lastResults)
	found := make([]attemptResult, len(s.found))
	copy(found, s.found)
	return map[string]interface{}{
		"id":          s.id,
		"status":      s.status,
		"total":       s.total,
		"done":        s.done,
		"found":       found,
		"foundCount":  len(found),
		"lastResults": last,
		"ratePerSec":  rate,
		"elapsedMs":   elapsed,
		"lockout":     s.lockout,
		"lockoutNote": s.lockoutNote,
		"errMsg":      s.errMsg,
	}
}

func (s *session) stopped() bool {
	select {
	case <-s.stop:
		return true
	default:
		return false
	}
}

func runSession(s *session) {
	defer func() {
		s.mu.Lock()
		if s.status == "running" {
			s.status = "done"
		}
		s.mu.Unlock()
	}()

	jobs := make(chan [2]string, s.total)
	for _, u := range s.cfg.Users {
		for _, p := range s.cfg.PassList {
			jobs <- [2]string{u, p}
		}
	}
	close(jobs)

	var wg sync.WaitGroup
	threads := s.cfg.Threads
	if threads < 1 {
		threads = 1
	}
	for i := 0; i < threads; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-s.stop:
					return
				case job, ok := <-jobs:
					if !ok {
						return
					}
					r := s.attempt(job[0], job[1])
					s.record(r)
					s.mu.Lock()
					capped := s.cfg.MaxRequests > 0 && s.done >= s.cfg.MaxRequests
					s.mu.Unlock()
					if capped {
						s.stopOnce.Do(func() { close(s.stop) })
						return
					}
					if s.cfg.DelayMs > 0 {
						select {
						case <-time.After(time.Duration(s.cfg.DelayMs) * time.Millisecond):
						case <-s.stop:
							return
						}
					}
				}
			}
		}()
	}
	wg.Wait()
}

func handleStart(id int64, input map[string]interface{}) {
	cfg := testConfig{
		URL:             strFrom(input, "url"),
		Method:          strings.ToUpper(strFrom(input, "method")),
		UserField:       strFrom(input, "userField"),
		PassField:       strFrom(input, "passField"),
		Users:           strSliceFrom(input, "users"),
		PassList:        strSliceFrom(input, "passList"),
		UseBuiltin:      boolFrom(input, "useBuiltin", false),
		ExtraFields:     strMapFrom(input, "extraFields"),
		Headers:         strMapFrom(input, "headers"),
		Cookie:          strFrom(input, "cookie"),
		UserAgent:       strFrom(input, "userAgent"),
		SuccessMode:     strFrom(input, "successMode"),
		FailKeyword:     strFrom(input, "failKeyword"),
		SuccessKeyword:  strFrom(input, "successKeyword"),
		SuccessStatus:   intFrom(input, "successStatus", 0),
		SuccessLocation: strFrom(input, "successLocation"),
		SuccessCookie:   strFrom(input, "successCookie"),
		Threads:         intFrom(input, "threads", 5),
		DelayMs:         intFrom(input, "delayMs", 0),
		MaxRequests:     intFrom(input, "maxRequests", 0),
		TimeoutMs:       intFrom(input, "timeoutMs", 10000),
		FollowRedirects: boolFrom(input, "followRedirects", false),
	}
	if csrf, ok := input["csrf"].(map[string]interface{}); ok {
		cfg.CSRF = csrfCfg{
			Enabled:    boolFrom(csrf, "enabled", false),
			URL:        strFrom(csrf, "url"),
			Regex:      strFrom(csrf, "regex"),
			Field:      strFrom(csrf, "field"),
			Header:     strFrom(csrf, "header"),
			PerRequest: boolFrom(csrf, "perRequest", false),
		}
	}

	// 校验
	if cfg.URL == "" {
		respondError(id, -32602, "请输入登录接口 URL")
		return
	}
	if !strings.HasPrefix(cfg.URL, "http://") && !strings.HasPrefix(cfg.URL, "https://") {
		respondError(id, -32602, "URL 必须以 http:// 或 https:// 开头")
		return
	}
	if cfg.Method == "" {
		cfg.Method = "POST"
	}
	if cfg.UserField == "" {
		cfg.UserField = "username"
	}
	if cfg.PassField == "" {
		cfg.PassField = "password"
	}
	if len(cfg.Users) == 0 {
		single := strFrom(input, "user")
		if single == "" {
			respondError(id, -32602, "请输入至少一个测试账号（user 或 users）")
			return
		}
		cfg.Users = []string{single}
	}
	if cfg.UseBuiltin {
		cfg.PassList = builtinPasswords()
	}
	if len(cfg.PassList) == 0 {
		respondError(id, -32602, "请输入密码库（passList）或勾选使用内置常见密码库")
		return
	}
	if cfg.SuccessMode == "" {
		cfg.SuccessMode = "failKeyword"
	}
	if cfg.SuccessMode == "failKeyword" && cfg.FailKeyword == "" {
		respondError(id, -32602, "失败关键词判定方式需要填写「失败文案」（如：密码错误）")
		return
	}

	total := len(cfg.Users) * len(cfg.PassList)
	if cfg.MaxRequests > 0 && cfg.MaxRequests < total {
		total = cfg.MaxRequests
	}

	s := &session{
		id:        newSessionID(),
		total:     total,
		startedAt: time.Now(),
		status:    "running",
		stop:      make(chan struct{}),
		cfg:       cfg,
	}

	sessionsMu.Lock()
	sessions[s.id] = s
	sessionsMu.Unlock()

	go runSession(s)

	respond(id, map[string]interface{}{
		"ok":     true,
		"id":     s.id,
		"total":  s.total,
		"status": "running",
	})
}

func handlePoll(id int64, input map[string]interface{}) {
	sid := strFrom(input, "id")
	if sid == "" {
		// 兼容直接传 sessionId 字段
		sid = strFrom(input, "sessionId")
	}
	sessionsMu.Lock()
	s := sessions[sid]
	sessionsMu.Unlock()
	if s == nil {
		respondError(id, -32602, "会话不存在或已结束: "+sid)
		return
	}
	respond(id, s.snapshot())
}

func handleStop(id int64, input map[string]interface{}) {
	sid := strFrom(input, "id")
	if sid == "" {
		sid = strFrom(input, "sessionId")
	}
	sessionsMu.Lock()
	s := sessions[sid]
	sessionsMu.Unlock()
	if s == nil {
		respondError(id, -32602, "会话不存在: "+sid)
		return
	}
	s.stopOnce.Do(func() { close(s.stop) })
	s.mu.Lock()
	s.status = "stopped"
	s.mu.Unlock()
	respond(id, map[string]interface{}{"ok": true, "id": sid, "status": "stopped"})
}

func handleBuiltin(id int64, input map[string]interface{}) {
	list := builtinPasswords()
	sample := []string{}
	for i, p := range list {
		if i >= 10 {
			break
		}
		sample = append(sample, p)
	}
	respond(id, map[string]interface{}{
		"count":  len(list),
		"sample": sample,
	})
}

/* ==================== RPC 分发 ==================== */

func dispatch(raw string) {
	var req rpcRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		respondError(0, -32700, "parse error: "+err.Error())
		return
	}
	switch req.Method {
	case "initialize":
		respond(req.ID, map[string]interface{}{"status": "ready", "name": "QuickDock Login Tester"})
	case "host.ping":
		respond(req.ID, map[string]interface{}{"pong": true})
	case "plugin.execute":
		var params executeParams
		if len(req.Params) > 0 {
			_ = json.Unmarshal(req.Params, &params)
		}
		switch strings.ToLower(strings.TrimSpace(params.Command)) {
		case "test.start":
			handleStart(req.ID, params.Input)
		case "test.poll":
			handlePoll(req.ID, params.Input)
		case "test.stop":
			handleStop(req.ID, params.Input)
		case "meta.builtin":
			handleBuiltin(req.ID, params.Input)
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
