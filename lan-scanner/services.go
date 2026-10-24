package main

import "strings"

// 端口号 → 常见服务。只标「在局域网里一眼能认出是什么设备」的那些端口，
// 认不出就留空，不硬凑一个名字误导用户。
var portServices = map[int]string{
	21: "FTP", 22: "SSH", 23: "Telnet", 25: "SMTP", 53: "DNS", 80: "HTTP",
	110: "POP3", 111: "RPC", 135: "MS-RPC", 139: "NetBIOS", 143: "IMAP",
	161: "SNMP", 389: "LDAP", 443: "HTTPS", 445: "SMB 文件共享",
	515: "LPD 打印", 548: "AFP", 554: "RTSP 视频流", 631: "IPP 打印",
	1433: "SQL Server", 1883: "MQTT", 1900: "UPnP", 2049: "NFS",
	3306: "MySQL", 3389: "远程桌面", 3689: "iTunes 共享", 5000: "群晖 DSM",
	5001: "群晖 DSM(HTTPS)", 5432: "PostgreSQL", 5900: "VNC", 6379: "Redis",
	8080: "HTTP 代理/服务", 8081: "HTTP 备用", 8096: "Jellyfin", 8123: "Home Assistant",
	8200: "VMware", 8443: "HTTPS 备用", 8554: "RTSP 备用", 8888: "HTTP 管理",
	9000: "服务端口", 9100: "打印机 RAW", 9200: "Elasticsearch",
	11211: "Memcached", 27017: "MongoDB", 32400: "Plex", 50000: "群晖 文件",
	5353: "mDNS", 62078: "iPhone 同步",
}

// 存活探测端口：不管用户选了什么端口集，都先用这几个探一下「这台机器在不在」。
// 选它们的理由是覆盖面广——路由器/NAS/打印机一般有 80 或 443，Windows 有 445，
// Linux/网络设备有 22，Windows 远程桌面是 3389。命中任意一个即可判定在线。
var probePorts = []int{80, 443, 445, 22, 3389}

// 端口预设。probePorts 始终参与扫描（存活探测顺带发现的端口也要展示），
// 预设只是在此之上再叠加多少。
var portPresets = map[string][]int{
	"fast": {},
	"common": {
		21, 23, 139, 5900, 631, 9100, 5000, 5001, 554, 8000, 8080, 8888,
		3306, 6379, 5432, 3689, 8123, 1883, 1900, 32400, 8096, 62078,
	},
	"full": {
		21, 23, 25, 53, 110, 111, 135, 139, 143, 161, 389, 515, 548, 554,
		5900, 631, 9100, 1433, 1883, 1900, 2049, 3306, 5000, 5001, 5432,
		6379, 8000, 8080, 8081, 8096, 8123, 8200, 8443, 8554, 8888, 9000,
		9200, 11211, 27017, 32400, 50000, 62078,
	},
}

func serviceName(port int) string { return portServices[port] }

// mergePorts 把用户端口集与存活探测端口合并、去重、升序。
func mergePorts(user []int) []int {
	seen := map[int]bool{}
	out := []int{}
	for _, p := range append(append([]int{}, probePorts...), user...) {
		if p <= 0 || p > 65535 || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	// 升序，结果列表看着才像样
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// guessKind 猜这台设备大概是干什么用的，依据依次是：
// 强特征端口（打印/视频/NAS 这类一锤定音的）→ 主机名关键词 → 通用端口 → 厂商。
//
// 只是辅助标签，猜不出就返回空串——宁可空着也别乱贴。
func guessKind(ports []int, vendor, hostname string) string {
	has := func(p int) bool {
		for _, x := range ports {
			if x == p {
				return true
			}
		}
		return false
	}
	// 1. 强特征端口：这些端口几乎只在特定设备上出现，命中即可定性
	switch {
	case has(9100) || has(631) || has(515):
		return "打印机"
	case has(554) || has(8554):
		return "摄像头 / NVR"
	case has(5000) && has(5001):
		return "群晖 NAS"
	case has(32400) || has(8096):
		return "影音服务器"
	case has(8123):
		return "Home Assistant"
	case has(62078):
		return "iPhone / iPad"
	}

	// 2. 主机名：设备常常自报家门（"xiaomi-gateway-hub1"、"raspberrypi"、"BRW1234"）
	if h := strings.ToLower(strings.TrimSpace(hostname)); h != "" {
		switch {
		case strings.Contains(h, "iphone") || strings.Contains(h, "ipad") || strings.Contains(h, "ipod"):
			return "iPhone / iPad"
		case strings.Contains(h, "gateway") || strings.Contains(h, "router") || strings.Contains(h, "gw-"):
			return "网关 / 路由器"
		case strings.Contains(h, "printer") || strings.Contains(h, "print") || strings.HasPrefix(h, "brw"):
			return "打印机"
		case strings.Contains(h, "raspberry") || strings.HasPrefix(h, "rpi"):
			return "开发板 / IoT"
		case strings.Contains(h, "nas") || strings.Contains(h, "synology") || strings.Contains(h, "qnap"):
			return "NAS 存储"
		case strings.Contains(h, "macbook") || strings.Contains(h, "imac") || strings.Contains(h, "mac-mini"):
			return "Mac"
		case strings.Contains(h, "tv") || strings.Contains(h, "bravia") || strings.Contains(h, "mi-tv"):
			return "电视 / 盒子"
		}
	}

	// 3. 通用端口与厂商
	switch {
	case has(3389):
		return "Windows 主机"
	case has(445) || has(139):
		return "Windows 主机"
	case has(22):
		return "Linux / Unix"
	case has(80) || has(443) || has(8080):
		return "Web 服务"
	case has(1900) || has(5353) || has(1883):
		return "物联网设备"
	}
	v := strings.ToLower(vendor)
	switch {
	case strings.Contains(v, "raspberry") || strings.Contains(v, "espressif"):
		return "开发板 / IoT"
	case strings.Contains(v, "hikvision") || strings.Contains(v, "dahua"):
		return "摄像头 / NVR"
	case strings.Contains(v, "synology") || strings.Contains(v, "qnap") || strings.Contains(v, "western"):
		return "NAS 存储"
	case strings.Contains(v, "hp printer") || strings.Contains(v, "epson") ||
		strings.Contains(v, "canon") || strings.Contains(v, "brother"):
		return "打印机"
	}
	return ""
}
