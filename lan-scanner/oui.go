package main

import (
	"fmt"
	"strings"
)

// MAC 前 3 字节（OUI）→ 厂商。只收录局域网里真正会碰到的牌子，
// 重点覆盖国内常见的路由器 / 手机 / IoT / NAS / 打印机。
//
// 完整 IEEE 注册库有三万多条，塞进插件既臃肿又没人看得完；这里取「够认出设备」的粒度：
// 命中就显示厂商，没命中就留空——留空比瞎猜一个错的强。
var ouiTable = map[string]string{
	// 路由器与网络设备
	"00:0a:eb": "ASUS", "00:1b:fc": "ASUS", "08:60:6e": "ASUS", "10:bf:48": "ASUS",
	"1c:b7:2c": "ASUS", "2c:4d:54": "ASUS", "30:5a:3a": "ASUS", "38:d5:47": "ASUS",
	"50:46:5d": "ASUS", "54:a0:50": "ASUS", "60:cf:84": "ASUS", "70:4d:7b": "ASUS",
	"74:d0:2b": "ASUS", "78:24:af": "ASUS", "88:d7:f6": "ASUS", "9c:5c:8e": "ASUS",
	"ac:9e:17": "ASUS", "b0:6e:bf": "ASUS", "c8:60:00": "ASUS", "f0:79:59": "ASUS",
	"14:cc:20": "TP-Link", "18:a6:f7": "TP-Link", "30:b5:c2": "TP-Link",
	"50:c7:bf": "TP-Link", "60:e3:27": "TP-Link", "6c:5a:b0": "TP-Link",
	"98:da:c4": "TP-Link", "a4:2b:b0": "TP-Link", "b0:4e:26": "TP-Link",
	"c0:25:e9": "TP-Link", "d8:0d:17": "TP-Link", "e8:94:f6": "TP-Link",
	"ec:08:6b": "TP-Link", "f4:f2:6d": "TP-Link", "00:31:92": "TP-Link",
	"b0:95:75": "TP-Link", "40:ae:30": "TP-Link",
	"00:1e:10": "HUAWEI", "00:25:9e": "HUAWEI", "04:c0:6f": "HUAWEI",
	"0c:96:bf": "HUAWEI", "20:2b:c1": "HUAWEI", "34:29:12": "HUAWEI",
	"48:7d:2e": "HUAWEI", "54:25:ea": "HUAWEI", "70:8a:09": "HUAWEI",
	"8c:70:5a": "HUAWEI", "a4:56:02": "HUAWEI", "c4:70:ab": "HUAWEI",
	"d4:6a:6a": "HUAWEI", "e0:24:7d": "HUAWEI", "f4:8c:50": "HUAWEI",
	"00:e0:fc": "HUAWEI", "28:31:52": "HUAWEI", "8c:34:fd": "HUAWEI",
	"04:cf:8c": "Xiaomi", "0c:1d:af": "Xiaomi", "10:2a:b3": "Xiaomi",
	"18:59:36": "Xiaomi", "20:82:c0": "Xiaomi", "28:6c:07": "Xiaomi",
	"34:80:b3": "Xiaomi", "44:23:7c": "Xiaomi", "50:64:2b": "Xiaomi",
	"64:b4:73": "Xiaomi", "78:11:dc": "Xiaomi", "8c:be:be": "Xiaomi",
	"9c:99:a0": "Xiaomi", "a0:9f:10": "Xiaomi", "b0:e2:35": "Xiaomi",
	"c4:0b:cb": "Xiaomi", "d4:97:0b": "Xiaomi", "f0:b4:29": "Xiaomi",
	"f8:a4:5f": "Xiaomi", "28:9c:c6": "Xiaomi", "50:8f:4c": "Xiaomi",
	"00:1b:44": "NETGEAR", "08:02:8e": "NETGEAR", "10:0d:7f": "NETGEAR",
	"20:4e:7f": "NETGEAR", "28:80:88": "NETGEAR", "44:94:fc": "NETGEAR",
	"a0:40:a0": "NETGEAR", "b0:39:56": "NETGEAR", "c4:04:15": "NETGEAR",
	"00:05:5d": "D-Link", "00:0f:3d": "D-Link", "00:1c:f0": "D-Link",
	"00:22:b0": "D-Link", "00:26:5a": "D-Link", "1c:af:f7": "D-Link",
	"78:54:2e": "D-Link", "90:94:e4": "D-Link", "b8:a3:86": "D-Link",
	"00:d0:b7": "Ruijie 锐捷", "00:1a:a9": "Ruijie 锐捷", "70:f9:6d": "Ruijie 锐捷",
	"00:0a:cd": "Tenda 腾达", "c8:3a:35": "Tenda 腾达", "08:10:76": "Tenda 腾达",
	"00:1f:33": "MERCURY 水星", "b0:48:1a": "MERCURY 水星",
	"74:5a:aa": "FAST 迅捷",
	"00:13:82": "Phicomm 斐讯", "f8:32:e4": "Phicomm 斐讯",
	"00:17:fa": "ZTE 中兴", "00:19:c6": "ZTE 中兴", "4c:54:99": "ZTE 中兴",
	"78:44:fd": "Ubiquiti", "24:a4:3c": "Ubiquiti", "80:2a:a8": "Ubiquiti",
	"4c:5e:0c": "MikroTik", "e4:8d:8c": "MikroTik", "74:4d:28": "MikroTik",
	"00:23:69": "Cisco", "00:24:97": "Cisco", "58:bf:ea": "Cisco",

	// 手机与电脑
	"00:1c:b3": "Apple", "00:22:41": "Apple", "04:0c:ce": "Apple",
	"0c:74:c2": "Apple", "10:9a:dd": "Apple", "14:98:77": "Apple",
	"18:af:61": "Apple", "28:cf:e9": "Apple", "3c:15:c2": "Apple",
	"40:3c:fc": "Apple", "48:60:bc": "Apple", "5c:95:ae": "Apple",
	"60:69:4a": "Apple", "6c:94:f8": "Apple", "70:56:81": "Apple",
	"78:7e:61": "Apple", "88:63:df": "Apple", "98:01:a7": "Apple",
	"a4:83:e7": "Apple", "b8:8d:12": "Apple", "c8:69:cd": "Apple",
	"d0:81:7a": "Apple", "dc:2b:2a": "Apple", "e4:8b:7f": "Apple",
	"f0:18:98": "Apple", "f8:1e:df": "Apple", "ac:de:48": "Apple",
	"00:12:47": "SAMSUNG", "00:15:99": "SAMSUNG", "00:1a:8a": "SAMSUNG",
	"00:23:39": "SAMSUNG", 	"08:08:c2": "SAMSUNG", "28:29:86": "SAMSUNG", "5c:0a:5b": "SAMSUNG", "78:52:1a": "SAMSUNG",
	"8c:77:12": "SAMSUNG", "a8:f2:74": "SAMSUNG", "cc:07:ab": "SAMSUNG",
	"e4:70:b8": "SAMSUNG", "f4:8b:32": "SAMSUNG",
	"00:1b:21": "Intel", "00:21:6a": "Intel", "08:11:96": "Intel",
	"3c:97:0e": "Intel", "68:05:ca": "Intel", "8c:8d:28": "Intel",
	"a0:36:9f": "Intel", "c4:8e:8f": "Intel", "ac:fd:ce": "Intel",
	"00:14:22": "Dell", "00:21:9b": "Dell", "18:66:da": "Dell",
	"24:b6:fd": "Dell", "f8:bc:12": "Dell", "b0:83:fe": "Dell",
	"00:1e:8f": "HP", "00:23:7d": "HP", "00:25:b3": "HP",
	"3c:d9:2b": "HP", "70:5a:0f": "HP", "a0:b3:cc": "HP", "94:57:a5": "HP",
	"00:16:d4": "Lenovo", "00:21:cc": "Lenovo", "54:ee:75": "Lenovo",
	"70:8b:cd": "Lenovo", "98:fa:9b": "Lenovo", "f8:9e:94": "Lenovo",
	"00:d8:61": "Microsoft", "7c:1e:52": "Microsoft", "98:5f:d3": "Microsoft",
	"00:0c:29": "VMware", "00:50:56": "VMware", "00:05:69": "VMware",
	"00:1c:14": "VMware", "00:1c:42": "Parallels",
	"08:00:27": "VirtualBox", "0a:00:27": "VirtualBox",
	"02:42:ac": "Docker", "02:42:c0": "Docker",

	// IoT / 开发板 / 摄像头
	"b8:27:eb": "Raspberry Pi", "dc:a6:32": "Raspberry Pi",
	"e4:5f:01": "Raspberry Pi", "28:cd:c1": "Raspberry Pi",
	"24:0a:c4": "Espressif", "30:ae:a4": "Espressif", "48:3f:da": "Espressif",
	"60:01:94": "Espressif", "84:cc:a8": "Espressif", "a0:20:a6": "Espressif",
	"ac:67:b2": "Espressif", "b4:e6:2d": "Espressif", "c4:5b:be": "Espressif",
	"cc:50:e3": "Espressif", "dc:4f:22": "Espressif", "ec:fa:bc": "Espressif",
	"f4:cf:a2": "Espressif",
	"54:c4:15": "Hikvision 海康", "44:19:b6": "Hikvision 海康",
	"c0:56:e3": "Hikvision 海康", "18:68:cb": "Hikvision 海康",
	"3c:ef:8c": "Dahua 大华", "e0:50:8b": "Dahua 大华", "a0:bd:1d": "Dahua 大华",
	"00:17:88": "Signify/Philips Hue", "ec:1a:59": "Philips",
	"00:1a:11": "Google", "3c:5a:b4": "Google", "54:60:09": "Google",
	"f4:f5:d8": "Google", "a4:77:33": "Google",
	"00:fc:8b": "Amazon", "68:37:e9": "Amazon", "74:c2:46": "Amazon",
	"a0:02:dc": "Amazon", "84:d6:d0": "Amazon",
	"00:11:32": "Synology 群晖", "00:1c:ef": "Synology",
	"00:08:9b": "QNAP", "24:5e:be": "QNAP",
	"00:14:ee": "Western Digital", "00:90:a9": "Western Digital",
	"2c:44:fd": "Western Digital",
	"b0:34:95": "Apple", "0c:bc:9f": "Apple",

	// 打印机
	"00:01:e6": "HP Printer", "00:21:5a": "HP Printer",
	"00:00:85": "Canon", "00:05:8a": "Canon", "00:11:2f": "Canon",
	"00:04:23": "Intel",
	"00:00:48": "Epson", "00:26:ab": "Epson", "00:01:2e": "Epson",
	"00:80:92": "Brother", "00:1b:a9": "Brother", "4c:11:bf": "Brother",

	// 电视与影音
	"00:1a:6b": "Sony", "00:1d:ba": "Sony", "00:24:be": "Sony",
	"78:6c:1c": "Sony", "ac:9b:0a": "Sony",
	"00:1e:75": "LG", "00:26:1d": "LG", "10:68:3f": "LG", "cc:5d:4a": "LG",
	"00:22:9d": "Hisense 海信", "8c:71:f8": "Hisense",
	"00:0d:0b": "TCL", "64:6c:2c": "TCL",
	"00:19:db": "Skyworth 创维",
	"00:0c:53": "Xiaomi TV",
}

// isRandomMAC 判断是否为「本地管理地址」——也就是手机 / 笔记本开着的随机 MAC。
//
// 判据是 MAC 首字节的 U/L 位（0x02）：硬件出厂地址这一位恒为 0，
// 操作系统随机生成的隐私地址会把它置 1。这类地址根本不在 IEEE 注册库里，
// 查厂商必然落空，所以单独识别出来给一句人话解释。
func isRandomMAC(mac string) bool {
	m := normalizeMAC(mac)
	if m == "" {
		return false
	}
	parts := strings.Split(m, ":")
	if len(parts) != 6 {
		return false
	}
	var b byte
	if _, err := fmt.Sscanf(parts[0], "%02x", &b); err != nil {
		return false
	}
	return b&0x02 != 0
}

// lookupVendor 由 MAC 查厂商；MAC 非法或不在表内返回空串。
func lookupVendor(mac string) string {
	m := normalizeMAC(mac)
	if m == "" {
		return ""
	}
	parts := strings.Split(m, ":")
	if len(parts) != 6 {
		return ""
	}
	return ouiTable[parts[0]+":"+parts[1]+":"+parts[2]]
}
