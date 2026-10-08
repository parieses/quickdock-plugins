package sysutil

import (
	"context"
	"encoding/base64"
	"fmt"
	"os/exec"
	"syscall"
	"time"
	"unicode/utf16"
)

// Hide 让子进程（powershell / sc / net 等）不弹 CMD 窗口。
func Hide(cmd *exec.Cmd) *exec.Cmd {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= 0x08000000
	return cmd
}

// PowerShell 以 -EncodedCommand 方式执行脚本，避免命令行引号转义问题。
// 默认 30s 超时，防止脚本（WMI/CIM 查询）卡死导致插件永不回包、前端永远"采集中"。
func PowerShell(script string) (string, error) {
	return PowerShellTimeout(script, 30*time.Second)
}

// PowerShellTimeout 执行脚本并施加超时；超时或失败时返回错误而非永久阻塞。
func PowerShellTimeout(script string, timeout time.Duration) (string, error) {
	u16 := utf16.Encode([]rune(script))
	b := make([]byte, len(u16)*2)
	for i, v := range u16 {
		b[i*2] = byte(v)
		b[i*2+1] = byte(v >> 8)
	}
	b64 := base64.StdEncoding.EncodeToString(b)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := Hide(exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive",
		"-ExecutionPolicy", "Bypass", "-EncodedCommand", b64))
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("PowerShell 执行超时（%s）", timeout)
		}
		return "", err
	}
	// 去除 UTF-8 BOM
	if len(out) >= 3 && out[0] == 0xEF && out[1] == 0xBB && out[2] == 0xBF {
		out = out[3:]
	}
	return string(out), nil
}
