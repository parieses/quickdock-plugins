//go:build darwin || linux

package sysutil

import "os/exec"

// Hide 非 Windows 平台没有控制台窗口概念，无操作。
func Hide(cmd *exec.Cmd) *exec.Cmd { return cmd }
