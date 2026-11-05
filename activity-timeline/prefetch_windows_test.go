//go:build windows

package main

// 用 ntdll!RtlCompressBuffer 造一个真实的 Xpress-Huffman 流，包上 MAM 头，
// 验证 decompressMAM 的两种头布局（无校验 0x04 / 带 CRC 0x84）都能解开。

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"runtime"
	"testing"
	"time"
	"unsafe"
)

func compressXpressHuff(plain []byte) ([]byte, error) {
	proc := pfNtdll.NewProc("RtlCompressBuffer")
	var ws, frag uint32
	r, _, _ := procGetWS.Call(uintptr(compressionFormatXpressHuff),
		uintptr(unsafe.Pointer(&ws)), uintptr(unsafe.Pointer(&frag)))
	if r != 0 {
		return nil, fmt.Errorf("RtlGetCompressionWorkSpaceSize=0x%x", r)
	}
	if ws == 0 {
		ws = 1
	}
	workspace := make([]byte, ws)
	buf := make([]byte, len(plain)*2+4096)
	var final uint32
	r, _, _ = proc.Call(
		uintptr(compressionFormatXpressHuff),
		uintptr(unsafe.Pointer(&plain[0])), uintptr(len(plain)),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)),
		uintptr(4096),
		uintptr(unsafe.Pointer(&final)),
		uintptr(unsafe.Pointer(&workspace[0])))
	runtime.KeepAlive(plain)
	runtime.KeepAlive(buf)
	runtime.KeepAlive(workspace)
	if r != 0 {
		return nil, fmt.Errorf("RtlCompressBuffer=0x%x", r)
	}
	if final == 0 {
		return nil, fmt.Errorf("压缩结果为空")
	}
	return buf[:final], nil
}

func TestDecompressMAMRoundTrip(t *testing.T) {
	exe := `C:\Windows\Lenovo\ImController\PluginHost\Lenovo.Modern.ImController.PluginHost.exe`
	plain := makeSCCA("LENOVO.MODERN.IMCONTROLLER.PL", []string{exe},
		[]time.Time{time.Date(2026, 11, 3, 8, 5, 0, 0, time.UTC)})
	comp, err := compressXpressHuff(plain)
	if err != nil {
		t.Skipf("本机 RtlCompressBuffer 不可用（不影响纯解析路径）: %v", err)
	}

	for _, tc := range []struct {
		name  string
		algo  byte
		extra int
	}{
		{"无校验", 0x04, 0},
		{"带CRC", 0x84, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mam := make([]byte, 8+tc.extra+len(comp))
			copy(mam[:3], "MAM")
			mam[3] = tc.algo
			binary.LittleEndian.PutUint32(mam[4:8], uint32(len(plain)))
			copy(mam[8+tc.extra:], comp)
			got, err := decompressMAM(mam)
			if err != nil {
				t.Fatalf("解压失败: %v", err)
			}
			if !bytes.Equal(got, plain) {
				t.Fatalf("解压结果与原文不一致: got %d bytes, want %d", len(got), len(plain))
			}
			info := parseSCCA(got, "LENOVO.MODERN.IMCONTROLLER.PL")
			if !info.Parsed || info.FullPath == "" {
				t.Fatalf("解压后应能解析出完整路径: %+v", info)
			}
		})
	}
}

func TestDecompressMAMRejectsBadHeader(t *testing.T) {
	if _, err := decompressMAM([]byte("NOTMAM0\x00\x00\x00\x00")); err == nil {
		t.Error("非 MAM 容器应返回错误")
	}
	if _, err := decompressMAM([]byte{'M', 'A', 'M', 0x09, 0, 0, 0, 0}); err == nil {
		t.Error("未知压缩算法应返回错误")
	}
}
