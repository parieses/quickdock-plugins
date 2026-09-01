// 端口服务指纹识别 — nmap-style 服务/版本探测
//
// 流程：TCP 端口探测 → banner 抓取（静默读 / 协议探针 / HTTP 试探）→ 正则特征库匹配。
// 全程只用 TCP/UDP 连接，不依赖 raw socket，因此无需管理员权限。
//
// 命令：start / poll / stop
//   端口范围可到 1-65535，同步执行必然超过宿主 20s 限制，
//   故采用「start 立即返回 + 前端轮询」的异步会话模型，顺带能实时显示进度。
package main

import (
	_ "embed"
	"bufio"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

/* ==================== 特征库 ==================== */

//go:embed signatures.json
var sigJSON []byte

type signature struct {
	Service      string  `json:"service"`
	Category     string  `json:"category"`
	Pattern      string  `json:"pattern"`
	VersionGroup int     `json:"version_group"`
	Confidence   float64 `json:"confidence"`
	Ports        []int   `json:"ports"`

	re *regexp.Regexp
}

var signatures []signature

func init() {
	var raw []signature
	if err := json.Unmarshal(sigJSON, &raw); err != nil {
		return
	}
	for i := range raw {
		re, err := regexp.Compile(raw[i].Pattern)
		if err != nil {
			continue // 跳过非法正则，不让单条坏特征拖垮整体
		}
		raw[i].re = re
		signatures = append(signatures, raw[i])
	}
}

type fpMatch struct {
	Service    string  `json:"service"`
	Category   string  `json:"category"`
	Version    string  `json:"version,omitempty"`
	Confidence float64 `json:"confidence"`
}

// matchFingerprint 对 banner 跑特征库。端口不在特征预期端口时轻微降权，
// 避免把 80 端口上的 nginx 认成别的。
func matchFingerprint(banner string, port int) []fpMatch {
	if banner == "" {
		return nil
	}
	var out []fpMatch
	seen := map[string]bool{}

	for _, s := range signatures {
		if s.re == nil || seen[s.Service] {
			continue
		}
		m := s.re.FindStringSubmatch(banner)
		if m == nil {
			continue
		}
		seen[s.Service] = true

		conf := s.Confidence
		if len(s.Ports) > 0 && !containsInt(s.Ports, port) {
			conf *= 0.85
		}
		ver := ""
		if s.VersionGroup > 0 && s.VersionGroup < len(m) {
			ver = strings.TrimSpace(m[s.VersionGroup])
		}
		out = append(out, fpMatch{
			Service: s.Service, Category: s.Category,
			Version: ver, Confidence: conf,
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Confidence > out[j].Confidence })
	return out
}

// httpStatusRe 识别 HTTP 响应行。很多设备（路由器、网关、管理后台）不返回 Server 头，
// 特征库匹配不到具体产品，但至少能确定「这是个 HTTP 服务」。
var httpStatusRe = regexp.MustCompile(`^HTTP/([\d.]+) (\d{3})`)

// serverHeaderRe 提取 Server 响应头。banner 的 \r\n 已被 sanitize 成空格，
// 因此头部之间以双空格分隔，用非贪婪匹配到下一个头（或结尾）为止。
var serverHeaderRe = regexp.MustCompile(`(?i)Server:\s*([^\r\n]+?)(?:\s{2,}|$)`)

// portGuess 连 banner 都抓不到时按端口推测服务（如 Windows 的 445/135/139 不主动发 banner）。
// 置信度刻意压低，且结果会标记 guessed=true，前端显示「推测」以区别于真实指纹。
var portGuess = map[int][2]string{
	22: {"SSH", "远程登录"}, 23: {"Telnet", "远程登录"}, 3389: {"RDP", "远程登录"},
	5900: {"VNC", "远程登录"}, 5901: {"VNC", "远程登录"},
	135: {"MS RPC", "网络服务"}, 139: {"NetBIOS", "网络服务"}, 445: {"SMB", "文件传输"},
	53: {"DNS", "网络服务"}, 161: {"SNMP", "网络服务"}, 389: {"LDAP", "网络服务"},
	636: {"LDAPS", "网络服务"}, 123: {"NTP", "网络服务"}, 514: {"Syslog", "网络服务"},
	21: {"FTP", "文件传输"}, 2049: {"NFS", "文件传输"}, 873: {"rsync", "文件传输"},
	3306: {"MySQL", "数据库"}, 3307: {"MySQL", "数据库"}, 5432: {"PostgreSQL", "数据库"},
	1433: {"SQL Server", "数据库"}, 1521: {"Oracle DB", "数据库"},
	27017: {"MongoDB", "数据库"}, 9200: {"Elasticsearch", "数据库"},
	8086: {"InfluxDB", "数据库"}, 9042: {"Cassandra", "数据库"},
	5984: {"CouchDB", "数据库"}, 8123: {"ClickHouse", "数据库"},
	6379: {"Redis", "缓存"}, 6380: {"Redis", "缓存"}, 11211: {"Memcached", "缓存"},
	2181: {"ZooKeeper", "消息队列"}, 9092: {"Kafka", "消息队列"},
	5672: {"RabbitMQ", "消息队列"}, 15672: {"RabbitMQ 管理台", "消息队列"},
	8161: {"ActiveMQ 管理台", "消息队列"}, 9876: {"RocketMQ", "消息队列"},
	5601: {"Kibana", "运维平台"}, 3000: {"Grafana", "运维平台"},
	9090: {"Prometheus", "运维平台"}, 9000: {"SonarQube", "运维平台"},
	8848: {"Nacos", "微服务"}, 8500: {"Consul", "微服务"}, 8761: {"Eureka", "微服务"},
	6443: {"Kubernetes API", "容器编排"}, 2375: {"Docker", "容器编排"},
	2376: {"Docker TLS", "容器编排"}, 2379: {"etcd", "容器编排"},
	25: {"SMTP", "邮件"}, 587: {"SMTP", "邮件"}, 465: {"SMTPS", "邮件"},
	110: {"POP3", "邮件"}, 143: {"IMAP", "邮件"}, 993: {"IMAPS", "邮件"},
	995: {"POP3S", "邮件"}, 3128: {"Squid 代理", "网络服务"},
}

func containsInt(list []int, v int) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

/* ==================== 端口预设 ==================== */

var portPresets = map[string][]int{
	// 高频综合端口
	"common": {21, 22, 23, 25, 53, 80, 110, 111, 135, 139, 143, 161, 389, 443, 445,
		465, 587, 636, 873, 993, 995, 1025, 1433, 1723, 1883, 2049, 2181, 2375, 2379,
		3000, 3306, 3389, 5432, 5601, 5672, 5900, 5984, 6379, 6443, 7001, 8000, 8069,
		8080, 8081, 8086, 8088, 8090, 8161, 8443, 8500, 8761, 8848, 8888, 9000, 9090,
		9092, 9200, 9300, 9443, 10000, 11211, 15672, 27017, 50000},
	// Web 相关
	"web": {80, 443, 8000, 8008, 8009, 8069, 8080, 8081, 8082, 8086, 8088, 8090,
		8098, 8161, 8181, 8222, 8443, 8500, 8530, 8531, 8761, 8848, 8880, 8888,
		9000, 9001, 9080, 9090, 9200, 9443, 10000},
	// 数据库与缓存
	"db": {1433, 1521, 3306, 3307, 5432, 5433, 5984, 6379, 6380, 7001, 8086,
		9042, 9160, 11211, 27017, 27018, 28017, 50000},
	// Top 100（nmap 常用集合裁剪）
	"top100": {7, 9, 13, 21, 22, 23, 25, 26, 37, 53, 79, 80, 81, 88, 106, 110,
		111, 113, 119, 135, 139, 143, 144, 179, 199, 389, 427, 443, 444, 445, 465,
		513, 514, 515, 543, 544, 548, 554, 587, 631, 636, 646, 873, 990, 993, 995,
		1025, 1026, 1027, 1028, 1029, 1110, 1433, 1720, 1723, 1755, 1900, 2000,
		2001, 2049, 2121, 2717, 3000, 3128, 3306, 3389, 3986, 4899, 5000, 5009,
		5051, 5060, 5101, 5190, 5357, 5432, 5631, 5666, 5800, 5900, 6000, 6001,
		6646, 7070, 8000, 8008, 8009, 8080, 8081, 8443, 8888, 9100, 9999, 10000,
		32768, 49152, 49157},
}

/* ==================== 探测原语 ==================== */

const bannerMax = 2048

// sanitize 把 banner 转成可读文本：可打印字符保留，控制字符转空格，
// 其余二进制转成 '.'。匹配与展示都用它，保证正则在干净文本上工作。
func sanitize(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 32 && r < 127:
			b.WriteRune(r)
		case r == '\n' || r == '\r' || r == '\t':
			b.WriteRune(' ')
		default:
			b.WriteRune('.')
		}
	}
	out := b.String()
	if len(out) > bannerMax {
		out = out[:bannerMax]
	}
	return strings.TrimSpace(out)
}

func dialTCP(ip string, port int, timeout time.Duration) (net.Conn, error) {
	return net.DialTimeout("tcp", net.JoinHostPort(ip, strconv.Itoa(port)), timeout)
}

// silentRead 连接后直接读：适用于 SSH / FTP / SMTP / MySQL 这类服务端主动发 banner 的服务
func silentRead(ip string, port int, timeout time.Duration) string {
	conn, err := dialTCP(ip, port, timeout)
	if err != nil {
		return ""
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(timeout))
	buf := make([]byte, bannerMax)
	n, err := conn.Read(buf)
	if err != nil && n == 0 {
		return ""
	}
	return sanitize(string(buf[:n]))
}

// sendRead 连接后先发探针再读：适用于 Redis / Memcached 这类请求-响应式服务
func sendRead(ip string, port int, payload string, timeout time.Duration) string {
	conn, err := dialTCP(ip, port, timeout)
	if err != nil {
		return ""
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(timeout))
	if _, err := conn.Write([]byte(payload)); err != nil {
		return ""
	}
	buf := make([]byte, bannerMax)
	n, err := conn.Read(buf)
	if err != nil && n == 0 {
		return ""
	}
	return sanitize(string(buf[:n]))
}

func udpQuery(ip string, port int, payload []byte, timeout time.Duration) string {
	conn, err := net.DialTimeout("udp", net.JoinHostPort(ip, strconv.Itoa(port)), timeout)
	if err != nil {
		return ""
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(timeout))
	if _, err := conn.Write(payload); err != nil {
		return ""
	}
	buf := make([]byte, bannerMax)
	n, err := conn.Read(buf)
	if err != nil && n == 0 {
		return ""
	}
	return sanitize(string(buf[:n]))
}

// httpProbe 发 HEAD 请求并读取响应头（Server / X-Powered-By 等指纹就在这里）
func httpProbe(host, ip string, port int, useTLS bool, timeout time.Duration) string {
	addr := net.JoinHostPort(ip, strconv.Itoa(port))
	var conn net.Conn
	var err error

	if useTLS {
		conn, err = tls.DialWithDialer(
			&net.Dialer{Timeout: timeout}, "tcp", addr,
			&tls.Config{InsecureSkipVerify: true, ServerName: host}, // 自签证书也要能探测
		)
	} else {
		conn, err = dialTCP(ip, port, timeout)
	}
	if err != nil {
		return ""
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(timeout))

	req := fmt.Sprintf("HEAD / HTTP/1.1\r\nHost: %s\r\nUser-Agent: Mozilla/5.0 (QuickDock)\r\nAccept: */*\r\nConnection: close\r\n\r\n", host)
	if _, err := conn.Write([]byte(req)); err != nil {
		return ""
	}
	buf := make([]byte, bannerMax)
	n, err := conn.Read(buf)
	if err != nil && n == 0 {
		return ""
	}
	return sanitize(string(buf[:n]))
}

// buildDNSVersionQuery 构造 chaos TXT version.bind 查询（探测 BIND/dnsmasq 版本）
func buildDNSVersionQuery() []byte {
	q := []byte{0x12, 0x34, 0x01, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	q = append(q, 0x07)
	q = append(q, []byte("version")...)
	q = append(q, 0x04)
	q = append(q, []byte("bind")...)
	q = append(q, 0x00)
	q = append(q, 0x00, 0x10) // QTYPE = TXT
	q = append(q, 0x00, 0x03) // QCLASS = CHAOS
	return q
}

// buildSNMPGet 构造 SNMPv2c GetRequest，取 sysDescr(1.3.6.1.2.1.1.1.0)，community=public
func buildSNMPGet() []byte {
	oid := []byte{0x2b, 0x06, 0x01, 0x02, 0x01, 0x01, 0x01, 0x00}

	varBind := []byte{0x30, byte(2 + len(oid) + 2), 0x06, byte(len(oid))}
	varBind = append(varBind, oid...)
	varBind = append(varBind, 0x05, 0x00)

	vbList := append([]byte{0x30, byte(len(varBind))}, varBind...)

	pduBody := []byte{0x02, 0x04, 0x00, 0x00, 0x00, 0x01, 0x02, 0x01, 0x00, 0x02, 0x01, 0x00}
	pduBody = append(pduBody, vbList...)
	pdu := append([]byte{0xa0, byte(len(pduBody))}, pduBody...)

	body := []byte{0x02, 0x01, 0x01, 0x04, 0x06}
	body = append(body, []byte("public")...)
	body = append(body, pdu...)

	return append([]byte{0x30, byte(len(body))}, body...)
}

var tlsPorts = map[int]bool{443: true, 8443: true, 9443: true, 465: true, 993: true, 995: true, 636: true}

// probePort 三段式探测：静默读 → 按端口的协议探针 → HTTP 试探 → TLS 回退
func probePort(host, ip string, port int, timeout time.Duration) (banner, scheme string) {
	scheme = "tcp"

	if b := silentRead(ip, port, timeout); b != "" {
		return b, scheme
	}

	switch port {
	case 6379, 6380: // Redis
		if b := sendRead(ip, port, "PING\r\nINFO\r\n", timeout); b != "" {
			return b, scheme
		}
	case 11211: // Memcached
		if b := sendRead(ip, port, "version\r\n", timeout); b != "" {
			return b, scheme
		}
	case 53: // DNS(UDP)
		if b := udpQuery(ip, port, buildDNSVersionQuery(), timeout); b != "" {
			return b, "udp"
		}
	case 161: // SNMP(UDP)
		if b := udpQuery(ip, port, buildSNMPGet(), timeout); b != "" {
			return b, "udp"
		}
	}

	if b := httpProbe(host, ip, port, false, timeout); b != "" {
		return b, scheme
	}
	if tlsPorts[port] {
		if b := httpProbe(host, ip, port, true, timeout); b != "" {
			return b, scheme
		}
	}
	return "", scheme
}

/* ==================== 端口解析 ==================== */

// parsePorts 支持预设名(common/web/db/top100)、逗号列表、"a-b" 范围与它们的组合
func parsePorts(spec string) ([]int, error) {
	spec = strings.TrimSpace(strings.ToLower(spec))
	if spec == "" {
		return portPresets["common"], nil
	}
	if p, ok := portPresets[spec]; ok {
		return p, nil
	}
	if spec == "all" {
		out := make([]int, 0, 65535)
		for i := 1; i <= 65535; i++ {
			out = append(out, i)
		}
		return out, nil
	}

	seen := map[int]bool{}
	out := []int{}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if idx := strings.Index(part, "-"); idx > 0 {
			lo, err1 := strconv.Atoi(strings.TrimSpace(part[:idx]))
			hi, err2 := strconv.Atoi(strings.TrimSpace(part[idx+1:]))
			if err1 != nil || err2 != nil || lo < 1 || hi > 65535 || lo > hi {
				return nil, fmt.Errorf("端口范围无效: %s", part)
			}
			if hi-lo > 20000 {
				return nil, fmt.Errorf("端口范围过大: %s（最多 20000 个）", part)
			}
			for i := lo; i <= hi; i++ {
				if !seen[i] {
					seen[i] = true
					out = append(out, i)
				}
			}
			continue
		}
		n, err := strconv.Atoi(part)
		if err != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("端口无效: %s", part)
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("未解析到任何端口")
	}
	sort.Ints(out)
	return out, nil
}

/* ==================== 会话 ==================== */

type portResult struct {
	Port       int       `json:"port"`
	Scheme     string    `json:"scheme"`
	Open       bool      `json:"open"`
	Service    string    `json:"service,omitempty"`
	Category   string    `json:"category,omitempty"`
	Version    string    `json:"version,omitempty"`
	Confidence float64   `json:"confidence"`
	Banner     string    `json:"banner,omitempty"`
	StatusCode int       `json:"statusCode,omitempty"` // HTTP 状态码（仅无产品指纹时给出）
	Guessed    bool      `json:"guessed,omitempty"`    // true = 按端口推测，非真实指纹
	Matches    []fpMatch `json:"matches,omitempty"`
}

type session struct {
	ID    string
	Host  string
	IP    string
	Ports []int

	mu      sync.Mutex
	open    []int
	results []portResult

	scanned int32
	running bool
	stopCh  chan struct{}
}

var (
	sessMu   sync.Mutex
	sessions = map[string]*session{}
	seqID    int
)

func (s *session) stopped() bool {
	select {
	case <-s.stopCh:
		return true
	default:
		return false
	}
}

func (s *session) run(timeoutMs, conc int) {
	defer func() {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
	}()

	dur := time.Duration(timeoutMs) * time.Millisecond

	// 阶段一：并发探测端口开放情况
	var wg sync.WaitGroup
	sem := make(chan struct{}, conc)
	var openMu sync.Mutex

	for _, p := range s.Ports {
		if s.stopped() {
			break
		}
		wg.Add(1)
		go func(port int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			conn, err := dialTCP(s.IP, port, dur)
			if err == nil {
				conn.Close()
				openMu.Lock()
				s.open = append(s.open, port)
				openMu.Unlock()
			}
			atomic.AddInt32(&s.scanned, 1)
		}(p)
	}
	wg.Wait()

	sort.Ints(s.open)
	if s.stopped() {
		return
	}

	// 阶段二：对开放端口逐个做指纹识别
	fpConc := conc / 2
	if fpConc < 8 {
		fpConc = 8
	}
	sem2 := make(chan struct{}, fpConc)

	for _, p := range s.open {
		if s.stopped() {
			return
		}
		wg.Add(1)
		go func(port int) {
			defer wg.Done()
			sem2 <- struct{}{}
			defer func() { <-sem2 }()

			banner, scheme := probePort(s.Host, s.IP, port, dur)
			res := portResult{Port: port, Scheme: scheme, Open: true, Banner: banner}

			if matches := matchFingerprint(banner, port); len(matches) > 0 {
				// 命中特征库：真实指纹
				res.Matches = matches
				best := matches[0]
				res.Service = best.Service
				res.Category = best.Category
				res.Version = best.Version
				res.Confidence = best.Confidence
			} else if m := httpStatusRe.FindStringSubmatch(banner); m != nil {
				// 抓不到产品指纹，但确认是 HTTP 服务
				code, _ := strconv.Atoi(m[2])
				res.Service = "HTTP"
				res.Category = "Web 服务"
				res.Version = m[1]
				res.Confidence = 0.50
				res.StatusCode = code
				// Server 头是服务自报的身份（如腾讯 stgw、自研网关），
				// 即便特征库没收录该产品，也比笼统的 "HTTP" 有信息量
				if sm := serverHeaderRe.FindStringSubmatch(banner); sm != nil {
					if srv := strings.TrimSpace(sm[1]); srv != "" {
						res.Service = srv
						res.Confidence = 0.60
					}
				}
				res.Matches = []fpMatch{{Service: res.Service, Category: "Web 服务", Version: m[1], Confidence: res.Confidence}}
			} else if g, ok := portGuess[port]; ok {
				// 完全无 banner：按端口推测（Windows 的 445/135/139 等）
				res.Service = g[0]
				res.Category = g[1]
				res.Confidence = 0.35
				res.Guessed = true
			}
			s.mu.Lock()
			s.results = append(s.results, res)
			s.mu.Unlock()
		}(p)
	}
	wg.Wait()

	// 结果按端口排序
	s.mu.Lock()
	sort.Slice(s.results, func(i, j int) bool { return s.results[i].Port < s.results[j].Port })
	s.mu.Unlock()
}

func (s *session) snapshot() map[string]interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()

	results := make([]portResult, len(s.results))
	copy(results, s.results)

	return map[string]interface{}{
		"sessionId": s.ID,
		"host":      s.Host,
		"ip":        s.IP,
		"running":   s.running,
		"progress": map[string]interface{}{
			"total":   len(s.Ports),
			"scanned": int(atomic.LoadInt32(&s.scanned)),
			"open":    len(s.open),
		},
		"results": results,
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

func intFrom(m map[string]interface{}, key string, def int) int {
	switch v := m[key].(type) {
	case float64:
		if n := int(v); n > 0 {
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

func handleStart(id int64, input map[string]interface{}) {
	host := strings.TrimSpace(strFrom(input, "host"))
	if host == "" {
		respondError(id, -32602, "请输入主机名或 IP")
		return
	}
	host = strings.TrimPrefix(host, "https://")
	host = strings.TrimPrefix(host, "http://")
	if idx := strings.IndexAny(host, "/"); idx >= 0 {
		host = host[:idx]
	}

	ips, err := net.LookupIP(host)
	if err != nil || len(ips) == 0 {
		respondError(id, -32001, "无法解析主机: "+host)
		return
	}
	ip := ips[0]

	ports, err := parsePorts(strFrom(input, "ports"))
	if err != nil {
		respondError(id, -32602, err.Error())
		return
	}

	timeoutMs := intFrom(input, "timeout", 800)
	if timeoutMs > 10000 {
		timeoutMs = 10000
	}
	conc := intFrom(input, "concurrency", 256)
	if conc > 1024 {
		conc = 1024
	}

	sessMu.Lock()
	seqID++
	sid := fmt.Sprintf("f%d", seqID)
	s := &session{
		ID: sid, Host: host, IP: ip.String(), Ports: ports,
		running: true, stopCh: make(chan struct{}),
	}
	sessions[sid] = s
	sessMu.Unlock()

	go s.run(timeoutMs, conc)

	respond(id, map[string]interface{}{
		"sessionId": sid, "host": host, "ip": ip.String(),
		"total": len(ports), "running": true,
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
		respond(req.ID, map[string]interface{}{
			"status": "ready", "name": "QuickDock Service Fingerprint",
			"signatures": len(signatures),
		})
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
