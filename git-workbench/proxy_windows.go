//go:build windows

package main

import (
	"net/url"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// systemProxyURL 读取 Windows 系统代理设置（国内访问 GitHub API 常需走代理）。
func systemProxyURL() *url.URL {
	k, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Internet Settings`, registry.QUERY_VALUE)
	if err != nil {
		return nil
	}
	defer k.Close()

	enable, _, err := k.GetIntegerValue("ProxyEnable")
	if err != nil || enable == 0 {
		return nil
	}
	srv, _, err := k.GetStringValue("ProxyServer")
	if err != nil || strings.TrimSpace(srv) == "" {
		return nil
	}
	for _, part := range strings.Split(srv, ";") {
		p := strings.TrimSpace(part)
		if i := strings.IndexByte(p, '='); i >= 0 {
			p = p[i+1:]
		}
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !strings.Contains(p, "://") {
			p = "http://" + p
		}
		if u, err := url.Parse(p); err == nil && u.Host != "" {
			return u
		}
	}
	return nil
}
