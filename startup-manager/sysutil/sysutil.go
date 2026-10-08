package sysutil

import (
	"os/exec"
	"syscall"
)

// Hide 让子进程（reg 等）不弹 CMD 窗口。
func Hide(cmd *exec.Cmd) *exec.Cmd {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= 0x08000000
	return cmd
}
