// 目标预检与端口扫描。
//
// 只用 TCP connect：不用 ICMP、不用 raw socket、不做 SYN 半开。
// 三态判定：连上=open；对端明确拒绝=closed；其余（超时/不可达/被丢弃）=filtered。
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type PortResult struct {
	Port  int    `json:"port"`
	State string `json:"state"`
	RTTMs int64  `json:"rttMs"`
}

/* ==================== 目标预检 ==================== */

// resolveTarget 归一化输入 → 解析 A 记录 → 拒绝内网/回环/组播。
func (s *auditSession) resolveTarget() (TargetInfo, error) {
	info := TargetInfo{
		Input:       s.opts.Host,
		StartedAt:   s.startedAt.Format(time.RFC3339),
		Mode:        s.opts.Mode,
		TimeoutMs:   s.opts.TimeoutMs,
		Concurrency: s.opts.Concurrency,
	}
	host, portHint := normalizeHost(s.opts.Host)
	if host == "" {
		return info, errors.New("目标为空，请填写域名或公网 IP")
	}
	info.Host = host
	if portHint > 0 {
		s.opts.PortHint = portHint
	}

	ips, err := net.LookupIP(host)
	if err != nil {
		return info, fmt.Errorf("解析失败：%s 没有可用的 A 记录（%v）", host, err)
	}
	var v4, all []string
	for _, ip := range ips {
		if ip.To4() != nil {
			v4 = append(v4, ip.String())
		}
		all = append(all, ip.String())
	}
	if len(v4) == 0 {
		return info, fmt.Errorf("解析失败：%s 只解析到 IPv6 或没有记录，本插件只扫 IPv4", host)
	}
	info.IPs = all
	info.IP = v4[0]

	// 防误扫：任何一个解析结果落在禁止网段就整体拒绝，避免 DNS 轮询绕过
	if !s.opts.AllowPrivate {
		for _, ipStr := range v4 {
			ip := net.ParseIP(ipStr)
			if reason := forbiddenIPReason(ip); reason != "" {
				return info, fmt.Errorf("拒绝扫描：%s 解析到%s %s，本插件只对公网自有资产做自查（防误扫他人内网）", host, reason, ipStr)
			}
		}
	}
	return info, nil
}

func normalizeHost(raw string) (string, int) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", 0
	}
	host, port := raw, 0
	if strings.Contains(raw, "://") {
		if u, err := url.Parse(raw); err == nil && u.Host != "" {
			host = u.Host
			if p := u.Port(); p != "" {
				port, _ = strconv.Atoi(p)
			}
		}
	}
	if i := strings.IndexAny(host, "/?#"); i >= 0 {
		host = host[:i]
	}
	if h, p, err := net.SplitHostPort(host); err == nil {
		host = h
		if n, e := strconv.Atoi(p); e == nil && n > 0 && n <= 65535 {
			port = n
		}
	}
	host = strings.Trim(host, "[]")
	return strings.ToLower(strings.TrimSpace(host)), port
}

var reservedCIDRs = func() []*net.IPNet {
	out := []*net.IPNet{}
	for _, c := range []string{"100.64.0.0/10", "0.0.0.0/8", "192.0.2.0/24", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4"} {
		if _, n, err := net.ParseCIDR(c); err == nil {
			out = append(out, n)
		}
	}
	return out
}()

func forbiddenIPReason(ip net.IP) string {
	if ip == nil {
		return "非法地址"
	}
	switch {
	case ip.IsUnspecified():
		return "未指定地址"
	case ip.IsLoopback():
		return "回环地址"
	case ip.IsPrivate():
		return "内网地址"
	case ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast():
		return "链路本地地址"
	case ip.IsMulticast():
		return "组播地址"
	}
	for _, n := range reservedCIDRs {
		if n.Contains(ip) {
			return "保留地址"
		}
	}
	return ""
}

/* ==================== IP 归属（可选，替代 whois） ==================== */

type ipwhoResp struct {
	Success    bool   `json:"success"`
	Message    string `json:"message"`
	IP         string `json:"ip"`
	Type       string `json:"type"`
	Country    string `json:"country"`
	Region     string `json:"region"`
	City       string `json:"city"`
	Connection struct {
		ASN  int    `json:"asn"`
		ISP  string `json:"isp"`
		Org  string `json:"org"`
		Desc string `json:"domain"`
	} `json:"connection"`
	Timezone struct {
		ID string `json:"id"`
	} `json:"timezone"`
}

func lookupGeo(ip string) GeoInfo {
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Get("https://ipwho.is/" + url.PathEscape(ip))
	if err != nil {
		return GeoInfo{OK: false, Error: "归属查询失败：" + err.Error()}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return GeoInfo{OK: false, Error: "归属查询读取失败：" + err.Error()}
	}
	var d ipwhoResp
	if err := json.Unmarshal(body, &d); err != nil {
		return GeoInfo{OK: false, Error: "归属查询解析失败"}
	}
	if !d.Success {
		msg := d.Message
		if msg == "" {
			msg = "查询失败"
		}
		return GeoInfo{OK: false, Error: msg}
	}
	fields := []map[string]string{
		{"label": "IP", "value": d.IP},
		{"label": "类型", "value": d.Type},
		{"label": "国家/地区", "value": strings.TrimSpace(d.Country + " " + d.Region + " " + d.City)},
		{"label": "运营商", "value": d.Connection.ISP},
		{"label": "组织", "value": d.Connection.Org},
		{"label": "ASN", "value": fmt.Sprintf("AS%d", d.Connection.ASN)},
		{"label": "时区", "value": d.Timezone.ID},
	}
	return GeoInfo{OK: true, Fields: fields}
}

/* ==================== 端口表 ==================== */

// hotPorts 放最可能对外暴露的端口，排在扫描队首，快速出结论。
var hotPorts = []int{
	80, 443, 22, 21, 3389, 3306, 6379, 8080, 8443, 25, 5432, 27017, 11211, 9200,
	2375, 2376, 6443, 8888, 9000, 9090, 3000, 5601, 8081, 8000, 7001, 1433, 5900,
	23, 445, 139, 161, 2181, 5672, 15672, 8500, 8848, 5000, 8008, 8086, 9092, 10000,
}

// extraPorts 1024 以上的常见服务端口（补齐常用表）
var extraPorts = []int{
	1080, 1099, 1521, 1723, 1883, 1935, 2049, 2082, 2083, 2086, 2087, 2095, 2096,
	2222, 2379, 2380, 2480, 3001, 3128, 3260, 3268, 3269, 3299, 3388, 3478, 3632,
	4000, 4040, 4200, 4369, 4443, 4444, 4567, 4848, 4899, 5001, 5005, 5010, 5038,
	5044, 5060, 5061, 5222, 5269, 5280, 5353, 5357, 5433, 5443, 5555, 5556, 5600,
	5666, 5671, 5683, 5800, 5901, 5938, 5984, 6000, 6001, 6379, 6380, 6443, 6660,
	6667, 6697, 6839, 7000, 7002, 7003, 7070, 7077, 7080, 7443, 7547, 7687, 7777,
	8009, 8010, 8032, 8042, 8069, 8070, 8082, 8083, 8085, 8087, 8090, 8091, 8098,
	8099, 8100, 8123, 8161, 8181, 8200, 8222, 8243, 8280, 8333, 8404, 8444, 8445,
	8501, 8530, 8531, 8761, 8765, 8800, 8834, 8848, 8877, 8880, 8883, 8889, 8899,
	8983, 9001, 9002, 9003, 9008, 9010, 9042, 9080, 9091, 9093, 9095, 9100, 9160,
	9201, 9222, 9229, 9300, 9418, 9443, 9444, 9500, 9600, 9800, 9876, 9898, 9900,
	9943, 9944, 9981, 9987, 9999, 10001, 10080, 10250, 10255, 10256, 10443, 11211,
	11371, 12000, 12345, 13306, 1337, 15672, 16379, 16992, 16993, 18080, 18181,
	19132, 20000, 25565, 27018, 27019, 28017, 32400, 32768, 33060, 33848, 35357,
	49152, 49153, 50000, 50070, 50075, 50090, 51820, 54321, 55555, 61616, 62078,
}

// buildPortList 按模式组装端口：常用表 / 全量 / 自定义。
func buildPortList(o auditOptions) []int {
	if o.PortHint > 0 {
		return []int{o.PortHint}
	}
	if spec := strings.TrimSpace(o.PortsSpec); spec != "" {
		if p, err := parsePortSpec(spec); err == nil && len(p) > 0 {
			return p
		}
	}
	seen := map[int]bool{}
	out := make([]int, 0, 1200)
	add := func(p int) {
		if p < 1 || p > 65535 || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	for _, p := range hotPorts {
		add(p)
	}
	for p := 1; p <= 1024; p++ {
		add(p)
	}
	for _, p := range extraPorts {
		add(p)
	}
	if o.Mode == "full" || o.Mode == "all" {
		// 第二阶段：补齐 1-65535 的剩余端口
		for p := 1; p <= 65535; p++ {
			add(p)
		}
		sort.Ints(out)
	}
	return out
}

func parsePortSpec(spec string) ([]int, error) {
	seen := map[int]bool{}
	var out []int
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if i := strings.Index(part, "-"); i > 0 {
			lo, err1 := strconv.Atoi(strings.TrimSpace(part[:i]))
			hi, err2 := strconv.Atoi(strings.TrimSpace(part[i+1:]))
			if err1 != nil || err2 != nil || lo < 1 || hi > 65535 || lo > hi {
				return nil, fmt.Errorf("端口范围无效：%s", part)
			}
			if hi-lo > 20000 {
				return nil, fmt.Errorf("端口范围过大：%s（单段最多 20000 个）", part)
			}
			for p := lo; p <= hi; p++ {
				if !seen[p] {
					seen[p] = true
					out = append(out, p)
				}
			}
			continue
		}
		p, err := strconv.Atoi(part)
		if err != nil || p < 1 || p > 65535 {
			return nil, fmt.Errorf("端口无效：%s", part)
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out, nil
}

/* ==================== 扫描 ==================== */

func (s *auditSession) scanPorts(ports []int) []int {
	timeout := time.Duration(s.opts.TimeoutMs) * time.Millisecond
	ip := s.target.IP

	var (
		mu     sync.Mutex
		opened []int
		stats  PortStats
		cursor int64
		wg     sync.WaitGroup
	)

	workers := s.opts.Concurrency
	if workers > len(ports) {
		workers = len(ports)
	}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				if s.stopped() {
					return
				}
				idx := int(atomic.AddInt64(&cursor, 1)) - 1
				if idx >= len(ports) {
					return
				}
				port := ports[idx]
				state, rtt := probeTCP(ip, port, timeout)
				atomic.AddInt64(&s.scanned, 1)

				mu.Lock()
				stats.Total++
				switch state {
				case "open":
					stats.Open++
					opened = append(opened, port)
					s.ports = append(s.ports, PortResult{Port: port, State: state, RTTMs: rtt})
				case "closed":
					stats.Closed++
				default:
					stats.Filtered++
				}
				s.stats = stats
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	s.mu.Lock()
	sort.Slice(s.ports, func(i, j int) bool { return s.ports[i].Port < s.ports[j].Port })
	s.stats = stats
	s.mu.Unlock()

	sort.Ints(opened)
	return opened
}

func probeTCP(ip string, port int, timeout time.Duration) (string, int64) {
	start := time.Now()
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(ip, strconv.Itoa(port)), timeout)
	rtt := time.Since(start).Milliseconds()
	if err == nil {
		_ = conn.Close()
		return "open", rtt
	}
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		return "filtered", rtt
	}
	low := strings.ToLower(err.Error())
	if strings.Contains(low, "refused") || strings.Contains(low, "actively") || strings.Contains(low, "被拒绝") {
		return "closed", rtt
	}
	return "filtered", rtt
}
