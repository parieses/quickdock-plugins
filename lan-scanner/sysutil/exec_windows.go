//go:build windows

package sysutil

import (
	"os/exec"
	"syscall"
)

// Hide 隐藏子进程控制台窗口（GUI 父进程拉起 console 子程序时不闪黑框）。
func Hide(cmd *exec.Cmd) *exec.Cmd {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	return cmd
}
