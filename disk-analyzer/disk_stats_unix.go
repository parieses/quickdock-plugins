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

// listDrives 在类 Unix 平台返回根文件系统（"/"）。
// 体积来自 getDiskStats（Statfs），瞬时返回。
func listDrives() ([]driveInfo, error) {
	info, err := getDiskStats("/")
	di := driveInfo{Letter: "/", Path: "/", Ready: err == nil}
	if err == nil {
		di.Total, di.Free, di.Used, di.UsagePct = info.Total, info.Free, info.Used, info.UsagePct
	}
	return []driveInfo{di}, nil
}
