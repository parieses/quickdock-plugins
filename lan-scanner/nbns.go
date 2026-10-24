package main

import (
	"encoding/binary"
	"math/rand"
	"net"
	"strings"
	"time"
)

// NetBIOS 名称服务（NBNS，UDP 137）的 NBSTAT 查询。
//
// 它是局域网里唯一「不需要凭据、不需要对方开端口」就能问到机器名的手段：
// 只要目标在线且没关掉 NetBIOS，向它的 137 端口发一个 NBSTAT 查询，
// 对方就会回自己的 NetBIOS 名（Windows 主机名、群晖/NAS 名、打印机名都在里面），
// 响应尾部还带着 Unit ID，也就是那块网卡的 MAC —— 连邻居表都不用查。
//
// 报文格式（RFC 1002）：DNS 风格 12 字节头，QTYPE=0x0021(NBSTAT)。
// 名字段用 NetBIOS 的「半字节 + 'A'」编码：通配符 '*' 编码后是 'C''K'，
// 后跟 30 个 'A'（补到 16 字节的 NetBIOS 名长度）。这段是各扫描器通用的写法。
const nbnsPort = 137

var nbnsQuery []byte

func init() {
	q := make([]byte, 0, 50)
	q = append(q, 0, 0)       // Transaction ID（发送前填随机值）
	q = append(q, 0x00, 0x00) // Flags: 标准查询
	q = append(q, 0x00, 0x01) // QDCOUNT = 1
	q = append(q, 0, 0, 0, 0, 0, 0)
	q = append(q, 0x20) // 名字长度：32（16 字节 NetBIOS 名编码后翻倍）
	q = append(q, 'C', 'K')
	for i := 0; i < 30; i++ {
		q = append(q, 'A')
	}
	q = append(q, 0x00)       // 名字结束
	q = append(q, 0x00, 0x21) // QTYPE = NBSTAT
	q = append(q, 0x00, 0x01) // QCLASS = IN
	nbnsQuery = q
}

type nbnsResult struct {
	Name string
	MAC  string
}

// nbnsQueryHost 向 ip 发一次 NBSTAT 查询，超时由 timeout 控制。
// 失败（超时 / 被防火墙丢 / 目标没开 NetBIOS）一律返回空，不报错——
// 名称解析是锦上添花，绝不能因为问不到名字就把设备判成离线。
func nbnsQueryHost(ip string, timeout time.Duration) nbnsResult {
	addr := &net.UDPAddr{IP: net.ParseIP(ip), Port: nbnsPort}
	conn, err := net.DialUDP("udp4", nil, addr)
	if err != nil {
		return nbnsResult{}
	}
	defer conn.Close()

	id := uint16(rand.Intn(0xffff)) //nolint:gosec // 事务 ID 只需区分并发响应，非安全用途
	q := make([]byte, len(nbnsQuery))
	copy(q, nbnsQuery)
	binary.BigEndian.PutUint16(q[0:2], id)

	_ = conn.SetDeadline(time.Now().Add(timeout))
	if _, err := conn.Write(q); err != nil {
		return nbnsResult{}
	}
	buf := make([]byte, 1024)
	n, err := conn.Read(buf)
	if err != nil || n < 12 {
		return nbnsResult{}
	}
	return parseNBSTAT(buf[:n], id)
}

func parseNBSTAT(pkt []byte, wantID uint16) nbnsResult {
	if binary.BigEndian.Uint16(pkt[0:2]) != wantID {
		return nbnsResult{}
	}
	if pkt[2]&0x80 == 0 { // QR 位必须是响应
		return nbnsResult{}
	}
	qd := int(binary.BigEndian.Uint16(pkt[4:6]))
	an := int(binary.BigEndian.Uint16(pkt[6:8]))
	if an == 0 {
		return nbnsResult{}
	}

	// 跳过头部，再跳过问题区（NBSTAT 的问题区固定 34 字节名 + 4 字节 type/class）
	off := 12
	for i := 0; i < qd && off < len(pkt); i++ {
		if off >= len(pkt) {
			return nbnsResult{}
		}
		l := int(pkt[off])
		if l == 0 {
			off++
			break
		}
		off += l + 1
	}
	off += 4 // QTYPE + QCLASS
	if off+10 > len(pkt) {
		return nbnsResult{}
	}

	// 应答区：名字一般是压缩指针 0xC00C
	if pkt[off]&0xC0 == 0xC0 {
		off += 2
	} else {
		l := int(pkt[off])
		off += l + 1
	}
	if off+10 > len(pkt) {
		return nbnsResult{}
	}
	rtype := binary.BigEndian.Uint16(pkt[off : off+2])
	off += 2 + 2 + 4 // TYPE + CLASS + TTL
	if rtype != 0x0021 || off+2 > len(pkt) {
		return nbnsResult{}
	}
	rdlen := int(binary.BigEndian.Uint16(pkt[off : off+2]))
	off += 2
	if off+rdlen > len(pkt) {
		return nbnsResult{}
	}
	rd := pkt[off : off+rdlen]
	if len(rd) < 2 {
		return nbnsResult{}
	}

	numNames := int(rd[0])
	if numNames <= 0 || len(rd) < 1+18*numNames+6 {
		return nbnsResult{}
	}
	res := nbnsResult{}
	// 名字区：每条 18 字节 = 15 字节名 + 1 字节后缀 + 2 字节标志
	// 后缀 0x00 = 工作站名（主机名本体），0x20 = Server 服务，0x03 = Messenger
	for i := 0; i < numNames; i++ {
		base := 1 + i*18
		nm := strings.TrimSpace(string(rd[base : base+15]))
		suffix := rd[base+15]
		if nm == "" || strings.ContainsRune(nm, 0x00) {
			continue
		}
		if res.Name == "" {
			res.Name = nm
		}
		if suffix == 0x00 {
			res.Name = nm // 工作站名最贴近「这台机器叫什么」
			break
		}
	}
	// 名字区之后紧跟 6 字节 Unit ID，就是网卡 MAC
	uid := rd[1+18*numNames : 1+18*numNames+6]
	res.MAC = normalizeMAC(macBytes(uid))
	return res
}

func macBytes(b []byte) string {
	parts := make([]string, 0, len(b))
	for _, x := range b {
		parts = append(parts, sprintfHex(x))
	}
	return strings.Join(parts, ":")
}

func sprintfHex(b byte) string {
	const digits = "0123456789abcdef"
	return string([]byte{digits[b>>4], digits[b&0x0f]})
}
