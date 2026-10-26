//go:build !windows

package main

// Unix 侧卷枚举：只报根卷与用户目录所在卷，够用即可。
// 介质类型从 /sys/block/<disk>/queue/rotational 读（0 = 闪存），
// 读不到（macOS、容器、权限受限）一律 unknown——不做猜测。

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func listVolumes() []Volume {
	seen := map[string]bool{}
	var vs []Volume

	add := func(mount string) {
		mount = filepath.Clean(mount)
		if seen[mount] {
			return
		}
		seen[mount] = true
		v := Volume{Path: mount, Name: filepath.Base(mount), Kind: "fixed"}
		var st syscall.Statfs_t
		if err := syscall.Statfs(mount, &st); err == nil {
			v.TotalBytes = uint64(st.Blocks) * uint64(st.Bsize)
			v.FreeBytes = uint64(st.Bavail) * uint64(st.Bsize)
		}
		v.FileSystem = fsNameOf(st)
		v.Media = rotationalMedia(mount)
		v.Writable = probeWritable(mount)
		if mount == "/" {
			v.Name = "/"
		}
		vs = append(vs, v)
	}

	add("/")
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		add(home)
	}
	return vs
}

func fsNameOf(st syscall.Statfs_t) string {
	// Statfs_t 的 Type 在 Linux 上是魔数；这里只认几个常见值，认不出就空着
	switch st.Type {
	case 0x5346414f:
		return "exfat"
	case 0x4d44:
		return "fat32"
	case 0x65735546:
		return "f2fs"
	case 0x9123683e:
		return "btrfs"
	case 0xef53:
		return "ext4"
	}
	return ""
}

// rotationalMedia 由挂载点反查块设备，再读 /sys/block/<disk>/queue/rotational。
func rotationalMedia(mount string) string {
	dev := deviceOf(mount)
	if dev == "" {
		return "unknown"
	}
	disk := strings.TrimRight(dev, "0123456789")
	if disk == "" {
		return "unknown"
	}
	b, err := os.ReadFile("/sys/block/" + disk + "/queue/rotational")
	if err != nil {
		return "unknown"
	}
	switch strings.TrimSpace(string(b)) {
	case "0":
		return "ssd"
	case "1":
		return "hdd"
	}
	return "unknown"
}

// deviceOf 从 /proc/mounts 找挂载点对应的源设备（如 /dev/sda1）。
func deviceOf(mount string) string {
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return ""
	}
	defer f.Close()
	best, bestLen := "", 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		src, mp := fields[0], fields[1]
		if !strings.HasPrefix(src, "/dev/") {
			continue
		}
		if (mount == mp) || strings.HasPrefix(mount, strings.TrimSuffix(mp, "/")+"/") {
			if len(mp) > bestLen {
				best, bestLen = src, len(mp)
			}
		}
	}
	return best
}

// volumeOf 取路径所属挂载点（最长前缀匹配）。
func volumeOf(path string) *Volume {
	if path == "" {
		return nil
	}
	p := filepath.Clean(path)
	best := (*Volume)(nil)
	bestLen := 0
	for _, v := range listVolumesSorted() {
		mp := filepath.Clean(v.Path)
		if p == mp || strings.HasPrefix(p, strings.TrimSuffix(mp, "/")+"/") {
			if len(mp) > bestLen {
				vv := v
				best, bestLen = &vv, len(mp)
			}
		}
	}
	return best
}
