//go:build windows

package main

// Windows 侧采集实现。
//
// 全部走 iphlpapi / kernel32 的系统调用，不 spawn 任何子进程：速率采样要 1 秒一次，
// 每次拉起 powershell / netstat 既慢又会在后台持续制造进程噪音。
//
// 结构布局均已在本机实测校准（探针见开发记录）：
//   · MIB_IFROW      sizeof = 860，MIB_IFTABLE 的行从偏移 4 开始
//   · MIB_TCPROW_OWNER_PID / MIB_UDPROW_OWNER_PID  24 / 12 字节，行从偏移 4 开始
//   · PROCESSENTRY32W sizeof = 568
// 这些是 Windows 的 ABI，改不得；改动前请先用探针复验。

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"syscall"
	"unsafe"
)

var (
	iphlpapi    = syscall.NewLazyDLL("iphlpapi.dll")
	kernel32    = syscall.NewLazyDLL("kernel32.dll")
	advapi32    = syscall.NewLazyDLL("advapi32.dll")
	procIfTable = iphlpapi.NewProc("GetIfTable")
	procTcpTbl  = iphlpapi.NewProc("GetExtendedTcpTable")
	procUdpTbl  = iphlpapi.NewProc("GetExtendedUdpTable")
	procMB2WC   = kernel32.NewProc("MultiByteToWideChar")
	procSnap    = kernel32.NewProc("CreateToolhelp32Snapshot")
	procFirst   = kernel32.NewProc("Process32FirstW")
	procNext    = kernel32.NewProc("Process32NextW")

	// 每条 TCP 连接的字节数（estats）：采集默认关闭，要先 Set 启用（需管理员权限）
	procGetTcpEstats = iphlpapi.NewProc("GetPerTcpConnectionEStats")
	procSetTcpEstats = iphlpapi.NewProc("SetPerTcpConnectionEStats")

	// 自检当前进程令牌是否已提升：estats 需要「Administrators 组 + UAC 完整令牌」，
	// 宿主声明自己是管理员不等于插件子进程真的拿到了提升令牌，直接测最可靠。
	procGetCurProcess  = kernel32.NewProc("GetCurrentProcess")
	procOpenProcessTok = advapi32.NewProc("OpenProcessToken")
	procGetTokenInfo   = advapi32.NewProc("GetTokenInformation")
)

// mibIfRow 对应 Windows 的 MIB_IFROW（bDescr 是单字节 ANSI，不是 WCHAR）。
type mibIfRow struct {
	Name        [256]uint16
	Index       uint32
	Type        uint32
	Mtu         uint32
	Speed       uint32
	PhysAddrLen uint32
	PhysAddr    [8]byte
	AdminStatus uint32
	OperStatus  uint32
	LastChange  uint32
	InOctets    uint32
	InUcast     uint32
	InNUcast    uint32
	InDiscards  uint32
	InErrors    uint32
	InUnknown   uint32
	OutOctets   uint32
	OutUcast    uint32
	OutNUcast   uint32
	OutDiscards uint32
	OutErrors   uint32
	OutQLen     uint32
	DescrLen    uint32
	Descr       [256]byte
}

const (
	ifRowSize  = unsafe.Sizeof(mibIfRow{})
	tcpRowSize = 24
	udpRowSize = 12
	afInet     = 2
	// TCP_TABLE_OWNER_PID_ALL / UDP_TABLE_OWNER_PID
	tcpTableOwnerPidAll = 5
	udpTableOwnerPid    = 1
	// ERROR_INSUFFICIENT_BUFFER：缓冲区不够时 Windows 会把所需大小写回 pdwSize
	errInsufficientBuffer = 122
	th32csSnapProcess     = 0x00000002
	maxPath               = 260

	// TCP_ESTATS_TYPE 里的 TcpConnectionEstatsData（枚举第 2 项）
	tcpEstatsData = 1
	// TCP_BOOLEAN_OPTIONAL 里的 TcpBoolOptEnabled
	tcpBoolOptEnabled = 1
	// sizeof(TCP_ESTATS_DATA_ROD_v0)：6×ULONG64 + 5×ULONG + 4 填充 + ULONG64 + ULONG + 4 填充 + ULONG64
	tcpEstatsRodSize = 96
	// TOKEN_QUERY / TokenElevation（TOKEN_INFORMATION_CLASS 第 20 项）
	tokenQuery    = 0x0008
	tokenElevated = 20
)

// tcpRow / udpRow 对应 MIB_TCPROW_OWNER_PID / MIB_UDPROW_OWNER_PID。
type tcpRow struct {
	state      uint32
	localAddr  uint32
	localPort  uint32
	remoteAddr uint32
	remotePort uint32
	pid        uint32
}

type udpRow struct {
	localAddr uint32
	localPort uint32
	pid       uint32
}

type processEntry32 struct {
	Size            uint32
	CntUsage        uint32
	ProcessID       uint32
	DefaultHeapID   uintptr
	ModuleID        uint32
	CntThreads      uint32
	ParentProcessID uint32
	PriClassBase    int32
	Flags           uint32
	ExeFile         [maxPath]uint16
}

func platformSupported() bool { return true }

// isElevated 读 TOKEN_ELEVATION（不是简单查 Administrators 组成员）：
// UAC 下管理员账户拿到的默认仍是筛选令牌，SetPerTcpConnectionEStats 会返回
// ERROR_ACCESS_DENIED；只有「以管理员身份运行」得到的完整令牌才算数。
func isElevated() bool {
	h, _, _ := procGetCurProcess.Call()
	var tok syscall.Handle
	ret, _, _ := procOpenProcessTok.Call(h, tokenQuery, uintptr(unsafe.Pointer(&tok)))
	if ret == 0 || tok == syscall.InvalidHandle {
		return false
	}
	defer syscall.CloseHandle(tok)
	var val, n uint32
	ret, _, _ = procGetTokenInfo.Call(uintptr(tok), tokenElevated,
		uintptr(unsafe.Pointer(&val)), 4, uintptr(unsafe.Pointer(&n)))
	return ret != 0 && val != 0
}

// collectIfaces 读网卡计数，并按物理网卡去重后返回。
//
// 为什么必须去重：GetIfTable 返回的是「NDIS 绑定」而不是网卡——一块物理网卡会带出
// 若干条 -WFP Native MAC Layer LightWeight Filter-0000 / -QoS Packet Scheduler-0000 之类的
// 行，它们的累计字节数完全相同。不去重的话同一块网卡会被算 5~6 遍，速率直接虚高。
// 同一网卡的所有绑定共享同一个 \DEVICE\TCPIP_{GUID}，用它做分组键。
func collectIfaces() ([]rawIface, error) {
	rows, err := getIfTable()
	if err != nil {
		return nil, err
	}
	type group struct {
		name     string
		typ      uint32
		speedBps uint64
		oper     uint32
		in, out  uint32
	}
	groups := make(map[string]*group, len(rows))
	order := make([]string, 0, len(rows))
	for i := range rows {
		r := &rows[i]
		descr := ansiCut(r.Descr[:])
		base := baseIfaceName(descr)
		// 分组键 = 类型 + 设备基础名：\DEVICE\TCPIP_{GUID} 看着像天然主键，但实测
		// 物理网卡行与它的各条 filter 绑定行 GUID 并不相同，按 GUID 分组等于没分。
		key := fmt.Sprintf("%d|%s", r.Type, base)
		g, ok := groups[key]
		if !ok {
			groups[key] = &group{name: descr, typ: r.Type, speedBps: uint64(r.Speed), oper: r.OperStatus, in: r.InOctets, out: r.OutOctets}
			order = append(order, key)
			continue
		}
		// 显示名取最短的那条：物理网卡行没有 "-WFP ...-0000" 这类后缀，
		// 因此最短的描述正好是干净的设备名。
		if descr != "" && (g.name == "" || len(descr) < len(g.name)) {
			g.name = descr
			g.typ = r.Type
			g.oper = r.OperStatus
		}
		// 计数取最大值：各绑定的累计值实测一致，取 max 可避免个别绑定慢一帧造成的抖动
		if r.InOctets > g.in {
			g.in = r.InOctets
		}
		if r.OutOctets > g.out {
			g.out = r.OutOctets
		}
		if uint64(r.Speed) > g.speedBps {
			g.speedBps = uint64(r.Speed)
		}
	}
	out := make([]rawIface, 0, len(order))
	for _, k := range order {
		g := groups[k]
		name := g.name
		if name == "" {
			name = k
		}
		out = append(out, rawIface{key: k, name: name, typ: g.typ, speedBps: g.speedBps, oper: g.oper, in: g.in, out: g.out})
	}
	return out, nil
}

// baseIfaceName 把「网卡描述」还原成设备名本身。
//
// GetIfTable 给出的是 NDIS 绑定，描述形如：
//     Intel(R) Dual Band Wireless-AC 3165                                  ← 物理网卡行
//     Intel(R) Dual Band Wireless-AC 3165-WFP Native MAC Layer LightWeight Filter-0000
//     Intel(R) Dual Band Wireless-AC 3165-QoS Packet Scheduler-0000
// 后两者是同一块网卡上的过滤器，流量与物理行完全一致。末尾 "-xxxx-NNNN"（4 位序号）
// 是绑定的固定后缀，剥掉它就能把 6 条记录并回 1 块网卡。
func baseIfaceName(descr string) string {
	if descr == "" {
		return descr
	}
	parts := strings.Split(descr, "-")
	if len(parts) >= 3 {
		last := parts[len(parts)-1]
		if len(last) == 4 && isDigits(last) {
			return strings.Join(parts[:len(parts)-2], "-")
		}
	}
	return descr
}

func isDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func getIfTable() ([]mibIfRow, error) {
	var size uint32
	ret, _, _ := procIfTable.Call(0, uintptr(unsafe.Pointer(&size)), 0)
	if ret != 0 && ret != errInsufficientBuffer {
		return nil, fmt.Errorf("GetIfTable 探测失败 code=%d", ret)
	}
	if size == 0 {
		size = 16 * 1024
	}
	for attempt := 0; attempt < 3; attempt++ {
		buf := make([]byte, int(size))
		ret, _, _ := procIfTable.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0)
		switch ret {
		case 0:
			n := int(*(*uint32)(unsafe.Pointer(&buf[0])))
			maxN := (len(buf) - 4) / int(ifRowSize)
			if n > maxN {
				n = maxN
			}
			if n <= 0 {
				return nil, nil
			}
			rows := make([]mibIfRow, 0, n)
			base := unsafe.Pointer(&buf[0])
			for i := 0; i < n; i++ {
				rows = append(rows, *(*mibIfRow)(unsafe.Add(base, 4+uintptr(i)*ifRowSize)))
			}
			return rows, nil
		case errInsufficientBuffer:
			// size 已被 Windows 更新为所需大小，下一轮按新 size 重试
			continue
		default:
			return nil, fmt.Errorf("GetIfTable 失败 code=%d", ret)
		}
	}
	return nil, errors.New("GetIfTable 多次重试仍失败（接口数持续增长？）")
}

func collectConns() ([]rawConn, error) {
	var out []rawConn
	tcp, err := tcpTable()
	if err == nil {
		for _, r := range tcp {
			out = append(out, rawConn{
				proto:      "TCP",
				local:      fmt.Sprintf("%s:%d", int2ip(r.localAddr), ntohs(r.localPort)),
				remote:     fmt.Sprintf("%s:%d", int2ip(r.remoteAddr), ntohs(r.remotePort)),
				state:      r.state,
				pid:        r.pid,
				localAddr:  r.localAddr,
				localPort:  r.localPort,
				remoteAddr: r.remoteAddr,
				remotePort: r.remotePort,
			})
		}
	} else {
		qdLogWarn("TCP 连接表读取失败 err=%v", err)
	}
	udp, err := udpTable()
	if err == nil {
		for _, r := range udp {
			if r.localAddr == 0 && r.localPort == 0 {
				continue
			}
			out = append(out, rawConn{
				proto:  "UDP",
				local:  fmt.Sprintf("%s:%d", int2ip(r.localAddr), ntohs(r.localPort)),
				remote: "-",
				pid:    r.pid,
			})
		}
	} else {
		qdLogWarn("UDP 连接表读取失败 err=%v", err)
	}
	if len(out) == 0 && err != nil {
		return nil, err
	}
	return out, nil
}

func tcpTable() ([]tcpRow, error) {
	buf, n, err := extendedTable(procTcpTbl, tcpTableOwnerPidAll, tcpRowSize)
	if err != nil {
		return nil, err
	}
	base := unsafe.Pointer(&buf[0])
	rows := make([]tcpRow, 0, n)
	for i := 0; i < n; i++ {
		p := unsafe.Add(base, 4+uintptr(i)*tcpRowSize)
		rows = append(rows, tcpRow{
			state:      *(*uint32)(p),
			localAddr:  *(*uint32)(unsafe.Add(p, 4)),
			localPort:  *(*uint32)(unsafe.Add(p, 8)),
			remoteAddr: *(*uint32)(unsafe.Add(p, 12)),
			remotePort: *(*uint32)(unsafe.Add(p, 16)),
			pid:        *(*uint32)(unsafe.Add(p, 20)),
		})
	}
	return rows, nil
}

func udpTable() ([]udpRow, error) {
	buf, n, err := extendedTable(procUdpTbl, udpTableOwnerPid, udpRowSize)
	if err != nil {
		return nil, err
	}
	base := unsafe.Pointer(&buf[0])
	rows := make([]udpRow, 0, n)
	for i := 0; i < n; i++ {
		p := unsafe.Add(base, 4+uintptr(i)*udpRowSize)
		rows = append(rows, udpRow{
			localAddr: *(*uint32)(p),
			localPort: *(*uint32)(unsafe.Add(p, 4)),
			pid:       *(*uint32)(unsafe.Add(p, 8)),
		})
	}
	return rows, nil
}

// extendedTable 通用的「先探大小、再取数据」两步调用（TCP / UDP 表共用）。
func extendedTable(proc *syscall.LazyProc, tableClass uintptr, rowSize uintptr) ([]byte, int, error) {
	var size uint32
	ret, _, _ := proc.Call(0, uintptr(unsafe.Pointer(&size)), 0, afInet, tableClass, 0)
	if ret != 0 && ret != errInsufficientBuffer {
		return nil, 0, fmt.Errorf("探测失败 code=%d", ret)
	}
	if size == 0 {
		size = 64 * 1024
	}
	for attempt := 0; attempt < 3; attempt++ {
		buf := make([]byte, int(size))
		ret, _, _ := proc.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0, afInet, tableClass, 0)
		switch ret {
		case 0:
			n := int(*(*uint32)(unsafe.Pointer(&buf[0])))
			maxN := (len(buf) - 4) / int(rowSize)
			if n > maxN {
				n = maxN
			}
			return buf, n, nil
		case errInsufficientBuffer:
			continue
		default:
			return nil, 0, fmt.Errorf("调用失败 code=%d", ret)
		}
	}
	return nil, 0, errors.New("多次重试仍失败")
}

// procNames 用 Toolhelp32 快照建一份 pid → 进程名（每次刷新建一次，不长期缓存：
// 进程会退出、pid 会被复用，缓存久了名字就对不上了）。
func procNames() map[uint32]string {
	names := make(map[uint32]string, 128)
	h, _, _ := procSnap.Call(th32csSnapProcess, 0)
	if h == 0 || h == uintptr(syscall.InvalidHandle) {
		qdLogWarn("CreateToolhelp32Snapshot 失败，进程名留空")
		return names
	}
	defer syscall.CloseHandle(syscall.Handle(h))

	var pe processEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	ret, _, _ := procFirst.Call(h, uintptr(unsafe.Pointer(&pe)))
	for ret != 0 {
		if pe.ProcessID != 0 {
			names[pe.ProcessID] = syscall.UTF16ToString(pe.ExeFile[:])
		}
		pe.Size = uint32(unsafe.Sizeof(pe))
		ret, _, _ = procNext.Call(h, uintptr(unsafe.Pointer(&pe)))
	}
	return names
}

// ansiCut 把 MIB_IFROW.bDescr（系统 ANSI 代码页的单字节串）转成 UTF-8。
// 中文系统的网卡描述是 GBK，直接按字节转 string 会出乱码，所以非 ASCII 时
// 走 MultiByteToWideChar(CP_ACP) —— 效果与资源管理器里看到的设备名一致。
func ansiCut(b []byte) string {
	n := 0
	for n < len(b) && b[n] != 0 {
		n++
	}
	if n == 0 {
		return ""
	}
	s := b[:n]
	if isASCII(s) {
		return string(s)
	}
	cnt, _, _ := procMB2WC.Call(0, 0, uintptr(unsafe.Pointer(&b[0])), uintptr(n), 0, 0)
	if cnt == 0 || cnt > 4096 {
		return string(s)
	}
	buf := make([]uint16, cnt)
	written, _, _ := procMB2WC.Call(0, 0, uintptr(unsafe.Pointer(&b[0])), uintptr(n), uintptr(unsafe.Pointer(&buf[0])), cnt)
	if written == 0 {
		return string(s)
	}
	return syscall.UTF16ToString(buf[:written])
}

func isASCII(b []byte) bool {
	for _, c := range b {
		if c >= 0x80 {
			return false
		}
	}
	return true
}

func utf16Cut(u []uint16) string {
	n := 0
	for n < len(u) && u[n] != 0 {
		n++
	}
	return syscall.UTF16ToString(u[:n])
}

func int2ip(v uint32) string {
	return net.IPv4(byte(v), byte(v>>8), byte(v>>16), byte(v>>24)).String()
}

// ntohs 端口是网络字节序（大端），Windows 直接给了原始值，需要翻转高低字节。
func ntohs(v uint32) uint32 {
	return ((v & 0xff) << 8) | ((v >> 8) & 0xff)
}

// ---------- 每连接字节统计（TCP estats）----------

// TCP_ESTATS_DATA_ROD_v0，布局以 tcpestats.h 为准（总 96 字节，改前先复验）：
//   DataBytesOut@0 / DataSegsOut@8 / DataBytesIn@16 / DataSegsIn@24，
//   其余字段（Segs/SoftErrors/SndUna/…）本插件用不到。
// DataBytesIn/Out 是 ULONG64，无 32 位回绕问题。
type tcpEstatsRod struct {
	DataBytesOut uint64
	DataSegsOut  uint64
	DataBytesIn  uint64
	DataSegsIn   uint64
	_            [tcpEstatsRodSize - 32]byte
}

// mibTcpRow 对应 MIB_TCPROW（20 字节）：estats 两个 API 都以它定位连接，
// 布局与 tcpRow 前 5 个字段一致（addr 小端、port 网络序的原始值）。
type mibTcpRow struct {
	state      uint32
	localAddr  uint32
	localPort  uint32
	remoteAddr uint32
	remotePort uint32
}

// estatsRwSize 缓存「本机实际能用的 Rw 缓冲区尺寸」，0 = 尚未探测成功。
// 采样每秒对上百条连接调用一次，探测成功后就不再重复试错。
// 用 atomic 是因为快照请求与后台采样可能落在不同 goroutine。
var estatsRwSize atomic.Uint32

// tcpEstatsEnable 打开一条连接的 Data 统计采集。
//
// 这里必须做尺寸自适应，不能写死 4 字节：TCP_ESTATS_DATA_RW_v0 只有
// 一个 1 字节的 BOOLEAN EnableCollection，而 MSDN 示例又传 4 字节的
// TCP_BOOLEAN_OPTIONAL 枚举值。两种写法在不同 Windows 版本上都有人踩坑，
// 传错时 SetPerTcpConnectionEStats 返回 1784 ERROR_INVALID_USER_BUFFER
// （「提供给请求操作的用户缓冲区无效」）——这是缓冲区格式问题，**不是权限
// 不足**（权限不足返回 5 ERROR_ACCESS_DENIED）。本机实测：未提权时任意尺寸
// 都返回 5，说明权限检查在参数校验之前；因此提权后仍拿到 1784，就一定是
// Rw 尺寸/格式没对上，再怎么重启管理员都没用。
//
// 按 1 字节 → 4 字节的顺序试，哪个成功就缓存哪个；某天缓存的尺寸失效
// （系统更新、连接类型不同）会自动重新探测。
func tcpEstatsEnable(row mibTcpRow) (ok bool, code uint32) {
	var enable byte = byte(tcpBoolOptEnabled)   // 1 字节 BOOLEAN
	var flag uint32 = tcpBoolOptEnabled          // 4 字节枚举值

	// 快路径：复用已验证的尺寸
	if cached := estatsRwSize.Load(); cached != 0 {
		var rc uintptr
		if cached == 1 {
			rc, _, _ = procSetTcpEstats.Call(
				uintptr(unsafe.Pointer(&row)), tcpEstatsData,
				uintptr(unsafe.Pointer(&enable)), 0, 1, 0)
		} else {
			rc, _, _ = procSetTcpEstats.Call(
				uintptr(unsafe.Pointer(&row)), tcpEstatsData,
				uintptr(unsafe.Pointer(&flag)), 0, 4, 0)
		}
		if rc == 0 {
			return true, 0
		}
		estatsRwSize.Store(0) // 缓存失效，下面重新探测
		code = uint32(rc)
	}

	// 探测路径：先 1 字节，再 4 字节
	ret, _, _ := procSetTcpEstats.Call(
		uintptr(unsafe.Pointer(&row)), tcpEstatsData,
		uintptr(unsafe.Pointer(&enable)), 0, 1, 0)
	if ret == 0 {
		estatsRwSize.Store(1)
		return true, 0
	}
	if code == 0 {
		code = uint32(ret)
	}
	ret, _, _ = procSetTcpEstats.Call(
		uintptr(unsafe.Pointer(&row)), tcpEstatsData,
		uintptr(unsafe.Pointer(&flag)), 0, 4, 0)
	if ret == 0 {
		estatsRwSize.Store(4)
		return true, 0
	}
	if code == 0 {
		code = uint32(ret)
	}
	return false, code
}

// tcpEstatsBytes 读一条连接的累计收发字节数（含重传，不含 TCP 头）。
//
// 注意：只对「启用成功」的连接调用——未启用采集时该 API 会成功返回但
// 结构体内容是未初始化的垃圾值，直接差分会得到天文数字速率。
func tcpEstatsBytes(row mibTcpRow) (in, out uint64, ok bool) {
	var rod tcpEstatsRod
	ret, _, _ := procGetTcpEstats.Call(
		uintptr(unsafe.Pointer(&row)), tcpEstatsData,
		0, 0, 0, // Rw（只读不写）
		0, 0, 0, // Ros（只读不取）
		uintptr(unsafe.Pointer(&rod)), 0, tcpEstatsRodSize)
	if ret != 0 {
		return 0, 0, false
	}
	return rod.DataBytesIn, rod.DataBytesOut, true
}
