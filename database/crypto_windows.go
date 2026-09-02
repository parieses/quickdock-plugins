//go:build windows

package main

import (
	"encoding/base64"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// DPAPI 加解密：与主程序 internal/platform/crypto_windows.go 完全同格式
// （CryptProtectData + base64），因此插件自身存储的密文格式与主程序一致。

type dpapiBlob struct {
	cbData uint32
	pbData *byte
}

func blobFromBytes(b []byte) *dpapiBlob {
	if len(b) == 0 {
		return &dpapiBlob{}
	}
	return &dpapiBlob{cbData: uint32(len(b)), pbData: &b[0]}
}

func bytesFromBlob(b *dpapiBlob) []byte {
	if b.cbData == 0 || b.pbData == nil {
		return nil
	}
	return unsafe.Slice(b.pbData, int(b.cbData))
}

var (
	modCrypt32    = windows.NewLazySystemDLL("crypt32.dll")
	modKernel32   = windows.NewLazySystemDLL("kernel32.dll")
	procProtect   = modCrypt32.NewProc("CryptProtectData")
	procUnprot    = modCrypt32.NewProc("CryptUnprotectData")
	procLocalFree = modKernel32.NewProc("LocalFree")
)

func encryptSecret(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	in := blobFromBytes([]byte(plain))
	var out dpapiBlob
	r, _, err := procProtect.Call(
		uintptr(unsafe.Pointer(in)),
		0, 0, 0, 0, 0,
		uintptr(unsafe.Pointer(&out)),
	)
	if r == 0 {
		return "", fmt.Errorf("DPAPI 加密失败: %v", err)
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(out.pbData)))
	return base64.StdEncoding.EncodeToString(bytesFromBlob(&out)), nil
}

func decryptSecret(cipher string) (string, error) {
	if cipher == "" {
		return "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(cipher)
	if err != nil {
		return "", err
	}
	in := blobFromBytes(raw)
	var out dpapiBlob
	r, _, err := procUnprot.Call(
		uintptr(unsafe.Pointer(in)),
		0, 0, 0, 0, 0,
		uintptr(unsafe.Pointer(&out)),
	)
	if r == 0 {
		return "", fmt.Errorf("DPAPI 解密失败: %v", err)
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(out.pbData)))
	return string(bytesFromBlob(&out)), nil
}
