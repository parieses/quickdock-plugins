package sysutil

import (
	"os/exec"
	"syscall"
)

// Hide 让子进程不弹 CMD 窗口（breach-check 仅用于网络请求，保留以备扩展）。
func Hide(cmd *exec.Cmd) *exec.Cmd {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= 0x08000000
	return cmd
}
