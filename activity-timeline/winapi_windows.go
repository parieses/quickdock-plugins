//go:build windows

package main

// Windows 专有的只读辅助：注册表键最后写入时间、文件创建时间、隐藏子进程窗口。
// 不引入 golang.org/x/sys，直接走 advapi32，保持 go.mod 零依赖、离线可编译。

import (
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
	"unsafe"
)

const ftEpochDiff100ns = int64(116444736000000000)

var (
	advapi32     = syscall.NewLazyDLL("advapi32.dll")
	kernel32     = syscall.NewLazyDLL("kernel32.dll")
	procRegOpen  = advapi32.NewProc("RegOpenKeyExW")
	procRegInfo  = advapi32.NewProc("RegQueryInfoKeyW")
	procRegClose = advapi32.NewProc("RegCloseKey")
	procMBToWC   = kernel32.NewProc("MultiByteToWideChar")
)

// decodeConsoleBytes 把中文 Windows 控制台的 GBK/OEM 输出转成 UTF-8。
// wevtutil / reg / auditpol 的错误信息是本地代码页，直接当 UTF-8 读会全是乱码。
func decodeConsoleBytes(b []byte) string {
	if len(b) == 0 || utf8.Valid(b) {
		return string(b)
	}
	n, _, _ := procMBToWC.Call(0, 0, uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)), 0, 0)
	if n == 0 {
		return string(b)
	}
	wide := make([]uint16, n)
	_, _, _ = procMBToWC.Call(0, 0, uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)),
		uintptr(unsafe.Pointer(&wide[0])), uintptr(n))
	return syscall.UTF16ToString(wide)
}

func applyProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}

func filetimeTime(ft syscall.Filetime) time.Time {
	v := int64(uint64(ft.HighDateTime)<<32 | uint64(ft.LowDateTime))
	if v == 0 {
		return time.Time{}
	}
	return time.Unix(0, (v-ftEpochDiff100ns)*100).UTC()
}

func fileCreationTime(fi os.FileInfo) time.Time {
	if st, ok := fi.Sys().(*syscall.Win32FileAttributeData); ok {
		return filetimeTime(st.CreationTime)
	}
	return time.Time{}
}

func splitRegRoot(full string) (uintptr, string, bool) {
	full = strings.TrimSpace(full)
	i := strings.IndexByte(full, '\\')
	if i < 0 {
		return 0, "", false
	}
	root := strings.ToUpper(full[:i])
	sub := full[i+1:]
	var h uintptr
	switch root {
	case "HKEY_LOCAL_MACHINE", "HKLM":
		h = 0x80000002
	case "HKEY_CURRENT_USER", "HKCU":
		h = 0x80000001
	case "HKEY_USERS", "HKU":
		h = 0x80000003
	case "HKEY_CLASSES_ROOT", "HKCR":
		h = 0x80000000
	default:
		return 0, "", false
	}
	return h, sub, sub != ""
}

// regKeyLastWrite 取注册表键的 LastWriteTime（FILETIME, UTC）。
// USBSTOR / RecentDocs 等键没有独立时间戳字段，这是唯一能拿到「何时被写过」的办法。
func regKeyLastWrite(full string) time.Time {
	root, sub, ok := splitRegRoot(full)
	if !ok {
		return time.Time{}
	}
	ptr, err := syscall.UTF16PtrFromString(sub)
	if err != nil {
		return time.Time{}
	}
	var h syscall.Handle
	// KEY_READ | KEY_WOW64_64KEY
	r, _, _ := procRegOpen.Call(root, uintptr(unsafe.Pointer(ptr)), 0, 0x20119, uintptr(unsafe.Pointer(&h)))
	if r != 0 {
		return time.Time{}
	}
	defer procRegClose.Call(uintptr(h))
	var ft syscall.Filetime
	// RegQueryInfoKeyW(h, class, cchClass, reserved, subKeys, maxSubKey, maxClass,
	//                  values, maxValueName, maxValueLen, securityDesc, lastWriteTime)
	ret, _, _ := procRegInfo.Call(uintptr(h), 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		uintptr(unsafe.Pointer(&ft)))
	if ret != 0 {
		return time.Time{}
	}
	return filetimeTime(ft)
}
