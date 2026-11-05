//go:build !windows

package main

// 非 Windows 平台的降级桩：插件清单声明 platforms=["windows"]，
// 这些实现只保证 go build ./... 在 Linux/macOS CI 上不会因为缺少符号而失败。

import "errors"

func decompressMAM([]byte) ([]byte, error) {
	return nil, errors.New("MAM 解压依赖 ntdll，仅 Windows 可用")
}

func devicePathToWin32(p string) string { return p }
