package main

import (
	"context"
	"net"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"

	"lan-scanner/sysutil"
)

// ARP / 邻居表：操作系统自己缓存的「IP ↔ MAC」对应关系。
//
// 为什么必须要它：手机（尤其 iOS）、IoT 设备在空闲时常常不响应任何 TCP 握手，
// 纯端口探测会把它们全判成离线；但只要最近通信过，它们就一定留在邻居表里。
// 反过来，表里也确实存在已经关机的陈旧条目，所以扫描结果里会带上「仅 ARP」标签，
// 让用户自己判断，而不是替他做决定。
//
// 三个平台的输出格式都不一样，但都是「同一行里同时出现 IP 和 MAC」，
// 所以统一按行扫描而不是按列定位，格式变一点也不会整块失效：
//
//	Windows : 192.168.1.1           aa-bb-cc-dd-ee-ff     动态
//	Linux   : 192.168.1.1 dev eth0 lladdr aa:bb:cc:dd:ee:ff REACHABLE   (ip neigh)
//	        : 192.168.1.1 ether    aa:bb:cc:dd:ee:ff   C eth0           (arp -n)
//	macOS   : ? (192.168.1.1) at aa:bb:cc:dd:ee:ff on en0 ifscope [ethernet]
var (
	reIP  = regexp.MustCompile(`\b(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3})\b`)
	reMAC = regexp.MustCompile(`(?i)\b([0-9a-f]{2}(?:[:-][0-9a-f]{2}){5})\b`)
)

// arpTable 读取系统邻居缓存，返回 ip -> mac（冒号分隔的小写）。
func arpTable() map[string]string {
	out := runNeighborCmd()
	if strings.TrimSpace(out) == "" {
		qdLogWarn("邻居表读取为空 cmd=%s", neighborCmdName())
		return map[string]string{}
	}
	table := parseNeighbor(out)
	if len(table) == 0 {
		qdLogWarn("邻居表解析出 0 条 raw=%d 字节", len(out))
	}
	return table
}

func neighborCmdName() string {
	if runtime.GOOS == "linux" {
		return "ip neigh show"
	}
	return "arp -a"
}

func runNeighborCmd() string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = sysutil.CommandContext(ctx, "arp", "-a")
	case "linux":
		// ip 属 iproute2，绝大多数发行版自带；老系统（只有 net-tools）回退 arp -n
		cmd = sysutil.CommandContext(ctx, "ip", "neigh", "show")
	default: // darwin 及其它 BSD
		cmd = sysutil.CommandContext(ctx, "arp", "-a")
	}

	out, err := cmd.Output()
	if err != nil {
		if runtime.GOOS == "linux" {
			qdLogWarn("ip neigh 失败（%v），回退 arp -n", err)
			if out2, err2 := sysutil.CommandContext(ctx, "arp", "-n").Output(); err2 == nil {
				return string(out2)
			}
		}
		qdLogWarn("邻居表命令失败 cmd=%s err=%v", neighborCmdName(), err)
		return ""
	}
	return string(out)
}

// parseNeighbor 逐行提取「IP + MAC」配对，跳过广播与全零/全 F 这类无效 MAC。
func parseNeighbor(raw string) map[string]string {
	table := make(map[string]string)
	for _, line := range strings.Split(raw, "\n") {
		ip := reIP.FindString(line)
		mac := reMAC.FindString(line)
		if ip == "" || mac == "" {
			continue
		}
		if ip == "0.0.0.0" || ip == "255.255.255.255" || strings.HasPrefix(ip, "224.") {
			continue
		}
		m := normalizeMAC(mac)
		if m == "" {
			continue
		}
		table[ip] = m
	}
	return table
}

// localMACs 本机网卡的 IP → MAC。
// 自己这台机器不会出现在自己的 ARP 表（邻居缓存里没有自己），
// 想让结果表里「本机」那行也有 MAC，只能从网卡直接读。
func localMACs() map[string]string {
	out := map[string]string{}
	ifaces, err := net.Interfaces()
	if err != nil {
		return out
	}
	for _, iface := range ifaces {
		mac := iface.HardwareAddr.String()
		if mac == "" {
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
			if ip == nil {
				continue
			}
			out[ip.String()] = normalizeMAC(mac)
		}
	}
	return out
}

// normalizeMAC 归一成冒号分隔的小写，并把无效值（全零 / 广播）判空。
func normalizeMAC(s string) string {
	s = strings.ToLower(strings.ReplaceAll(s, "-", ":"))
	if s == "00:00:00:00:00:00" || s == "ff:ff:ff:ff:ff:ff" {
		return ""
	}
	// 只认 6 段、每段两位十六进制
	parts := strings.Split(s, ":")
	if len(parts) != 6 {
		return ""
	}
	for _, p := range parts {
		if len(p) != 2 {
			return ""
		}
		for i := 0; i < len(p); i++ {
			c := p[i]
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
				return ""
			}
		}
	}
	return s
}
