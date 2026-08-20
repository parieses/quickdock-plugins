//go:build windows

package main

import (
	"fmt"
	"path/filepath"
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

	var totalFree, totalSize, availFree uint64
	ptr, _, err := syscall.NewLazyDLL("kernel32.dll").NewProc("GetDiskFreeSpaceExW").Call(
		uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr(root))),
		uintptr(unsafe.Pointer(&totalFree)),
		uintptr(unsafe.Pointer(&totalSize)),
		uintptr(unsafe.Pointer(&availFree)),
	)
	if ptr == 0 || err != nil {
		return nil, fmt.Errorf("获取磁盘信息失败: %v", err)
	}

	used := totalSize - totalFree
	var pct float64
	if totalSize > 0 {
		pct = float64(used) / float64(totalSize) * 100
	}
	return &diskStats{Total: totalSize, Used: used, Free: totalFree, UsagePct: pct}, nil
}
