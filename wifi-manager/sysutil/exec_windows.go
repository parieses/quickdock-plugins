//go:build windows

package sysutil

import (
	"errors"
	"os/exec"
	"strings"
	"syscall"
)

const (
	// createNoWindow = CREATE_NO_WINDOW：不创建控制台，从根上杜绝黑框。
	// 优于 syscall.SysProcAttr{HideWindow:true}（只设 STARTF_USESHOWWINDOW/SW_HIDE）：
	// 后者仍会创建控制台，只是不显示，残留的 console 句柄会干扰管道读取。
	createNoWindow = 0x08000000
	// detachedProcess = DETACHED_PROCESS：子进程不继承父进程控制台（避免黑框与
	// 控制台 Ctrl 信号按进程组传播）。注意：它并不能让子进程脱离“作业(Job Object)”，
	// 父进程处于某作业时，作业关闭仍会连带杀掉子进程——必须与下面两个标志配合。
	detachedProcess = 0x00000008
	// createNewProcessGroup = CREATE_NEW_PROCESS_GROUP：让子进程成为新进程组的组长，
	// 父进程退出/被结束时，子进程不再随父进程组一起被终止。
	createNewProcessGroup = 0x00000200
	// createBreakawayFromJob = CREATE_BREAKAWAY_FROM_JOB：让子进程脱离父进程所属的作业
	// (Job Object)。当 QuickDock 自身运行在某个作业里（如 dev 运行时/某些启动器创建的作业），
	// 作业关闭会杀掉其中所有进程；此标志使第三方软件逃逸作业、独立存活。
	// 若父进程不在作业中 → 该标志被忽略，无副作用；
	// 若父进程在“不允许脱离”的作业中 → CreateProcess 会返回“拒绝访问”，此时由
	// StartDetached 自动去掉该标志重试（仅失去作业脱离能力，仍能正常启动）。
	createBreakawayFromJob = 0x01000000
)

// Hide 附加“隐藏控制台窗口”属性。
// 用 |= 合并而非整体覆盖：调用方可能已在 SysProcAttr 上手写了 CmdLine 等字段，
// 整体覆盖会静默丢掉这些字段。
func Hide(cmd *exec.Cmd) *exec.Cmd {
	attr(cmd).CreationFlags |= createNoWindow
	return cmd
}

// Detach 让子进程脱离父进程组与作业：父进程退出时子进程不会被连带杀掉。
// 组合 DETACHED_PROCESS + CREATE_NEW_PROCESS_GROUP + CREATE_BREAKAWAY_FROM_JOB。
func Detach(cmd *exec.Cmd) *exec.Cmd {
	a := attr(cmd)
	a.CreationFlags |= detachedProcess | createNewProcessGroup | createBreakawayFromJob
	return cmd
}

// StartDetached 以“隐藏 + 脱离父进程组/作业”的方式启动外部程序，并异步回收进程句柄
// （exec.Command.Start 之后若不 Wait，Windows 上内核句柄不会被释放，长期累积泄漏）。
func StartDetached(cmd *exec.Cmd) error {
	Hide(cmd)
	Detach(cmd)

	if err := cmd.Start(); err == nil {
		reap(cmd)
		return nil
	} else if !isAccessDenied(err) {
		return err
	}

	// 父进程处于不允许脱离的作业：去掉 BREAKAWAY 标志，用克隆的 Cmd 重试。
	clone := *cmd
	if sp := clone.SysProcAttr; sp != nil {
		sp2 := *sp
		sp2.CreationFlags &^= createBreakawayFromJob
		clone.SysProcAttr = &sp2
	}
	if err2 := clone.Start(); err2 != nil {
		return err2
	}
	reap(&clone)
	return nil
}

func reap(cmd *exec.Cmd) {
	go func() { _ = cmd.Wait() }()
}

// OpenDetached 以“系统默认方式”打开一个目标，并让被打开的进程独立于父进程存活。
func OpenDetached(target string, workingDir string) error {
	c := exec.Command("explorer.exe", target)
	if workingDir != "" {
		c.Dir = workingDir
	}
	Hide(c)
	if err := c.Start(); err != nil {
		return err
	}
	reap(c)
	return nil
}

// isAccessDenied 判断错误是否为 Windows “拒绝访问”(errno 5)。
func isAccessDenied(err error) bool {
	if err == nil {
		return false
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno == 5
	}
	return strings.Contains(err.Error(), "Access is denied")
}

func attr(cmd *exec.Cmd) *syscall.SysProcAttr {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	return cmd.SysProcAttr
}
