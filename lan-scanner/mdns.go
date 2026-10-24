package main

import (
	"encoding/binary"
	"net"
	"strings"
	"time"
)

// mDNS 反向查询：拿到 IP 问「你叫什么名字」。
//
// NBNS（NetBIOS）只覆盖 Windows 系设备，而且从 Windows 10 后期开始默认越来越常被关掉，
// 实测在不少网络里命中率是 0。mDNS（Bonjour / Avahi）是另一套：Mac、Linux、
// 打印机、NAS、iPhone、Android、智能音箱基本都开着，问法也简单——
// 往 224.0.0.251:5353 发一条 <反查地址>.in-addr.arpa 的 PTR 查询，谁的名字对应这个 IP 就谁答。
//
// 与 NBNS 互补：先问 NBNS，没答再问 mDNS，两条都空就老实留空。
const (
	mdnsGroup = "224.0.0.251"
	mdnsPort  = 5353
)

// mdnsReverse 返回形如 "raspberrypi" 的主机名（已去掉 .local 后缀）。
func mdnsReverse(ip string, timeout time.Duration) string {
	target := net.ParseIP(ip)
	if target == nil {
		return ""
	}
	conn, err := net.ListenUDP("udp4", nil)
	if err != nil {
		return ""
	}
	defer conn.Close()

	q := buildPTRQuery(reverseName(ip))
	grp := &net.UDPAddr{IP: net.ParseIP(mdnsGroup), Port: mdnsPort}
	if _, err := conn.WriteToUDP(q, grp); err != nil {
		qdLogWarn("mDNS 组播发送失败 ip=%s err=%v", ip, err)
		return ""
	}

	buf := make([]byte, 1500)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(deadline)
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			break
		}
		// 只认目标自己发回来的应答：组播里谁都可能答，不校验源地址会张冠李戴
		if from == nil || !from.IP.Equal(target) {
			continue
		}
		if name := extractPTRName(buf[:n]); name != "" {
			return name
		}
	}
	return ""
}

// reverseName 192.168.1.20 → 20.1.168.192.in-addr.arpa
func reverseName(ip string) string {
	parts := strings.Split(ip, ".")
	if len(parts) != 4 {
		return ""
	}
	return parts[3] + "." + parts[2] + "." + parts[1] + "." + parts[0] + ".in-addr.arpa"
}

// buildPTRQuery 构造标准 DNS 查询报文。QCLASS 高位（0x8000）置 1 是 mDNS 的
// 「请单播回我」位——我们绑的是随机端口、收不到组播回包，必须靠它把应答引回本机。
func buildPTRQuery(fqdn string) []byte {
	msg := []byte{0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0}
	for _, label := range strings.Split(fqdn, ".") {
		if label == "" {
			continue
		}
		msg = append(msg, byte(len(label)))
		msg = append(msg, label...)
	}
	msg = append(msg, 0x00)       // 名字结束
	msg = append(msg, 0x00, 0x0c) // QTYPE = PTR
	msg = append(msg, 0x80, 0x01) // QCLASS = IN + QU 位
	return msg
}

// extractPTRName 从应答报文里取出 PTR 记录指向的主机名。
// 只处理最标致的形态：问题区 1 条、应答里第一条 PTR，够用了——
// 反向查询的应答通常也就一条 PTR + 一条 A。
func extractPTRName(pkt []byte) string {
	if len(pkt) < 12 {
		return ""
	}
	anCount := int(binary.BigEndian.Uint16(pkt[6:8]))
	if anCount == 0 {
		return ""
	}
	off := 12
	// 跳过问题区
	for i := 0; i < int(binary.BigEndian.Uint16(pkt[4:6])) && off < len(pkt); i++ {
		var ok bool
		off, ok = skipName(pkt, off)
		if !ok {
			return ""
		}
		off += 4 // QTYPE + QCLASS
	}
	for i := 0; i < anCount && off+10 <= len(pkt); i++ {
		_, next, ok := readName(pkt, off)
		if !ok {
			return ""
		}
		off = next
		if off+10 > len(pkt) {
			return ""
		}
		rtype := binary.BigEndian.Uint16(pkt[off : off+2])
		off += 2 + 2 + 4 // TYPE + CLASS + TTL
		rdlen := int(binary.BigEndian.Uint16(pkt[off : off+2]))
		off += 2
		if off+rdlen > len(pkt) {
			return ""
		}
		if rtype == 0x0c { // PTR
			name, _, ok := readName(pkt, off)
			if !ok || name == "" {
				return ""
			}
			return trimLocal(name)
		}
		off += rdlen
	}
	return ""
}

// trimLocal 去掉 .local. / .local 尾巴，只留裸主机名。
func trimLocal(name string) string {
	name = strings.TrimSuffix(name, ".")
	if strings.HasSuffix(strings.ToLower(name), ".local") {
		name = name[:len(name)-6]
	}
	return strings.TrimSpace(name)
}

// readName 读取（可能带压缩指针的）DNS 名字，返回名字与结束偏移。
func readName(pkt []byte, off int) (string, int, bool) {
	var parts []string
	for {
		if off >= len(pkt) {
			return "", off, false
		}
		l := int(pkt[off])
		if l&0xC0 == 0xC0 { // 压缩指针
			if off+1 >= len(pkt) {
				return "", off, false
			}
			ptr := int(binary.BigEndian.Uint16(pkt[off:off+2]) & 0x3FFF)
			n, _, ok := readName(pkt, ptr)
			if !ok {
				return "", off + 2, false
			}
			parts = append(parts, n)
			return strings.Join(parts, "."), off + 2, true
		}
		if l == 0 {
			return strings.Join(parts, "."), off + 1, true
		}
		off++
		if off+l > len(pkt) {
			return "", off, false
		}
		parts = append(parts, string(pkt[off:off+l]))
		off += l
		if len(parts) > 32 { // 防畸形报文把循环拖死
			return "", off, false
		}
	}
}

func skipName(pkt []byte, off int) (int, bool) {
	_, next, ok := readName(pkt, off)
	return next, ok
}
