// Site Audit — 站点审计箱（原生 JSON-RPC 子进程，标准库）
//
// 合并 6 个工具为单一多标签插件，共享一个原生子进程：
//
//	whois.lookup   查询域名/IP 的 WHOIS 注册信息（TCP 43 + IANA referral）
//	ssl.check      检查 TLS 证书信息（crypto/tls）
//	dns.lookup     查询域名 DNS 记录（net.Lookup*）
//	prop.check     向多个公共 DNS 并行查询并对比一致性
//	headers.audit  抓取响应头做安全头审计评分（net/http）
//	status.lookup  HTTP 状态码字典查询（内置数据）
//
// 所有命令走 plugin.execute，params.command 决定路由。长任务（prop 并行）同步完成，
// 整体远小于宿主 20s 限制。
package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
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
				out = append(out, s)
			}
		}
		return out
	}
	return nil
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

/* ==================== WHOIS ==================== */

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

// stripScheme 移除字符串中的协议头（http://、https://、tcp:// 等）及多余斜杠，
// 便于用户粘贴完整 URL 时仍能正确解析出 host / path。
func stripScheme(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	s = strings.TrimLeft(s, "/")
	return s
}

func handleWhois(id int64, input map[string]interface{}) {
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
		iana, err := whoisQuery("whois.iana.org", query)
		if err != nil {
			respondError(id, -1, err.Error())
			return
		}
		if ref := parseReferral(iana); ref != "" {
			server = ref
		} else {
			respond(id, map[string]interface{}{
				"ok":        true,
				"query":     query,
				"server":    "whois.iana.org",
				"raw":       strings.TrimSpace(iana),
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

/* ==================== SSL ==================== */

func handleSSLCheck(id int64, input map[string]interface{}) {
	host := stripScheme(strFrom(input, "host"))
	if host == "" {
		respondError(id, -32602, "请输入域名或 host:port")
		return
	}
	port := strings.TrimSpace(strFrom(input, "port"))
	if port == "" {
		port = "443"
	}
	serverName := host
	addr := host
	if !strings.Contains(host, ":") {
		addr = host + ":" + port
	} else {
		h, p, has := strings.Cut(host, ":")
		if has {
			serverName = h
			addr = h + ":" + p
		}
	}

	cfg := &tls.Config{InsecureSkipVerify: true, ServerName: serverName}
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 8 * time.Second}, "tcp", addr, cfg)
	if err != nil {
		respondError(id, -1, "连接失败: "+err.Error())
		return
	}
	defer conn.Close()

	cs := conn.ConnectionState()
	if len(cs.PeerCertificates) == 0 {
		respondError(id, -1, "未获取到证书")
		return
	}
	cert := cs.PeerCertificates[0]

	verifyErr := ""
	if err := conn.VerifyHostname(serverName); err != nil {
		verifyErr = err.Error()
	}
	now := time.Now()
	daysLeft := int(time.Until(cert.NotAfter).Hours() / 24)

	respond(id, map[string]interface{}{
		"subjectCN":   cert.Subject.CommonName,
		"issuerCN":    cert.Issuer.CommonName,
		"subject":     cert.Subject.String(),
		"issuer":      cert.Issuer.String(),
		"san":         cert.DNSNames,
		"serial":      cert.SerialNumber.String(),
		"notBefore":   cert.NotBefore.Format(time.RFC3339),
		"notAfter":    cert.NotAfter.Format(time.RFC3339),
		"expired":     now.After(cert.NotAfter),
		"notYetValid": now.Before(cert.NotBefore),
		"sigAlgo":     cert.SignatureAlgorithm.String(),
		"pubAlgo":     cert.PublicKeyAlgorithm.String(),
		"version":     cert.Version,
		"verifyError": verifyErr,
		"daysLeft":    daysLeft,
	})
}

/* ==================== DNS ==================== */

func handleDNSLookup(id int64, input map[string]interface{}) {
	domain := strings.TrimSpace(strFrom(input, "domain"))
	if domain == "" {
		respondError(id, -32602, "请输入域名")
		return
	}
	for len(domain) > 0 && domain[len(domain)-1] == '.' {
		domain = domain[:len(domain)-1]
	}
	types := strSliceFrom(input, "types")
	if len(types) == 0 {
		types = []string{"A", "AAAA", "MX", "TXT", "CNAME", "NS"}
	}
	recs := []map[string]interface{}{}
	add := func(rtype, value string) {
		recs = append(recs, map[string]interface{}{"type": rtype, "value": value})
	}
	for _, t := range types {
		rt := strings.ToUpper(strings.TrimSpace(t))
		switch rt {
		case "A", "AAAA":
			ips, err := net.LookupIP(domain)
			if err != nil {
				add(rt, "查询失败: "+err.Error())
				continue
			}
			for _, ip := range ips {
				if rt == "A" && ip.To4() != nil {
					add("A", ip.String())
				}
				if rt == "AAAA" && ip.To4() == nil {
					add("AAAA", ip.String())
				}
			}
		case "MX":
			mxs, err := net.LookupMX(domain)
			if err != nil {
				add("MX", "查询失败: "+err.Error())
				continue
			}
			for _, mx := range mxs {
				add("MX", fmt.Sprintf("%d %s", mx.Pref, mx.Host))
			}
		case "TXT":
			txts, err := net.LookupTXT(domain)
			if err != nil {
				add("TXT", "查询失败: "+err.Error())
				continue
			}
			for _, t := range txts {
				add("TXT", t)
			}
		case "CNAME":
			cname, err := net.LookupCNAME(domain)
			if err != nil {
				add("CNAME", "查询失败: "+err.Error())
				continue
			}
			add("CNAME", cname)
		case "NS":
			nss, err := net.LookupNS(domain)
			if err != nil {
				add("NS", "查询失败: "+err.Error())
				continue
			}
			for _, ns := range nss {
				add("NS", ns.Host)
			}
		default:
			add(rt, "不支持的类型")
		}
	}
	respond(id, map[string]interface{}{"domain": domain, "records": recs})
}

/* ==================== DNS 传播 ==================== */

type resolver struct {
	Name   string
	Server string
}

var defaultResolvers = []resolver{
	{"Cloudflare", "1.1.1.1"},
	{"Google", "8.8.8.8"},
	{"Quad9", "9.9.9.9"},
	{"阿里 DNS", "223.5.5.5"},
	{"DNSPod", "119.29.29.29"},
	{"OpenDNS", "208.67.222.222"},
}

func normalizeServer(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimPrefix(s, "https://")
	return s
}

func queryResolver(r resolver, domain, qtype string) map[string]interface{} {
	d := &net.Resolver{
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			dialer := net.Dialer{Timeout: 4 * time.Second}
			addr := r.Server
			if !strings.Contains(addr, ":") {
				addr = net.JoinHostPort(addr, "53")
			}
			return dialer.DialContext(ctx, "udp", addr)
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	start := time.Now()
	answers := []string{}
	var errStr string

	switch qtype {
	case "A", "AAAA":
		netw := "ip4"
		if qtype == "AAAA" {
			netw = "ip6"
		}
		ips, err := d.LookupIP(ctx, netw, domain)
		if err != nil {
			errStr = err.Error()
		} else {
			seen := map[string]bool{}
			for _, ip := range ips {
				s := ip.String()
				if !seen[s] {
					seen[s] = true
					answers = append(answers, s)
				}
			}
		}
	case "CNAME":
		c, err := d.LookupCNAME(ctx, domain)
		if err != nil {
			errStr = err.Error()
		} else {
			answers = append(answers, c)
		}
	case "MX":
		mxs, err := d.LookupMX(ctx, domain)
		if err != nil {
			errStr = err.Error()
		} else {
			for _, m := range mxs {
				answers = append(answers, fmt.Sprintf("%d %s", m.Pref, m.Host))
			}
			sort.Strings(answers)
		}
	case "NS":
		nss, err := d.LookupNS(ctx, domain)
		if err != nil {
			errStr = err.Error()
		} else {
			for _, n := range nss {
				answers = append(answers, n.Host)
			}
			sort.Strings(answers)
		}
	case "TXT":
		txts, err := d.LookupTXT(ctx, domain)
		if err != nil {
			errStr = err.Error()
		} else {
			for _, t := range txts {
				answers = append(answers, t)
			}
			sort.Strings(answers)
		}
	default:
		errStr = "不支持的记录类型: " + qtype
	}

	el := int64(time.Since(start).Milliseconds())
	return map[string]interface{}{
		"name":      r.Name,
		"server":    r.Server,
		"answers":   answers,
		"error":     errStr,
		"elapsedMs": el,
		"ok":        errStr == "",
	}
}

func handlePropagation(id int64, input map[string]interface{}) {
	domain := strFrom(input, "domain")
	if domain == "" {
		respondError(id, -32602, "请输入域名")
		return
	}
	domain = strings.TrimSuffix(domain, ".")
	qtype := strings.ToUpper(strFrom(input, "type"))
	if qtype == "" {
		qtype = "A"
	}
	switch qtype {
	case "A", "AAAA", "CNAME", "MX", "TXT", "NS":
	default:
		respondError(id, -32602, "不支持的记录类型: "+qtype)
		return
	}

	resolvers := defaultResolvers
	if custom := strFrom(input, "resolver"); custom != "" {
		resolvers = []resolver{{Name: "自定义", Server: normalizeServer(custom)}}
	}

	results := make([]map[string]interface{}, len(resolvers))
	var wg sync.WaitGroup
	for i, r := range resolvers {
		wg.Add(1)
		go func(i int, r resolver) {
			defer wg.Done()
			results[i] = queryResolver(r, domain, qtype)
		}(i, r)
	}
	wg.Wait()

	var successful [][]string
	consistent := true
	var firstSet string
	for _, res := range results {
		if res["ok"].(bool) {
			ans := toStringSlice(res["answers"])
			sort.Strings(ans)
			key := strings.Join(ans, "|")
			if firstSet == "" {
				firstSet = key
			} else if key != firstSet {
				consistent = false
			}
			successful = append(successful, ans)
		}
	}
	if len(successful) == 0 {
		consistent = false
	}

	uniq := map[string]bool{}
	for _, set := range successful {
		for _, a := range set {
			uniq[a] = true
		}
	}
	consensus := make([]string, 0, len(uniq))
	for a := range uniq {
		consensus = append(consensus, a)
	}
	sort.Strings(consensus)

	respond(id, map[string]interface{}{
		"domain":     domain,
		"type":       qtype,
		"resolvers":  results,
		"consistent": consistent,
		"consensus":  consensus,
		"okCount":    len(successful),
	})
}

func toStringSlice(v interface{}) []string {
	if arr, ok := v.([]string); ok {
		return arr
	}
	return []string{}
}

/* ==================== HTTP 安全头审计 ==================== */

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
	rawURL := stripScheme(strFrom(input, "url"))
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
	req.Header.Set("User-Agent", "QuickDock-SiteAudit/1.0")
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
		"url":           rawURL,
		"finalUrl":      finalURL,
		"statusCode":    resp.StatusCode,
		"elapsedMs":     int64(elapsed.Milliseconds()),
		"ok":            true,
		"scheme":        scheme,
		"redirectChain": chain,
		"score":         score,
		"grade":         grade,
		"findings":      findings,
		"headers":       headers,
		"cookies":       cookies,
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

	if xcto == "nosniff" {
		f = append(f, finding{"xcto", "X-Content-Type-Options", "pass", 8, "已设置为 nosniff", ""})
	} else {
		f = append(f, finding{"xcto", "X-Content-Type-Options", "fail", 8,
			"未设置 X-Content-Type-Options: nosniff", "添加 X-Content-Type-Options: nosniff 防止 MIME 嗅探"})
	}

	if xfo == "DENY" || xfo == "SAMEORIGIN" || cspHasFrameAncestors {
		f = append(f, finding{"xfo", "点击劫持防护 (X-Frame-Options / frame-ancestors)", "pass", 8,
			"已限制页面被嵌套（点击劫持防护）", ""})
	} else {
		f = append(f, finding{"xfo", "点击劫持防护 (X-Frame-Options / frame-ancestors)", "warn", 8,
			"未限制页面嵌入，存在点击劫持风险", "添加 X-Frame-Options: SAMEORIGIN 或 CSP frame-ancestors 'self'"})
	}

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

	if pp != "" {
		f = append(f, finding{"pp", "Permissions-Policy", "pass", 6, "已设置 Permissions-Policy", ""})
	} else {
		f = append(f, finding{"pp", "Permissions-Policy", "warn", 6,
			"未设置 Permissions-Policy，浏览器默认开放敏感 API", "添加 Permissions-Policy 关闭不必要的摄像头/麦克风/地理位置等"})
	}

	if coop == "same-origin" || coop == "cross-origin-isolated" {
		f = append(f, finding{"coop", "Cross-Origin-Opener-Policy", "pass", 5, "已设置 COOP，缓解标签页劫持", ""})
	} else {
		f = append(f, finding{"coop", "Cross-Origin-Opener-Policy", "warn", 5,
			"未设置 Cross-Origin-Opener-Policy", "添加 Cross-Origin-Opener-Policy: same-origin"})
	}

	if corp != "" {
		f = append(f, finding{"corp", "Cross-Origin-Resource-Policy", "pass", 3, "已设置 CORP", ""})
	} else {
		f = append(f, finding{"corp", "Cross-Origin-Resource-Policy", "info", 3,
			"未设置 CORP（可选，防止跨站资源加载）", "可选：添加 Cross-Origin-Resource-Policy: same-origin"})
	}

	if strings.HasPrefix(strings.TrimSpace(xss), "0") {
		f = append(f, finding{"xss", "X-XSS-Protection", "warn", 0,
			"X-XSS-Protection: 0 禁用了浏览器内置防护（现代浏览器可接受，旧版暴露）", "依赖 CSP 进行 XSS 防护即可"})
	} else if xss != "" {
		f = append(f, finding{"xss", "X-XSS-Protection", "info", 0, "已设置 X-XSS-Protection（现代标准已弃用，CSP 更可靠）", ""})
	} else {
		f = append(f, finding{"xss", "X-XSS-Protection", "info", 0, "未设置 X-XSS-Protection（现代标准已弃用）", ""})
	}

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

/* ==================== HTTP 状态码字典 ==================== */

type statusCode struct {
	Code     int    `json:"code"`
	Label    string `json:"label"`
	Desc     string `json:"desc"`
	Category string `json:"category"`
	Detail   string `json:"detail"`
}

var statusCodes = []statusCode{
	{100, "Continue", "继续", "信息", "服务器已收到请求头，客户端应继续发送请求体。"},
	{101, "Switching Protocols", "切换协议", "信息", "服务器已理解客户端的请求，将切换到更合适的协议。"},
	{102, "Processing", "处理中", "信息", "WebDAV 请求，服务器正在处理但尚无响应。"},
	{103, "Early Hints", "早期提示", "信息", "服务器在最终响应前提前返回一些响应头，主要用于预加载资源。"},
	{200, "OK", "成功", "成功", "请求成功。GET 返回资源，POST 返回操作结果。"},
	{201, "Created", "已创建", "成功", "请求已被实现，新资源已创建。常用于 POST/PUT 响应。"},
	{202, "Accepted", "已接受", "成功", "请求已接受但尚未处理完成，用于异步操作。"},
	{204, "No Content", "无内容", "成功", "请求成功但无返回内容。常用于 DELETE 响应。"},
	{301, "Moved Permanently", "永久重定向", "重定向", "请求的资源已被永久移动到新 URL，后续应使用新地址。"},
	{302, "Found", "临时重定向", "重定向", "请求的资源临时位于另一个 URL，后续仍用原地址。"},
	{304, "Not Modified", "未修改", "重定向", "资源未修改，客户端可使用缓存版本。用于条件请求。"},
	{307, "Temporary Redirect", "临时重定向", "重定向", "类似 302 但要求客户端保持原 HTTP 方法不变。"},
	{308, "Permanent Redirect", "永久重定向", "重定向", "类似 301 但要求客户端保持原 HTTP 方法不变。"},
	{400, "Bad Request", "错误请求", "客户端错误", "服务器无法理解请求的格式，客户端不应不经修改重试。"},
	{401, "Unauthorized", "未授权", "客户端错误", "请求需要身份验证。客户端应提供有效的认证凭据。"},
	{403, "Forbidden", "禁止访问", "客户端错误", "服务器拒绝执行请求，即使有认证也无权限。"},
	{404, "Not Found", "未找到", "客户端错误", "服务器找不到请求的资源。可能是 URL 错误或资源已删除。"},
	{405, "Method Not Allowed", "方法不允许", "客户端错误", "请求方法不被该资源支持。例如不允许 DELETE。"},
	{408, "Request Timeout", "请求超时", "客户端错误", "服务器等待客户端发送请求时超时。"},
	{409, "Conflict", "冲突", "客户端错误", "请求与资源的当前状态冲突。常用于 PUT 版本冲突。"},
	{410, "Gone", "已删除", "客户端错误", "请求的资源已永久删除，与 404 不同，410 明确表示资源曾存在。"},
	{413, "Payload Too Large", "请求实体过大", "客户端错误", "请求体超过服务器允许的大小限制。"},
	{415, "Unsupported Media Type", "不支持的媒体类型", "客户端错误", "请求的格式不被请求的资源支持。"},
	{422, "Unprocessable Entity", "不可处理的实体", "客户端错误", "请求格式正确但语义错误。常用于表单验证失败。"},
	{429, "Too Many Requests", "请求过多", "客户端错误", "客户端在指定时间内发送了太多请求（限流）。"},
	{500, "Internal Server Error", "服务器内部错误", "服务器错误", "服务器遇到意外错误，无法完成请求。"},
	{501, "Not Implemented", "未实现", "服务器错误", "服务器不支持请求的功能。"},
	{502, "Bad Gateway", "网关错误", "服务器错误", "网关或代理从上游服务器收到无效响应。"},
	{503, "Service Unavailable", "服务不可用", "服务器错误", "服务器暂时无法处理请求（过载或维护中）。"},
	{504, "Gateway Timeout", "网关超时", "服务器错误", "网关或代理等待上游服务器响应超时。"},
	{505, "HTTP Version Not Supported", "HTTP 版本不支持", "服务器错误", "服务器不支持请求中使用的 HTTP 协议版本。"},
}

func handleStatus(id int64, input map[string]interface{}) {
	var code int
	switch v := input["code"].(type) {
	case float64:
		code = int(v)
	case string:
		code, _ = strconv.Atoi(strings.TrimSpace(v))
	default:
		// 兼容直接传数字字符串到 query 字段
		if s := strFrom(input, "query"); s != "" {
			code, _ = strconv.Atoi(strings.TrimSpace(s))
		}
	}
	if code <= 0 {
		respondError(id, -32602, "请输入有效的 3 位 HTTP 状态码")
		return
	}
	for _, sc := range statusCodes {
		if sc.Code == code {
			respond(id, map[string]interface{}{
				"ok":       true,
				"code":     sc.Code,
				"label":    sc.Label,
				"desc":     sc.Desc,
				"category": sc.Category,
				"detail":   sc.Detail,
			})
			return
		}
	}
	respond(id, map[string]interface{}{
		"ok":       false,
		"code":     code,
		"category": "未知",
		"desc":     "未收录的状态码",
		"detail":   "该状态码不在内置字典中（仅收录常见 1xx~5xx 共 30 条）。",
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
		respond(req.ID, map[string]interface{}{"status": "ready", "name": "QuickDock Site Audit"})
	case "host.ping":
		respond(req.ID, map[string]interface{}{"pong": true})
	case "plugin.execute":
		var params executeParams
		if len(req.Params) > 0 {
			_ = json.Unmarshal(req.Params, &params)
		}
		switch strings.ToLower(strings.TrimSpace(params.Command)) {
		case "whois.lookup":
			handleWhois(req.ID, params.Input)
		case "ssl.check":
			handleSSLCheck(req.ID, params.Input)
		case "dns.lookup":
			handleDNSLookup(req.ID, params.Input)
		case "prop.check":
			handlePropagation(req.ID, params.Input)
		case "headers.audit":
			handleAudit(req.ID, params.Input)
		case "status.lookup":
			handleStatus(req.ID, params.Input)
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
