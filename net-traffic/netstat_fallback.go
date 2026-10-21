//go:build !windows

package main

// 非 Windows 平台的空实现：本插件读的是 Windows 的 iphlpapi，
// 其它平台没有等价接口，插件要能编译（CI 按 platforms 只打 windows），
// 但不能假装在工作——快照会带 supported=false，前端据此显示明确提示。

import "errors"

func platformSupported() bool { return false }

func collectIfaces() ([]rawIface, error) {
	return nil, errors.New("仅支持 Windows")
}

func collectConns() ([]rawConn, error) {
	return nil, errors.New("仅支持 Windows")
}

func procNames() map[uint32]string { return nil }
