// 网络诊断箱 — 将 ping / traceroute / lan-scanner / ip-geo / service-fingerprint
// 五个原生插件合并为单一多标签插件，避免各自一个子进程、重复占用与上下文切换。
//
// 八个命令前缀：
//
//	ping.start / ping.poll / ping.stop       持续延迟与丢包统计（真实 ICMP，无需管理员）
//	trace.start / trace.poll / trace.stop    逐跳路由追踪（递增 TTL，无需 raw socket）
//	lan.interfaces / lan.start / lan.poll / lan.stop  局域网存活设备扫描（ARP + 端口 + 厂商）
//	geo.lookup                               IP 归属地/运营商/经纬度（ipwho.is）
//	fp.start / fp.poll / fp.stop             端口服务指纹识别（banner + 多协议探针 + 特征库）
//
// 所有长任务（ping/trace/lan/fp）统一采用「start 立即返回 + 前端轮询 poll」的异步会话模型，
// 以规避宿主 20s 的 plugin.execute 超时。ICMP 走 iphlpapi.dll，不依赖管理员权限。
package main

import (
	"bufio"
	"crypto/tls"
	_ "embed"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

/* ==================== 共享 RPC 层 ==================== */

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

func boolFrom(m map[string]interface{}, key string, def bool) bool {
	if v, ok := m[key].(bool); ok {
		return v
	}
	return def
}

func intSliceFrom(m map[string]interface{}, key string) []int {
	v, ok := m[key].([]interface{})
	if !ok {
		return nil
	}
	out := []int{}
	for _, x := range v {
		switch n := x.(type) {
		case float64:
			out = append(out, int(n))
		case string:
			var i int
			if _, err := fmt.Sscanf(n, "%d", &i); err == nil {
				out = append(out, i)
			}
		}
	}
	return out
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

/* ==================== ICMP（Windows iphlpapi，统一实现） ==================== */

var (
	iphlpapi        = syscall.NewLazyDLL("iphlpapi.dll")
	procIcmpCreate  = iphlpapi.NewProc("IcmpCreateFile")
	procIcmpSend    = iphlpapi.NewProc("IcmpSendEcho")
	procIcmpClose   = iphlpapi.NewProc("IcmpCloseHandle")
	procGetNetTable = iphlpapi.NewProc("GetIpNetTable")
)

const (
	ipSuccess     = 0
	ipReqTimedOut = 11010
	ipTTLExpired  = 11013
)

// ipOptionInfo 对应 IP_OPTION_INFORMATION（64 位下长度 16，OptionsData 按 8 字节对齐）
type ipOptionInfo struct {
	TTL         byte
	TOS         byte
	Flags       byte
	OptionsSize byte
	OptionsData uintptr
}

// icmpReply 对应 ICMP_ECHO_REPLY 前几个字段，按小端解析
type icmpReply struct {
	Address       string
	Status        uint32
	RoundTripTime uint32
}

func icmpStatusText(s uint32) string {
	switch s {
	case ipReqTimedOut:
		return "请求超时"
	case ipTTLExpired:
		return "TTL 过期"
	case 11003:
		return "目标不可达"
	case 11050:
		return "常规失败"
	default:
		return fmt.Sprintf("ICMP 状态 %d", s)
	}
}

func icmpAvailable() bool {
	h, _, _ := procIcmpCreate.Call()
	if h == 0 || h == ^uintptr(0) {
		return false
	}
	procIcmpClose.Call(h)
	return true
}

// icmpEcho 发送一次 ICMP 回显请求。返回 *icmpReply；调用失败返回 error。
// 注意：LazyProc.Call 成功时 err 也可能是非 nil 的 Errno(0)，必须显式比较。
func icmpEcho(destIP net.IP, ttl, timeoutMs int) (*icmpReply, error) {
	h, _, _ := procIcmpCreate.Call()
	if h == 0 || h == ^uintptr(0) {
		return nil, errors.New("IcmpCreateFile 不可用")
	}
	defer procIcmpClose.Call(h)

	v4 := destIP.To4()
	if v4 == nil {
		return nil, errors.New("仅支持 IPv4 目标")
	}
	addr := uintptr(uint32(v4[0]) | uint32(v4[1])<<8 | uint32(v4[2])<<16 | uint32(v4[3])<<24)

	opts := ipOptionInfo{TTL: byte(ttl)}
	payload := []byte("quickdock")
	buf := make([]byte, 512)

	var payloadPtr uintptr
	if len(payload) > 0 {
		payloadPtr = uintptr(unsafe.Pointer(&payload[0]))
	}

	n, _, err := procIcmpSend.Call(
		h,
		addr,
		payloadPtr,
		uintptr(len(payload)),
		uintptr(unsafe.Pointer(&opts)),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
		uintptr(timeoutMs),
	)
	if n == 0 {
		if err != nil && err != syscall.Errno(0) {
			return nil, err
		}
		return nil, errors.New("无响应")
	}
	return &icmpReply{
		Address:       net.IPv4(buf[0], buf[1], buf[2], buf[3]).String(),
		Status:        binary.LittleEndian.Uint32(buf[4:8]),
		RoundTripTime: binary.LittleEndian.Uint32(buf[8:12]),
	}, nil
}

/* ==================== Ping 模块 ==================== */

type pingResult struct {
	Seq int     `json:"seq"`
	RTT float64 `json:"rtt"`
	OK  bool    `json:"ok"`
	Err string  `json:"error,omitempty"`
}

type pingSession struct {
	ID      string
	Host    string
	IP      string
	Mode    string
	Timeout int
	Port    int

	mu      sync.Mutex
	results []pingResult
	sent    int
	recv    int
	min     float64
	max     float64
	sum     float64
	running bool
	stopCh  chan struct{}
}

var (
	pingSessMu   sync.Mutex
	pingSessions = map[string]*pingSession{}
	pingSeq      int
)

func (s *pingSession) pingOnce(seq, timeoutMs, port int) pingResult {
	res := pingResult{Seq: seq, RTT: -1}
	ip := net.ParseIP(s.IP)
	if ip == nil {
		res.Err = "IP 解析失败"
		return res
	}
	if s.Mode == "icmp" {
		r, err := icmpEcho(ip, 64, timeoutMs)
		if err != nil {
			res.Err = err.Error()
			return res
		}
		if r.Status != ipSuccess {
			res.Err = icmpStatusText(r.Status)
			return res
		}
		res.OK = true
		res.RTT = float64(r.RoundTripTime)
		return res
	}
	start := time.Now()
	c, err := net.DialTimeout("tcp", net.JoinHostPort(s.IP, fmt.Sprint(port)), time.Duration(timeoutMs)*time.Millisecond)
	if err != nil {
		res.Err = "连接失败"
		return res
	}
	c.Close()
	res.OK = true
	res.RTT = float64(time.Since(start).Microseconds()) / 1000
	return res
}

func (s *pingSession) run(intervalMs, count int) {
	defer func() {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
	}()
	for {
		select {
		case <-s.stopCh:
			return
		default:
		}
		s.mu.Lock()
		if !s.running {
			s.mu.Unlock()
			return
		}
		if count > 0 && s.sent >= count {
			s.mu.Unlock()
			return
		}
		s.sent++
		seq := s.sent
		s.mu.Unlock()

		res := s.pingOnce(seq, s.Timeout, s.Port)

		s.mu.Lock()
		s.results = append(s.results, res)
		if len(s.results) > 1000 {
			s.results = s.results[len(s.results)-1000:]
		}
		if res.OK {
			s.recv++
			s.sum += res.RTT
			if s.min == 0 || res.RTT < s.min {
				s.min = res.RTT
			}
			if res.RTT > s.max {
				s.max = res.RTT
			}
		}
		s.mu.Unlock()

		select {
		case <-s.stopCh:
			return
		case <-time.After(time.Duration(intervalMs) * time.Millisecond):
		}
	}
}

func (s *pingSession) snapshot() map[string]interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	loss := 0.0
	if s.sent > 0 {
		loss = float64(s.sent-s.recv) / float64(s.sent) * 100
	}
	avg := 0.0
	if s.recv > 0 {
		avg = s.sum / float64(s.recv)
	}
	last := -1.0
	if len(s.results) > 0 {
		last = s.results[len(s.results)-1].RTT
	}
	min := s.min
	if s.recv == 0 {
		min = 0
	}
	rs := make([]pingResult, len(s.results))
	copy(rs, s.results)
	return map[string]interface{}{
		"sessionId": s.ID, "host": s.Host, "ip": s.IP, "mode": s.Mode, "running": s.running,
		"results": rs,
		"stats": map[string]interface{}{
			"sent": s.sent, "recv": s.recv, "loss": loss,
			"min": min, "max": s.max, "avg": avg, "last": last,
		},
	}
}

func pingHandleStart(id int64, input map[string]interface{}) {
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

	mode := "icmp"
	if !icmpAvailable() {
		mode = "tcp"
	}
	interval := intFrom(input, "interval", 1000)
	count := intFrom(input, "count", 0)
	timeoutMs := intFrom(input, "timeout", 2000)
	port := intFrom(input, "port", 80)
	if interval < 200 {
		interval = 200
	}
	if timeoutMs < 100 {
		timeoutMs = 100
	}

	pingSessMu.Lock()
	pingSeq++
	sid := fmt.Sprintf("p%d", pingSeq)
	s := &pingSession{
		ID: sid, Host: host, IP: ip.String(), Mode: mode,
		Timeout: timeoutMs, Port: port,
		running: true, stopCh: make(chan struct{}),
	}
	pingSessions[sid] = s
	pingSessMu.Unlock()

	go s.run(interval, count)

	respond(id, map[string]interface{}{
		"sessionId": sid, "host": host, "ip": ip.String(), "mode": mode, "running": true,
	})
}

func getPingSession(id int64, input map[string]interface{}) *pingSession {
	sid := strFrom(input, "sessionId")
	if sid == "" {
		respondError(id, -32602, "缺少 sessionId")
		return nil
	}
	pingSessMu.Lock()
	s := pingSessions[sid]
	pingSessMu.Unlock()
	if s == nil {
		respondError(id, -32002, "会话不存在或已结束")
		return nil
	}
	return s
}

func pingHandlePoll(id int64, input map[string]interface{}) {
	s := getPingSession(id, input)
	if s == nil {
		return
	}
	respond(id, s.snapshot())
}

func pingHandleStop(id int64, input map[string]interface{}) {
	s := getPingSession(id, input)
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
	pingSessMu.Lock()
	delete(pingSessions, s.ID)
	pingSessMu.Unlock()
	snap["stopped"] = true
	respond(id, snap)
}

/* ==================== Traceroute 模块 ==================== */

type hopResult struct {
	TTL  int       `json:"ttl"`
	IP   string    `json:"ip"`
	Host string    `json:"host,omitempty"`
	RTTs []float64 `json:"rtts"`
	OK   bool      `json:"ok"`
	Done bool      `json:"done"`
}

type traceSession struct {
	ID   string
	Host string
	IP   string

	mu      sync.Mutex
	hops    []hopResult
	running bool
	stopCh  chan struct{}
}

var (
	traceSessMu   sync.Mutex
	traceSessions = map[string]*traceSession{}
	traceSeq      int
)

func resolveHost(ip string, timeoutMs int) string {
	ch := make(chan string, 1)
	go func() {
		names, err := net.LookupAddr(ip)
		if err == nil && len(names) > 0 {
			ch <- strings.TrimSuffix(names[0], ".")
			return
		}
		ch <- ""
	}()
	select {
	case n := <-ch:
		return n
	case <-time.After(time.Duration(timeoutMs) * time.Millisecond):
		return ""
	}
}

func probeHop(destIP net.IP, ttl, probes, timeoutMs int) hopResult {
	h := hopResult{TTL: ttl, RTTs: []float64{}}
	type pr struct {
		rtt    float64
		addr   string
		status uint32
		err    error
	}
	ch := make(chan pr, probes)
	for i := 0; i < probes; i++ {
		go func() {
			r, err := icmpEcho(destIP, ttl, timeoutMs)
			if err != nil {
				ch <- pr{err: err}
				return
			}
			ch <- pr{rtt: float64(r.RoundTripTime), addr: r.Address, status: r.Status}
		}()
	}
	for i := 0; i < probes; i++ {
		p := <-ch
		if p.err != nil {
			h.RTTs = append(h.RTTs, -1)
			continue
		}
		if p.status == ipSuccess || p.status == ipTTLExpired {
			h.RTTs = append(h.RTTs, p.rtt)
			h.OK = true
			if h.IP == "" {
				h.IP = p.addr
			}
			if p.status == ipSuccess {
				h.Done = true
			}
		} else {
			h.RTTs = append(h.RTTs, -1)
		}
	}
	return h
}

func (s *traceSession) run(maxHops, probes, timeoutMs int, resolve bool) {
	defer func() {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
	}()
	dest := net.ParseIP(s.IP)
	if dest == nil {
		return
	}
	for ttl := 1; ttl <= maxHops; ttl++ {
		select {
		case <-s.stopCh:
			return
		default:
		}
		h := probeHop(dest, ttl, probes, timeoutMs)
		if resolve && h.IP != "" {
			h.Host = resolveHost(h.IP, 800)
		}
		s.mu.Lock()
		s.hops = append(s.hops, h)
		done := h.Done
		s.mu.Unlock()
		if done {
			return
		}
	}
}

func (s *traceSession) snapshot() map[string]interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	hops := make([]hopResult, len(s.hops))
	copy(hops, s.hops)
	return map[string]interface{}{
		"sessionId": s.ID, "host": s.Host, "ip": s.IP,
		"running": s.running, "hops": hops,
	}
}

func traceHandleStart(id int64, input map[string]interface{}) {
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
	if ip.To4() == nil {
		respondError(id, -32003, "暂仅支持 IPv4 目标")
		return
	}
	maxHops := intFrom(input, "maxHops", 30)
	probes := intFrom(input, "probes", 3)
	timeoutMs := intFrom(input, "timeout", 1500)
	resolve := boolFrom(input, "resolve", true)
	if maxHops > 64 {
		maxHops = 64
	}
	if probes > 5 {
		probes = 5
	}
	if timeoutMs > 5000 {
		timeoutMs = 5000
	}
	traceSessMu.Lock()
	traceSeq++
	sid := fmt.Sprintf("t%d", traceSeq)
	s := &traceSession{ID: sid, Host: host, IP: ip.String(), running: true, stopCh: make(chan struct{})}
	traceSessions[sid] = s
	traceSessMu.Unlock()

	go s.run(maxHops, probes, timeoutMs, resolve)

	respond(id, map[string]interface{}{
		"sessionId": sid, "host": host, "ip": ip.String(), "running": true,
	})
}

func getTraceSession(id int64, input map[string]interface{}) *traceSession {
	sid := strFrom(input, "sessionId")
	if sid == "" {
		respondError(id, -32602, "缺少 sessionId")
		return nil
	}
	traceSessMu.Lock()
	s := traceSessions[sid]
	traceSessMu.Unlock()
	if s == nil {
		respondError(id, -32002, "会话不存在或已结束")
		return nil
	}
	return s
}

func traceHandlePoll(id int64, input map[string]interface{}) {
	s := getTraceSession(id, input)
	if s == nil {
		return
	}
	respond(id, s.snapshot())
}

func traceHandleStop(id int64, input map[string]interface{}) {
	s := getTraceSession(id, input)
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
	traceSessMu.Lock()
	delete(traceSessions, s.ID)
	traceSessMu.Unlock()
	snap["stopped"] = true
	respond(id, snap)
}

/* ==================== 局域网扫描模块 ==================== */

// arpTable 返回 IP → MAC 映射。dwType 为 2 表示无效条目，忽略。
func arpTable() map[string]string {
	out := map[string]string{}
	var size uint32
	r, _, _ := procGetNetTable.Call(0, uintptr(unsafe.Pointer(&size)), 0)
	if r != 0 && r != 122 {
		return out
	}
	if size < 8 {
		return out
	}
	buf := make([]byte, size)
	r, _, _ = procGetNetTable.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0)
	if r != 0 {
		return out
	}
	num := binary.LittleEndian.Uint32(buf[0:4])
	off := 4
	for i := uint32(0); i < num; i++ {
		if off+24 > len(buf) {
			break
		}
		physLen := binary.LittleEndian.Uint32(buf[off+4 : off+8])
		typ := binary.LittleEndian.Uint32(buf[off+20 : off+24])
		if physLen >= 6 && typ != 2 {
			mac := fmt.Sprintf("%02X-%02X-%02X-%02X-%02X-%02X",
				buf[off+8], buf[off+9], buf[off+10], buf[off+11], buf[off+12], buf[off+13])
			ip := net.IPv4(buf[off+16], buf[off+17], buf[off+18], buf[off+19]).String()
			out[ip] = mac
		}
		off += 24
	}
	return out
}

func localMACs() map[string]string {
	out := map[string]string{}
	ifaces, err := net.Interfaces()
	if err != nil {
		return out
	}
	for _, iface := range ifaces {
		mac := strings.ToUpper(strings.ReplaceAll(iface.HardwareAddr.String(), ":", "-"))
		if mac == "" {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if ipnet, ok := a.(*net.IPNet); ok {
				if ip4 := ipnet.IP.To4(); ip4 != nil {
					out[ip4.String()] = mac
				}
			}
		}
	}
	return out
}

var ouiTable = map[string]string{
	"000C29": "VMware", "005056": "VMware", "000569": "VMware",
	"080027": "VirtualBox",
	"00155D": "Microsoft Hyper-V",
	"525400": "QEMU/KVM",
	"001C42": "Parallels",
	"B827EB": "Raspberry Pi", "DCA632": "Raspberry Pi", "E45F01": "Raspberry Pi",
	"240AC4": "Espressif", "30AEA4": "Espressif",
	"001CB3": "Apple", "3C15C2": "Apple", "F01898": "Apple", "A483E7": "Apple",
	"001B21": "Intel", "3C970E": "Intel", "A434D9": "Intel",
	"00E0FC": "Huawei",
	"3C2AF4": "Brother",
}

func vendorOf(mac string) string {
	key := strings.ReplaceAll(strings.ToUpper(mac), ":", "")
	key = strings.ReplaceAll(key, "-", "")
	if len(key) < 6 {
		return ""
	}
	if v, ok := ouiTable[key[:6]]; ok {
		return v
	}
	return ""
}

type hostResult struct {
	IP        string  `json:"ip"`
	Hostname  string  `json:"hostname,omitempty"`
	MAC       string  `json:"mac,omitempty"`
	Vendor    string  `json:"vendor,omitempty"`
	RTT       float64 `json:"rtt"`
	OpenPorts []int   `json:"openPorts,omitempty"`
	Alive     bool    `json:"alive"`
}

var defaultPorts = []int{80, 443, 445, 22, 3389, 8080, 139, 135}

func incIP(ip net.IP) {
	for j := len(ip) - 1; j >= 0; j-- {
		ip[j]++
		if ip[j] > 0 {
			break
		}
	}
}

func hostsInCIDR(cidr string) ([]string, error) {
	ip, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, err
	}
	ip4 := ip.To4()
	m := ipnet.Mask
	if ip4 == nil {
		return nil, errors.New("仅支持 IPv4 网段")
	}
	if len(m) == 16 {
		m = m[12:]
	}
	base := binary.BigEndian.Uint32(ip4) & binary.BigEndian.Uint32(m)
	total := ^binary.BigEndian.Uint32(m) + 1
	if total < 4 {
		return nil, errors.New("网段过小")
	}
	if total > 4096 {
		return nil, fmt.Errorf("网段过大（%d 个地址），请改用 /24 之类更小的网段", total)
	}
	hosts := make([]string, 0, total-2)
	for i := uint32(1); i < total-1; i++ {
		v := base + i
		hosts = append(hosts, fmt.Sprintf("%d.%d.%d.%d", byte(v>>24), byte(v>>16), byte(v>>8), byte(v)))
	}
	return hosts, nil
}

type probeRes struct {
	alive bool
	port  int
	ports []int
	rtt   float64
}

func probeHost(ip string, ports []int, timeoutMs int, useICMP bool) hostResult {
	res := hostResult{IP: ip, OpenPorts: []int{}}
	n := 1
	if useICMP {
		n = 2
	}
	ch := make(chan probeRes, n)
	if useICMP {
		go func() {
			if r, err := icmpEcho(net.ParseIP(ip), 64, timeoutMs); err == nil && r.Status == ipSuccess {
				ch <- probeRes{alive: true, rtt: float64(r.RoundTripTime)}
			} else {
				ch <- probeRes{}
			}
		}()
	}
	go func() {
		pch := make(chan probeRes, len(ports))
		for _, p := range ports {
			go func(port int) {
				start := time.Now()
				c, err := net.DialTimeout("tcp", net.JoinHostPort(ip, strconv.Itoa(port)),
					time.Duration(timeoutMs)*time.Millisecond)
				if err != nil {
					pch <- probeRes{}
					return
				}
				c.Close()
				pch <- probeRes{alive: true, port: port,
					rtt: float64(time.Since(start).Microseconds()) / 1000}
			}(p)
		}
		open := []int{}
		var best float64
		for i := 0; i < len(ports); i++ {
			pr := <-pch
			if !pr.alive {
				continue
			}
			open = append(open, pr.port)
			if best <= 0 || pr.rtt < best {
				best = pr.rtt
			}
		}
		sort.Ints(open)
		ch <- probeRes{alive: len(open) > 0, rtt: best, ports: open}
	}()
	for i := 0; i < n; i++ {
		pr := <-ch
		if pr.alive {
			res.Alive = true
			res.OpenPorts = append(res.OpenPorts, pr.ports...)
		}
		if pr.rtt > 0 && (res.RTT <= 0 || pr.rtt < res.RTT) {
			res.RTT = pr.rtt
		}
	}
	sort.Ints(res.OpenPorts)
	return res
}

func defaultLocalCIDR() (string, string) {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "", ""
	}
	defer conn.Close()
	if ua, ok := conn.LocalAddr().(*net.UDPAddr); ok {
		ip := ua.IP.To4()
		if ip != nil {
			net24 := net.IPv4(ip[0], ip[1], ip[2], 0)
			return ip.String(), fmt.Sprintf("%s/24", net24.String())
		}
	}
	return "", ""
}

func localSubnets() []map[string]interface{} {
	out := []map[string]interface{}{}
	ifaces, err := net.Interfaces()
	if err != nil {
		return out
	}
	seen := map[string]bool{}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipnet.IP.To4()
			if ip4 == nil {
				continue
			}
			if ip4[0] == 169 && ip4[1] == 254 {
				continue
			}
			m := ipnet.Mask
			if len(m) == 16 {
				m = m[12:]
			}
			if len(m) < 4 {
				continue
			}
			ones, _ := net.IPv4Mask(m[0], m[1], m[2], m[3]).Size()
			cidr := fmt.Sprintf("%s/%d", ip4.Mask(net.IPv4Mask(m[0], m[1], m[2], m[3])).String(), ones)
			if seen[cidr] {
				continue
			}
			seen[cidr] = true
			out = append(out, map[string]interface{}{
				"iface": iface.Name, "ip": ip4.String(), "cidr": cidr,
			})
		}
	}
	if len(out) == 0 {
		if ip, cidr := defaultLocalCIDR(); cidr != "" {
			out = append(out, map[string]interface{}{
				"iface": "自动推断", "ip": ip, "cidr": cidr,
			})
		}
	}
	return out
}

type scanSession struct {
	ID        string
	CIDR      string
	Total     int
	UseICMP   bool
	Resolve   bool
	Ports     []int
	TimeoutMs int

	mu      sync.Mutex
	found   []*hostResult
	scanned int
	running bool
	done    chan struct{}
}

var (
	scanSessMu   sync.Mutex
	scanSessions = map[string]*scanSession{}
	scanSeq      int
)

func (s *scanSession) snapshot() map[string]interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	hosts := make([]*hostResult, len(s.found))
	copy(hosts, s.found)
	sort.Slice(hosts, func(i, j int) bool {
		return ipToUint(hosts[i].IP) < ipToUint(hosts[j].IP)
	})
	return map[string]interface{}{
		"sessionId": s.ID, "cidr": s.CIDR, "total": s.Total,
		"scanned": s.scanned, "running": s.running, "hosts": hosts,
	}
}

func lanHandleInterfaces(id int64) {
	respond(id, map[string]interface{}{"interfaces": localSubnets()})
}

func lanHandleStart(id int64, input map[string]interface{}) {
	cidr := strings.TrimSpace(strFrom(input, "cidr"))
	if cidr == "" {
		subs := localSubnets()
		if len(subs) > 0 {
			cidr, _ = subs[0]["cidr"].(string)
		} else if _, inferred := defaultLocalCIDR(); inferred != "" {
			cidr = inferred
		}
		if cidr == "" {
			respondError(id, -32004, "未检测到可用的本地网段，请手动输入（如 192.168.1.0/24）")
			return
		}
	}
	if !strings.Contains(cidr, "/") {
		cidr += "/24"
	}
	hosts, err := hostsInCIDR(cidr)
	if err != nil {
		respondError(id, -32602, err.Error())
		return
	}
	if len(hosts) == 0 {
		respondError(id, -32602, "网段内无可扫描主机")
		return
	}
	timeoutMs := intFrom(input, "timeout", 600)
	if timeoutMs > 5000 {
		timeoutMs = 5000
	}
	conc := intFrom(input, "concurrency", 128)
	if conc > 512 {
		conc = 512
	}
	resolve := boolFrom(input, "resolve", true)
	useICMP := boolFrom(input, "icmp", true) && icmpAvailable()
	ports := intSliceFrom(input, "ports")
	if len(ports) == 0 {
		ports = defaultPorts
	}

	scanSessMu.Lock()
	scanSeq++
	sid := fmt.Sprintf("ls%d", scanSeq)
	s := &scanSession{
		ID: sid, CIDR: cidr, Total: len(hosts),
		UseICMP: useICMP, Resolve: resolve, Ports: ports, TimeoutMs: timeoutMs,
		running: true, done: make(chan struct{}),
	}
	scanSessions[sid] = s
	scanSessMu.Unlock()

	localMAC := localMACs()

	go func() {
		defer func() {
			s.mu.Lock()
			s.running = false
			s.mu.Unlock()
			close(s.done)
		}()
		sem := make(chan struct{}, conc)
		var wg sync.WaitGroup
		for _, h := range hosts {
			wg.Add(1)
			go func(ip string) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				r := probeHost(ip, ports, timeoutMs, useICMP)
				s.mu.Lock()
				s.scanned++
				s.mu.Unlock()
				if !r.Alive {
					return
				}
				if resolve {
					r.Hostname = resolveHost(ip, 1000)
				}
				if mac, ok := arpTable()[ip]; ok {
					r.MAC = mac
				} else if mac, ok := localMAC[ip]; ok {
					r.MAC = mac
				}
				r.Vendor = vendorOf(r.MAC)
				s.mu.Lock()
				s.found = append(s.found, &r)
				s.mu.Unlock()
			}(h)
		}
		wg.Wait()
	}()

	respond(id, map[string]interface{}{
		"sessionId": sid, "cidr": cidr, "total": len(hosts), "running": true, "icmp": useICMP,
	})
}

func getScanSession(id int64, input map[string]interface{}) *scanSession {
	sid := strFrom(input, "sessionId")
	if sid == "" {
		respondError(id, -32602, "缺少 sessionId")
		return nil
	}
	scanSessMu.Lock()
	s := scanSessions[sid]
	scanSessMu.Unlock()
	if s == nil {
		respondError(id, -32002, "会话不存在或已结束")
		return nil
	}
	return s
}

func lanHandlePoll(id int64, input map[string]interface{}) {
	s := getScanSession(id, input)
	if s == nil {
		return
	}
	respond(id, s.snapshot())
}

func lanHandleStop(id int64, input map[string]interface{}) {
	s := getScanSession(id, input)
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.running {
		s.running = false
		close(s.done)
	}
	s.mu.Unlock()
	snap := s.snapshot()
	scanSessMu.Lock()
	delete(scanSessions, s.ID)
	scanSessMu.Unlock()
	snap["stopped"] = true
	respond(id, snap)
}

func ipToUint(s string) uint32 {
	ip := net.ParseIP(s).To4()
	if ip == nil {
		return 0
	}
	return binary.BigEndian.Uint32(ip)
}

/* ==================== IP 归属地模块（ipwho.is） ==================== */

type ipResponse struct {
	IP        string  `json:"ip"`
	Success   bool    `json:"success"`
	Message   string  `json:"message"`
	Type      string  `json:"type"`
	Continent string  `json:"continent"`
	Country   string  `json:"country"`
	Region    string  `json:"region"`
	City      string  `json:"city"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Timezone  struct {
		Id   string `json:"id"`
		Abbr string `json:"abbr"`
	} `json:"timezone"`
	Connection struct {
		ASN    int    `json:"asn"`
		Org    string `json:"org"`
		ISP    string `json:"isp"`
		Domain string `json:"domain"`
	} `json:"connection"`
	Currency struct {
		Code string `json:"code"`
		Name string `json:"name"`
	} `json:"currency"`
}

func geoHandleLookup(id int64, input map[string]interface{}) {
	ip := strings.TrimSpace(strFrom(input, "ip"))
	url := "https://ipwho.is"
	if ip != "" {
		url = "https://ipwho.is/" + ip
	}
	client := &http.Client{Timeout: 12 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		respondError(id, -1, "请求失败: "+err.Error())
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		respondError(id, -1, "读取响应失败: "+err.Error())
		return
	}
	var data ipResponse
	if err := json.Unmarshal(body, &data); err != nil {
		respondError(id, -1, "解析响应失败: "+err.Error())
		return
	}
	if !data.Success {
		msg := data.Message
		if msg == "" {
			msg = "查询失败"
		}
		respond(id, map[string]interface{}{"ok": false, "error": msg})
		return
	}
	fields := []map[string]string{
		{"label": "IP", "value": data.IP},
		{"label": "类型", "value": data.Type},
		{"label": "大洲", "value": data.Continent},
		{"label": "国家", "value": data.Country},
		{"label": "地区", "value": data.Region},
		{"label": "城市", "value": data.City},
		{"label": "时区", "value": data.Timezone.Id + " (" + data.Timezone.Abbr + ")"},
		{"label": "运营商", "value": data.Connection.ISP},
		{"label": "组织", "value": data.Connection.Org},
		{"label": "ASN", "value": fmt.Sprintf("AS%d", data.Connection.ASN)},
		{"label": "域名", "value": data.Connection.Domain},
		{"label": "货币", "value": data.Currency.Code + " - " + data.Currency.Name},
		{"label": "经纬度", "value": fmt.Sprintf("%.4f, %.4f", data.Latitude, data.Longitude)},
	}
	mapURL := ""
	if data.Latitude != 0 || data.Longitude != 0 {
		mapURL = fmt.Sprintf("https://www.openstreetmap.org/?mlat=%.4f&mlon=%.4f#map=10/%.4f/%.4f",
			data.Latitude, data.Longitude, data.Latitude, data.Longitude)
	}
	respond(id, map[string]interface{}{
		"ok": true, "query": ip, "fields": fields,
		"lat": data.Latitude, "lng": data.Longitude, "mapUrl": mapURL,
	})
}

/* ==================== 服务指纹模块 ==================== */

//go:embed signatures.json
var sigJSON []byte

type signature struct {
	Service      string  `json:"service"`
	Category     string  `json:"category"`
	Pattern      string  `json:"pattern"`
	VersionGroup int     `json:"version_group"`
	Confidence   float64 `json:"confidence"`
	Ports        []int   `json:"ports"`
	re           *regexp.Regexp
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
			continue
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

var httpStatusRe = regexp.MustCompile(`^HTTP/([\d.]+) (\d{3})`)
var serverHeaderRe = regexp.MustCompile(`(?i)Server:\s*([^\r\n]+?)(?:\s{2,}|$)`)

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

var portPresets = map[string][]int{
	"common": {21, 22, 23, 25, 53, 80, 110, 111, 135, 139, 143, 161, 389, 443, 445,
		465, 587, 636, 873, 993, 995, 1025, 1433, 1723, 1883, 2049, 2181, 2375, 2379,
		3000, 3306, 3389, 5432, 5601, 5672, 5900, 5984, 6379, 6443, 7001, 8000, 8069,
		8080, 8081, 8086, 8088, 8090, 8161, 8443, 8500, 8761, 8848, 8888, 9000, 9090,
		9092, 9200, 9300, 9443, 10000, 11211, 15672, 27017, 50000},
	"web": {80, 443, 8000, 8008, 8009, 8069, 8080, 8081, 8082, 8086, 8088, 8090,
		8098, 8161, 8181, 8222, 8443, 8500, 8530, 8531, 8761, 8848, 8880, 8888,
		9000, 9001, 9080, 9090, 9200, 9443, 10000},
	"db": {1433, 1521, 3306, 3307, 5432, 5433, 5984, 6379, 6380, 7001, 8086,
		9042, 9160, 11211, 27017, 27018, 28017, 50000},
	"top100": {7, 9, 13, 21, 22, 23, 25, 26, 37, 53, 79, 80, 81, 88, 106, 110,
		111, 113, 119, 135, 139, 143, 144, 179, 199, 389, 427, 443, 444, 445, 465,
		513, 514, 515, 543, 544, 548, 554, 587, 631, 636, 646, 873, 990, 993, 995,
		1025, 1026, 1027, 1028, 1029, 1110, 1433, 1720, 1723, 1755, 1900, 2000,
		2001, 2049, 2121, 2717, 3000, 3128, 3306, 3389, 3986, 4899, 5000, 5009,
		5051, 5060, 5101, 5190, 5357, 5432, 5631, 5666, 5800, 5900, 6000, 6001,
		6646, 7070, 8000, 8008, 8009, 8080, 8081, 8443, 8888, 9100, 9999, 10000,
		32768, 49152, 49157},
}

const bannerMax = 2048

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

func httpProbe(host, ip string, port int, useTLS bool, timeout time.Duration) string {
	addr := net.JoinHostPort(ip, strconv.Itoa(port))
	var conn net.Conn
	var err error
	if useTLS {
		conn, err = tls.DialWithDialer(
			&net.Dialer{Timeout: timeout}, "tcp", addr,
			&tls.Config{InsecureSkipVerify: true, ServerName: host},
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

func buildDNSVersionQuery() []byte {
	q := []byte{0x12, 0x34, 0x01, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	q = append(q, 0x07)
	q = append(q, []byte("version")...)
	q = append(q, 0x04)
	q = append(q, []byte("bind")...)
	q = append(q, 0x00)
	q = append(q, 0x00, 0x10)
	q = append(q, 0x00, 0x03)
	return q
}

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

func probePort(host, ip string, port int, timeout time.Duration) (banner, scheme string) {
	scheme = "tcp"
	if b := silentRead(ip, port, timeout); b != "" {
		return b, scheme
	}
	switch port {
	case 6379, 6380:
		if b := sendRead(ip, port, "PING\r\nINFO\r\n", timeout); b != "" {
			return b, scheme
		}
	case 11211:
		if b := sendRead(ip, port, "version\r\n", timeout); b != "" {
			return b, scheme
		}
	case 53:
		if b := udpQuery(ip, port, buildDNSVersionQuery(), timeout); b != "" {
			return b, "udp"
		}
	case 161:
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

type fpPortResult struct {
	Port       int       `json:"port"`
	Scheme     string    `json:"scheme"`
	Open       bool      `json:"open"`
	Service    string    `json:"service,omitempty"`
	Category   string    `json:"category,omitempty"`
	Version    string    `json:"version,omitempty"`
	Confidence float64   `json:"confidence"`
	Banner     string    `json:"banner,omitempty"`
	StatusCode int       `json:"statusCode,omitempty"`
	Guessed    bool      `json:"guessed,omitempty"`
	Matches    []fpMatch `json:"matches,omitempty"`
}

type fpSession struct {
	ID    string
	Host  string
	IP    string
	Ports []int

	mu      sync.Mutex
	open    []int
	results []fpPortResult

	scanned int32
	running bool
	stopCh  chan struct{}
}

var (
	fpSessMu   sync.Mutex
	fpSessions = map[string]*fpSession{}
	fpSeq      int
)

func (s *fpSession) stopped() bool {
	select {
	case <-s.stopCh:
		return true
	default:
		return false
	}
}

func (s *fpSession) run(timeoutMs, conc int) {
	defer func() {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
	}()
	dur := time.Duration(timeoutMs) * time.Millisecond
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
			res := fpPortResult{Port: port, Scheme: scheme, Open: true, Banner: banner}
			if matches := matchFingerprint(banner, port); len(matches) > 0 {
				best := matches[0]
				res.Matches = matches
				res.Service = best.Service
				res.Category = best.Category
				res.Version = best.Version
				res.Confidence = best.Confidence
			} else if m := httpStatusRe.FindStringSubmatch(banner); m != nil {
				code, _ := strconv.Atoi(m[2])
				res.Service = "HTTP"
				res.Category = "Web 服务"
				res.Version = m[1]
				res.Confidence = 0.50
				res.StatusCode = code
				if sm := serverHeaderRe.FindStringSubmatch(banner); sm != nil {
					if srv := strings.TrimSpace(sm[1]); srv != "" {
						res.Service = srv
						res.Confidence = 0.60
					}
				}
				res.Matches = []fpMatch{{Service: res.Service, Category: "Web 服务", Version: m[1], Confidence: res.Confidence}}
			} else if g, ok := portGuess[port]; ok {
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
	s.mu.Lock()
	sort.Slice(s.results, func(i, j int) bool { return s.results[i].Port < s.results[j].Port })
	s.mu.Unlock()
}

func (s *fpSession) snapshot() map[string]interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	results := make([]fpPortResult, len(s.results))
	copy(results, s.results)
	return map[string]interface{}{
		"sessionId": s.ID, "host": s.Host, "ip": s.IP, "running": s.running,
		"progress": map[string]interface{}{
			"total":   len(s.Ports),
			"scanned": int(atomic.LoadInt32(&s.scanned)),
			"open":    len(s.open),
		},
		"results": results,
	}
}

func fpHandleStart(id int64, input map[string]interface{}) {
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
	fpSessMu.Lock()
	fpSeq++
	sid := fmt.Sprintf("f%d", fpSeq)
	s := &fpSession{
		ID: sid, Host: host, IP: ip.String(), Ports: ports,
		running: true, stopCh: make(chan struct{}),
	}
	fpSessions[sid] = s
	fpSessMu.Unlock()

	go s.run(timeoutMs, conc)

	respond(id, map[string]interface{}{
		"sessionId": sid, "host": host, "ip": ip.String(),
		"total": len(ports), "running": true,
	})
}

func getFpSession(id int64, input map[string]interface{}) *fpSession {
	sid := strFrom(input, "sessionId")
	if sid == "" {
		respondError(id, -32602, "缺少 sessionId")
		return nil
	}
	fpSessMu.Lock()
	s := fpSessions[sid]
	fpSessMu.Unlock()
	if s == nil {
		respondError(id, -32002, "会话不存在或已结束")
		return nil
	}
	return s
}

func fpHandlePoll(id int64, input map[string]interface{}) {
	s := getFpSession(id, input)
	if s == nil {
		return
	}
	respond(id, s.snapshot())
}

func fpHandleStop(id int64, input map[string]interface{}) {
	s := getFpSession(id, input)
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
	fpSessMu.Lock()
	delete(fpSessions, s.ID)
	fpSessMu.Unlock()
	snap["stopped"] = true
	respond(id, snap)
}

/* ==================== 统一分发 ==================== */

func dispatch(raw string) {
	var req rpcRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		respondError(0, -32700, "parse error: "+err.Error())
		return
	}
	switch req.Method {
	case "initialize":
		respond(req.ID, map[string]interface{}{
			"status": "ready", "name": "QuickDock 网络诊断箱", "signatures": len(signatures),
		})
	case "host.ping":
		respond(req.ID, map[string]interface{}{"pong": true})
	case "plugin.execute":
		var params executeParams
		if len(req.Params) > 0 {
			_ = json.Unmarshal(req.Params, &params)
		}
		cmd := strings.ToLower(strings.TrimSpace(params.Command))
		mod := cmd
		sub := ""
		if idx := strings.Index(cmd, "."); idx >= 0 {
			mod = cmd[:idx]
			sub = cmd[idx+1:]
		}
		switch mod {
		case "ping":
			switch sub {
			case "start":
				pingHandleStart(req.ID, params.Input)
			case "poll":
				pingHandlePoll(req.ID, params.Input)
			case "stop":
				pingHandleStop(req.ID, params.Input)
			default:
				respondError(req.ID, -32601, "unknown command: "+params.Command)
			}
		case "trace":
			switch sub {
			case "start":
				traceHandleStart(req.ID, params.Input)
			case "poll":
				traceHandlePoll(req.ID, params.Input)
			case "stop":
				traceHandleStop(req.ID, params.Input)
			default:
				respondError(req.ID, -32601, "unknown command: "+params.Command)
			}
		case "lan":
			switch sub {
			case "interfaces":
				lanHandleInterfaces(req.ID)
			case "start":
				lanHandleStart(req.ID, params.Input)
			case "poll":
				lanHandlePoll(req.ID, params.Input)
			case "stop":
				lanHandleStop(req.ID, params.Input)
			default:
				respondError(req.ID, -32601, "unknown command: "+params.Command)
			}
		case "geo":
			switch sub {
			case "lookup":
				geoHandleLookup(req.ID, params.Input)
			default:
				respondError(req.ID, -32601, "unknown command: "+params.Command)
			}
		case "fp":
			switch sub {
			case "start":
				fpHandleStart(req.ID, params.Input)
			case "poll":
				fpHandlePoll(req.ID, params.Input)
			case "stop":
				fpHandleStop(req.ID, params.Input)
			default:
				respondError(req.ID, -32601, "unknown command: "+params.Command)
			}
		default:
			respondError(req.ID, -32601, "unknown method: "+req.Method)
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
