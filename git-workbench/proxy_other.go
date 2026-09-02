//go:build !windows

package main

import "net/url"

// systemProxyURL 非 Windows 平台不读注册表，交给环境变量（http_proxy / https_proxy）。
func systemProxyURL() *url.URL { return nil }
