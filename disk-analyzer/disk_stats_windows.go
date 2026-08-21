//go:build windows

package main

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"unsafe"
)

type diskStats struct {
	Total    uint64
	Used     uint64
	Free     uint64
	UsagePct float64
}

func getDiskStats(path string) (*diskStats, error) {
	root := filepath.VolumeName(path)
	if root == "" {
		root = "C:\\"
	}

	var callerFree, totalSize, totalFree uint64
	// 注意：LazyProc.Call 返回的 err 是 syscall.Errno(0)，成功时同样非 nil（坑！），
	// 因此只能以 BOOL 返回值 ptr 是否为 0 判断是否失败，绝不能再判 err != nil。
	// 参数语义：
	//   arg2 = lpFreeBytesAvailableToCaller（调用者可用，受配额影响）
	//   arg3 = lpTotalNumberOfBytes      （盘总容量）
	//   arg4 = lpTotalNumberOfFreeBytes  （真实剩余，用于已用/剩余计算）
	ptr, _, _ := syscall.NewLazyDLL("kernel32.dll").NewProc("GetDiskFreeSpaceExW").Call(
		uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr(root))),
		uintptr(unsafe.Pointer(&callerFree)),
		uintptr(unsafe.Pointer(&totalSize)),
		uintptr(unsafe.Pointer(&totalFree)),
	)
	if ptr == 0 {
		return nil, fmt.Errorf("获取磁盘信息失败: GetDiskFreeSpaceExW 返回 0")
	}

	used := totalSize - totalFree
	var pct float64
	if totalSize > 0 {
		pct = float64(used) / float64(totalSize) * 100
	}
	return &diskStats{Total: totalSize, Used: used, Free: totalFree, UsagePct: pct}, nil
}

// listDrives 枚举本机所有可访问的本地盘符（固定盘 / 可移动盘 / RAM 盘）。
// 体积来自 getDiskStats（GetDiskFreeSpaceExW），瞬时返回，不遍历目录。
func listDrives() ([]driveInfo, error) {
	buf := make([]uint16, 256)
	r, _, err := syscall.NewLazyDLL("kernel32.dll").NewProc("GetLogicalDriveStringsW").Call(
		uintptr(len(buf)), uintptr(unsafe.Pointer(&buf[0])),
	)
	if r == 0 {
		return nil, fmt.Errorf("枚举盘符失败: %v", err)
	}
	var drives []driveInfo
	n := int(r)
	for i := 0; i < n; {
		if buf[i] == 0 {
			i++
			continue
		}
		j := i
		for j < n && buf[j] != 0 {
			j++
		}
		drive := syscall.UTF16ToString(buf[i:j]) // e.g. "C:\\"
		i = j + 1

		dt, _, _ := syscall.NewLazyDLL("kernel32.dll").NewProc("GetDriveTypeW").Call(
			uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr(drive))),
		)
		typ := uint32(dt)
		// 只纳入本地可访问盘，跳过光盘 / 网络盘 / 未就绪盘
		// DRIVE_FIXED=3, DRIVE_REMOVABLE=2, DRIVE_RAMDISK=6（syscall 包未导出这些常量）
		if typ != 3 && typ != 2 && typ != 6 {
			continue
		}
		info, e := getDiskStats(drive)
		di := driveInfo{Letter: strings.TrimRight(drive, "\\"), Path: drive, Ready: e == nil}
		if e == nil {
			di.Total, di.Free, di.Used, di.UsagePct = info.Total, info.Free, info.Used, info.UsagePct
		}
		drives = append(drives, di)
	}
	sortDrives(drives)
	return drives, nil
}

// sortDrives 排序：C: 优先，其余按字母升序
func sortDrives(drives []driveInfo) {
	sort.Slice(drives, func(a, b int) bool {
		if drives[a].Letter == "C:" {
			return true
		}
		if drives[b].Letter == "C:" {
			return false
		}
		return drives[a].Letter < drives[b].Letter
	})
}
