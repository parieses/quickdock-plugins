package main

// 采样器：常驻 goroutine 按固定间隔采集，前端只取快照。

import (
	"fmt"
	"runtime"
	"sort"
	"sync"
	"time"
)

const (
	sampleInterval   = time.Second
	connInterval     = 3 * time.Second
	historyPoints    = 300 // 5 分钟
	maxConnsReturned = 300
)

// rawIface 一次采样得到的单个接口（已按物理网卡去重）。
type rawIface struct {
	key      string // 同一物理网卡的所有 NDIS 绑定共享同一个 \DEVICE\TCPIP_{GUID}
	name     string
	typ      uint32
	speedBps uint64
	oper     uint32
	in       uint32 // 网卡累计字节数（32 位，会回绕）
	out      uint32
}

// rawConn 一条连接（TCP / UDP）。
type rawConn struct {
	proto  string
	local  string
	remote string
	state  uint32
	pid    uint32
	// 原始四元组（地址小端、端口网络序原始值），用于 estats 定位连接
	localAddr  uint32
	localPort  uint32
	remoteAddr uint32
	remotePort uint32
}

type ifaceState struct {
	name     string
	typ      uint32
	speedBps uint64
	oper     uint32
	prevIn   uint32
	prevOut  uint32
	hasPrev  bool
	seen     bool
	inRate   float64
	outRate  float64
	inTotal  uint64
	outTotal uint64
}

type histPoint struct {
	T   int64   `json:"t"`
	In  float64 `json:"in"`
	Out float64 `json:"out"`
}

type ifaceInfo struct {
	Key      string  `json:"key"`
	Name     string  `json:"name"`
	Type     string  `json:"type"`
	SpeedBps uint64  `json:"speedBps"`
	Oper     uint32  `json:"oper"`
	Up       bool    `json:"up"`
	InRate   float64 `json:"inRate"`
	OutRate  float64 `json:"outRate"`
	InTotal  uint64  `json:"inTotal"`
	OutTotal uint64  `json:"outTotal"`
}

type connInfo struct {
	Proto   string  `json:"proto"`
	Local   string  `json:"local"`
	Remote  string  `json:"remote"`
	State   string  `json:"state"`
	PID     uint32  `json:"pid"`
	Proc    string  `json:"proc"`
	InRate  float64 `json:"inRate"`  // 该连接的下载速率 B/s（仅 TCP，UDP 无法按连接归属）
	OutRate float64 `json:"outRate"` // 该连接的上传速率 B/s
}

type snapshot struct {
	Supported  bool        `json:"supported"`
	Error      string      `json:"error,omitempty"`
	SampledAt  string      `json:"sampledAt"`
	ConnAt     string      `json:"connAt"`
	IntervalMs int         `json:"intervalMs"`
	InRate     float64     `json:"inRate"`
	OutRate    float64     `json:"outRate"`
	PeakIn     float64     `json:"peakIn"`
	PeakOut    float64     `json:"peakOut"`
	InTotal    uint64      `json:"inTotal"`
	OutTotal   uint64      `json:"outTotal"`
	PerConn    bool        `json:"perConn"` // 连接级上传/下载是否可用（需管理员权限开启 estats）
	Interfaces []ifaceInfo `json:"interfaces"`
	History    []histPoint `json:"history"`
	Conns      []connInfo  `json:"conns"`
	ConnTotal  int         `json:"connTotal"`
}

// connBytesState 一条 TCP 连接的 estats 字节基线。
type connBytesState struct {
	prevIn  uint64
	prevOut uint64
	hasPrev bool
	enabled bool // estats 采集启用成功（未启用时读到的计数是垃圾值，绝不使用）
	seen    bool
}

type samplerState struct {
	supported bool
	err       string

	ifaces    map[string]*ifaceState
	history   []histPoint
	conns     []connInfo
	connTotal int
	connAt    time.Time
	sampledAt time.Time

	// 每连接字节统计（TCP estats）。key = 本地地址:端口>远端地址:端口 的原始值。
	// estats 采集默认关闭，首见连接时 Set 启用（需管理员）；被拒后不再重试。
	connBytes    map[string]*connBytesState
	connPrevAt   time.Time
	estatsTried  bool
	estatsDenied bool

	inRate   float64
	outRate  float64
	peakIn   float64
	peakOut  float64
	inTotal  uint64
	outTotal uint64
}

var (
	stMu sync.Mutex
	st   = &samplerState{ifaces: map[string]*ifaceState{}, connBytes: map[string]*connBytesState{}}
)

func startSampler() {
	if !platformSupported() {
		stMu.Lock()
		st.supported = false
		st.err = "当前平台不支持：" + runtime.GOOS + "（仅 Windows）"
		stMu.Unlock()
		qdLogError("平台不支持，采样器未启动 os=%s arch=%s", runtime.GOOS, runtime.GOARCH)
		return
	}
	stMu.Lock()
	st.supported = true
	stMu.Unlock()
	qdLogInfo("采样器启动 间隔=%s 连接刷新=%s", sampleInterval, connInterval)

	go func() {
		defer func() {
			if r := recover(); r != nil {
				qdLogPanic("sampler goroutine", r)
			}
		}()
		lastConn := time.Time{}
		sampleConns() // 首屏就有连接表，不用等 3 秒
		lastConn = time.Now()
		for {
			sampleOnce()
			if time.Since(lastConn) >= connInterval {
				sampleConns()
				lastConn = time.Now()
			}
			time.Sleep(sampleInterval)
		}
	}()
}

func sampleOnce() {
	rows, err := collectIfaces()
	now := time.Now()
	secs := sampleInterval.Seconds()

	stMu.Lock()
	defer stMu.Unlock()

	if err != nil {
		st.err = err.Error()
		qdLogWarn("网卡采样失败 err=%v", err)
		return
	}
	st.err = ""
	st.sampledAt = now

	var rateIn, rateOut float64
	var deltaIn, deltaOut uint64

	for _, r := range rows {
		s := st.ifaces[r.key]
		if s == nil {
			s = &ifaceState{}
			st.ifaces[r.key] = s
		}
		s.name = r.name
		s.typ = r.typ
		s.speedBps = r.speedBps
		s.oper = r.oper
		s.seen = true

		if !s.hasPrev {
			// 首帧只建基线：否则会把「开机至今的累计」当成这一秒的流量，
			// 表现为刚打开插件就有一条几十 GB/s 的假峰值。
			s.prevIn, s.prevOut = r.in, r.out
			s.hasPrev = true
			continue
		}
		// uint32 减法天然回绕：网卡计数器是 32 位的，过 4 GiB 归零后
		// (cur - prev) 仍然等于真实增量。
		dIn := uint64(r.in - s.prevIn)
		dOut := uint64(r.out - s.prevOut)
		s.prevIn, s.prevOut = r.in, r.out
		s.inRate = float64(dIn) / secs
		s.outRate = float64(dOut) / secs
		s.inTotal += dIn
		s.outTotal += dOut
		rateIn += s.inRate
		rateOut += s.outRate
		deltaIn += dIn
		deltaOut += dOut
	}

	// 摘掉本轮没再出现的接口（禁用网卡 / 拔掉网卡后不该留在列表里）
	for k, s := range st.ifaces {
		if !s.seen {
			delete(st.ifaces, k)
			continue
		}
		s.seen = false
	}

	st.inRate, st.outRate = rateIn, rateOut
	st.inTotal += deltaIn
	st.outTotal += deltaOut
	if rateIn > st.peakIn {
		st.peakIn = rateIn
	}
	if rateOut > st.peakOut {
		st.peakOut = rateOut
	}
	st.history = append(st.history, histPoint{T: now.UnixMilli(), In: rateIn, Out: rateOut})
	if len(st.history) > historyPoints {
		st.history = append(st.history[:0], st.history[len(st.history)-historyPoints:]...)
	}
}

func sampleConns() {
	raws, err := collectConns()
	now := time.Now()

	stMu.Lock()
	defer stMu.Unlock()

	if err != nil {
		qdLogWarn("连接表采集失败 err=%v", err)
		return
	}
	names := procNames()

	// 两次连接采样的实际间隔（异常大间隔说明线程被挂起过，差分不可信，本轮只建基线）
	secs := -1.0
	if !st.connPrevAt.IsZero() {
		if d := now.Sub(st.connPrevAt).Seconds(); d > 0 && d <= 30 {
			secs = d
		}
	}

	estatsDenied := st.estatsTried && st.estatsDenied
	list := make([]connInfo, 0, len(raws))
	for _, r := range raws {
		ci := connInfo{
			Proto:  r.proto,
			Local:  r.local,
			Remote: r.remote,
			State:  connStateName(r.proto, r.state),
			PID:    r.pid,
			Proc:   names[r.pid],
		}
		// 仅已建立的 TCP 连接有字节计数；UDP 没有按连接归属的计数接口
		if r.proto == "TCP" && r.state == 5 {
			key := fmt.Sprintf("%08x:%04x>%08x:%04x", r.localAddr, r.localPort, r.remoteAddr, r.remotePort)
			bs := st.connBytes[key]
			if bs == nil {
				bs = &connBytesState{}
				st.connBytes[key] = bs
				// 新连接首见：打开 estats 采集，从现在开始计数（之前的流量追不回来）
				if !estatsDenied {
					st.estatsTried = true
					if ok, code := tcpEstatsEnable(mibTcpRow{state: r.state, localAddr: r.localAddr, localPort: r.localPort, remoteAddr: r.remoteAddr, remotePort: r.remotePort}); ok {
						bs.enabled = true
					} else {
						st.estatsDenied = true
						estatsDenied = true
						qdLogWarn("estats 采集启用失败 code=%d（需管理员权限），连接级上传/下载不可用", code)
					}
				}
			}
			bs.seen = true
			if bs.enabled {
				if in, out, ok := tcpEstatsBytes(mibTcpRow{state: r.state, localAddr: r.localAddr, localPort: r.localPort, remoteAddr: r.remoteAddr, remotePort: r.remotePort}); ok {
					// 计数合理性守卫：256TB 以上的「计数」必是未初始化垃圾
					if in > 1<<48 || out > 1<<48 {
						bs.hasPrev = false
					} else if !bs.hasPrev || secs <= 0 {
						bs.prevIn, bs.prevOut, bs.hasPrev = in, out, true
					} else if in < bs.prevIn || out < bs.prevOut {
						// 计数回退：四元组被新连接复用，重建基线，本轮不计速率
						bs.prevIn, bs.prevOut = in, out
					} else {
						ci.InRate = float64(in-bs.prevIn) / secs
						ci.OutRate = float64(out-bs.prevOut) / secs
						bs.prevIn, bs.prevOut = in, out
					}
				}
			}
		}
		list = append(list, ci)
	}
	// 摘掉本轮没再出现的连接（连接关闭 / 四元组复用后重建基线）
	for k, bs := range st.connBytes {
		if !bs.seen {
			delete(st.connBytes, k)
			continue
		}
		bs.seen = false
	}
	sort.Slice(list, func(i, j int) bool {
		// 已建立连接优先（LISTEN / TIME_WAIT 之类对「谁在传数据」没有参考价值）；
		// 同级按速率（正在传的排前面，方便盯某个软件）、再按进程名 / 远端地址稳定排序。
		ei, ej := rankConn(list[i]), rankConn(list[j])
		if ei != ej {
			return ei < ej
		}
		ri, rj := list[i].InRate+list[i].OutRate, list[j].InRate+list[j].OutRate
		if ri != rj {
			return ri > rj
		}
		if list[i].Proc != list[j].Proc {
			return list[i].Proc < list[j].Proc
		}
		return list[i].Remote < list[j].Remote
	})
	st.conns = list
	st.connTotal = len(list)
	st.connAt = now
	st.connPrevAt = now
}

// rankConn 越小越靠前：0=已建立的 TCP，1=其它 TCP，2=有进程名的 UDP，3=其它。
func rankConn(c connInfo) int {
	switch {
	case c.Proto == "TCP" && c.State == "ESTABLISHED":
		return 0
	case c.Proto == "TCP":
		return 1
	case c.Proc != "":
		return 2
	default:
		return 3
	}
}

func resetStats() {
	stMu.Lock()
	defer stMu.Unlock()
	st.ifaces = map[string]*ifaceState{}
	st.connBytes = map[string]*connBytesState{}
	st.history = nil
	st.inRate, st.outRate = 0, 0
	st.peakIn, st.peakOut = 0, 0
	st.inTotal, st.outTotal = 0, 0
	qdLogInfo("统计已重置")
}

func buildSnapshot() snapshot {
	stMu.Lock()
	defer stMu.Unlock()

	s := snapshot{
		Supported:  st.supported,
		Error:      st.err,
		SampledAt:  st.sampledAt.Format("15:04:05"),
		ConnAt:     st.connAt.Format("15:04:05"),
		IntervalMs: int(sampleInterval.Milliseconds()),
		InRate:     st.inRate,
		OutRate:    st.outRate,
		PeakIn:     st.peakIn,
		PeakOut:    st.peakOut,
		InTotal:    st.inTotal,
		OutTotal:   st.outTotal,
		PerConn:    st.supported && !st.estatsDenied,
		Interfaces: make([]ifaceInfo, 0, len(st.ifaces)),
		History:    append([]histPoint(nil), st.history...),
		ConnTotal:  st.connTotal,
	}
	for k, v := range st.ifaces {
		s.Interfaces = append(s.Interfaces, ifaceInfo{
			Key:      k,
			Name:     v.name,
			Type:     ifaceTypeName(v.typ),
			SpeedBps: v.speedBps,
			Oper:     v.oper,
			Up:       v.oper == 1,
			InRate:   v.inRate,
			OutRate:  v.outRate,
			InTotal:  v.inTotal,
			OutTotal: v.outTotal,
		})
	}
	sort.Slice(s.Interfaces, func(i, j int) bool {
		pi, pj := s.Interfaces[i].InRate+s.Interfaces[i].OutRate, s.Interfaces[j].InRate+s.Interfaces[j].OutRate
		if pi != pj {
			return pi > pj
		}
		return s.Interfaces[i].Name < s.Interfaces[j].Name
	})
	s.Conns = st.conns
	if len(s.Conns) > maxConnsReturned {
		s.Conns = s.Conns[:maxConnsReturned]
	}
	if s.Interfaces == nil {
		s.Interfaces = []ifaceInfo{}
	}
	if s.History == nil {
		s.History = []histPoint{}
	}
	if s.Conns == nil {
		s.Conns = []connInfo{}
	}
	return s
}

// connStateName 把 TCP 状态码翻成人话；UDP 无状态。
func connStateName(proto string, state uint32) string {
	if proto != "TCP" {
		return "-"
	}
	switch state {
	case 1:
		return "CLOSED"
	case 2:
		return "LISTEN"
	case 3:
		return "SYN_SENT"
	case 4:
		return "SYN_RCVD"
	case 5:
		return "ESTABLISHED"
	case 6:
		return "FIN_WAIT_1"
	case 7:
		return "FIN_WAIT_2"
	case 8:
		return "CLOSE_WAIT"
	case 9:
		return "CLOSING"
	case 10:
		return "LAST_ACK"
	case 11:
		return "TIME_WAIT"
	case 12:
		return "DELETE_TCB"
	default:
		return "UNKNOWN"
	}
}

func ifaceTypeName(t uint32) string {
	switch t {
	case 1:
		return "其他"
	case 6:
		return "以太网"
	case 9:
		return "令牌环"
	case 23:
		return "PPP"
	case 24:
		return "回环"
	case 28:
		return "SLIP"
	case 37:
		return "ATM"
	case 53:
		return "隧道"
	case 71:
		return "无线"
	case 117:
		return "千兆以太"
	case 131:
		return "隧道"
	case 144:
		return "WWAN"
	case 243:
		return "移动宽带"
	default:
		return "接口"
	}
}
