package main

// 卷（分区）信息。介质类型是这个插件的关键前提：覆写在机械盘上有效，
// 在 SSD / NVMe 上大概率无效（FTL 磨损均衡会把新数据写到别的物理块），
// 判定不出就如实返回 unknown，UI 必须把「未知」也当成风险提示，不能默认乐观。

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// Volume 描述一个可写卷。
type Volume struct {
	Path       string `json:"path"`       // Windows: C:\ ；Unix: / 或挂载点
	Name       string `json:"name"`       // 卷标（没有就用路径）
	FileSystem string `json:"fs"`         // NTFS / FAT32 / exFAT / apfs …，取不到为空
	Kind       string `json:"kind"`       // fixed / removable / network / cdrom / ramdisk / unknown
	Media      string `json:"media"`      // ssd / hdd / unknown
	TotalBytes uint64 `json:"totalBytes"`
	FreeBytes  uint64 `json:"freeBytes"`
	Writable   bool   `json:"writable"` // 能否在卷上建临时文件（空闲空间覆写的前提）
}

// mediaText 介质的人类可读名。
func mediaText(m string) string {
	switch m {
	case "ssd":
		return "SSD / 闪存"
	case "hdd":
		return "机械硬盘"
	default:
		return "未知介质"
	}
}

// mediaRisk 介质对应的覆写有效性说明，直接给 UI 用。
func mediaRisk(m string) string {
	switch m {
	case "ssd":
		return "该卷是 SSD / 闪存：覆写会被 FTL 重映射到新物理块，旧数据大概率仍可读。要真正擦除请用厂商工具做 Secure Erase，或依赖全盘加密后丢弃密钥。"
	case "hdd":
		return "该卷是机械硬盘：覆写能有效破坏原有磁记录，1 遍已满足 NIST SP 800-88 Clear 级别。"
	default:
		return "无法判定介质类型：若为 SSD / U 盘 / 存储卡，覆写不一定能真正擦除旧数据。"
	}
}

// kindText 卷类型的人类可读名。
func kindText(k string) string {
	switch k {
	case "fixed":
		return "本地磁盘"
	case "removable":
		return "可移动设备"
	case "network":
		return "网络驱动器"
	case "cdrom":
		return "光盘"
	case "ramdisk":
		return "内存盘"
	default:
		return "未知类型"
	}
}

// listVolumesSorted 列出卷并按路径排序；只读类（光盘 / 网络盘）仍返回，由 UI 决定要不要禁用。
func listVolumesSorted() []Volume {
	vs := listVolumes()
	sort.Slice(vs, func(i, j int) bool { return vs[i].Path < vs[j].Path })
	return vs
}

// probeWritable 试探能否在 dir 里建文件。空闲空间覆写要在卷上写满，
// 先确认有写权限，免得跑了一半才发现 C:\ 根不给写。
func probeWritable(dir string) bool {
	name := dir
	if !strings.HasSuffix(name, string(os.PathSeparator)) {
		name += string(os.PathSeparator)
	}
	name += ".qd-write-probe.tmp"
	f, err := os.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return false
	}
	f.Close()
	os.Remove(name)
	return true
}

// humanBytes 1.4 GB 这种紧凑写法。
func humanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
