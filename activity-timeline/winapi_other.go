//go:build !windows

package main

// 非 Windows 平台的降级实现：插件清单声明 platforms=["windows"]，
// 这些桩只保证 go build ./... 在 Linux/macOS CI 上不会因为缺少符号而失败。

import (
	"os"
	"os/exec"
	"time"
)

func applyProcAttr(cmd *exec.Cmd) {}

func fileCreationTime(os.FileInfo) time.Time { return time.Time{} }

func regKeyLastWrite(string) time.Time { return time.Time{} }

func decodeConsoleBytes(b []byte) string { return string(b) }
