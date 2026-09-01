// 局域网设备扫描 — 探测同网段存活主机，解析主机名 / MAC / 厂商
//
// MAC 通过 iphlpapi.dll 的 GetIpNetTable 直接读取系统 ARP 表，不启动子进程、
// 也不解析 arp -a 输出（后者表头随系统语言变化，数据行虽稳定但解析仍脆弱）。
//
// 命令：interfaces（列出本机网段）/ scan（扫描）
package main

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

/* ==================== ICMP（Windows iphlpapi） ==================== */

var (
	iphlpapi        = syscall.NewLazyDLL("iphlpapi.dll")
	procIcmpCreate  = iphlpapi.NewProc("IcmpCreateFile")
	procIcmpSend    = iphlpapi.NewProc("IcmpSendEcho")
	procIcmpClose   = iphlpapi.NewProc("IcmpCloseHandle")
	procGetNetTable = iphlpapi.NewProc("GetIpNetTable")
)

const ipSuccess = 0

type ipOptionInfo struct {
	TTL         byte
	TOS         byte
	Flags       byte
	OptionsSize byte
	OptionsData uintptr
}

func icmpAvailable() bool {
	h, _, _ := procIcmpCreate.Call()
	if h == 0 || h == ^uintptr(0) {
		return false
	}
	procIcmpClose.Call(h)
	return true
}

func icmpEcho(destIP net.IP, ttl, timeoutMs int) (rttMs float64, ok bool) {
	h, _, _ := procIcmpCreate.Call()
	if h == 0 || h == ^uintptr(0) {
		return 0, false
	}
	defer procIcmpClose.Call(h)

	v4 := destIP.To4()
	if v4 == nil {
		return 0, false
	}
	addr := uintptr(uint32(v4[0]) | uint32(v4[1])<<8 | uint32(v4[2])<<16 | uint32(v4[3])<<24)

	opts := ipOptionInfo{TTL: byte(ttl)}
	payload := []byte("quickdock")
	buf := make([]byte, 512)

	n, _, _ := procIcmpSend.Call(
		h, addr, uintptr(unsafe.Pointer(&payload[0])), uintptr(len(payload)),
		uintptr(unsafe.Pointer(&opts)), uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)), uintptr(timeoutMs),
	)
	if n == 0 {
		return 0, false
	}
	if binary.LittleEndian.Uint32(buf[4:8]) != ipSuccess {
		return 0, false
	}
	return float64(binary.LittleEndian.Uint32(buf[8:12])), true
}

/* ==================== ARP 表（GetIpNetTable） ==================== */

// arpTable 返回 IP → MAC 映射。dwType 为 2 表示无效条目，忽略。
func arpTable() map[string]string {
	out := map[string]string{}

	var size uint32
	r, _, _ := procGetNetTable.Call(0, uintptr(unsafe.Pointer(&size)), 0)
	// ERROR_INSUFFICIENT_BUFFER(122) / NO_ERROR(0) 均表示已回填所需大小
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
	off := 4 // MIB_IPNETTABLE.dwNumEntries 之后即 MIB_IPNETROW 数组（4 字节对齐）
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

// localMACs 返回本机网卡 IP → MAC。
// 本机不会出现在自己的 ARP 表里，扫描到自己时需要从这里补 MAC。
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

/* ==================== 厂商库（内置小型 OUI 表） ====================
 * 只收录可确认的条目——错误的厂商标签比留空更有害。
 * 未收录时厂商字段留空，MAC 本身始终准确。
 * ================================================================== */

var ouiTable = map[string]string{
	// 虚拟化（虚拟网卡 OUI 固定，可确认）
	"000C29": "VMware", "005056": "VMware", "000569": "VMware",
	"080027": "VirtualBox",
	"00155D": "Microsoft Hyper-V",
	"525400": "QEMU/KVM",
	"001C42": "Parallels",
	// 开发板 / IoT
	"B827EB": "Raspberry Pi", "DCA632": "Raspberry Pi", "E45F01": "Raspberry Pi",
	"240AC4": "Espressif", "30AEA4": "Espressif",
	// 常见终端与网络设备
	"001CB3": "Apple", "3C15C2": "Apple", "F01898": "Apple", "A483E7": "Apple",
	"001B21": "Intel", "3C970E": "Intel", "A434D9": "Intel",
	"00E0FC": "Huawei",
	"3C2AF4": "Brother", // 实测佐证：主机名形如 BRN3C2AF4C7F74F
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

/* ==================== 扫描 ==================== */

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
			if rtt, ok := icmpEcho(net.ParseIP(ip), 64, timeoutMs); ok {
				ch <- probeRes{alive: true, rtt: rtt}
			} else {
				ch <- probeRes{}
			}
		}()
	}

	// 端口并行探测：串行试 8 个端口会让单主机最坏耗时 = 8×timeout，
	// 整个 /24 就会逼近宿主 20s 上限。并行后单机耗时恒为 1×timeout。
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

// defaultLocalCIDR 兜底：当 net.Interfaces() 枚举不到可用 IPv4 网段时，
// 通过 UDP 探测本机出网 IP（不会真正发包，仅读取 OS 路由表选定的本地地址），
// 再以 /24 反推局域网段。几乎在所有真实环境都能拿到可扫描的网段。
func defaultLocalCIDR() (string, string) {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "", ""
	}
	defer conn.Close()
	if ua, ok := conn.LocalAddr().(*net.UDPAddr); ok {
		ip := ua.IP.To4()
		if ip != nil {
			// 去掉末位主机号，得到 /24 网络地址
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
			// 跳过 APIPA 169.254.x.x（DHCP 失败，扫描无意义）
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
				"iface": iface.Name,
				"ip":    ip4.String(),
				"cidr":  cidr,
			})
		}
	}
	// 兜底：枚举不到任何 IPv4 网段时，用出网 IP 反推 /24
	if len(out) == 0 {
		if ip, cidr := defaultLocalCIDR(); cidr != "" {
			out = append(out, map[string]interface{}{
				"iface": "自动推断",
				"ip":    ip,
				"cidr":  cidr,
			})
		}
	}
	return out
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

func handleInterfaces(id int64) {
	respond(id, map[string]interface{}{"interfaces": localSubnets()})
}

/* ==================== 扫描会话（start / poll / stop，流式增量） ==================== */

type scanSession struct {
	ID        string
	CIDR      string
	Total     int
	UseICMP   bool
	Resolve   bool
	Ports     []int
	TimeoutMs int

	mu      sync.Mutex
	found   []*hostResult // 已发现的存活主机（实时追加）
	scanned int           // 已完成探测的主机数（含不存活）
	running bool
	done    chan struct{}
}

var (
	scanSessMu   sync.Mutex
	scanSessions = map[string]*scanSession{}
	scanSeq      int
)

// snapshot 返回到目前为止的进度与已发现主机，前端每轮 poll 取一次即可看到新增。
func (s *scanSession) snapshot() map[string]interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	hosts := make([]*hostResult, len(s.found))
	copy(hosts, s.found)
	sort.Slice(hosts, func(i, j int) bool {
		return ipToUint(hosts[i].IP) < ipToUint(hosts[j].IP)
	})
	return map[string]interface{}{
		"sessionId": s.ID,
		"cidr":      s.CIDR,
		"total":     s.Total,
		"scanned":   s.scanned,
		"running":   s.running,
		"hosts":     hosts,
	}
}

func handleStart(id int64, input map[string]interface{}) {
	cidr := strings.TrimSpace(strFrom(input, "cidr"))
	if cidr == "" {
		// 未指定则取第一个非回环网段；枚举不到时反推出网 IP 的 /24
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

	// 预先缓存本机网卡 MAC，探测过程中即时补全（本机不在自己的 ARP 表里）
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
				// 存活主机：即时解析主机名与 MAC，使前端出现的每一条都是完整信息
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

	// 立即返回受理，由前端轮询 poll 拉取增量结果
	respond(id, map[string]interface{}{
		"sessionId": sid,
		"cidr":      cidr,
		"total":     len(hosts),
		"running":   true,
		"icmp":      useICMP,
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

func handlePoll(id int64, input map[string]interface{}) {
	s := getScanSession(id, input)
	if s == nil {
		return
	}
	respond(id, s.snapshot())
}

func handleStop(id int64, input map[string]interface{}) {
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

func dispatch(raw string) {
	var req rpcRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		respondError(0, -32700, "parse error: "+err.Error())
		return
	}
	switch req.Method {
	case "initialize":
		respond(req.ID, map[string]interface{}{"status": "ready", "name": "QuickDock LAN Scanner"})
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
		case "interfaces":
			handleInterfaces(req.ID)
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
