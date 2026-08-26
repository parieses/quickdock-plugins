// Mail Check - 邮箱足迹与有效性查询
// JSON-RPC 2.0 over stdin/stdout (native 插件协议)
//
// 命令：
//   sites       同步：返回支持的探测站点清单（前端渲染用）
//   check       异步：输入 email，并发执行站点足迹探测 + 邮箱有效性检查（语法/MX/SMTP）
//   trace       同步：快速摘要（语法+MX+在线探测），命令面板/无前端场景用
//   task-status 异步任务进度轮询
//
// 探测引擎：
//   - 站点规则表驱动：sites.json（go:embed）按 holehe 模块归纳生成，
//     每站 = 预热取令牌(可选) + GET/POST 探测 + 判定规则(状态码/响应文本)
//   - 结果五态：found / not_found / unknown / error / skip
//   - 全量 122 站来自 megadose/holehe（GitHub 热榜），复杂站标记 needs-review，
//     可在线网段探测的站已校准，网络不可达时统一归 unknown，绝不误报。

package main

import (
	"bufio"
	"context"
	"crypto/md5"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"regexp"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows/registry"
)

// ---- 站点配置（go:embed，随 exe 打包）----

//go:embed sites.json
var sitesJSON []byte

type judgment struct {
	IfStatusIn []int  `json:"ifStatusIn,omitempty"` // 响应状态码命中
	IfContains string `json:"ifContains,omitempty"` // 响应文本包含
	IfJson     string `json:"ifJson,omitempty"`     // 响应 JSON 字段点路径（如 data.is_available / authType.0）
	JsonValue  any    `json:"jsonValue,omitempty"`  // ifJson 期望值（bool/string/number）
	Result     string `json:"result"`               // found | not_found | unknown
}

type warmup struct {
	URL   string `json:"url"`
	Regex string `json:"regex"`
}

type siteConfig struct {
	Key       string            `json:"key"`
	Name      string            `json:"name"`
	NameEn    string            `json:"nameEn,omitempty"`
	Category  string            `json:"category"`
	URL       string            `json:"url,omitempty"`
	Desc      string            `json:"desc,omitempty"`
	Status    string            `json:"status,omitempty"` // adapted | needs-review | skip
	Engine    string            `json:"engine,omitempty"` // gravatar 等专用探测器
	Method    string            `json:"method,omitempty"` // GET | POST（默认 GET）
	ProbeURL  string            `json:"probeUrl"`
	Warmup    *warmup           `json:"warmup,omitempty"`
	BodyType  string            `json:"bodyType,omitempty"` // form | json
	Body      string            `json:"body,omitempty"`     // {email}/{domain}/{token} 占位
	RandomUser bool             `json:"randomUser,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Rules     []judgment        `json:"rules"`
	Default   string            `json:"default,omitempty"`
}

var siteList []siteConfig

func init() {
	_ = json.Unmarshal(sitesJSON, &siteList)
}

// ---- JSON-RPC 结构 ----

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type executeParams struct {
	Command string                 `json:"command"`
	Input   map[string]interface{} `json:"input"`
}

var (
	writeMu sync.Mutex
	stdout  = bufio.NewWriter(os.Stdout)
)

func respond(id int64, payload interface{}) {
	b, err := json.Marshal(payload)
	if err != nil {
		respondError(id, -32603, "internal error: "+err.Error())
		return
	}
	resp := rpcResponse{JSONRPC: "2.0", ID: id, Result: b}
	writeMu.Lock()
	_ = json.NewEncoder(stdout).Encode(resp)
	_ = stdout.Flush()
	writeMu.Unlock()
}

func respondError(id int64, code int, msg string) {
	resp := rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}}
	writeMu.Lock()
	_ = json.NewEncoder(stdout).Encode(resp)
	_ = stdout.Flush()
	writeMu.Unlock()
}

func strFrom(input map[string]interface{}, key string) string {
	if input == nil {
		return ""
	}
	if v, ok := input[key].(string); ok {
		return v
	}
	return ""
}

// ---- 代理支持 ----
// Go http.Client 默认只认 HTTP_PROXY/HTTPS_PROXY 环境变量，不读 Windows 系统代理。
// 这里补上：环境变量优先，否则读注册表 Internet Settings 的系统代理
//（Clash/V2ray 等"系统代理"模式写入的位置），让境外探测站也能走代理可达。
func systemProxyURL() *url.URL {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Internet Settings`, registry.QUERY_VALUE)
	if err != nil {
		return nil
	}
	defer k.Close()
	enable, _, err := k.GetIntegerValue("ProxyEnable")
	if err != nil || enable == 0 {
		return nil
	}
	srv, _, err := k.GetStringValue("ProxyServer")
	if err != nil || strings.TrimSpace(srv) == "" {
		return nil
	}
	// ProxyServer 形如 "127.0.0.1:7890" 或 "http=127.0.0.1:7890;https=127.0.0.1:7890"
	for _, part := range strings.Split(srv, ";") {
		p := strings.TrimSpace(part)
		if strings.HasPrefix(p, "http=") || strings.HasPrefix(p, "https=") {
			p = p[strings.Index(p, "=")+1:]
		}
		if p == "" {
			continue
		}
		if !strings.Contains(p, "://") {
			p = "http://" + p
		}
		if u, err := url.Parse(p); err == nil && u.Host != "" {
			return u
		}
	}
	return nil
}

// newProbeClient 构造探测用 HTTP 客户端：显式环境变量代理优先，其次 Windows 系统代理。
func newProbeClient(timeout time.Duration) *http.Client {
	jar, _ := cookiejar.New(nil)
	sysURL := systemProxyURL()
	proxyFn := http.ProxyFromEnvironment
	if sysURL != nil {
		proxyFn = func(req *http.Request) (*url.URL, error) {
			if u, err := http.ProxyFromEnvironment(req); err == nil && u != nil {
				return u, nil
			}
			return sysURL, nil
		}
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = proxyFn
	return &http.Client{Timeout: timeout, Jar: jar, Transport: tr}
}

// ---- 异步任务 ----

type asyncTask struct {
	ID       string                 `json:"id"`
	Status   string                 `json:"status"`
	Message  string                 `json:"message,omitempty"`
	Result   map[string]interface{} `json:"result,omitempty"`
	Error    string                 `json:"error,omitempty"`
	Progress *checkProgress         `json:"-"`
	finished time.Time
}

// checkProgress 运行中的增量进度：每个站点探测完成即追加（task-status 轮询时下发 partial）
type checkProgress struct {
	mu       sync.Mutex
	done     int                     // 已完成站点数
	validity *validityResult         // 有效性检测结果（可能为 nil）
	probes   []map[string]interface{} // 已完成站点（按完成顺序）
}

var (
	tasksMu sync.Mutex
	tasks   = make(map[string]*asyncTask)
	taskSeq int64
)

const taskTTL = 30 * time.Minute

func startTask() *asyncTask {
	tasksMu.Lock()
	defer tasksMu.Unlock()
	now := time.Now()
	for id, t := range tasks {
		if t.Status != "running" && now.Sub(t.finished) > taskTTL {
			delete(tasks, id)
		}
	}
	taskSeq++
	t := &asyncTask{ID: fmt.Sprintf("mc-%d", taskSeq), Status: "running"}
	tasks[t.ID] = t
	return t
}

func getTask(id string) (*asyncTask, bool) {
	tasksMu.Lock()
	defer tasksMu.Unlock()
	t, ok := tasks[id]
	return t, ok
}

func finishTask(t *asyncTask, result map[string]interface{}, err error) {
	tasksMu.Lock()
	defer tasksMu.Unlock()
	t.finished = time.Now()
	if err != nil {
		t.Status = "error"
		t.Error = err.Error()
		t.Message = "检测失败"
	} else {
		t.Status = "done"
		t.Result = result
	}
}

// ---- 表驱动探测引擎 ----

type probeStatus string

const (
	statusFound    probeStatus = "found"
	statusNotFound probeStatus = "not_found"
	statusUnknown  probeStatus = "unknown"
	statusError    probeStatus = "error"
	statusSkip     probeStatus = "skip"
)

func statusText(st probeStatus) string {
	switch st {
	case statusFound:
		return "平台返回占用/重置提示，判定已注册"
	case statusNotFound:
		return "平台返回未注册提示"
	case statusUnknown:
		return "无法判定（网络/站点响应异常）"
	case statusError:
		return "探测出错"
	case statusSkip:
		return "站点未适配（接口复杂，需人工校准）"
	}
	return string(st)
}

// fillTpl 替换 {email} {domain} {token} 占位。
func fillTpl(s string, email, domain, token string, escape bool) string {
	e := email
	if escape {
		e = url.QueryEscape(email)
	}
	s = strings.ReplaceAll(s, "{email}", e)
	s = strings.ReplaceAll(s, "{domain}", url.QueryEscape(domain))
	s = strings.ReplaceAll(s, "{token}", token)
	return s
}

var randSrc = rand.New(rand.NewSource(time.Now().UnixNano()))

func randomUsername() string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	n := 6 + randSrc.Intn(10)
	b := make([]byte, n)
	for i := range b {
		b[i] = chars[randSrc.Intn(len(chars))]
	}
	return string(b)
}

// probeSite 执行单个站点的规则探测。
func probeSite(ctx context.Context, client *http.Client, s siteConfig, email, domain string) (probeStatus, string) {
	if s.Engine == "gravatar" {
		return probeGravatar(ctx, client, email)
	}
	if s.Status == "skip" || s.ProbeURL == "" {
		return statusSkip, statusText(statusSkip)
	}

	// 预热：拉取页面提取令牌（如 CSRF / authenticity_token）
	token := ""
	if s.Warmup != nil && s.Warmup.URL != "" {
		wreq, err := http.NewRequestWithContext(ctx, http.MethodGet, s.Warmup.URL, nil)
		if err == nil {
			wreq.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 QuickDock/1.0")
			wresp, werr := client.Do(wreq)
			if werr == nil {
				body, _ := io.ReadAll(io.LimitReader(wresp.Body, 4<<20))
				wresp.Body.Close()
				if s.Warmup.Regex != "" {
					re, rerr := regexp.Compile(s.Warmup.Regex)
					if rerr == nil {
						if m := re.FindStringSubmatch(string(body)); len(m) > 1 {
							token = m[1]
						}
					}
				}
			}
		}
	}

	// 构造探测请求
	escape := s.BodyType != "json"
	probeURL := fillTpl(s.ProbeURL, email, domain, token, true)
	body := fillTpl(s.Body, email, domain, token, escape)
	if s.RandomUser {
		body = strings.ReplaceAll(body, "{username}", randomUsername())
	}

	var bodyReader io.Reader
	if s.Method != "GET" && body != "" {
		bodyReader = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, s.MethodOrGet(), probeURL, bodyReader)
	if err != nil {
		return statusError, statusText(statusError)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 QuickDock/1.0")
	req.Header.Set("Accept", "*/*")
	if s.BodyType == "json" {
		req.Header.Set("Content-Type", "application/json")
	} else if body != "" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, v := range s.Headers {
		req.Header.Set(k, fillTpl(v, email, domain, token, false))
	}

	resp, err := client.Do(req)
	if err != nil {
		// 网络层失败 → unknown（"网络不可达"），不判 error 吓人
		return statusUnknown, "网络不可达（超时/连接失败）"
	}
	code := resp.StatusCode
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	resp.Body.Close()

	// 按序判定
	for _, r := range s.Rules {
		match := false
		if len(r.IfStatusIn) > 0 {
			for _, c := range r.IfStatusIn {
				if code == c {
					match = true
					break
				}
			}
		}
		if !match && r.IfContains != "" {
			match = strings.Contains(string(respBody), r.IfContains)
		}
		if !match && r.IfJson != "" {
			var obj any
			if json.Unmarshal(respBody, &obj) == nil {
				v := jsonPath(obj, r.IfJson)
				match = jsonEquals(v, r.JsonValue)
			}
		}
		if match {
			return probeStatus(r.Result), statusText(probeStatus(r.Result))
		}
	}
	if s.Default != "" {
		return probeStatus(s.Default), statusText(probeStatus(s.Default))
	}
	return statusUnknown, fmt.Sprintf("HTTP %d 无匹配规则", code)
}

func (s siteConfig) MethodOrGet() string {
	if s.Method == "" {
		return http.MethodGet
	}
	return strings.ToUpper(s.Method)
}

// jsonPath 按点路径取 JSON 值：支持 map 键与数组下标（如 data.is_available、authType.0）。
func jsonPath(obj any, path string) any {
	cur := obj
	for _, part := range strings.Split(path, ".") {
		switch m := cur.(type) {
		case map[string]any:
			cur = m[part]
		case []any:
			if idx, err := strconv.Atoi(part); err == nil && idx >= 0 && idx < len(m) {
				cur = m[idx]
			} else {
				return nil
			}
		default:
			return nil
		}
		if cur == nil {
			return nil
		}
	}
	return cur
}

// jsonEquals 比较 JSON 路径取值与期望值（bool/string/number）。
func jsonEquals(v any, want any) bool {
	switch w := want.(type) {
	case bool:
		b, ok := v.(bool)
		return ok && b == w
	case string:
		s, ok := v.(string)
		return ok && s == w
	case float64:
		f, ok := v.(float64)
		return ok && f == w
	}
	return false
}

// gravatarHosts 备用域名：secure → www → 裸域 依次尝试，防单点超时。
var gravatarHosts = []string{"https://secure.gravatar.com/", "https://www.gravatar.com/", "https://gravatar.com/"}

// probeGravatar 通过 md5(email).json 头像 API 判定：200=存在档案 / 404=未发现。
// 网络超时 / 任一主域不可达时归 unknown（不误报 error）。
func probeGravatar(ctx context.Context, client *http.Client, email string) (probeStatus, string) {
	sum := md5.Sum([]byte(strings.ToLower(strings.TrimSpace(email))))
	h := hex.EncodeToString(sum[:])
	var lastErr error
	for _, host := range gravatarHosts {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, host+h+".json", nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", "QuickDock-MailCheck/1.0")
		req.Header.Set("Accept", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue // 换备用域
		}
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		switch resp.StatusCode {
		case 200:
			return statusFound, "存在 Gravatar 头像档案（注册过）"
		case 404:
			return statusNotFound, "未发现 Gravatar 档案（从未注册 / 无头像记录）"
		default:
			return statusUnknown, fmt.Sprintf("Gravatar 返回 HTTP %d", resp.StatusCode)
		}
	}
	// 全部域名失败：超时等 → 网络不可达，归 unknown
	_ = lastErr
	return statusUnknown, "Gravatar 网络不可达（本轮超时，可重试）"
}

// ---- 邮箱有效性 ----

type validityResult struct {
	SyntaxOK  bool     `json:"syntaxOk"`
	LocalPart string   `json:"localPart,omitempty"`
	Domain    string   `json:"domain"`
	MXHosts   []string `json:"mxHosts,omitempty"`
	HasMX     bool     `json:"hasMX"`
	SMTP      string   `json:"smtp"` // ok | rejected | timeout | skipped
	SMTPNote  string   `json:"smtpNote,omitempty"`
}

var emailRe = regexp.MustCompile(`^[A-Za-z0-9.!#$%&'*+/=?^_` + "`" + `{|}~-]+@[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?)+$`)

func splitEmail(email string) (local, domain string, ok bool) {
	email = strings.ToLower(strings.TrimSpace(email))
	at := strings.LastIndex(email, "@")
	if at <= 0 || at == len(email)-1 {
		return "", "", false
	}
	return email[:at], email[at+1:], emailRe.MatchString(email)
}

func checkValidity(email string) validityResult {
	local, domain, syntaxOK := splitEmail(email)
	v := validityResult{LocalPart: local, Domain: domain, SyntaxOK: syntaxOK, SMTP: "skipped"}
	if !syntaxOK {
		return v
	}
	mxDone := make(chan struct{})
	go func() {
		defer close(mxDone)
		mxs, err := net.LookupMX(domain)
		if err == nil {
			sort.SliceStable(mxs, func(i, j int) bool { return mxs[i].Pref < mxs[j].Pref })
			for _, mx := range mxs {
				h := strings.TrimSuffix(mx.Host, ".")
				if h != "" {
					v.MXHosts = append(v.MXHosts, h)
				}
			}
			if len(v.MXHosts) > 0 {
				v.HasMX = true
				return
			}
		}
		if ips, err := net.LookupIP(domain); err == nil && len(ips) > 0 {
			v.MXHosts = []string{domain + " (直接 A 记录)"}
		}
	}()
	select {
	case <-mxDone:
	case <-time.After(5 * time.Second):
	}
	if len(v.MXHosts) > 0 {
		probeSMTP(&v, email)
	}
	return v
}

func probeSMTP(v *validityResult, email string) {
	host := v.MXHosts[0]
	if strings.Contains(host, " (直接") {
		host = v.Domain
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, "25"), 5*time.Second)
	if err != nil {
		v.SMTP = "timeout"
		v.SMTPNote = "25 端口不可达（本地网络常封禁，不代表邮箱无效）"
		return
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(8 * time.Second))
	br := bufio.NewReader(conn)
	readLine := func() string {
		line, _ := br.ReadString('\n')
		return strings.TrimSpace(line)
	}
	if !strings.HasPrefix(readLine(), "220") {
		v.SMTP = "timeout"
		v.SMTPNote = "MX 未响应"
		return
	}
	fmt.Fprintf(conn, "HELO quickdock.local\r\n")
	if !strings.HasPrefix(readLine(), "250") {
		v.SMTP = "timeout"
		return
	}
	fmt.Fprintf(conn, "MAIL FROM:<check@example.com>\r\n")
	if !strings.HasPrefix(readLine(), "250") {
		v.SMTP = "skipped"
		v.SMTPNote = "发信人被拒（灰名单/反垃圾）"
		return
	}
	fmt.Fprintf(conn, "RCPT TO:<%s>\r\n", email)
	resp := readLine()
	switch {
	case strings.HasPrefix(resp, "250"):
		v.SMTP = "ok"
		v.SMTPNote = "该地址被 MX 接受（大概率真实存在）"
	case strings.HasPrefix(resp, "55"):
		v.SMTP = "rejected"
		v.SMTPNote = "MX 明确拒绝此地址（大概率不存在）"
	default:
		v.SMTP = "skipped"
		v.SMTPNote = "MX 响应 " + resp + " 无法判定"
	}
}

// ---- 命令处理 ----

func buildSummary(email string, v validityResult, probes []map[string]interface{}) string {
	var b strings.Builder
	b.WriteString("邮箱: " + email + "\n")
	parts := []string{}
	if v.SyntaxOK {
		parts = append(parts, "语法正常")
	} else {
		parts = append(parts, "语法异常")
	}
	if v.HasMX {
		parts = append(parts, "MX: "+strings.Join(v.MXHosts, ", "))
	}
	if v.SMTP == "ok" {
		parts = append(parts, "SMTP: 有效")
	} else if v.SMTP == "rejected" {
		parts = append(parts, "SMTP: 被拒")
	}
	b.WriteString("有效性: " + strings.Join(parts, " | ") + "\n")

	// 统计行：一眼看清全貌，不逐行铺 123 站
	cnt := map[string]int{}
	for _, p := range probes {
		st, _ := p["status"].(string)
		cnt[st]++
	}
	var stat []string
	stat = append(stat, fmt.Sprintf("已登记 %d 站", cnt["found"]))
	stat = append(stat, fmt.Sprintf("未发现 %d 站", cnt["not_found"]))
	if cnt["unknown"] > 0 {
		stat = append(stat, fmt.Sprintf("无法判定 %d 站", cnt["unknown"]))
	}
	if cnt["skip"] > 0 {
		stat = append(stat, fmt.Sprintf("未适配 %d 站", cnt["skip"]))
	}
	b.WriteString("足迹: " + strings.Join(stat, " · ") + "\n")

	mark := map[string]string{
		"found": "✓ 已登记", "not_found": "✗ 未发现",
		"unknown": "? 无法判定", "error": "! 探测出错",
	}
	// 明细：只列有结论的站；无法判定按「响应异常 / 网络不可达」两类压成汇总行
	var uResp []string
	netUnreach := 0
	for _, p := range probes {
		st, _ := p["status"].(string)
		if st == "skip" {
			continue
		}
		name, _ := p["name"].(string)
		if st == "unknown" {
			detail, _ := p["detail"].(string)
			if strings.Contains(detail, "网络不可达") {
				netUnreach++
				continue
			}
			uResp = append(uResp, name)
			continue
		}
		detail, _ := p["detail"].(string)
		b.WriteString(fmt.Sprintf("  %s: %s - %s\n", name, mark[st], detail))
	}
	if len(uResp) > 0 {
		shown := uResp
		suffix := ""
		if len(shown) > 12 {
			shown = shown[:12]
			suffix = fmt.Sprintf(" 等 %d 站", len(uResp))
		}
		b.WriteString(fmt.Sprintf("  响应异常无法判定: %s%s\n", strings.Join(shown, ", "), suffix))
	}
	if netUnreach > 0 {
		b.WriteString(fmt.Sprintf("  另有 %d 站网络不可达（多为境外站点，需可访问的网络环境）\n", netUnreach))
	}
	return strings.TrimRight(b.String(), "\n")
}

func runProbes(ctx context.Context, client *http.Client, email, domain string, onDone func(map[string]interface{})) []map[string]interface{} {
	results := make([]map[string]interface{}, len(siteList))
	var wg sync.WaitGroup
	var mu sync.Mutex
	for i, s := range siteList {
		wg.Add(1)
		go func(i int, s siteConfig) {
			defer wg.Done()
			st, detail := probeSite(ctx, client, s, email, domain)
			r := map[string]interface{}{
				"key": s.Key, "name": s.Name, "nameEn": s.NameEn,
				"category": s.Category, "url": s.URL,
				"status": string(st), "detail": detail,
			}
			mu.Lock()
			results[i] = r
			mu.Unlock()
			if onDone != nil {
				onDone(r)
			}
		}(i, s)
	}
	wg.Wait()
	return results
}

func handleSites(id int64) {
	list := make([]map[string]interface{}, 0, len(siteList))
	stat := map[string]int{}
	for _, s := range siteList {
		list = append(list, map[string]interface{}{
			"key": s.Key, "name": s.Name, "nameEn": s.NameEn,
			"category": s.Category, "url": s.URL, "desc": s.Desc,
			"status": s.Status,
		})
		st := s.Status
		if st == "" {
			st = "adapted"
		}
		stat[st]++
	}
	respond(id, map[string]interface{}{
		"sites": list, "total": len(siteList), "statusCount": stat,
	})
}

func handleCheck(id int64, input map[string]interface{}) {
	email := strFrom(input, "email")
	if strings.TrimSpace(email) == "" {
		respondError(id, -32602, "缺少 email")
		return
	}
	t := startTask()
	prog := &checkProgress{}
	t.Progress = prog
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		client := newProbeClient(12 * time.Second)
		_, domain, _ := splitEmail(email)

		// validity 并行推进（DNS/SMTP 可能较慢，完成后写入进度）
		vch := make(chan validityResult, 1)
		go func() {
			v := checkValidity(email)
			prog.mu.Lock()
			prog.validity = &v
			prog.mu.Unlock()
			vch <- v
		}()

		onDone := func(r map[string]interface{}) {
			prog.mu.Lock()
			prog.probes = append(prog.probes, r)
			prog.done++
			prog.mu.Unlock()
		}
		probes := runProbes(ctx, client, email, domain, onDone)
		validity := <-vch
		summary := buildSummary(email, validity, probes)
		finishTask(t, map[string]interface{}{
			"email": email, "validity": validity, "probes": probes, "summary": summary,
		}, nil)
	}()
	respond(id, map[string]interface{}{"async": true, "taskId": t.ID})
}

func handleTrace(id int64, input map[string]interface{}) {
	email := strFrom(input, "email")
	if strings.TrimSpace(email) == "" {
		respondError(id, -32602, "缺少 email")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	client := newProbeClient(10 * time.Second)
	_, domain, _ := splitEmail(email)
	validity := checkValidity(email)
	probes := runProbes(ctx, client, email, domain, nil)
	summary := buildSummary(email, validity, probes)
	respond(id, map[string]interface{}{"text": summary, "email": email})
}

func handleTaskStatus(id int64, input map[string]interface{}) {
	taskID := strFrom(input, "taskId")
	if taskID == "" {
		respondError(id, -32602, "缺少 taskId")
		return
	}
	t, ok := getTask(taskID)
	if !ok {
		respond(id, map[string]interface{}{"status": "missing", "taskId": taskID})
		return
	}
	resp := map[string]interface{}{"status": t.Status, "taskId": t.ID}
	if t.Message != "" {
		resp["message"] = t.Message
	}
	if t.Error != "" {
		resp["error"] = t.Error
	}
	if t.Result != nil {
		resp["result"] = t.Result
	}
	// 运行中：下发增量 partial（前端逐站回显，无需等全部完成）
	if t.Status == "running" && t.Progress != nil {
		p := t.Progress
		p.mu.Lock()
		partial := map[string]interface{}{
			"total": len(siteList),
			"done":  p.done,
		}
		if p.validity != nil {
			partial["validity"] = p.validity
		}
		if len(p.probes) > 0 {
			probesCopy := make([]map[string]interface{}, len(p.probes))
			copy(probesCopy, p.probes)
			partial["probes"] = probesCopy
		}
		p.mu.Unlock()
		resp["partial"] = partial
	}
	respond(id, resp)
}

// ---- 诊断模式（调试用）：输出每站 HTTP 状态码 + 响应特征 ----

type diagItem struct {
	Key    string `json:"key"`
	Name   string `json:"name"`
	Code   int    `json:"code"`
	Sample string `json:"sample"`
}

func handleDiag(id int64, input map[string]interface{}) {
	email := strFrom(input, "email")
	if strings.TrimSpace(email) == "" {
		respondError(id, -32602, "缺少 email")
		return
	}
	t := startTask()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		client := newProbeClient(8 * time.Second)
		_, domain, _ := splitEmail(email)

		var mu sync.Mutex
		results := make([]diagItem, 0, len(siteList))
		sem := make(chan struct{}, 15)
		var wg sync.WaitGroup
		for _, s := range siteList {
			if s.Engine == "gravatar" || s.ProbeURL == "" {
				continue
			}
			wg.Add(1)
			sem <- struct{}{}
			go func(s siteConfig) {
				defer wg.Done()
				defer func() { <-sem }()
				d := diagProbe(ctx, client, s, email, domain)
				mu.Lock()
				results = append(results, d)
				mu.Unlock()
			}(s)
		}
		wg.Wait()
		sort.Slice(results, func(i, j int) bool { return results[i].Key < results[j].Key })
		finishTask(t, map[string]interface{}{"email": email, "results": results}, nil)
	}()
	respond(id, map[string]interface{}{"async": true, "taskId": t.ID})
}

func diagProbe(ctx context.Context, client *http.Client, s siteConfig, email, domain string) diagItem {
	d := diagItem{Key: s.Key, Name: s.Name}
	escape := s.BodyType != "json"
	probeURL := fillTpl(s.ProbeURL, email, domain, "", escape)
	body := fillTpl(s.Body, email, domain, "", escape)
	var bodyReader io.Reader
	if s.Method != "GET" && body != "" {
		bodyReader = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, s.MethodOrGet(), probeURL, bodyReader)
	if err != nil {
		d.Code = 0
		d.Sample = "BUILD_ERR"
		return d
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36")
	req.Header.Set("Accept", "*/*")
	for k, v := range s.Headers {
		req.Header.Set(k, fillTpl(v, email, domain, "", false))
	}
	resp, err := client.Do(req)
	if err != nil {
		d.Code = 0
		e := err.Error()
		if len(e) > 60 {
			e = e[:60]
		}
		d.Sample = "ERR " + e
		return d
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	d.Code = resp.StatusCode
	sample := strings.TrimSpace(string(rb))
	sample = regexp.MustCompile(`\s+`).ReplaceAllString(sample, " ")
	if len(sample) > 140 {
		sample = sample[:140] + "…"
	}
	if sample == "" {
		sample = "(空)"
	}
	d.Sample = sample
	return d
}

// ---- 主循环 ----

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

func dispatch(raw string) {
	defer func() {
		if r := recover(); r != nil {
			_, _ = fmt.Fprintf(os.Stderr, "PANIC %v\n%s", r, debug.Stack())
		}
	}()
	var req rpcRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		respondError(0, -32700, "parse error: "+err.Error())
		return
	}
	switch req.Method {
	case "initialize":
		respond(req.ID, map[string]interface{}{"status": "ready", "name": "QuickDock Mail Check", "sites": len(siteList)})
	case "host.ping":
		respond(req.ID, map[string]interface{}{"pong": true})
	case "plugin.execute":
		handleExecute(req)
	default:
		respondError(req.ID, -32601, "unknown method: "+req.Method)
	}
}

func handleExecute(req rpcRequest) {
	var params executeParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			respondError(req.ID, -32602, "invalid params: "+err.Error())
			return
		}
	}
	switch strings.ToLower(strings.TrimSpace(params.Command)) {
	case "sites":
		handleSites(req.ID)
	case "check":
		handleCheck(req.ID, params.Input)
	case "trace":
		handleTrace(req.ID, params.Input)
	case "task-status":
		handleTaskStatus(req.ID, params.Input)
	case "diag":
		handleDiag(req.ID, params.Input)
	default:
		respondError(req.ID, -32601, "unknown command: "+params.Command)
	}
}