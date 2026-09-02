//go:build !windows

package main

import "encoding/base64"

// 非 Windows 平台：DPAPI 不可用，与主程序 crypto_darwin.go 一致退化为 base64
// （非加密，仅避免明文落库）。

func encryptSecret(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	return base64.StdEncoding.EncodeToString([]byte(plain)), nil
}

func decryptSecret(cipher string) (string, error) {
	if cipher == "" {
		return "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(cipher)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
