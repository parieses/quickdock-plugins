//go:build darwin || linux

package main

import (
	"fmt"
	"syscall"
)

type diskStats struct {
	Total    uint64
	Used     uint64
	Free     uint64
	UsagePct float64
}

func getDiskStats(path string) (*diskStats, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return nil, fmt.Errorf("获取磁盘信息失败: %v", err)
	}
	total := uint64(stat.Blocks) * uint64(stat.Bsize)
	free := uint64(stat.Bfree) * uint64(stat.Bsize)
	used := total - free
	var pct float64
	if total > 0 {
		pct = float64(used) / float64(total) * 100
	}
	return &diskStats{Total: total, Used: used, Free: free, UsagePct: pct}, nil
}
