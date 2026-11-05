//go:build windows

package main

// Win10/11 的 .pf 外层是 MAM 容器：
//   0x00 "MAM" + 1 字节算法标识（0x04 = Xpress-Huffman；最高位 0x80 表示后面多 4 字节 CRC32）
//   0x04 解压后大小（uint32 LE）
//   0x08（有校验和时 0x0C）起是 Xpress-Huffman 压缩流
// 用系统自带的 ntdll!RtlDecompressBufferEx 解开，避免引入第三方依赖或自己实现 Huffman。

import (
	"encoding/binary"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"syscall"
	"unsafe"
)

const compressionFormatXpressHuff = 4 // COMPRESSION_FORMAT_XPRESS_HUFF | COMPRESSION_ENGINE_STANDARD

var (
	pfNtdll       = syscall.NewLazyDLL("ntdll.dll")
	pfKernel32    = syscall.NewLazyDLL("kernel32.dll")
	procGetWS     = pfNtdll.NewProc("RtlGetCompressionWorkSpaceSize")
	procDecompEx  = pfNtdll.NewProc("RtlDecompressBufferEx")
	procDosDevice = pfKernel32.NewProc("QueryDosDeviceW")
)

func decompressMAM(b []byte) ([]byte, error) {
	if len(b) < 8 || string(b[:3]) != "MAM" {
		return nil, errors.New("不是 MAM 容器")
	}
	algo := b[3]
	if algo&0x7f != compressionFormatXpressHuff {
		return nil, fmt.Errorf("未知压缩算法 0x%02x", algo)
	}
	want := binary.LittleEndian.Uint32(b[4:8])
	if want == 0 || want > pfMaxBytes {
		return nil, fmt.Errorf("解压后大小异常: %d", want)
	}
	off := 8
	if algo&0x80 != 0 {
		off = 12 // 带 CRC32 校验和（SuperFetch 系文件常见）
	}
	if off >= len(b) {
		return nil, errors.New("压缩流为空")
	}

	var wsSize, fragSize uint32
	r, _, _ := procGetWS.Call(uintptr(compressionFormatXpressHuff),
		uintptr(unsafe.Pointer(&wsSize)), uintptr(unsafe.Pointer(&fragSize)))
	if r != 0 {
		return nil, fmt.Errorf("RtlGetCompressionWorkSpaceSize=0x%x", r)
	}
	if wsSize == 0 {
		wsSize = 1
	}
	workspace := make([]byte, wsSize)
	out := make([]byte, want)
	var final uint32
	r, _, _ = procDecompEx.Call(
		uintptr(compressionFormatXpressHuff),
		uintptr(unsafe.Pointer(&out[0])), uintptr(len(out)),
		uintptr(unsafe.Pointer(&b[off])), uintptr(len(b)-off),
		uintptr(unsafe.Pointer(&final)),
		uintptr(unsafe.Pointer(&workspace[0])))
	runtime.KeepAlive(out)
	runtime.KeepAlive(b)
	runtime.KeepAlive(workspace)
	if r != 0 {
		return nil, fmt.Errorf("RtlDecompressBufferEx=0x%x", r)
	}
	if final == 0 || int(final) > len(out) {
		return nil, errors.New("解压结果为空")
	}
	return out[:final], nil
}

// devicePathToWin32 把 pf 字符串区里的 \DEVICE\HARDDISKVOLUMEn\... 转成 C:\...。
// \Volume{GUID}\... 无法用 QueryDosDevice 映射，原样保留（仍是有效的取证信息）。
func devicePathToWin32(p string) string {
	up := strings.ToUpper(p)
	for d := byte('A'); d <= 'Z'; d++ {
		dev := queryDosDevice(string(d) + ":")
		if dev == "" {
			continue
		}
		if strings.HasPrefix(up, strings.ToUpper(dev)+`\`) {
			return string(d) + `:` + p[len(dev):]
		}
	}
	return p
}

func queryDosDevice(name string) string {
	ptr, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return ""
	}
	buf := make([]uint16, 1024)
	n, _, _ := procDosDevice.Call(uintptr(unsafe.Pointer(ptr)),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n == 0 {
		return ""
	}
	return strings.TrimRight(syscall.UTF16ToString(buf[:n]), "\x00 ")
}
