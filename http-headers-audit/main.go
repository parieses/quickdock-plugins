// HTTP 安全响应头审计 — 抓取目标响应头与 Cookie，按安全最佳实践逐项评分(0-100)
//
// 维度：HSTS / CSP / X-Content-Type-Options / X-Frame-Options / Referrer-Policy /
//      Permissions-Policy / COOP / CORP / Cookie 标志 / 技术栈信息泄露。
// 同步执行（单次请求，受内部 12s 超时约束，远低于宿主 20s 限制）。
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
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

/* ==================== 审计 ==================== */

type finding struct {
	Key     string `json:"key"`
	Title   string `json:"title"`
	Status  string `json:"status"` // pass | warn | fail | info
	Weight  int    `json:"weight"`
	Message string `json:"message"`
	Advice  string `json:"advice,omitempty"`
}

func maxAgeOf(h string) int {
	for _, part := range strings.Split(h, ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(strings.ToLower(part), "max-age=") {
			if n, err := strconv.Atoi(part[8:]); err == nil {
				return n
			}
		}
	}
	return -1
}

func hasDir(h, dir string) bool {
	for _, part := range strings.Split(h, ";") {
		if strings.TrimSpace(strings.ToLower(part)) == strings.ToLower(dir) {
			return true
		}
	}
	return false
}

func sameSiteName(s http.SameSite) string {
	switch s {
	case http.SameSiteNoneMode:
		return "None"
	case http.SameSiteLaxMode:
		return "Lax"
	case http.SameSiteStrictMode:
		return "Strict"
	case http.SameSiteDefaultMode:
		return "Default"
	default:
		return fmt.Sprintf("%d", int(s))
	}
}

func handleAudit(id int64, input map[string]interface{}) {
	rawURL := strFrom(input, "url")
	if rawURL == "" {
		respondError(id, -32602, "请输入目标 URL")
		return
	}
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		rawURL = "https://" + rawURL
	}
	timeout := time.Duration(intFrom(input, "timeout", 12)) * time.Second
	method := strings.ToUpper(strFrom(input, "method"))
	if method == "" {
		method = "GET"
	}

	var chain []map[string]interface{}
	client := &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 0 {
				prev := via[len(via)-1]
				st := 0
				if prev.Response != nil {
					st = prev.Response.StatusCode
				}
				chain = append(chain, map[string]interface{}{
					"from":   prev.URL.String(),
					"status": st,
					"to":     req.URL.String(),
				})
			}
			if len(via) >= 6 {
				return fmt.Errorf("stopped after %d redirects", len(via))
			}
			return nil
		},
	}

	start := time.Now()
	req, err := http.NewRequest(method, rawURL, nil)
	if err != nil {
		respondError(id, -32602, err.Error())
		return
	}
	req.Header.Set("User-Agent", "QuickDock-HeadersAudit/1.0")
	req.Header.Set("Accept", "*/*")

	resp, err := client.Do(req)
	elapsed := time.Since(start)
	if err != nil {
		respond(id, map[string]interface{}{
			"url": rawURL, "ok": false, "error": err.Error(),
			"elapsedMs": int64(elapsed.Milliseconds()),
		})
		return
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))

	finalURL := resp.Request.URL.String()
	scheme := resp.Request.URL.Scheme

	headers := map[string]string{}
	for k, v := range resp.Header {
		headers[k] = strings.Join(v, ", ")
	}

	cookies := []map[string]interface{}{}
	for _, c := range resp.Cookies() {
		cookies = append(cookies, map[string]interface{}{
			"name":     c.Name,
			"secure":   c.Secure,
			"httpOnly": c.HttpOnly,
			"sameSite": sameSiteName(c.SameSite),
			"path":     c.Path,
		})
	}

	findings := auditHeaders(headers, cookies, scheme)

	// 评分
	earned, total := 0, 0
	for _, f := range findings {
		if f.Weight <= 0 {
			continue
		}
		total += f.Weight
		switch f.Status {
		case "pass":
			earned += f.Weight
		case "warn":
			earned += f.Weight / 2
		default:
			// fail / info 不计分
		}
	}
	score := 0
	if total > 0 {
		score = int(float64(earned) / float64(total) * 100)
	}
	grade := "F"
	switch {
	case score >= 90:
		grade = "A"
	case score >= 75:
		grade = "B"
	case score >= 60:
		grade = "C"
	case score >= 40:
		grade = "D"
	}

	respond(id, map[string]interface{}{
		"url":          rawURL,
		"finalUrl":     finalURL,
		"statusCode":   resp.StatusCode,
		"elapsedMs":    int64(elapsed.Milliseconds()),
		"ok":           true,
		"scheme":       scheme,
		"redirectChain": chain,
		"score":        score,
		"grade":        grade,
		"findings":     findings,
		"headers":      headers,
		"cookies":      cookies,
	})
}

func auditHeaders(h map[string]string, cookies []map[string]interface{}, scheme string) []finding {
	var f []finding

	hsts := h["Strict-Transport-Security"]
	csp := h["Content-Security-Policy"]
	xcto := strings.ToLower(h["X-Content-Type-Options"])
	xfo := strings.ToUpper(h["X-Frame-Options"])
	rp := h["Referrer-Policy"]
	pp := h["Permissions-Policy"]
	coop := h["Cross-Origin-Opener-Policy"]
	corp := h["Cross-Origin-Resource-Policy"]
	xss := h["X-XSS-Protection"]

	cspHasFrameAncestors := hasDir(csp, "frame-ancestors")

	// 1. HSTS
	if scheme == "http" {
		f = append(f, finding{"hsts", "HSTS (Strict-Transport-Security)", "warn", 20,
			"站点使用 HTTP 明文传输，HSTS 不适用", "部署 TLS 证书并强制 301 跳转到 HTTPS 后再配置 HSTS"})
	} else if hsts == "" {
		f = append(f, finding{"hsts", "HSTS (Strict-Transport-Security)", "fail", 20,
			"未设置 HSTS，存在 SSL 剥离（降级攻击）风险",
			"添加 Strict-Transport-Security: max-age=31536000; includeSubDomains; preload"})
	} else {
		ma := maxAgeOf(hsts)
		sub := hasDir(hsts, "includeSubDomains")
		pre := hasDir(hsts, "preload")
		switch {
		case ma >= 31536000 && sub && pre:
			f = append(f, finding{"hsts", "HSTS (Strict-Transport-Security)", "pass", 20,
				"HSTS 配置完善（含 includeSubDomains 与 preload）", ""})
		case ma >= 31536000:
			f = append(f, finding{"hsts", "HSTS (Strict-Transport-Security)", "warn", 20,
				"HSTS 已设置但缺少 includeSubDomains/preload", "补全为 max-age=31536000; includeSubDomains; preload"})
		default:
			f = append(f, finding{"hsts", "HSTS (Strict-Transport-Security)", "warn", 20,
				"HSTS max-age 过短（建议 ≥ 31536000）", "增大 max-age 并加上 includeSubDomains; preload"})
		}
	}

	// 2. CSP
	if csp == "" {
		f = append(f, finding{"csp", "CSP (Content-Security-Policy)", "fail", 20,
			"未设置 CSP，XSS 与数据注入防护较弱", "设置 Content-Security-Policy 并收敛 default-src 'self'"})
	} else if hasDir(csp, "unsafe-inline") || hasDir(csp, "unsafe-eval") {
		f = append(f, finding{"csp", "CSP (Content-Security-Policy)", "warn", 20,
			"CSP 含 unsafe-inline / unsafe-eval，防护降级", "移除 unsafe-inline/unsafe-eval，改用 nonce/hash"})
	} else {
		f = append(f, finding{"csp", "CSP (Content-Security-Policy)", "pass", 20,
			"CSP 配置良好（未发现 unsafe-inline/eval）", ""})
	}

	// 3. X-Content-Type-Options
	if xcto == "nosniff" {
		f = append(f, finding{"xcto", "X-Content-Type-Options", "pass", 8, "已设置为 nosniff", ""})
	} else {
		f = append(f, finding{"xcto", "X-Content-Type-Options", "fail", 8,
			"未设置 X-Content-Type-Options: nosniff", "添加 X-Content-Type-Options: nosniff 防止 MIME 嗅探"})
	}

	// 4. X-Frame-Options / frame-ancestors
	if xfo == "DENY" || xfo == "SAMEORIGIN" || cspHasFrameAncestors {
		f = append(f, finding{"xfo", "点击劫持防护 (X-Frame-Options / frame-ancestors)", "pass", 8,
			"已限制页面被嵌套（点击劫持防护）", ""})
	} else {
		f = append(f, finding{"xfo", "点击劫持防护 (X-Frame-Options / frame-ancestors)", "warn", 8,
			"未限制页面嵌入，存在点击劫持风险", "添加 X-Frame-Options: SAMEORIGIN 或 CSP frame-ancestors 'self'"})
	}

	// 5. Referrer-Policy
	strict := map[string]bool{"no-referrer": true, "same-origin": true, "strict-origin": true, "strict-origin-when-cross-origin": true}
	weak := map[string]bool{"origin-when-cross-origin": true, "origin": true, "unsafe-url": true}
	rpL := strings.ToLower(strings.TrimSpace(rp))
	switch {
	case rp == "":
		f = append(f, finding{"rp", "Referrer-Policy", "fail", 6,
			"未设置 Referrer-Policy，可能泄露来源 URL", "添加 Referrer-Policy: strict-origin-when-cross-origin"})
	case rpL == "unsafe-url":
		f = append(f, finding{"rp", "Referrer-Policy", "fail", 6,
			"Referrer-Policy 为 unsafe-url，会发送完整来源地址", "改为 strict-origin-when-cross-origin"})
	case weak[rpL]:
		f = append(f, finding{"rp", "Referrer-Policy", "warn", 6,
			"Referrer-Policy 较弱（可能泄露源站主机名）", "升级为 strict-origin-when-cross-origin"})
	default:
		if strict[rpL] {
			f = append(f, finding{"rp", "Referrer-Policy", "pass", 6, "Referrer-Policy 配置合理", ""})
		} else {
			f = append(f, finding{"rp", "Referrer-Policy", "warn", 6, "Referrer-Policy 值不常见", "建议 strict-origin-when-cross-origin"})
		}
	}

	// 6. Permissions-Policy
	if pp != "" {
		f = append(f, finding{"pp", "Permissions-Policy", "pass", 6, "已设置 Permissions-Policy", ""})
	} else {
		f = append(f, finding{"pp", "Permissions-Policy", "warn", 6,
			"未设置 Permissions-Policy，浏览器默认开放敏感 API", "添加 Permissions-Policy 关闭不必要的摄像头/麦克风/地理位置等"})
	}

	// 7. COOP
	if coop == "same-origin" || coop == "cross-origin-isolated" {
		f = append(f, finding{"coop", "Cross-Origin-Opener-Policy", "pass", 5, "已设置 COOP，缓解标签页劫持", ""})
	} else {
		f = append(f, finding{"coop", "Cross-Origin-Opener-Policy", "warn", 5,
			"未设置 Cross-Origin-Opener-Policy", "添加 Cross-Origin-Opener-Policy: same-origin"})
	}

	// 8. CORP
	if corp != "" {
		f = append(f, finding{"corp", "Cross-Origin-Resource-Policy", "pass", 3, "已设置 CORP", ""})
	} else {
		f = append(f, finding{"corp", "Cross-Origin-Resource-Policy", "info", 3,
			"未设置 CORP（可选，防止跨站资源加载）", "可选：添加 Cross-Origin-Resource-Policy: same-origin"})
	}

	// 9. X-XSS-Protection（已弃用，仅信息）
	if strings.HasPrefix(strings.TrimSpace(xss), "0") {
		f = append(f, finding{"xss", "X-XSS-Protection", "warn", 0,
			"X-XSS-Protection: 0 禁用了浏览器内置防护（现代浏览器可接受，旧版暴露）", "依赖 CSP 进行 XSS 防护即可"})
	} else if xss != "" {
		f = append(f, finding{"xss", "X-XSS-Protection", "info", 0, "已设置 X-XSS-Protection（现代标准已弃用，CSP 更可靠）", ""})
	} else {
		f = append(f, finding{"xss", "X-XSS-Protection", "info", 0, "未设置 X-XSS-Protection（现代标准已弃用）", ""})
	}

	// 10. Cookie 安全标志
	if len(cookies) == 0 {
		f = append(f, finding{"cookie", "Cookie 安全标志", "info", 10, "无 Set-Cookie，跳过 Cookie 评估", ""})
	} else {
		bad := 0
		for _, c := range cookies {
			secure, _ := c["secure"].(bool)
			httpOnly, _ := c["httpOnly"].(bool)
			ss, _ := c["sameSite"].(string)
			if !secure || !httpOnly || (ss != "Strict" && ss != "Lax") {
				bad++
			}
		}
		switch {
		case bad == 0:
			f = append(f, finding{"cookie", "Cookie 安全标志", "pass", 10,
				fmt.Sprintf("全部 %d 个 Cookie 均含 Secure+HttpOnly+SameSite", len(cookies)), ""})
		case bad < len(cookies):
			f = append(f, finding{"cookie", "Cookie 安全标志", "warn", 10,
				fmt.Sprintf("%d/%d 个 Cookie 缺少 Secure/HttpOnly/SameSite", bad, len(cookies)),
				"为会话 Cookie 设置 Secure; HttpOnly; SameSite=Lax/Strict"})
		default:
			f = append(f, finding{"cookie", "Cookie 安全标志", "fail", 10,
				fmt.Sprintf("全部 %d 个 Cookie 均缺少安全标志", len(cookies)),
				"为 Cookie 设置 Secure; HttpOnly; SameSite=Lax/Strict"})
		}
	}

	// 11. 技术栈信息泄露
	leak := []string{}
	for _, k := range []string{"Server", "X-Powered-By", "X-AspNet-Version", "X-AspNetMvc-Version", "X-Drupal-Cache", "X-Varnish", "X-Cache", "X-Runtime", "X-Generator"} {
		if h[k] != "" {
			leak = append(leak, k+"="+h[k])
		}
	}
	if len(leak) > 0 {
		f = append(f, finding{"leak", "技术栈信息泄露", "warn", 0,
			"响应暴露技术栈：" + strings.Join(leak, ", "), "移除或混淆 Server/X-Powered-By 等头部减少指纹"})
	} else {
		f = append(f, finding{"leak", "技术栈信息泄露", "info", 0, "未暴露明显技术栈指纹", ""})
	}

	return f
}

/* ==================== RPC ==================== */

func dispatch(raw string) {
	var req rpcRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		respondError(0, -32700, "parse error: "+err.Error())
		return
	}
	switch req.Method {
	case "initialize":
		respond(req.ID, map[string]interface{}{"status": "ready", "name": "QuickDock HTTP Headers Audit"})
	case "host.ping":
		respond(req.ID, map[string]interface{}{"pong": true})
	case "plugin.execute":
		var params executeParams
		if len(req.Params) > 0 {
			_ = json.Unmarshal(req.Params, &params)
		}
		switch strings.ToLower(strings.TrimSpace(params.Command)) {
		case "audit":
			handleAudit(req.ID, params.Input)
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
