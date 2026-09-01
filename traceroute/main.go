// Traceroute 路由追踪 — 逐跳显示到目标的路径与延迟
//
// 实现方式：递增 IcmpSendEcho 的 IP_OPTION_INFORMATION.Ttl。当报文 TTL 在中途耗尽时，
// 路由器会回送 ICMP TTL Exceeded，IcmpSendEcho 返回状态 IP_TTL_EXPIRED_TRANSIT，
// 且 ICMP_ECHO_REPLY.Address 即为该跳路由器地址。因此无需 raw socket / 管理员权限。
//
// 命令：start / poll / stop
//   最坏情况（30 跳 × 多次探测 × 超时）会远超宿主 20s execute 限制，
//   因此同样采用「start 立即返回 + 前端轮询」的异步会话模型。
package main

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

/* ==================== ICMP（Windows iphlpapi） ==================== */

var (
	iphlpapi       = syscall.NewLazyDLL("iphlpapi.dll")
	procIcmpCreate = iphlpapi.NewProc("IcmpCreateFile")
	procIcmpSend   = iphlpapi.NewProc("IcmpSendEcho")
	procIcmpClose  = iphlpapi.NewProc("IcmpCloseHandle")
)

const (
	ipSuccess     = 0
	ipReqTimedOut = 11010
	ipTTLExpired  = 11013
)

func icmpStatusText(s uint32) string {
	switch s {
	case ipReqTimedOut:
		return "请求超时"
	case ipTTLExpired:
		return "TTL 过期"
	case 11003:
		return "目标不可达"
	default:
		return fmt.Sprintf("ICMP 状态 %d", s)
	}
}

// ipOptionInfo 对应 IP_OPTION_INFORMATION（64 位下长度 16，OptionsData 按 8 字节对齐）
type ipOptionInfo struct {
	TTL         byte
	TOS         byte
	Flags       byte
	OptionsSize byte
	OptionsData uintptr
}

type icmpReply struct {
	Address       string
	Status        uint32
	RoundTripTime uint32
}

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
		h, addr, payloadPtr, uintptr(len(payload)),
		uintptr(unsafe.Pointer(&opts)), uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)), uintptr(timeoutMs),
	)
	if n == 0 {
		// 调用成功时 err 也可能是非 nil 的 Errno(0)，必须显式比较
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

/* ==================== 会话 ==================== */

type hopResult struct {
	TTL  int       `json:"ttl"`
	IP   string    `json:"ip"`
	Host string    `json:"host,omitempty"`
	RTTs []float64 `json:"rtts"` // -1 表示该次探测超时
	OK   bool      `json:"ok"`
	Done bool      `json:"done"` // 已到达目标
}

type session struct {
	ID   string
	Host string
	IP   string

	mu      sync.Mutex
	hops    []hopResult
	running bool
	stopCh  chan struct{}
}

var (
	sessMu   sync.Mutex
	sessions = map[string]*session{}
	seqID    int
)

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

func (s *session) run(maxHops, probes, timeoutMs int, resolve bool) {
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

func (s *session) snapshot() map[string]interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	hops := make([]hopResult, len(s.hops))
	copy(hops, s.hops)
	return map[string]interface{}{
		"sessionId": s.ID, "host": s.Host, "ip": s.IP,
		"running": s.running, "hops": hops,
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

	sessMu.Lock()
	seqID++
	sid := fmt.Sprintf("t%d", seqID)
	s := &session{ID: sid, Host: host, IP: ip.String(), running: true, stopCh: make(chan struct{})}
	sessions[sid] = s
	sessMu.Unlock()

	go s.run(maxHops, probes, timeoutMs, resolve)

	respond(id, map[string]interface{}{
		"sessionId": sid, "host": host, "ip": ip.String(), "running": true,
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
		respond(req.ID, map[string]interface{}{"status": "ready", "name": "QuickDock Traceroute"})
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
