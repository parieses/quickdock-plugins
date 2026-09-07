package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	logMu          sync.Mutex
	logPath        string // 当前进程日志文件路径
	backendLogPath string // 暴露给前端的日志路径（与 logPath 一致）
	logF           *os.File
)

// initLog 初始化落盘日志：写入插件可执行文件同目录的 ocr-tool.log，
// 并把内置 OCR 脚本另存为 ocr-tool.script.ps1 便于核对实际执行的 PowerShell。
// 顺带扫一次上一次进程被强杀遗留的 qd-ocr-*.tmp 图，避免 %TEMP% 越积越多。
func initLog() {
	logPath = logFilePath()
	backendLogPath = logPath
	if logPath == "" {
		return
	}
	// 日志过大时轮转
	if fi, err := os.Stat(logPath); err == nil && fi.Size() > 4*1024*1024 {
		_ = os.Rename(logPath, logPath+".old")
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		logF = nil
		return
	}
	logF = f
	logf("===== OCR plugin process start %s =====", time.Now().Format("2006-01-02 15:04:05"))
	logf("exe=%s", exePath())
	logf("log=%s", logPath)
	logf("清扫历史 tmp: %d", cleanupStaleTmp())
}

// cleanupStaleTmp 扫描 os.TempDir() 删除上一进程异常退出遗留的 qd-ocr-* 文件。
// 正常路径下 runOcrTask defer 已清理；只在强杀/崩溃后才有遗留。
func cleanupStaleTmp() int {
	dir := os.TempDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "qd-ocr-") {
			if err := os.Remove(filepath.Join(dir, name)); err == nil {
				n++
			}
		}
	}
	return n
}

func exePath() string {
	if e, err := os.Executable(); err == nil {
		return e
	}
	return "(unknown)"
}

func logFilePath() string {
	if env := os.Getenv("OCR_LOG_DIR"); env != "" {
		return filepath.Join(env, "ocr-tool.log")
	}
	if e, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(e), "ocr-tool.log")
	}
	return filepath.Join(os.TempDir(), "ocr-tool.log")
}

// logf 写入一条带时间戳的日志（多字节安全，按 rune 截断）。
func logf(format string, args ...interface{}) {
	if logF == nil {
		return
	}
	logMu.Lock()
	defer logMu.Unlock()
	ts := time.Now().Format("15:04:05.000")
	fmt.Fprintf(logF, "[%s] %s\n", ts, fmt.Sprintf(format, args...))
	_ = logF.Sync()
}

// trunc 把过长的字符串按 rune 截断，避免日志刷屏。
func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + fmt.Sprintf("…(截断 +%d 字符)", len(r)-n)
}
