package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

type portInfo struct {
	Port    int    `json:"port"`
	Service string `json:"service"`
}

type device struct {
	IP       string     `json:"ip"`
	MAC      string     `json:"mac"`
	Vendor   string     `json:"vendor"`
	Hostname string     `json:"hostname"`
	Kind     string     `json:"kind"`
	Ports    []portInfo `json:"ports"`
	RTTMs    int        `json:"rttMs"`
	Tags     []string   `json:"tags"`
	Source   string     `json:"source"`
}

type subnetInfo struct {
	CIDR  string `json:"cidr"`
	Iface string `json:"iface"`
	IP    string `json:"ip"`
	Hosts int    `json:"hosts"`
}

// 单次扫描最多覆盖的地址数（/22 = 1022 个可用地址）。
// 再大就不是「扫一下家里有什么」了，超时和噪音都失控。
const maxHosts = 1024

/* ==================== 网段 ==================== */

// localSubnets 列出本机所有私网 IPv4 网段，供前端下拉选择。
func localSubnets() []subnetInfo {
	var out []subnetInfo
	ifaces, err := net.Interfaces()
	if err != nil {
		qdLogWarn("枚举网卡失败 err=%v", err)
		return out
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
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
			ip := ipnet.IP.To4()
			if ip == nil || ip.IsLoopback() || !isPrivateIPv4(ip) {
				continue
			}
			ones, bits := ipnet.Mask.Size()
			if bits != 32 || ones >= 32 {
				continue
			}
			netIP := &net.IPNet{IP: ip.Mask(ipnet.Mask), Mask: ipnet.Mask}
			hosts := (1 << (32 - ones)) - 2
			if hosts <= 0 {
				continue
			}
			out = append(out, subnetInfo{
				CIDR:  netIP.String(),
				Iface: iface.Name,
				IP:    ip.String(),
				Hosts: hosts,
			})
		}
	}
	// 常用网段（地址少的）排前面，/24 这种正合适的一眼就看到
	sort.Slice(out, func(i, j int) bool { return out[i].Hosts < out[j].Hosts })
	qdLogInfo("本机网段 %d 个", len(out))
	return out
}

// selfIPs 本机所有 IPv4 地址，用来在结果里把「这台机器就是你自己」标出来。
func selfIPs() []string {
	var out []string
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return out
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipnet.IP.To4()
		if ip == nil {
			continue
		}
		out = append(out, ip.String())
	}
	return out
}

func isPrivateIPv4(ip net.IP) bool {
	return ip[0] == 10 ||
		(ip[0] == 172 && ip[1]&0xf0 == 16) ||
		(ip[0] == 192 && ip[1] == 168)
}

// expandCIDR 展开网段为待探测地址列表（去掉网络号与广播地址）。
func expandCIDR(cidr string) ([]string, error) {
	_, ipnet, err := net.ParseCIDR(strings.TrimSpace(cidr))
	if err != nil {
		return nil, err
	}
	ip := ipnet.IP.To4()
	if ip == nil {
		return nil, errors.New("暂不支持 IPv6 网段")
	}
	ones, bits := ipnet.Mask.Size()
	if bits != 32 {
		return nil, errors.New("网段格式无效")
	}
	total := 1 << (32 - ones)
	if total-2 > maxHosts {
		return nil, fmt.Errorf("网段太大（%d 个地址），请填 /22 或更小的网段", total-2)
	}
	base := binary4(ip)
	ips := make([]string, 0, total-2)
	for n := uint32(1); n < uint32(total)-1; n++ {
		v := base + n
		ips = append(ips, fmt.Sprintf("%d.%d.%d.%d", v>>24&0xff, v>>16&0xff, v>>8&0xff, v&0xff))
	}
	return ips, nil
}

func binary4(ip net.IP) uint32 {
	return uint32(ip[0])<<24 | uint32(ip[1])<<16 | uint32(ip[2])<<8 | uint32(ip[3])
}

/* ==================== 探测 ==================== */

type probeResult struct {
	open    bool
	refused bool // 端口关但主机在（收到 RST）
	rtt     time.Duration
}

// tcpProbe 一次 TCP 握手探测。
//
// 关键在三种结果的区分：
//   - 握手成功        → 主机在，端口开
//   - 被拒绝(RST)     → 主机在，端口关（Windows 的「actively refused」、POSIX 的 ECONNREFUSED）
//   - 超时 / 不可达   → 没动静，不能证明它在
//
// 只看「握手成功」会把大量开着防火墙的活设备漏掉——手机、打印机、IoT 基本全军覆没。
// 所以「被拒绝」必须算在线，这是本插件能扫出东西的前提。
func tcpProbe(ctx context.Context, host string, port int, timeout time.Duration) probeResult {
	d := net.Dialer{Timeout: timeout}
	t0 := time.Now()
	c, err := d.DialContext(ctx, "tcp4", fmt.Sprintf("%s:%d", host, port))
	rtt := time.Since(t0)
	if err == nil {
		_ = c.Close()
		return probeResult{open: true, rtt: rtt}
	}
	if isRefused(err) {
		return probeResult{refused: true, rtt: rtt}
	}
	return probeResult{}
}

// isRefused 判定「连接被对端明确拒绝」。
// errors.Is 在 Windows 上未必命中（错误经 WSA 包装），所以再叠一层英文串兜底——
// Windows 与 POSIX 的拒绝文案都含 "refused"，中文系统返回的也是英文原始串。
func isRefused(err error) bool {
	if errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		if errors.Is(opErr.Err, syscall.ECONNREFUSED) {
			return true
		}
	}
	return strings.Contains(strings.ToLower(err.Error()), "refused")
}

/* ==================== 会话 ==================== */

type session struct {
	ID      string
	mu      sync.Mutex
	running bool
	stopCh  chan struct{}
	ctx     context.Context
	cancel  context.CancelFunc

	CIDR        string
	Ports       []int
	ProbeMS     int
	PortMS      int
	Concurrency int
	DoNames     bool

	phase   string
	done    int
	total   int
	devices []*device
	index   map[string]*device
	errMsg  string
}

var (
	sessMu   sync.Mutex
	sessions = map[string]*session{}
	sessSeq  int
)

func newSession(cidr string, ports []int, probeMS, portMS, conc int, doNames bool) *session {
	ctx, cancel := context.WithCancel(context.Background())
	sessMu.Lock()
	sessSeq++
	s := &session{
		ID:          fmt.Sprintf("ls%d", sessSeq),
		running:     true,
		stopCh:      make(chan struct{}),
		ctx:         ctx,
		cancel:      cancel,
		CIDR:        cidr,
		Ports:       ports,
		ProbeMS:     probeMS,
		PortMS:      portMS,
		Concurrency: conc,
		DoNames:     doNames,
		index:       map[string]*device{},
		phase:       "准备中",
	}
	sessions[s.ID] = s
	sessMu.Unlock()
	return s
}

func (s *session) stopped() bool {
	select {
	case <-s.stopCh:
		return true
	default:
		return false
	}
}

func (s *session) bump(n int) {
	s.mu.Lock()
	s.done += n
	s.mu.Unlock()
}

func (s *session) setPhase(p string) {
	s.mu.Lock()
	s.phase = p
	s.mu.Unlock()
	qdLogInfo("[%s] %s", s.ID, p)
}

func (s *session) device(ip string) *device {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.index[ip]
}

func (s *session) addDevice(ip, source string) *device {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d, ok := s.index[ip]; ok {
		return d
	}
	d := &device{IP: ip, Source: source, Ports: []portInfo{}, Tags: []string{}}
	s.index[ip] = d
	s.devices = append(s.devices, d)
	return d
}

func (s *session) snapshot() map[string]interface{} {
	s.mu.Lock()
	devices := make([]device, 0, len(s.devices))
	for _, d := range s.devices {
		ports := make([]portInfo, len(d.Ports))
		copy(ports, d.Ports)
		tags := make([]string, len(d.Tags))
		copy(tags, d.Tags)
		devices = append(devices, device{
			IP: d.IP, MAC: d.MAC, Vendor: d.Vendor, Hostname: d.Hostname,
			Kind: d.Kind, Ports: ports, RTTMs: d.RTTMs, Tags: tags, Source: d.Source,
		})
	}
	snap := map[string]interface{}{
		"sessionId": s.ID,
		"running":   s.running,
		"cidr":      s.CIDR,
		"phase":     s.phase,
		"done":      s.done,
		"total":     s.total,
		"online":    len(s.devices),
		"devices":   devices,
		"error":     s.errMsg,
	}
	s.mu.Unlock()
	return snap
}

// run 完整扫描流程。顺序是刻意的：
// 先读邻居表（免费拿到不响应 TCP 的设备）→ 并发探存活 → 对活着的扫端口
// → 问名字 → 再读一次邻居表（刚才的探测会把新设备写进缓存，MAC 这才有）。
func (s *session) run(ips []string) {
	defer func() {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
		if r := recover(); r != nil {
			qdLogPanic("[panic] lan-scanner run", r)
			s.mu.Lock()
			s.errMsg = fmt.Sprintf("扫描异常: %v", r)
			s.mu.Unlock()
		}
		qdLogInfo("[%s] 扫描结束 在线=%d", s.ID, len(s.devices))
	}()

	// 阶段 0：邻居表（ARP / NDP 缓存）
	s.setPhase("读取本机 ARP 缓存")
	arpBefore := arpTable()
	qdLogInfo("ARP 缓存 %d 条", len(arpBefore))

	// 阶段 1：存活探测
	s.mu.Lock()
	s.total = len(ips)
	s.mu.Unlock()
	s.setPhase("探测存活主机")
	probeTimeout := time.Duration(s.ProbeMS) * time.Millisecond
	s.probeAlive(ips, arpBefore, probeTimeout)
	if s.stopped() {
		return
	}

	// 阶段 2：端口扫描（只对已确认在线的设备）
	online := make([]string, 0, len(s.devices))
	s.mu.Lock()
	for _, d := range s.devices {
		online = append(online, d.IP)
	}
	// 名称解析也算进度，否则最后一段进度条会卡在 99% 或直接冲过 100%
	nameJobs := 0
	if s.DoNames {
		nameJobs = len(online)
	}
	s.total = len(ips) + len(online)*len(s.Ports) + nameJobs
	s.mu.Unlock()
	if len(online) > 0 {
		s.setPhase("扫描开放端口")
		s.scanPorts(online, time.Duration(s.PortMS)*time.Millisecond)
	}
	if s.stopped() {
		return
	}

	// 阶段 3：主机名（NBNS，带 6 字节 MAC 回报）
	if s.DoNames && len(online) > 0 {
		s.setPhase("解析主机名")
		s.resolveNames(online)
	}
	if s.stopped() {
		return
	}

	// 阶段 4：二次读邻居表补全 MAC，顺带贴「本机 / 疑似网关」标签
	s.setPhase("识别网卡厂商")
	arpAfter := arpTable()
	self := map[string]bool{}
	for _, ip := range selfIPs() {
		self[ip] = true
	}
	s.mu.Lock()
	for _, d := range s.devices {
		if self[d.IP] {
			d.Tags = append(d.Tags, "本机")
		} else if strings.HasSuffix(d.IP, ".1") || strings.HasSuffix(d.IP, ".254") {
			// 拿不到默认路由（要读路由表 / 提权），按最常见的两个地址做启发式标注，
			// 措辞用「疑似」——猜错比不猜更容易误导。
			d.Tags = append(d.Tags, "疑似网关")
		}
		if d.MAC == "" {
			if m, ok := arpAfter[d.IP]; ok {
				d.MAC = m
			} else if m, ok := localMACs()[d.IP]; ok {
				// 本机不会出现在自己的 ARP 表里，直接从网卡取
				d.MAC = m
			}
		}
		if d.Vendor == "" && d.MAC != "" {
			if isRandomMAC(d.MAC) {
				// 手机 / 笔记本连 Wi-Fi 时普遍开着「随机 MAC」，OUI 是临时编的，
				// 查不出厂商是必然结果——说明白，别让用户以为插件没识别出牌子。
				d.Vendor = "随机 MAC（设备隐藏了真实网卡地址）"
			} else {
				d.Vendor = lookupVendor(d.MAC)
			}
		}
		ports := make([]int, 0, len(d.Ports))
		for _, p := range d.Ports {
			ports = append(ports, p.Port)
		}
		if d.Kind == "" {
			d.Kind = guessKind(ports, d.Vendor, d.Hostname)
		}
		if len(d.Ports) == 0 {
			d.Tags = append(d.Tags, "无开放端口")
		}
	}
	s.done = s.total
	s.mu.Unlock()

	s.sortDevices()
	s.setPhase("完成")
}

// probeAlive 并发存活探测：每个 IP 依次尝试 probePorts，
// 遇到「握手成功」或「被拒绝」即判定在线并停止后续尝试；全超时才算没动静。
func (s *session) probeAlive(ips []string, arpBefore map[string]string, timeout time.Duration) {
	jobs := make(chan string)
	var wg sync.WaitGroup
	workers := s.Concurrency
	if workers > len(ips) {
		workers = len(ips)
	}
	if workers < 1 {
		workers = 1
	}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					qdLogPanic("[panic] lan-scanner probeAlive", r)
				}
			}()
			for ip := range jobs {
				if s.stopped() {
					continue
				}
				for _, p := range probePorts {
					if s.stopped() || s.ctx.Err() != nil {
						break
					}
					r := tcpProbe(s.ctx, ip, p, timeout)
					if !r.open && !r.refused {
						continue // 没动静，换下一个探针端口
					}
					d := s.addDevice(ip, "probe")
					s.mu.Lock()
					if r.open {
						d.Ports = appendPort(d.Ports, p)
					}
					if d.RTTMs == 0 || int(r.rtt.Milliseconds()) < d.RTTMs {
						d.RTTMs = int(r.rtt.Milliseconds())
					}
					s.mu.Unlock()
					break
				}
				s.bump(1)
			}
		}()
	}
	for _, ip := range ips {
		if s.stopped() {
			break
		}
		jobs <- ip
	}
	close(jobs)
	wg.Wait()

	// 邻居表里出现过但 TCP 完全没响应的设备：手机 / IoT 常见，照样列出来，
	// 只是打上「仅 ARP 缓存」标签说明它是从缓存里认出来的，可能已经离线。
	//
	// 只认本次网段内的地址：Windows 的 arp 表里常年挂着 239.x / 224.x 的组播条目，
	// 不切这一刀，扫描结果里就会冒出「239.255.255.250 在线」这种谁也看不懂的东西。
	inScope := map[string]bool{}
	for _, ip := range ips {
		inScope[ip] = true
	}
	for ip := range arpBefore {
		if s.stopped() {
			break
		}
		if !inScope[ip] || s.device(ip) != nil {
			continue
		}
		d := s.addDevice(ip, "arp")
		s.mu.Lock()
		d.Tags = append(d.Tags, "仅 ARP 缓存")
		s.mu.Unlock()
	}
}

// sortDevices 按 IP 数值排序。发现顺序取决于谁先回包，直接展示会跳来跳去，
// 用户想找「.100 那台」时没法按位置定位。
func (s *session) sortDevices() {
	s.mu.Lock()
	defer s.mu.Unlock()
	sort.Slice(s.devices, func(i, j int) bool {
		a, b := net.ParseIP(s.devices[i].IP), net.ParseIP(s.devices[j].IP)
		if a == nil || b == nil {
			return s.devices[i].IP < s.devices[j].IP
		}
		return binary4(a.To4()) < binary4(b.To4())
	})
}

func appendPort(list []portInfo, p int) []portInfo {
	for _, x := range list {
		if x.Port == p {
			return list
		}
	}
	return append(list, portInfo{Port: p, Service: serviceName(p)})
}

// scanPorts 对在线设备扫剩余端口（存活阶段已确认开放的会跳过）。
func (s *session) scanPorts(online []string, timeout time.Duration) {
	type job struct {
		ip   string
		port int
	}
	jobs := make(chan job)
	var wg sync.WaitGroup
	workers := s.Concurrency
	if workers > len(online)*len(s.Ports) {
		workers = len(online) * len(s.Ports)
	}
	if workers < 1 {
		workers = 1
	}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					qdLogPanic("[panic] lan-scanner scanPorts", r)
				}
			}()
			for j := range jobs {
				if s.stopped() || s.ctx.Err() != nil {
					continue
				}
				r := tcpProbe(s.ctx, j.ip, j.port, timeout)
				if r.open {
					d := s.device(j.ip)
					if d != nil {
						s.mu.Lock()
						d.Ports = appendPort(d.Ports, j.port)
						s.mu.Unlock()
					}
				}
				s.bump(1)
			}
		}()
	}
feed:
	for _, ip := range online {
		for _, p := range s.Ports {
			if s.stopped() {
				break feed
			}
			d := s.device(ip)
			if d != nil && hasPort(d, p) {
				s.bump(1) // 存活阶段已确认，不重复探测
				continue
			}
			jobs <- job{ip, p}
		}
	}
	close(jobs)
	wg.Wait()
}

func hasPort(d *device, p int) bool {
	d2 := d
	for _, x := range d2.Ports {
		if x.Port == p {
			return true
		}
	}
	return false
}

// resolveNames 并发问 NetBIOS 名（顺带可能拿到 MAC）。
func (s *session) resolveNames(online []string) {
	jobs := make(chan string)
	var wg sync.WaitGroup
	var got int64
	workers := 24
	if workers > len(online) {
		workers = len(online)
	}
	if workers < 1 {
		workers = 1
	}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					qdLogPanic("[panic] lan-scanner resolveNames", r)
				}
			}()
			for ip := range jobs {
				if s.stopped() || s.ctx.Err() != nil {
					continue
				}
				// 先问 NetBIOS（Windows 系），问不到再问 mDNS（Mac / Linux / 打印机 / NAS / 手机）。
				// 两条路都走一遍才不至于把整张表留成一片「未知设备」。
				r := nbnsQueryHost(ip, 800*time.Millisecond)
				if r.Name == "" {
					if n := mdnsReverse(ip, 1200*time.Millisecond); n != "" {
						r.Name = n
					}
				}
				if r.Name != "" || r.MAC != "" {
					atomic.AddInt64(&got, 1)
					qdLogInfo("名称 %s → name=%q mac=%s", ip, r.Name, r.MAC)
					if d := s.device(ip); d != nil {
						s.mu.Lock()
						if r.Name != "" {
							d.Hostname = r.Name
						}
						if d.MAC == "" && r.MAC != "" {
							d.MAC = r.MAC
						}
						s.mu.Unlock()
					}
				}
				s.bump(1)
			}
		}()
	}
	for _, ip := range online {
		if s.stopped() {
			break
		}
		jobs <- ip
	}
	close(jobs)
	wg.Wait()
	qdLogInfo("名称解析完成 命中 %d/%d（NBNS + mDNS；没命中通常是对方关了这两套或被防火墙拦）",
		atomic.LoadInt64(&got), len(online))
}
