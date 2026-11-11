package main

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// newTestSession 直连本机 httptest 服务，因此显式放开内网限制（仅测试路径使用）。
func newTestSession(host string, ports string) *auditSession {
	return &auditSession{
		ID:        "test",
		stopCh:    make(chan struct{}),
		startedAt: time.Now(),
		running:   true,
		opts: auditOptions{
			Host:         host,
			Mode:         "common",
			PortsSpec:    ports,
			TimeoutMs:    500,
			Concurrency:  32,
			CheckPaths:   true,
			CheckUnauth:  true,
			AllowPrivate: true,
		},
	}
}

func TestPrecheckRejectsPrivateTarget(t *testing.T) {
	s := &auditSession{
		stopCh:    make(chan struct{}),
		startedAt: time.Now(),
		opts:      auditOptions{Host: "127.0.0.1"},
	}
	if _, err := s.resolveTarget(); err == nil {
		t.Fatal("回环地址应被拒绝")
	}
	s.opts.Host = "192.168.1.1"
	if _, err := s.resolveTarget(); err == nil {
		t.Fatal("内网地址应被拒绝")
	}
	s.opts.Host = "10.0.0.5"
	if _, err := s.resolveTarget(); err == nil {
		t.Fatal("内网 10/8 应被拒绝")
	}
}

func TestForbiddenIPReason(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1": true, "10.1.2.3": true, "172.16.0.9": true, "192.168.0.1": true,
		"169.254.1.1": true, "224.0.0.1": true, "100.64.0.1": true, "0.0.0.0": true,
		"1.1.1.1": false, "45.33.32.156": false,
	}
	for ip, want := range cases {
		got := forbiddenIPReason(net.ParseIP(ip)) != ""
		if got != want {
			t.Errorf("%s: 期望禁止=%v 实际=%v", ip, want, got)
		}
	}
}

func TestParsePortSpec(t *testing.T) {
	p, err := parsePortSpec("80,443,8000-8002")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(p) != 5 {
		t.Fatalf("期望 5 个端口，实际 %d -> %v", len(p), p)
	}
	if _, err := parsePortSpec("0"); err == nil {
		t.Error("端口 0 应报错")
	}
	if _, err := parsePortSpec("70000"); err == nil {
		t.Error("端口 70000 应报错")
	}
}

func TestBuildPortListModes(t *testing.T) {
	common := buildPortList(auditOptions{Mode: "common"})
	if len(common) < 900 || len(common) > 1600 {
		t.Errorf("常用端口表规模异常: %d", len(common))
	}
	full := buildPortList(auditOptions{Mode: "full"})
	if len(full) != 65535 {
		t.Errorf("全量端口应为 65535，实际 %d", len(full))
	}
	hint := buildPortList(auditOptions{Mode: "common", Host: "1.2.3.4", PortHint: 8443})
	if len(hint) != 1 || hint[0] != 8443 {
		t.Errorf("带端口输入应只扫该端口，实际 %v", hint)
	}
}

func TestAuditPipelineLocal(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/.env", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("DB_PASSWORD=supersecret\nAPP_KEY=abc\n"))
	})
	mux.HandleFunc("/.git/config", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("[core]\n\trepositoryformatversion = 0\n"))
	})
	mux.HandleFunc("/phpinfo.php", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html>phpinfo()</html>"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			w.Header().Set("Server", "nginx/1.24.0")
			w.Header().Set("Content-Type", "text/html")
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("测试服务地址解析失败: %v", err)
	}
	port, _ := strconv.Atoi(u.Port())

	s := newTestSession(u.Hostname(), strconv.Itoa(port))
	target, err := s.resolveTarget()
	if err != nil {
		t.Fatalf("预检失败: %v", err)
	}
	s.target = target
	if target.IP != "127.0.0.1" {
		t.Fatalf("目标 IP 异常: %s", target.IP)
	}

	open := s.scanPorts([]int{port, 1})
	if len(open) != 1 || open[0] != port {
		t.Fatalf("端口扫描结果异常: open=%v", open)
	}

	s.fingerprint(open)
	if len(s.services) != 1 {
		t.Fatalf("指纹结果数异常: %d", len(s.services))
	}
	svc := s.services[0]
	if svc.Service != "http" {
		t.Errorf("服务归应为 http，实际 %s（证据 %s）", svc.Service, svc.Evidence)
	}
	if !strings.Contains(svc.Version, "nginx") {
		t.Errorf("未从 Server 头取到版本: %q", svc.Version)
	}

	s.checkPaths()
	hits := map[string]bool{}
	for _, h := range s.paths {
		hits[h.Path] = true
		if h.Evidence != "" && strings.Contains(h.Evidence, "supersecret") {
			t.Errorf("证据里不应出现明文口令: %s", h.Evidence)
		}
	}
	for _, want := range []string{"/.env", "/.git/config", "/phpinfo.php"} {
		if !hits[want] {
			t.Errorf("未命中 %s，实际命中 %v", want, hits)
		}
	}

	s.checkUnauth()
	findings := buildFindings(s)
	if len(findings) == 0 {
		t.Fatal("应至少产出风险条目")
	}
	var hasCritical bool
	for _, f := range findings {
		if f.Severity == "critical" && strings.Contains(f.Title, "/.env") {
			hasCritical = true
		}
	}
	if !hasCritical {
		t.Errorf("漏报 .env 泄露（critical）")
	}
	if sm := summarize(findings); sm.Critical == 0 {
		t.Errorf("汇总分级异常: %+v", sm)
	}
	if rep := s.textReport(); !strings.Contains(rep, "服务器自查报告") || !strings.Contains(rep, ".env") {
		t.Errorf("文本报告内容异常")
	}
}

/* ==================== 安全边界回归 ==================== */

func TestClassifyBanner(t *testing.T) {
	cases := []struct {
		banner string
		port   int
		want   string
	}{
		{"SSH-2.0-OpenSSH_8.9p1 Ubuntu-3ubuntu0.4", 22, "ssh"},
		{"220 (vsFTPd 3.0.5)", 21, "ftp"},
		{"220 mail.example.com ESMTP Postfix", 25, "smtp"},
		{"+OK Dovecot ready.", 110, "pop3"},
		{"* OK IMAP4rev1 ready", 143, "imap"},
		{"HTTP/1.1 400 Bad Request", 8080, "http"},
		{"VERSION 1.6.22", 11211, "memcached"},
		{"", 80, ""},
	}
	for _, c := range cases {
		svc, _, _ := classifyBanner(c.banner, c.port)
		if svc != c.want {
			t.Errorf("%q -> %q，期望 %q", c.banner, svc, c.want)
		}
	}
	if _, label, ver := classifyBanner(cases[0].banner, 22); label != "SSH" || ver == "" {
		t.Errorf("SSH 版本未解析出来: label=%q ver=%q", label, ver)
	}
}

func TestConcurrencyHardCap(t *testing.T) {
	for _, tc := range []struct{ in, want int }{
		{-1, defConcurrency}, {0, defConcurrency}, {1, 1},
		{defConcurrency, defConcurrency}, {1000, 1000}, {5000, maxConcurrency},
	} {
		if got := clampConcurrency(tc.in); got != tc.want {
			t.Errorf("clampConcurrency(%d)=%d，期望 %d", tc.in, got, tc.want)
		}
	}
	if maxConcurrency != 1000 {
		t.Fatal("并发硬上限被改动，必须保持 1000")
	}
}

// fakeLineServer 起一个按行应答的 TCP 服务，记录客户端发来的每一行，
// 用来验证「未授权 / 匿名口令」校验确实只发一次、不出现字典轮询。
type fakeLineServer struct {
	mu    sync.Mutex
	lines []string
	port  int
}

func newFakeLineServer(t *testing.T, greet string, reply func(n int, line string) string) *fakeLineServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	f := &fakeLineServer{port: ln.Addr().(*net.TCPAddr).Port}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(3 * time.Second))
				if greet != "" {
					_, _ = c.Write([]byte(greet))
				}
				sc := bufio.NewScanner(c)
				n := 0
				for sc.Scan() {
					line := sc.Text()
					f.mu.Lock()
					f.lines = append(f.lines, line)
					f.mu.Unlock()
					n++
					if reply != nil {
						if r := reply(n, line); r != "" {
							if _, err := c.Write([]byte(r)); err != nil {
								return
							}
						}
					}
				}
			}(c)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return f
}

func (f *fakeLineServer) sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.lines...)
}

func TestRedisUnauthIsSingleShot(t *testing.T) {
	for _, tc := range []struct {
		name       string
		reply      string
		vulnerable bool
	}{
		{"无认证", "+PONG\r\n", true},
		{"已启用认证", "-NOAUTH Authentication required.\r\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeLineServer(t, "", func(n int, _ string) string {
				if n == 1 {
					return tc.reply
				}
				return ""
			})
			s := &auditSession{
				stopCh: make(chan struct{}),
				opts:   auditOptions{},
				target: TargetInfo{IP: "127.0.0.1", Host: "127.0.0.1"},
			}
			chk, ok := s.unauthFor(ServiceInfo{Port: f.port, Service: "redis"}, 2*time.Second)
			if !ok {
				t.Fatal("应产出校验结果")
			}
			if chk.Vulnerable != tc.vulnerable {
				t.Errorf("Vulnerable=%v，期望 %v（%s）", chk.Vulnerable, tc.vulnerable, chk.Detail)
			}
			sent := f.sent()
			if len(sent) != 1 || strings.TrimSpace(sent[0]) != "PING" {
				t.Fatalf("应只发一次 PING，实际 %v", sent)
			}
			for _, l := range sent {
				up := strings.ToUpper(l)
				if strings.Contains(up, "AUTH") || strings.Contains(up, "CONFIG") || strings.Contains(up, "SET ") {
					t.Errorf("出现非只读命令: %q", l)
				}
			}
		})
	}
}

func TestFTPAnonymousIsSingleShot(t *testing.T) {
	f := newFakeLineServer(t, "220 (vsFTPd 3.0.5)\r\n", func(n int, _ string) string {
		switch n {
		case 1:
			return "331 Please specify the password.\r\n"
		case 2:
			return "230 Login successful.\r\n"
		}
		return ""
	})
	ok, detail := ftpAnonymous("127.0.0.1", f.port, 2*time.Second)
	if !ok {
		t.Fatalf("匿名登录应判定成功: %s", detail)
	}
	sent := f.sent()
	if len(sent) != 2 {
		t.Fatalf("匿名校验应只发 USER/PASS 两条命令，实际 %v", sent)
	}
	if sent[0] != "USER anonymous" || sent[1] != "PASS anonymous@example.com" {
		t.Errorf("匿名口令不符合预期: %v", sent)
	}
}

func TestPathProbesAreReadOnly(t *testing.T) {
	var mu sync.Mutex
	methods := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		methods[r.Method]++
		mu.Unlock()
		if r.URL.Path == "/.env" {
			_, _ = w.Write([]byte("DB_PASSWORD=secret\n"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())
	s := newTestSession(u.Hostname(), strconv.Itoa(port))
	s.target = TargetInfo{Host: u.Hostname(), IP: "127.0.0.1"}
	s.services = []ServiceInfo{{Port: port, Service: "http", Scheme: "http", Status: 200}}
	s.checkPaths()

	mu.Lock()
	defer mu.Unlock()
	if len(methods) == 0 {
		t.Fatal("路径探测没有发出任何请求")
	}
	for m := range methods {
		if m != http.MethodGet && m != http.MethodHead {
			t.Errorf("路径探测出现非只读方法 %s（次数 %d）", m, methods[m])
		}
	}
}

/* ==================== 加固对比 / 基线 ==================== */

func TestCompareFindingsThreeStates(t *testing.T) {
	base := []Finding{
		{ID: "path-80-/.env", Severity: "critical", Title: "敏感路径暴露：/.env", Port: 80},
		{ID: "svc-redis-6379", Severity: "high", Title: "Redis 对公网开放", Port: 6379},
		{ID: "hsts-443", Severity: "medium", Title: "HSTS 缺失", Port: 443},
		{ID: "exposure-summary", Severity: "info", Title: "暴露汇总"}, // 应被剔除
	}
	cur := []Finding{
		{ID: "svc-redis-6379", Severity: "critical", Title: "Redis 对公网开放", Port: 6379},
		{ID: "hsts-443", Severity: "medium", Title: "HSTS 缺失", Port: 443},
		{ID: "unauth-docker-2375", Severity: "critical", Title: "Docker API 未授权", Port: 2375},
	}
	fixed, still, added := compareFindings(base, cur)
	if len(fixed) != 1 || fixed[0].ID != "path-80-/.env" {
		t.Errorf("已拦截判定错误: %+v", fixed)
	}
	if len(still) != 2 {
		t.Errorf("仍暴露应为 2 项: %+v", still)
	}
	var worse bool
	for _, r := range still {
		if r.ID == "svc-redis-6379" && r.State == "worse" {
			worse = true
		}
	}
	if !worse {
		t.Error("等级升高应标为 worse")
	}
	if len(added) != 1 || added[0].ID != "unauth-docker-2375" {
		t.Errorf("新增判定错误: %+v", added)
	}
}

func TestBaselineStoreRoundTrip(t *testing.T) {
	t.Setenv("QUICKDOCK_PLUGIN_DATA", t.TempDir())
	s := newTestSession("example.com", "")
	s.target = TargetInfo{Host: "example.com", IP: "1.2.3.4"}
	s.stage = "done"
	s.findings = []Finding{{ID: "path-80-/.env", Severity: "critical", Title: "敏感路径暴露：/.env"}}
	s.ports = []PortResult{{Port: 80, State: "open"}, {Port: 22, State: "closed"}}

	item, err := saveBaselineFromSession(s, "修复前")
	if err != nil {
		t.Fatalf("保存基线失败: %v", err)
	}
	list := listBaselines("example.com")
	if len(list) != 1 || list[0].RiskCount != 1 || list[0].Label != "修复前" {
		t.Fatalf("基线列表异常: %+v", list)
	}
	got, ok := findBaseline(item.ID)
	if !ok || len(got.Findings) != 1 || len(got.OpenPorts) != 1 {
		t.Fatalf("基线读回异常: %+v", got)
	}
	if len(listBaselines("other.com")) != 0 {
		t.Error("按主机过滤失效")
	}

	for i := 0; i < maxBaselinesPerHost+2; i++ {
		if _, err := saveBaselineFromSession(s, ""); err != nil {
			t.Fatalf("第 %d 次保存失败: %v", i, err)
		}
	}
	if got := listBaselines("example.com"); len(got) != maxBaselinesPerHost {
		t.Errorf("应按主机保留 %d 条，实际 %d", maxBaselinesPerHost, len(got))
	}
	if _, ok := findBaseline(item.ID); ok {
		t.Error("最旧基线应被裁剪掉")
	}
	if !deleteBaseline(listBaselines("example.com")[0].ID) {
		t.Error("删除基线失败")
	}
}

func TestQuickVerifyIsReadOnlySingleShot(t *testing.T) {
	t.Setenv("QUICKDOCK_PLUGIN_DATA", t.TempDir())
	for _, tc := range []struct {
		name      string
		status    int
		wantFixed int
		wantStill int
	}{
		{"已拦截", http.StatusForbidden, 1, 0},
		{"仍暴露", http.StatusOK, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			methods := map[string]int{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				methods[r.Method]++
				mu.Unlock()
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()
			u, _ := url.Parse(srv.URL)
			port, _ := strconv.Atoi(u.Port())
			base := BaselineItem{
				ID: "bl-test", Host: u.Hostname(), IP: "127.0.0.1",
				SavedAt: time.Now().Format(time.RFC3339),
				Findings: []Finding{{
					ID: fmt.Sprintf("path-%d-/.env", port), Severity: "critical",
					Title: "敏感路径暴露：/.env", Port: port,
				}},
				Services: []ServiceInfo{{Port: port, Scheme: "http"}},
			}
			cr := quickVerify(base)
			if cr.Mode != "quick" || cr.Counts["fixed"] != tc.wantFixed || cr.Counts["still"] != tc.wantStill {
				t.Errorf("复验结论异常: %+v", cr.Counts)
			}
			if !strings.Contains(cr.Report, "加固对比报告") {
				t.Error("对比报告缺失")
			}
			mu.Lock()
			defer mu.Unlock()
			if len(methods) != 1 || methods[http.MethodGet] != 1 {
				t.Errorf("每个风险点应只发一次 GET，实际 %v", methods)
			}
		})
	}
}

func TestRPCEntryNeverAllowsPrivate(t *testing.T) {
	t.Setenv("QUICKDOCK_PLUGIN_DATA", t.TempDir())
	// 即使入参塞了 allowPrivate，RPC 入口也必须恒为 false。
	r, errMsg := handleAuditStart(map[string]interface{}{
		"host": "192.168.1.1", "allowPrivate": true, "checkPaths": false, "checkUnauth": false,
	})
	if errMsg != "" {
		t.Fatalf("启动失败: %s", errMsg)
	}
	s := getSession(r["sessionId"].(string))
	if s == nil {
		t.Fatal("会话未创建")
	}
	if s.opts.AllowPrivate {
		t.Error("内网限制被 RPC 入参放开了")
	}
	for i := 0; i < 100; i++ {
		if s.snapshot()["running"] == false {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if snap := s.snapshot(); snap["error"] == "" {
		t.Error("内网目标应在预检阶段被拒绝")
	} else if !strings.Contains(snap["error"].(string), "拒绝扫描") {
		t.Errorf("拒绝原因异常: %v", snap["error"])
	}
}
