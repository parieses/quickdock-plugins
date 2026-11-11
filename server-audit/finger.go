// 服务指纹：TLS 握手与证书解析 → banner 抓取 → 轻量主动探测 → 服务归类。
//
// 每一步都是「读」，唯一的写操作是发一条协议探测指令（HEAD / PING / version / isMaster），
// 不修改目标任何状态、不读取业务数据。
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const bannerMax = 2048

type HeaderKV struct {
	K string `json:"k"`
	V string `json:"v"`
}

type TLSInfo struct {
	Version      string   `json:"version"`
	Cipher       string   `json:"cipher"`
	ALPN         string   `json:"alpn,omitempty"`
	Subject      string   `json:"subject"`
	SAN          []string `json:"san"`
	Issuer       string   `json:"issuer"`
	NotBefore    string   `json:"notBefore"`
	NotAfter     string   `json:"notAfter"`
	DaysLeft     int      `json:"daysLeft"`
	SigAlg       string   `json:"sigAlg"`
	KeyAlg       string   `json:"keyAlg"`
	KeyBits      int      `json:"keyBits"`
	SelfSigned   bool     `json:"selfSigned"`
	HostMismatch bool     `json:"hostMismatch"`
}

type ServiceInfo struct {
	Port     int        `json:"port"`
	Service  string     `json:"service"`
	Label    string     `json:"label"`
	Version  string     `json:"version,omitempty"`
	Scheme   string     `json:"scheme"`
	Status   int        `json:"status,omitempty"`
	Banner   string     `json:"banner,omitempty"`
	Evidence string     `json:"evidence,omitempty"`
	Headers  []HeaderKV `json:"headers,omitempty"`
	TLS      *TLSInfo   `json:"tls,omitempty"`
}

/* ==================== 编排 ==================== */

func (s *auditSession) fingerprint(open []int) {
	if len(open) == 0 {
		return
	}
	// 指纹阶段比端口扫描宽松：一次 TLS 握手要 2~3 个 RTT，再叠一层 HTTP HEAD，
	// 沿用端口扫描的 800ms 会把正常服务误判成"无响应"
	timeout := time.Duration(s.opts.TimeoutMs*3) * time.Millisecond
	if timeout < 2*time.Second {
		timeout = 2 * time.Second
	}
	if timeout > 8*time.Second {
		timeout = 8 * time.Second
	}
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, port := range open {
		if s.stopped() {
			break
		}
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			info := s.probeService(p, timeout)
			atomic.AddInt64(&s.scanned, 1)
			mu.Lock()
			s.services = append(s.services, info)
			mu.Unlock()
		}(port)
	}
	wg.Wait()
	s.mu.Lock()
	sortServices(s.services)
	s.mu.Unlock()
}

func (s *auditSession) probeService(port int, timeout time.Duration) ServiceInfo {
	host, ip := s.target.Host, s.target.IP
	info := ServiceInfo{Port: port, Scheme: "tcp"}

	// 1) 常见 TLS 端口先试握手：证书信息本身就是要报给用户的东西
	if tlsLikelyPort(port) {
		if ti, ok := tlsProbe(host, ip, port, timeout); ok {
			info.TLS = ti
			if st, hdr, body, err := httpHead(host, ip, port, true, timeout); err == nil {
				info.Scheme, info.Status, info.Headers = "https", st, pickHeaders(hdr)
				info.Service, info.Label, info.Version, info.Evidence = classifyHTTP(st, hdr, body, true)
			}
			if info.Service == "" {
				info.Service, info.Label = "https", "HTTPS"
				info.Evidence = "TLS 握手成功（未取到 HTTP 响应头）"
			}
			return info
		}
	}

	// 2) 静默读 banner（SSH / FTP / MySQL / SMTP 等会主动打招呼）
	banner := silentRead(ip, port, timeout)
	if svc, label, ver := classifyBanner(banner, port); svc != "" {
		info.Banner, info.Service, info.Label, info.Version = banner, svc, label, ver
		info.Evidence = "服务主动返回 banner"
		return info
	}

	// 3) HTTP HEAD（明文）
	if st, hdr, body, err := httpHead(host, ip, port, false, timeout); err == nil {
		info.Scheme, info.Status, info.Headers = "http", st, pickHeaders(hdr)
		info.Service, info.Label, info.Version, info.Evidence = classifyHTTP(st, hdr, body, false)
		if info.Banner == "" {
			info.Banner = banner
		}
		return info
	}

	// 4) 非 TLS 端口也可能跑 TLS
	if !tlsLikelyPort(port) {
		if ti, ok := tlsProbe(host, ip, port, timeout); ok {
			info.TLS = ti
			if st, hdr, body, err := httpHead(host, ip, port, true, timeout); err == nil {
				info.Scheme, info.Status, info.Headers = "https", st, pickHeaders(hdr)
				info.Service, info.Label, info.Version, info.Evidence = classifyHTTP(st, hdr, body, true)
			}
			if info.Service == "" {
				info.Service, info.Label = "https", "HTTPS"
				info.Evidence = "TLS 握手成功（未取到 HTTP 响应头）"
			}
			return info
		}
	}

	// 5) 按端口/协议做单次轻量探测
	if svc, label, ver, ev := s.protocolProbe(port, timeout); svc != "" {
		info.Banner, info.Service, info.Label, info.Version, info.Evidence = banner, svc, label, ver, ev
		return info
	}

	// 6) 兜底：端口推测
	info.Banner = banner
	info.Service, info.Label = guessByPort(port)
	if info.Service == "" {
		info.Service, info.Label = "other", "未知服务"
		info.Evidence = "无可识别响应，仅端口开放"
	} else {
		info.Evidence = "无 banner，按端口号推测"
	}
	return info
}

func (s *auditSession) protocolProbe(port int, timeout time.Duration) (svc, label, ver, evidence string) {
	ip := s.target.IP
	switch port {
	case 6379, 6380, 16379:
		if resp, ok := sendRecv(ip, port, "PING\r\n", timeout); ok {
			up := strings.ToUpper(resp)
			switch {
			case strings.Contains(up, "+PONG"):
				return "redis", "Redis（无认证）", redisVersion(ip, port, timeout), "PING 返回 +PONG，无需认证"
			case strings.Contains(up, "NOAUTH"), strings.Contains(up, "WRONGPASS"):
				return "redis", "Redis", redisVersion(ip, port, timeout), "PING 返回 NOAUTH，已启用认证"
			}
		}
	case 11211:
		if resp, ok := sendRecv(ip, port, "version\r\n", timeout); ok {
			if v := versionAfter(resp, "VERSION "); v != "" {
				return "memcached", "Memcached", v, "version 返回 VERSION 行"
			}
		}
	case 27017, 27018:
		if v, ok := mongoProbe(ip, port, timeout); ok {
			return "mongodb", "MongoDB", v, "isMaster 握手应答"
		}
	case 2375, 2376:
		if st, hdr, body, err := httpFetch(s.target.Host, ip, port, port == 2376, "GET", "/version", timeout, 4096); err == nil && st == 200 {
			if v := firstMatch(body, `"ApiVersion"\s*:\s*"([^"]+)"`); v != "" {
				_ = hdr
				return "docker", "Docker API（未授权）", v, "GET /version 返回 API 版本"
			}
		}
	case 9200, 9201:
		if st, _, body, err := httpFetch(s.target.Host, ip, port, false, "GET", "/", timeout, 8192); err == nil && st == 200 {
			if strings.Contains(body, "cluster_name") || strings.Contains(body, "You Know, for Search") {
				return "es", "Elasticsearch（未授权）", firstMatch(body, `"number"\s*:\s*"([^"]+)"`), "GET / 返回集群信息 JSON"
			}
		}
	}
	return "", "", "", ""
}

/* ==================== TLS ==================== */

func tlsLikelyPort(port int) bool {
	switch port {
	case 443, 465, 636, 853, 993, 995, 2376, 4443, 5223, 5671, 6443, 8443, 8444,
		8530, 8531, 8883, 9443, 9444, 10443:
		return true
	}
	return false
}

func tlsProbe(host, ip string, port int, timeout time.Duration) (*TLSInfo, bool) {
	addr := net.JoinHostPort(ip, strconv.Itoa(port))
	cfg := &tls.Config{
		InsecureSkipVerify: true, // 自查场景：证书链是否可信由报告分级判断，不阻断握手
		MinVersion:         tls.VersionTLS10,
	}
	if net.ParseIP(host) == nil {
		cfg.ServerName = host
	}
	dialer := &net.Dialer{Timeout: timeout}
	conn, err := tls.DialWithDialer(dialer, "tcp", addr, cfg)
	if err != nil {
		return nil, false
	}
	defer conn.Close()
	state := conn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return nil, false
	}
	cert := state.PeerCertificates[0]

	ti := &TLSInfo{
		Version:   tls.VersionName(state.Version),
		Cipher:    tls.CipherSuiteName(state.CipherSuite),
		ALPN:      state.NegotiatedProtocol,
		Subject:   cert.Subject.CommonName,
		SAN:       sanList(cert.DNSNames, cert.IPAddresses),
		Issuer:    issuerName(cert.Issuer.CommonName, cert.Issuer.Organization),
		NotBefore: cert.NotBefore.Format("2006-01-02"),
		NotAfter:  cert.NotAfter.Format("2006-01-02"),
		DaysLeft:  int(time.Until(cert.NotAfter).Hours() / 24),
		SigAlg:    cert.SignatureAlgorithm.String(),
	}
	if ti.Subject == "" {
		ti.Subject = "(无 CN)"
	}
	ti.SelfSigned = cert.CheckSignatureFrom(cert) == nil || strings.EqualFold(ti.Subject, ti.Issuer)
	if net.ParseIP(host) == nil {
		ti.HostMismatch = cert.VerifyHostname(host) != nil
	}
	switch pub := cert.PublicKey.(type) {
	case *rsa.PublicKey:
		ti.KeyAlg, ti.KeyBits = "RSA", pub.N.BitLen()
	case *ecdsa.PublicKey:
		ti.KeyAlg, ti.KeyBits = "ECDSA", pub.Curve.Params().BitSize
	case ed25519.PublicKey:
		ti.KeyAlg, ti.KeyBits = "Ed25519", 256
	default:
		ti.KeyAlg = fmt.Sprintf("%T", cert.PublicKey)
	}
	return ti, true
}

func sanList(dns []string, ips []net.IP) []string {
	out := append([]string(nil), dns...)
	for _, ip := range ips {
		out = append(out, ip.String())
	}
	if len(out) > 20 {
		out = out[:20]
	}
	return out
}

func issuerName(cn string, org []string) string {
	if cn != "" {
		return cn
	}
	if len(org) > 0 {
		return org[0]
	}
	return "(未知颁发者)"
}

/* ==================== HTTP ==================== */

func httpFetch(host, ip string, port int, useTLS bool, method, path string, timeout time.Duration, maxBody int64) (int, http.Header, string, error) {
	scheme := "http"
	if useTLS {
		scheme = "https"
	}
	dialer := &net.Dialer{Timeout: timeout}
	tr := &http.Transport{
		Proxy:             nil,
		DisableKeepAlives: true,
		// 固定连到已解析的 IP，Host 头与 SNI 仍用原始域名：既避免重复解析，
		// 也让虚拟主机识别正确。
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialer.DialContext(ctx, "tcp", net.JoinHostPort(ip, strconv.Itoa(port)))
		},
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true},
		ForceAttemptHTTP2: false,
	}
	client := &http.Client{
		Transport: tr,
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse // 不跟随跳转：只读探测不追进业务页面
		},
	}
	defer tr.CloseIdleConnections()

	hostPort := host
	if (scheme == "http" && port != 80) || (scheme == "https" && port != 443) {
		hostPort = net.JoinHostPort(host, strconv.Itoa(port))
	}
	u := scheme + "://" + hostPort + path
	req, err := http.NewRequest(method, u, nil)
	if err != nil {
		return 0, nil, "", err
	}
	req.Header.Set("User-Agent", "QuickDock-SelfAudit/1.0 (+read-only)")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Connection", "close")
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, "", err
	}
	defer resp.Body.Close()
	body := ""
	if maxBody > 0 {
		b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
		if err == nil {
			body = string(b)
		}
	} else {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
	}
	return resp.StatusCode, resp.Header, body, nil
}

// httpHead 取响应头：跨境线路的首个请求常因握手+排队超时，允许一次重试。
// 仅用于指纹阶段的 HEAD，路径探测不重试（避免任何"重复请求"倾向）。
func httpHead(host, ip string, port int, useTLS bool, timeout time.Duration) (int, http.Header, string, error) {
	st, hdr, body, err := httpFetch(host, ip, port, useTLS, "HEAD", "/", timeout, 0)
	if err == nil {
		return st, hdr, body, nil
	}
	retry := timeout
	if retry < 4*time.Second {
		retry = 4 * time.Second
	}
	return httpFetch(host, ip, port, useTLS, "HEAD", "/", retry, 0)
}

func pickHeaders(h http.Header) []HeaderKV {
	keys := []string{"server", "x-powered-by", "x-aspnet-version", "x-generator", "content-type", "location", "x-frame-options", "content-security-policy", "strict-transport-security", "x-content-type-options", "www-authenticate"}
	var out []HeaderKV
	for _, k := range keys {
		if v := strings.TrimSpace(h.Get(k)); v != "" {
			out = append(out, HeaderKV{K: k, V: truncate(v, 160)})
		}
	}
	return out
}

/* ==================== banner / 归类 ==================== */

func silentRead(ip string, port int, timeout time.Duration) string {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(ip, strconv.Itoa(port)), timeout)
	if err != nil {
		return ""
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	buf := make([]byte, bannerMax)
	n, err := conn.Read(buf)
	if n == 0 && err != nil {
		return ""
	}
	return sanitize(string(buf[:n]))
}

func sendRecv(ip string, port int, payload string, timeout time.Duration) (string, bool) {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(ip, strconv.Itoa(port)), timeout)
	if err != nil {
		return "", false
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if _, err := conn.Write([]byte(payload)); err != nil {
		return "", false
	}
	buf := make([]byte, bannerMax)
	n, err := conn.Read(buf)
	if n == 0 && err != nil {
		return "", false
	}
	return sanitize(string(buf[:n])), true
}

var (
	reSSH   = regexp.MustCompile(`(?i)^SSH-([\d.]+)-(\S+)`)
	reMySQL = regexp.MustCompile(`(?i)(\d+\.\d+\.\d+[\w.-]*)`)
)

func classifyBanner(banner string, port int) (svc, label, version string) {
	b := strings.TrimSpace(banner)
	if b == "" {
		return "", "", ""
	}
	if m := reSSH.FindStringSubmatch(b); m != nil {
		name := m[2]
		ver := ""
		if i := strings.LastIndexAny(name, "_-"); i > 0 {
			ver = name[i+1:]
		}
		return "ssh", "SSH", ver
	}
	low := strings.ToLower(b)
	if strings.Contains(low, "mysql_native_password") || (port == 3306 && strings.Contains(b, "\x00")) {
		return "mysql", "MySQL", firstMatch(b, reMySQL.String())
	}
	if strings.HasPrefix(b, "220") && (strings.Contains(low, "ftp") || strings.Contains(low, "vsftpd") || strings.Contains(low, "proftpd") || port == 21) {
		return "ftp", "FTP", firstMatch(low, `(?:vsftpd|proftpd|pure-ftpd|filezilla|microsoft ftp service)[/ ]?([\d.]+)?`)
	}
	if strings.HasPrefix(b, "220") && (strings.Contains(low, "smtp") || strings.Contains(low, "esmtp") || port == 25 || port == 587) {
		return "smtp", "SMTP", ""
	}
	if strings.HasPrefix(b, "+OK") || port == 110 || port == 995 {
		return "pop3", "POP3", ""
	}
	if strings.HasPrefix(b, "* OK") || port == 143 || port == 993 {
		return "imap", "IMAP", ""
	}
	if strings.Contains(low, "version ") && port == 11211 {
		return "memcached", "Memcached", versionAfter(b, "VERSION ")
	}
	if strings.HasPrefix(b, "HTTP/1.") {
		return "http", "HTTP", ""
	}
	return "", "", ""
}

func classifyHTTP(status int, h http.Header, body string, useTLS bool) (svc, label, version, evidence string) {
	server := h.Get("Server")
	lowBody := strings.ToLower(body)
	if v := firstMatch(body, `"ApiVersion"\s*:\s*"([^"]+)"`); v != "" {
		return "docker", "Docker API（未授权）", v, "HTTP 响应暴露 Docker API 版本"
	}
	if strings.Contains(body, "cluster_name") && strings.Contains(lowBody, "you know, for search") {
		return "es", "Elasticsearch（未授权）", firstMatch(body, `"number"\s*:\s*"([^"]+)"`), "HTTP 响应暴露 ES 集群信息"
	}
	ver := ""
	if server != "" {
		if m := regexp.MustCompile(`([A-Za-z][\w.-]*)/([\d.]+)`).FindStringSubmatch(server); m != nil {
			ver = m[1] + " " + m[2]
		}
	}
	if useTLS {
		if ver != "" {
			return "https", "HTTPS", ver, "HTTP 响应头：Server: " + server
		}
		return "https", "HTTPS", "", "TLS + HTTP 响应"
	}
	if ver != "" {
		return "http", "HTTP", ver, "HTTP 响应头：Server: " + server
	}
	return "http", "HTTP", "", "HTTP 响应（状态码 " + strconv.Itoa(status) + "）"
}

var portGuess = map[int][2]string{
	80: {"http", "HTTP"}, 443: {"https", "HTTPS"}, 8080: {"http", "HTTP"}, 8443: {"https", "HTTPS"},
	8000: {"http", "HTTP"}, 8888: {"http", "HTTP"}, 8081: {"http", "HTTP"},
	21: {"ftp", "FTP"}, 22: {"ssh", "SSH"}, 23: {"telnet", "Telnet"}, 25: {"smtp", "SMTP"},
	53: {"dns", "DNS"}, 110: {"pop3", "POP3"}, 135: {"msrpc", "MS RPC"}, 139: {"netbios", "NetBIOS"},
	143: {"imap", "IMAP"}, 161: {"snmp", "SNMP"}, 389: {"ldap", "LDAP"}, 445: {"smb", "SMB"},
	465: {"smtps", "SMTPS"}, 587: {"smtp", "SMTP"}, 636: {"ldaps", "LDAPS"}, 873: {"rsync", "rsync"},
	993: {"imaps", "IMAPS"}, 995: {"pop3s", "POP3S"}, 1433: {"mssql", "SQL Server"},
	1521: {"oracle", "Oracle DB"}, 2049: {"nfs", "NFS"}, 2375: {"docker", "Docker API"},
	2376: {"docker", "Docker API"}, 2379: {"etcd", "etcd"}, 3306: {"mysql", "MySQL"},
	3389: {"rdp", "RDP"}, 5432: {"postgres", "PostgreSQL"}, 5900: {"vnc", "VNC"},
	5984: {"couchdb", "CouchDB"}, 6379: {"redis", "Redis"}, 6443: {"k8s", "Kubernetes API"},
	7001: {"weblogic", "WebLogic"}, 8086: {"influxdb", "InfluxDB"}, 8161: {"activemq", "ActiveMQ"},
	8500: {"consul", "Consul"}, 8848: {"nacos", "Nacos"}, 9042: {"cassandra", "Cassandra"},
	9092: {"kafka", "Kafka"}, 9200: {"es", "Elasticsearch"}, 11211: {"memcached", "Memcached"},
	15672: {"rabbitmq", "RabbitMQ 管理台"}, 27017: {"mongodb", "MongoDB"}, 50000: {"db2", "DB2"},
}

func guessByPort(port int) (string, string) {
	if v, ok := portGuess[port]; ok {
		return v[0], v[1]
	}
	if port >= 8000 && port <= 9999 {
		return "", ""
	}
	return "", ""
}

/* ==================== 协议探测 ==================== */

func redisVersion(ip string, port int, timeout time.Duration) string {
	// 单次 INFO server：只取版本号，不读数据、不改状态
	if resp, ok := sendRecv(ip, port, "INFO server\r\n", timeout); ok {
		if v := firstMatch(resp, `redis_version:([\d.]+)`); v != "" {
			return v
		}
	}
	return ""
}

func mongoProbe(ip string, port int, timeout time.Duration) (string, bool) {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(ip, strconv.Itoa(port)), timeout)
	if err != nil {
		return "", false
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))

	// BSON {isMaster: 1}
	query := []byte{0x13, 0x00, 0x00, 0x00, 0x10}
	query = append(query, []byte("isMaster\x00")...)
	query = append(query, 0x01, 0x00, 0x00, 0x00, 0x00)

	body := []byte{0x00, 0x00, 0x00, 0x00} // flags
	body = append(body, []byte("admin.$cmd\x00")...)
	body = append(body, 0x00, 0x00, 0x00, 0x00) // numberToSkip
	body = append(body, 0xFF, 0xFF, 0xFF, 0xFF) // numberToReturn = -1
	body = append(body, query...)

	msg := make([]byte, 16)
	binary.LittleEndian.PutUint32(msg[0:], uint32(16+len(body)))
	binary.LittleEndian.PutUint32(msg[4:], 1)
	binary.LittleEndian.PutUint32(msg[8:], 0)
	binary.LittleEndian.PutUint32(msg[12:], 2004) // OP_QUERY
	msg = append(msg, body...)
	if _, err := conn.Write(msg); err != nil {
		return "", false
	}
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil && n == 0 {
		return "", false
	}
	resp := string(buf[:n])
	low := strings.ToLower(resp)
	if strings.Contains(low, "ismaster") || strings.Contains(low, "maxwireversion") ||
		strings.Contains(low, "unauthorized") {
		return "", true
	}
	return "", false
}

/* ==================== 工具 ==================== */

func sanitize(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 32 && r < 127:
			b.WriteRune(r)
		case r == '\n' || r == '\r' || r == '\t':
			b.WriteRune(' ')
		case r == 0:
			b.WriteRune(' ')
		default:
			b.WriteRune('.')
		}
	}
	out := strings.TrimSpace(b.String())
	return truncate(out, bannerMax)
}

func truncate(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max]
}

func firstMatch(s, pattern string) string {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return ""
	}
	m := re.FindStringSubmatch(s)
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(m[1])
}

func versionAfter(s, prefix string) string {
	i := strings.Index(strings.ToUpper(s), strings.ToUpper(prefix))
	if i < 0 {
		return ""
	}
	rest := s[i+len(prefix):]
	if j := strings.IndexAny(rest, " \r\n"); j >= 0 {
		rest = rest[:j]
	}
	return strings.TrimSpace(rest)
}
