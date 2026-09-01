// 子域名枚举 — 被动收集目标域名的子域名
//
// 仅使用被动数据源（无需向目标发起探测），多源冗余以提高覆盖：
//   1. certspotter   证书透明日志（稳定，主源）
//   2. crt.sh        证书透明日志（含 %25.domain 模糊匹配，但常 5xx/超时）
//   3. hackertarget  hostsearch API（免费额度有限）
//   4. urlscan.io    扫描结果页提取域名（无需 key）
//   5. rapiddns.io   被动 DNS 聚合（HTML 解析，best-effort）
//   6. AlienVault OTX 被动 DNS（无需 key，偶发限流 429）
// 收集完成后可选并发解析 A 记录，筛选出当前可解析的子域名。
//
// 命令：start / poll / stop
//   crt.sh 响应常在 10s 以上（偶发 5xx/超时），同步执行会撞上宿主 20s 上限，
//   因此采用「start 立即返回 + 前端轮询」的异步会话模型。
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

/* ==================== 数据源 ==================== */

type crtEntry struct {
	CommonName string `json:"common_name"`
	NameValue  string `json:"name_value"`
}

type certspotterEntry struct {
	DNSNames []string `json:"dns_names"`
}

// fetchCRT 从证书透明日志收集（返回原始条目，未做域名归属过滤）
//
// crt.sh 的两个坑（实测）：
//   1. 不带 Accept: application/json 时大概率返回 502 —— 必须显式声明
//   2. 大域名（如 qq.com）单次响应可达 24s 以上 —— 超时不能按常规 HTTP 设太短
// 另外它对突发请求返回 429/5xx，需要退避重试。
func fetchCRT(domain string, timeout time.Duration) ([]string, error) {
	url := "https://crt.sh/?q=%25." + domain + "&output=json"

	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			time.Sleep(2 * time.Second)
		}

		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 QuickDock/3.0")
		req.Header.Set("Accept", "application/json")

		client := &http.Client{Timeout: timeout}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}

		// 限长读取，避免超大响应拖死进程
		body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}

		if resp.StatusCode == 429 || resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("crt.sh 返回 HTTP %d（服务繁忙，请稍后重试）", resp.StatusCode)
			continue
		}
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("crt.sh 返回 HTTP %d", resp.StatusCode)
		}

		var entries []crtEntry
		if err := json.Unmarshal(body, &entries); err != nil {
			hint := strings.TrimSpace(string(body))
			if len(hint) > 80 {
				hint = hint[:80]
			}
			return nil, fmt.Errorf("crt.sh 响应解析失败（可能服务不可用）: %s", hint)
		}

		out := []string{}
		for _, e := range entries {
			for _, part := range strings.Split(e.NameValue, "\n") {
				if part = strings.TrimSpace(part); part != "" {
					out = append(out, part)
				}
			}
			if cn := strings.TrimSpace(e.CommonName); cn != "" {
				out = append(out, cn)
			}
		}
		return out, nil
	}
	return nil, lastErr
}

// fetchCertSpotter 另一个证书透明源。实测比 crt.sh 稳定得多（crt.sh 频繁 429/502）。
func fetchCertSpotter(domain string, timeout time.Duration) ([]string, error) {
	url := "https://api.certspotter.com/v1/issuances?domain=" + domain +
		"&include_subdomains=true&expand=dns_names"

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 QuickDock/3.0")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("certspotter 返回 HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}

	var entries []certspotterEntry
	if err := json.Unmarshal(body, &entries); err != nil {
		return nil, fmt.Errorf("certspotter 响应解析失败: %v", err)
	}
	out := []string{}
	for _, e := range entries {
		for _, n := range e.DNSNames {
			if n = strings.TrimSpace(n); n != "" {
				out = append(out, n)
			}
		}
	}
	return out, nil
}

// fetchHackerTarget hostsearch（纯文本 "sub,ip" 行）
func fetchHackerTarget(domain string, timeout time.Duration) ([]string, error) {
	url := "https://api.hackertarget.com/hostsearch/?q=" + domain
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	text := strings.TrimSpace(string(body))
	if text == "" || strings.HasPrefix(strings.ToLower(text), "error") || strings.Contains(text, "API count exceeded") {
		if strings.Contains(text, "API count exceeded") {
			return nil, fmt.Errorf("hackertarget 免费额度已用尽")
		}
		return nil, fmt.Errorf("hackertarget 无数据")
	}

	out := []string{}
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Split(strings.TrimSpace(line), ",")
		if len(fields) > 0 && strings.TrimSpace(fields[0]) != "" {
			out = append(out, strings.TrimSpace(fields[0]))
		}
	}
	return out, nil
}

// fetchURLScan 从 urlscan.io 的公开扫描结果中提取子域名（无需 API key）。
// 注意：返回 results[].page.domain 才是被扫页面的真实子域；task.domain 多为 apex 域名。
func fetchURLScan(domain string, timeout time.Duration) ([]string, error) {
	url := "https://urlscan.io/api/v1/search/?q=domain:" + domain + "&size=10000"
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 QuickDock/3.0")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 429 {
		return nil, fmt.Errorf("urlscan 限流（请稍后重试）")
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("urlscan 返回 HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}

	var data struct {
		Results []struct {
			Page struct {
				Domain string `json:"domain"`
			} `json:"page"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("urlscan 响应解析失败: %v", err)
	}
	out := []string{}
	seen := map[string]bool{}
	for _, r := range data.Results {
		d := strings.TrimSpace(r.Page.Domain)
		if d != "" && !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return out, nil
}

// fetchRapidDNS 被动 DNS 聚合（HTML 页面，best-effort）。
// 用正则提取以目标域名结尾的主机名——add() 会再按归属做二次过滤。
func fetchRapidDNS(domain string, timeout time.Duration) ([]string, error) {
	url := "https://rapiddns.io/subdomain/" + domain + "?full=1"
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36")

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("rapiddns 返回 HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}

	// 匹配以目标域名结尾的主机名（忽略大小写）
	re := regexp.MustCompile(`(?i)[a-z0-9](?:[a-z0-9\-_]{0,61}[a-z0-9])?(?:\.[a-z0-9\-_]{1,63})*\.` + regexp.QuoteMeta(domain) + `(?:\b|$)`)
	out := []string{}
	seen := map[string]bool{}
	for _, m := range re.FindAllString(string(body), -1) {
		m = strings.TrimSuffix(strings.TrimSpace(m), ".")
		if m != "" && !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	return out, nil
}

// fetchOTX AlienVault OTX 被动 DNS（无需 key，偶发 429 限流，带退避重试）。
func fetchOTX(domain string, timeout time.Duration) ([]string, error) {
	url := "https://otx.alienvault.com/api/v1/indicators/domain/" + domain + "/passive_dns"

	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			time.Sleep(3 * time.Second)
		}
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 QuickDock/3.0")
		req.Header.Set("Accept", "application/json")

		client := &http.Client{Timeout: timeout}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode == 429 {
			lastErr = fmt.Errorf("otx 限流（请稍后重试）")
			continue
		}
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("otx 返回 HTTP %d", resp.StatusCode)
		}

		var data struct {
			PassiveDNS []struct {
				Hostname string `json:"hostname"`
			} `json:"passive_dns"`
		}
		if err := json.Unmarshal(body, &data); err != nil {
			return nil, fmt.Errorf("otx 响应解析失败: %v", err)
		}
		out := []string{}
		for _, e := range data.PassiveDNS {
			if h := strings.TrimSpace(e.Hostname); h != "" {
				out = append(out, h)
			}
		}
		return out, nil
	}
	return nil, lastErr
}

/* ==================== 会话 ==================== */

type subdomain struct {
	Name     string   `json:"name"`
	IPs      []string `json:"ips,omitempty"`
	Source   string   `json:"source"`
	Wildcard bool     `json:"wildcard,omitempty"`
	Alive    bool     `json:"alive"`
}

// maxSubdomains 收集上限。热门域名在 CT 日志里可有数千条，
// 不加限制会让 poll 响应撑爆宿主 1MB 单行 stdout 上限。
const maxSubdomains = 2000

type session struct {
	ID      string
	Domain  string
	Resolve bool

	mu        sync.Mutex
	found     map[string]*subdomain
	order     []string
	sources   map[string]string // 数据源 → running / done / error:msg
	truncated bool
	running   bool
	stopCh    chan struct{}
}

var (
	sessMu   sync.Mutex
	sessions = map[string]*session{}
	seqID    int
)

func (s *session) add(name, source string) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return
	}
	wildcard := false
	// 通配证书条目（*.a.example.com）去掉 *. 后记录，并标记
	if strings.HasPrefix(name, "*.") {
		name = strings.TrimPrefix(name, "*.")
		wildcard = true
	}
	name = strings.TrimSuffix(name, ".")
	// 只保留目标域名及其子域名
	if name != s.Domain && !strings.HasSuffix(name, "."+s.Domain) {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if cur, ok := s.found[name]; ok {
		if wildcard {
			cur.Wildcard = true
		}
		if !strings.Contains(cur.Source, source) {
			cur.Source += "," + source
		}
		return
	}
	if len(s.found) >= maxSubdomains {
		s.truncated = true
		return
	}
	s.found[name] = &subdomain{Name: name, Source: source, Wildcard: wildcard}
	s.order = append(s.order, name)
}

func (s *session) run() {
	defer func() {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
	}()

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		s.mu.Lock(); s.sources["crt.sh"] = "running"; s.mu.Unlock()
		// 大域名单次响应实测 24s+，超时给到 30s（异步模型下不受宿主 20s 限制）
		names, err := fetchCRT(s.Domain, 30*time.Second)
		s.mu.Lock()
		if err != nil {
			s.sources["crt.sh"] = "error:" + err.Error()
		} else {
			s.sources["crt.sh"] = "done"
		}
		s.mu.Unlock()
		for _, n := range names {
			s.add(n, "crt.sh")
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		s.mu.Lock(); s.sources["certspotter"] = "running"; s.mu.Unlock()
		names, err := fetchCertSpotter(s.Domain, 15*time.Second)
		s.mu.Lock()
		if err != nil {
			s.sources["certspotter"] = "error:" + err.Error()
		} else {
			s.sources["certspotter"] = "done"
		}
		s.mu.Unlock()
		for _, n := range names {
			s.add(n, "certspotter")
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		s.mu.Lock(); s.sources["hackertarget"] = "running"; s.mu.Unlock()
		names, err := fetchHackerTarget(s.Domain, 10*time.Second)
		s.mu.Lock()
		if err != nil {
			s.sources["hackertarget"] = "error:" + err.Error()
		} else {
			s.sources["hackertarget"] = "done"
		}
		s.mu.Unlock()
		for _, n := range names {
			s.add(n, "hackertarget")
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		s.mu.Lock(); s.sources["urlscan"] = "running"; s.mu.Unlock()
		names, err := fetchURLScan(s.Domain, 15*time.Second)
		s.mu.Lock()
		if err != nil {
			s.sources["urlscan"] = "error:" + err.Error()
		} else {
			s.sources["urlscan"] = "done"
		}
		s.mu.Unlock()
		for _, n := range names {
			s.add(n, "urlscan")
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		s.mu.Lock(); s.sources["rapiddns"] = "running"; s.mu.Unlock()
		names, err := fetchRapidDNS(s.Domain, 15*time.Second)
		s.mu.Lock()
		if err != nil {
			s.sources["rapiddns"] = "error:" + err.Error()
		} else {
			s.sources["rapiddns"] = "done"
		}
		s.mu.Unlock()
		for _, n := range names {
			s.add(n, "rapiddns")
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		s.mu.Lock(); s.sources["otx"] = "running"; s.mu.Unlock()
		names, err := fetchOTX(s.Domain, 15*time.Second)
		s.mu.Lock()
		if err != nil {
			s.sources["otx"] = "error:" + err.Error()
		} else {
			s.sources["otx"] = "done"
		}
		s.mu.Unlock()
		for _, n := range names {
			s.add(n, "otx")
		}
	}()

	wg.Wait()

	if !s.Resolve {
		return
	}

	// 并发解析 A 记录，标记当前可解析的子域名
	s.mu.Lock()
	names := append([]string{}, s.order...)
	s.mu.Unlock()

	sem := make(chan struct{}, 64)
	var rwg sync.WaitGroup
	for _, n := range names {
		select {
		case <-s.stopCh:
			rwg.Wait()
			return
		default:
		}
		rwg.Add(1)
		go func(name string) {
			defer rwg.Done()
			sem <- struct{}{}
			ips, err := net.LookupIP(name)
			<-sem
			if err != nil || len(ips) == 0 {
				return
			}
			strs := make([]string, 0, len(ips))
			for _, ip := range ips {
				strs = append(strs, ip.String())
			}
			s.mu.Lock()
			if cur, ok := s.found[name]; ok {
				cur.IPs = strs
				cur.Alive = true
			}
			s.mu.Unlock()
		}(n)
	}
	rwg.Wait()
}

func (s *session) snapshot() map[string]interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()

	list := make([]subdomain, 0, len(s.order))
	for _, n := range s.order {
		if v, ok := s.found[n]; ok {
			list = append(list, *v)
		}
	}
	// 可解析的排前面，其余按名称
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].Alive != list[j].Alive {
			return list[i].Alive
		}
		return list[i].Name < list[j].Name
	})

	srcs := map[string]string{}
	for k, v := range s.sources {
		srcs[k] = v
	}

	alive := 0
	for _, v := range list {
		if v.Alive {
			alive++
		}
	}
	return map[string]interface{}{
		"sessionId": s.ID, "domain": s.Domain, "running": s.running,
		"subdomains": list, "sources": srcs, "total": len(list), "alive": alive,
		"truncated": s.truncated,
	}
}

/* ==================== RPC ==================== */

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

func boolFrom(m map[string]interface{}, key string, def bool) bool {
	if v, ok := m[key].(bool); ok {
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

func handleStart(id int64, input map[string]interface{}) {
	domain := strings.TrimSpace(strings.ToLower(strFrom(input, "domain")))
	if domain == "" {
		respondError(id, -32602, "请输入域名")
		return
	}
	domain = strings.TrimPrefix(domain, "https://")
	domain = strings.TrimPrefix(domain, "http://")
	domain = strings.TrimPrefix(domain, "*.")
	if idx := strings.IndexAny(domain, "/"); idx >= 0 {
		domain = domain[:idx]
	}
	domain = strings.TrimSuffix(domain, ".")
	if domain == "" || strings.Contains(domain, " ") {
		respondError(id, -32602, "域名格式不正确")
		return
	}

	sessMu.Lock()
	seqID++
	sid := fmt.Sprintf("s%d", seqID)
	s := &session{
		ID: sid, Domain: domain,
		Resolve: boolFrom(input, "resolve", true),
		found:   map[string]*subdomain{},
		order:   []string{},
		sources: map[string]string{},
		running: true, stopCh: make(chan struct{}),
	}
	sessions[sid] = s
	sessMu.Unlock()

	go s.run()

	respond(id, map[string]interface{}{
		"sessionId": sid, "domain": domain, "running": true,
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
		respond(req.ID, map[string]interface{}{"status": "ready", "name": "QuickDock Subdomain Enum"})
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
