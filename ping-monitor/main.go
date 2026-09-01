// Ping 延迟监视器 — 持续探测目标延迟并统计（丢包率/最小/平均/最大）
//
// ICMP 实现走 iphlpapi.dll 的 IcmpSendEcho：与 raw socket 不同，它不需要管理员权限，
// 因此普通权限下即可发送真实 ICMP 回显请求。若该 API 不可用，自动回退为 TCP 握手计时。
//
// 命令：start / poll / stop
//   受宿主 20s execute 超时限制，start 只创建会话并立即返回，由前端轮询 poll 拉结果。
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

// ipOptionInfo 对应 IP_OPTION_INFORMATION（64 位下长度 16，OptionsData 按 8 字节对齐）
type ipOptionInfo struct {
	TTL         byte
	TOS         byte
	Flags       byte
	OptionsSize byte
	OptionsData uintptr
}

// icmpReply 对应 ICMP_ECHO_REPLY 的前几个字段，按小端解析
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
	// IPAddr 为网络字节序（等价于 inet_addr）
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
		// 注意：调用成功时 err 也可能是非 nil 的 Errno(0)，必须显式比较
		if err != nil && err != syscall.Errno(0) {
			return nil, err
		}
		return nil, errors.New("IcmpSendEcho 无响应")
	}
	return &icmpReply{
		Address:       net.IPv4(buf[0], buf[1], buf[2], buf[3]).String(),
		Status:        binary.LittleEndian.Uint32(buf[4:8]),
		RoundTripTime: binary.LittleEndian.Uint32(buf[8:12]),
	}, nil
}

/* ==================== 会话 ==================== */

type pingResult struct {
	Seq int     `json:"seq"`
	RTT float64 `json:"rtt"` // 毫秒，-1 表示失败
	OK  bool    `json:"ok"`
	Err string  `json:"error,omitempty"`
}

type session struct {
	ID      string
	Host    string
	IP      string
	Mode    string // icmp | tcp
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
	sessMu   sync.Mutex
	sessions = map[string]*session{}
	seqID    int
)

func intFrom(m map[string]interface{}, key string, def int) int {
	switch v := m[key].(type) {
	case float64:
		n := int(v)
		if n <= 0 {
			return def
		}
		return n
	case string:
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func (s *session) pingOnce(seq, timeoutMs, port int) pingResult {
	res := pingResult{Seq: seq, RTT: -1}

	if s.Mode == "icmp" {
		ip := net.ParseIP(s.IP)
		if ip == nil {
			res.Err = "IP 解析失败"
			return res
		}
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

	// TCP 回退：以 TCP 握手完成耗时作为延迟指标
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

func (s *session) run(intervalMs, count int) {
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
		if len(s.results) > 1000 { // 防止长时间监控无限增长
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

func (s *session) snapshot() map[string]interface{} {
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
		"sessionId": s.ID,
		"host":      s.Host,
		"ip":        s.IP,
		"mode":      s.Mode,
		"running":   s.running,
		"results":   rs,
		"stats": map[string]interface{}{
			"sent": s.sent, "recv": s.recv, "loss": loss,
			"min": min, "max": s.max, "avg": avg, "last": last,
		},
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
	// 去掉可能的协议前缀
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

	sessMu.Lock()
	seqID++
	sid := fmt.Sprintf("p%d", seqID)
	s := &session{
		ID: sid, Host: host, IP: ip.String(), Mode: mode,
		Timeout: timeoutMs, Port: port,
		running: true, stopCh: make(chan struct{}),
	}
	sessions[sid] = s
	sessMu.Unlock()

	go s.run(interval, count)

	respond(id, map[string]interface{}{
		"sessionId": sid, "host": host, "ip": ip.String(), "mode": mode, "running": true,
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
		respond(req.ID, map[string]interface{}{"status": "ready", "name": "QuickDock Ping Monitor"})
	case "host.ping":
		respond(req.ID, map[string]interface{}{"pong": true})
	case "plugin.execute":
		var params executeParams
		if len(req.Params) > 0 {
			_ = json.Unmarshal(req.Params, &params)
		}
		// 命令统一小写匹配（宿主分发前已 ToLower，此处再兜一层）
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
