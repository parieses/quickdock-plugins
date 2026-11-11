// 敏感路径只读探测 + 未授权访问 / 匿名口令单次校验。
//
// 边界（别越线）：
//   - 路径探测只用 GET，单次、不递归、不跟随跳转、正文最多读 1KB 仅作证据
//   - 认证类检查每个服务只做一次，不分发字典、不重试、不并发认证
//   - 不下载业务数据、不探测注入、不上传写入
package main

import (
	"crypto/rand"
	"encoding/hex"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type PathHit struct {
	Port     int    `json:"port"`
	Path     string `json:"path"`
	Status   int    `json:"status"`
	Size     int    `json:"size"`
	Type     string `json:"type,omitempty"`
	Severity string `json:"severity"`
	Note     string `json:"note,omitempty"`
	Evidence string `json:"evidence,omitempty"`
}

type UnauthCheck struct {
	Port       int    `json:"port"`
	Service    string `json:"service"`
	Label      string `json:"label"`
	Vulnerable bool   `json:"vulnerable"`
	Detail     string `json:"detail"`
	Fix        string `json:"fix,omitempty"`
	Severity   string `json:"severity"`
}

// pathProbe：marker 用于确认「响应内容真的像目标文件」，避免软 404 误报
type pathProbe struct {
	Path     string
	Severity string
	Note     string
	Marker   string
}

var sensitivePaths = []pathProbe{
	{"/.git/config", "high", "源码仓库元数据泄露（可据此还原源码）", "[core]"},
	{"/.git/HEAD", "medium", "Git 目录可访问", "ref: refs/"},
	{"/.svn/entries", "medium", "SVN 元数据泄露", ""},
	{"/.env", "critical", "环境变量文件泄露（常含数据库口令、密钥）", "="},
	{"/.env.local", "high", "环境变量文件泄露", "="},
	{"/.env.production", "high", "生产环境变量文件泄露", "="},
	{"/.vscode/sftp.json", "critical", "编辑器同步配置泄露（常含 SSH 口令）", "host"},
	{"/config.php.bak", "critical", "配置备份文件泄露", "password"},
	{"/wp-config.php.bak", "critical", "WordPress 配置备份泄露", "DB_PASSWORD"},
	{"/backup.sql", "critical", "数据库备份可下载", "INSERT INTO"},
	{"/db.sql", "critical", "数据库备份可下载", ""},
	{"/dump.sql", "critical", "数据库备份可下载", ""},
	{"/www.zip", "high", "站点源码压缩包可下载", ""},
	{"/backup.zip", "high", "备份压缩包可下载", ""},
	{"/phpinfo.php", "high", "phpinfo 暴露环境与路径", "phpinfo()"},
	{"/info.php", "high", "phpinfo 暴露环境与路径", "phpinfo"},
	{"/server-status", "high", "Apache 状态页暴露请求与客户端 IP", "Apache Server Status"},
	{"/server-info", "medium", "Apache 配置信息页", "Server Settings"},
	{"/nginx_status", "medium", "Nginx 状态页暴露连接数", "Active connections"},
	{"/actuator", "medium", "Spring Boot Actuator 入口可访问", "_links"},
	{"/actuator/env", "critical", "Actuator env 泄露配置与密钥", "activeProfiles"},
	{"/actuator/health", "low", "健康检查端点对外暴露", "status"},
	{"/metrics", "medium", "指标端点暴露内部指标", "# HELP"},
	{"/swagger-ui.html", "low", "接口文档页对外暴露", "swagger"},
	{"/v2/api-docs", "medium", "接口文档 JSON 可读", "swagger"},
	{"/openapi.json", "low", "接口文档 JSON 可读", "openapi"},
	{"/docker-compose.yml", "high", "编排文件泄露服务与口令", "services"},
	{"/web.config", "medium", "IIS 配置文件可读", "configuration"},
	{"/.htaccess", "low", "Apache 重写规则可读", ""},
	{"/manager/html", "high", "Tomcat 管理台暴露（尝试弱口令的前提）", ""},
	{"/console", "medium", "H2/管理控制台暴露", ""},
	{"/WEB-INF/web.xml", "high", "Java 应用描述文件泄露", "web-app"},
	{"/storage/logs/laravel.log", "high", "应用日志泄露（常含路径与报错）", "Stack trace"},
	{"/.DS_Store", "low", "目录结构泄露", ""},
	{"/robots.txt", "info", "robots 暴露内部路径线索", ""},
	{"/.well-known/security.txt", "info", "已有安全联系信息（正向）", ""},
}

func (s *auditSession) checkPaths() {
	// 只对「确认能返回 HTTP 响应」的服务做路径探测，避免对开着但不说 HTTP 的端口干等超时
	var targets []ServiceInfo
	for _, svc := range s.services {
		if (svc.Scheme == "http" || svc.Scheme == "https") && svc.Status > 0 {
			targets = append(targets, svc)
		}
	}
	sort.Slice(targets, func(i, j int) bool { return pathPortPriority(targets[i].Port) < pathPortPriority(targets[j].Port) })
	if len(targets) > maxWebTargets {
		targets = targets[:maxWebTargets]
	}
	if len(targets) == 0 {
		return
	}

	type task struct {
		svc   ServiceInfo
		probe pathProbe
	}
	tasks := make([]task, 0, len(targets)*len(sensitivePaths))
	for _, t := range targets {
		for _, pr := range sensitivePaths {
			tasks = append(tasks, task{svc: t, probe: pr})
		}
	}

	const pathTimeout = 6 * time.Second
	const pathConc = 8
	const pathBudget = 45 * time.Second // 阶段总预算：个别慢站点不能拖死整轮巡检

	// 每个 web 目标先取一次随机路径基线，用于识别「软 404」兜底页
	type baseInfo struct {
		status int
		length int
		ok     bool
	}
	bases := map[int]baseInfo{}
	for _, t := range targets {
		if s.stopped() {
			return
		}
		st, _, body, err := httpFetch(s.target.Host, s.target.IP, t.Port, t.Scheme == "https",
			"GET", "/"+randToken()+"-notfound", pathTimeout, 1024)
		if err == nil {
			bases[t.Port] = baseInfo{status: st, length: len(body), ok: true}
		}
	}

	atomic.StoreInt64(&s.total, int64(len(tasks)))
	atomic.StoreInt64(&s.scanned, 0)
	s.mu.Lock()
	s.progress = Progress{Phase: "paths", Total: len(tasks)}
	s.mu.Unlock()

	deadline := time.Now().Add(pathBudget)
	sem := make(chan struct{}, pathConc)
	var wg sync.WaitGroup
	var mu sync.Mutex

	for _, tk := range tasks {
		if s.stopped() || time.Now().After(deadline) {
			break
		}
		wg.Add(1)
		go func(tk task) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			https := tk.svc.Scheme == "https"
			status, hdr, body, err := httpFetch(s.target.Host, s.target.IP, tk.svc.Port, https,
				"GET", tk.probe.Path, pathTimeout, 1024)
			atomic.AddInt64(&s.scanned, 1)
			mu.Lock()
			s.pathsCheck++
			mu.Unlock()
			if err != nil || status == 0 || status == 404 || status == 410 {
				return
			}
			if base := bases[tk.svc.Port]; base.ok && status == base.status && abs(len(body)-base.length) <= 32 {
				return // 软 404：与随机路径同状态同长度，判定为站点兜底页
			}
			if tk.probe.Marker != "" && status == 200 &&
				!strings.Contains(strings.ToLower(body), strings.ToLower(tk.probe.Marker)) {
				return
			}

			sev, note := tk.probe.Severity, tk.probe.Note
			switch status {
			case 401, 403:
				sev, note = "info", "存在但受访问控制保护（"+strconv.Itoa(status)+"）"
			case 301, 302, 303, 307, 308:
				if sev != "info" {
					sev = "low"
				}
				note = "跳转到 " + truncate(hdr.Get("Location"), 80)
			}

			hit := PathHit{
				Port: tk.svc.Port, Path: tk.probe.Path, Status: status, Size: len(body),
				Type: truncate(hdr.Get("Content-Type"), 60), Severity: sev, Note: note,
			}
			if status == 200 && len(body) > 0 {
				hit.Evidence = evidenceSnippet(body)
			}
			mu.Lock()
			if len(s.paths) < maxPathHits {
				s.paths = append(s.paths, hit)
			}
			mu.Unlock()
		}(tk)
	}
	wg.Wait()

	s.mu.Lock()
	sort.Slice(s.paths, func(i, j int) bool {
		if s.paths[i].Port != s.paths[j].Port {
			return s.paths[i].Port < s.paths[j].Port
		}
		return sevRank(s.paths[i].Severity) < sevRank(s.paths[j].Severity)
	})
	s.mu.Unlock()
}

func pathPortPriority(port int) int {
	switch port {
	case 443:
		return 0
	case 80:
		return 1
	case 8443:
		return 2
	default:
		return 10
	}
}

func evidenceSnippet(body string) string {
	body = strings.ReplaceAll(body, "\r", " ")
	body = strings.ReplaceAll(body, "\n", " ")
	body = strings.Join(strings.Fields(body), " ")
	snippet := truncate(body, 120)
	// 打码明显的口令字段值，报告里不落明文口令
	for _, key := range []string{"password", "passwd", "secret", "token", "api_key", "apikey", "DB_PASSWORD"} {
		snippet = maskValue(snippet, key)
	}
	return snippet
}

func maskValue(s, key string) string {
	low := strings.ToLower(s)
	idx := strings.Index(low, strings.ToLower(key))
	if idx < 0 {
		return s
	}
	tail := s[idx+len(key):]
	sep := strings.IndexAny(tail, ":=")
	if sep < 0 || sep > 6 {
		return s
	}
	valStart := idx + len(key) + sep + 1
	rest := s[valStart:]
	trim := strings.TrimLeft(rest, " \"'")
	lead := len(rest) - len(trim)
	end := len(trim)
	if j := strings.IndexAny(trim, " ,;&\"'"); j >= 0 {
		end = j
	}
	if end <= 3 {
		return s
	}
	return s[:valStart+lead] + "***" + trim[end:]
}

/* ==================== 未授权 / 匿名口令（单次） ==================== */

func (s *auditSession) checkUnauth() {
	timeout := 5 * time.Second
	for _, svc := range s.services {
		if s.stopped() {
			return
		}
		if chk, ok := s.unauthFor(svc, timeout); ok {
			s.mu.Lock()
			s.unauth = append(s.unauth, chk)
			s.mu.Unlock()
		}
	}
}

func (s *auditSession) unauthFor(svc ServiceInfo, timeout time.Duration) (UnauthCheck, bool) {
	ip := s.target.IP
	host := s.target.Host
	chk := UnauthCheck{Port: svc.Port, Service: svc.Service, Severity: "info"}

	switch svc.Service {
	case "redis":
		chk.Label = "Redis 无认证"
		chk.Fix = "在 redis.conf 开 requirepass 或 rename-command，禁止绑定 0.0.0.0，只让本机访问"
		resp, ok := sendRecv(ip, svc.Port, "PING\r\n", timeout)
		if !ok {
			return chk, false
		}
		if strings.Contains(strings.ToUpper(resp), "+PONG") {
			chk.Vulnerable = true
			chk.Severity = "critical"
			chk.Detail = "未认证直接 PING 返回 +PONG，任何人可读写全部键（可写 SSH 公钥/计划任务）"
		} else if strings.Contains(strings.ToUpper(resp), "NOAUTH") {
			chk.Detail = "PING 返回 NOAUTH，已启用认证"
		} else {
			chk.Detail = "响应异常：" + truncate(resp, 60)
		}
		return chk, true

	case "memcached":
		chk.Label = "Memcached 无认证"
		chk.Fix = "绑定 127.0.0.1 或用防火墙只放行内网，memcached 本身不支持认证"
		resp, ok := sendRecv(ip, svc.Port, "version\r\n", timeout)
		if !ok {
			return chk, false
		}
		if v := versionAfter(resp, "VERSION "); v != "" {
			chk.Vulnerable = true
			chk.Severity = "critical"
			chk.Detail = "未认证可直接取版本 " + v + "，缓存内容可被任意读取"
		}
		return chk, true

	case "docker":
		chk.Label = "Docker API 未授权"
		chk.Fix = "关闭 2375 明文端口，改用 2376 + 客户端证书，或仅监听 unix socket"
		st, _, body, err := httpFetch(host, ip, svc.Port, svc.Port == 2376, "GET", "/version", timeout, 2048)
		if err != nil {
			return chk, false
		}
		if st == 200 && (strings.Contains(body, "ApiVersion") || strings.Contains(body, "Docker")) {
			chk.Vulnerable = true
			chk.Severity = "critical"
			chk.Detail = "GET /version 直接返回 API 信息，等于宿主机 root 权限"
		} else {
			chk.Detail = "GET /version 返回 " + strconv.Itoa(st)
		}
		return chk, true

	case "es":
		chk.Label = "Elasticsearch 未授权"
		chk.Fix = "开启 xpack.security，禁止 9200 对外，加反代与鉴权"
		st, _, body, err := httpFetch(host, ip, svc.Port, false, "GET", "/", timeout, 4096)
		if err != nil {
			return chk, false
		}
		if st == 200 && strings.Contains(body, "cluster_name") {
			chk.Vulnerable = true
			chk.Severity = "critical"
			chk.Detail = "GET / 返回集群名与版本 " + firstMatch(body, `"number"\s*:\s*"([^"]+)"`) + "，可继续读取索引"
		} else {
			chk.Detail = "GET / 返回 " + strconv.Itoa(st) + "，未泄露集群信息"
		}
		return chk, true

	case "ftp":
		chk.Label = "FTP 匿名登录"
		chk.Fix = "关闭匿名访问（anonymous_enable=NO），改用 SFTP，禁止明文 FTP 对外"
		ok, detail := ftpAnonymous(ip, svc.Port, timeout)
		chk.Detail = detail
		if ok {
			chk.Vulnerable = true
			chk.Severity = "high"
		}
		return chk, true
	}
	return chk, false
}

// ftpAnonymous 只尝试一次 anonymous/anonymous，绝不轮询口令。
func ftpAnonymous(ip string, port int, timeout time.Duration) (bool, string) {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(ip, strconv.Itoa(port)), timeout)
	if err != nil {
		return false, "连接失败"
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout * 2))
	buf := make([]byte, 1024)

	read := func() string {
		n, err := conn.Read(buf)
		if n == 0 && err != nil {
			return ""
		}
		return strings.TrimSpace(string(buf[:n]))
	}

	if r := read(); !strings.HasPrefix(r, "220") {
		return false, "非标准 FTP 握手：" + truncate(r, 40)
	}
	if _, err := conn.Write([]byte("USER anonymous\r\n")); err != nil {
		return false, "发送 USER 失败"
	}
	r1 := read()
	if _, err := conn.Write([]byte("PASS anonymous@example.com\r\n")); err != nil {
		return false, "发送 PASS 失败"
	}
	r2 := read()
	if strings.HasPrefix(r2, "230") {
		return true, "匿名账号登录成功（" + truncate(r2, 40) + "）"
	}
	return false, "拒绝匿名登录（" + truncate(r1, 24) + " / " + truncate(r2, 24) + "）"
}

/* ==================== 工具 ==================== */

func randToken() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "qdselfaudit"
	}
	return hex.EncodeToString(b)
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func sevRank(s string) int {
	switch s {
	case "critical":
		return 0
	case "high":
		return 1
	case "medium":
		return 2
	case "low":
		return 3
	case "info":
		return 4
	}
	return 5
}
