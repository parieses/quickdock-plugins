package main

// 注册表枚举 + 卸载执行。

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"system-tools/sysutil"
)

// uninstallKeys 三处卸载信息根键：本机 64 位、本机 32 位（WOW6432Node）、当前用户。
var uninstallKeys = []string{
	`HKLM:\Software\Microsoft\Windows\CurrentVersion\Uninstall\*`,
	`HKLM:\Software\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall\*`,
	`HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\*`,
}

// msiRe 只认 MsiExec /X{GUID}（或 /I）这一种形态，是唯一允许插件代执行的形式。
var msiRe = regexp.MustCompile(`(?i)^\s*(?:"?[^"]*msiexec(?:\.exe)?"?|msiexec(?:\.exe)?)\s*/[xi]\s*(\{[0-9a-fA-F\-]{36}\})`)

type rawApp struct {
	DisplayName         string      `json:"DisplayName"`
	DisplayVersion      string      `json:"DisplayVersion"`
	Publisher           string      `json:"Publisher"`
	InstallDate         string      `json:"InstallDate"`
	EstimatedSize       interface{} `json:"EstimatedSize"`
	InstallLocation     string      `json:"InstallLocation"`
	UninstallString     string      `json:"UninstallString"`
	QuietUninstallString string     `json:"QuietUninstallString"`
	PSPath              string      `json:"PSPath"`
}

type appItem struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	Publisher   string `json:"publisher"`
	InstallDate string `json:"installDate"`
	SizeKB      uint64 `json:"sizeKB"`
	Location    string `json:"location"`
	Uninstall   string `json:"uninstall"`
	Scope       string `json:"scope"`
	Mode        string `json:"mode"` // msi = 可直接卸载 / manual = 需手动 / "" = 无卸载入口
}

type appList struct {
	Apps      []appItem `json:"apps"`
	Count     int       `json:"count"`
	TotalKB   uint64    `json:"totalKB"`
	ScannedAt string    `json:"scannedAt"`
	Error     string    `json:"error,omitempty"`
}

var (
	cacheMu sync.Mutex
	cache   *appList
)

// scanScript 末尾把 JSON 编成 Base64 再输出。
//
// 为什么绕这一道：Windows PowerShell 5.1 的 stdout 用的是本机代码页（中文环境 GBK），
// 直接取回 JSON 时中文软件名全是问号与乱字（实测 91 个软件里中文名几乎全军覆没）。
// 改 [Console]::OutputEncoding 只在部分场景生效，而 Base64(UTF-8) 与终端编码
// 完全无关，是唯一稳的做法。
const scanScript = `
$ErrorActionPreference = 'SilentlyContinue'
$paths = @('%s')
$items = @(Get-ItemProperty $paths | Where-Object {
    $_.DisplayName -and -not $_.SystemComponent -and -not $_.ParentKeyName -and -not $_.ReleaseType
})
$json = $items | Select-Object DisplayName, DisplayVersion, Publisher, InstallDate, EstimatedSize,
    InstallLocation, UninstallString, QuietUninstallString, PSPath |
    ConvertTo-Json -Compress -Depth 3
[Convert]::ToBase64String([System.Text.Encoding]::UTF8.GetBytes($json))
`

func handleList(id int64, input map[string]interface{}) {
	force := getBool(input, "refresh")
	cacheMu.Lock()
	if !force && cache != nil {
		c := *cache
		cacheMu.Unlock()
		respond(id, c)
		return
	}
	cacheMu.Unlock()

	apps, err := scanApps()
	now := time.Now()
	result := appList{
		Apps:      apps,
		Count:     len(apps),
		ScannedAt: now.Format("15:04:05"),
	}
	if err != nil {
		result.Error = err.Error()
		qdLogError("扫描已安装应用失败 err=%v", err)
	}
	for _, a := range apps {
		result.TotalKB += a.SizeKB
	}
	cacheMu.Lock()
	cache = &result
	cacheMu.Unlock()
	qdLogInfo("扫描完成 count=%d 总体积=%dMB", result.Count, result.TotalKB/1024)
	respond(id, result)
}

func scanApps() ([]appItem, error) {
	script := fmt.Sprintf(scanScript, strings.Join(uninstallKeys, "','"))
	out, err := sysutil.PowerShellTimeout(script, 60*time.Second)
	if err != nil {
		return nil, fmt.Errorf("读取注册表失败: %v", err)
	}
	decoded, berr := base64.StdEncoding.DecodeString(strings.TrimSpace(out))
	if berr != nil {
		return nil, fmt.Errorf("解析注册表数据失败: %v", berr)
	}
	out = string(decoded)
	var raws []rawApp
	if err := json.Unmarshal([]byte(out), &raws); err != nil {
		// 只装了一个软件时 ConvertTo-Json 给的是对象而不是数组
		var one rawApp
		if err2 := json.Unmarshal([]byte(out), &one); err2 == nil {
			raws = []rawApp{one}
		} else {
			return nil, fmt.Errorf("解析注册表数据失败: %v", err)
		}
	}

	apps := make([]appItem, 0, len(raws))
	for _, r := range raws {
		name := strings.TrimSpace(r.DisplayName)
		if name == "" {
			continue
		}
		uninstall := strings.TrimSpace(r.UninstallString)
		if uninstall == "" {
			uninstall = strings.TrimSpace(r.QuietUninstallString)
		}
		item := appItem{
			ID:          registryPath(r.PSPath),
			Name:        name,
			Version:     strings.TrimSpace(r.DisplayVersion),
			Publisher:   strings.TrimSpace(r.Publisher),
			InstallDate: strings.TrimSpace(r.InstallDate),
			SizeKB:      toUint64(r.EstimatedSize),
			Location:    strings.TrimSpace(r.InstallLocation),
			Uninstall:   uninstall,
			Scope:       scopeOf(r.PSPath),
			Mode:        modeOf(uninstall),
		}
		if item.ID == "" {
			// 没有 PSPath 就没法唯一定位，退化成名称 + 版本
			item.ID = name + "|" + item.Version
		}
		apps = append(apps, item)
	}
	sort.Slice(apps, func(i, j int) bool {
		return strings.ToLower(apps[i].Name) < strings.ToLower(apps[j].Name)
	})
	return apps, nil
}

func handleUninstall(id int64, input map[string]interface{}) {
	cmd := getStr(input, "uninstall")
	if cmd == "" {
		respondError(id, -32602, "缺少卸载命令")
		return
	}
	m := msiRe.FindStringSubmatch(cmd)
	if m == nil {
		respondError(id, -1, "该程序使用自带卸载器，插件不代执行（已提供复制命令与打开安装目录）")
		return
	}
	guid := m[1]
	qdLogInfo("卸载 MSI guid=%s", guid)
	if err := launchDetached("msiexec", "/X"+guid); err != nil {
		respondError(id, -1, "启动卸载程序失败: "+err.Error())
		return
	}
	respond(id, map[string]interface{}{"ok": true, "mode": "msi", "guid": guid})
}

func handleOpenDir(id int64, input map[string]interface{}) {
	dir := getStr(input, "path")
	if dir == "" {
		respondError(id, -32602, "缺少路径")
		return
	}
	if _, err := os.Stat(dir); err != nil {
		respondError(id, -1, "目录不存在或不可访问: "+dir)
		return
	}
	if err := launchDetached("explorer", dir); err != nil {
		respondError(id, -1, "打开目录失败: "+err.Error())
		return
	}
	respond(id, map[string]interface{}{"ok": true})
}

// handleOpenSettings 打开系统「已安装的应用」设置页。
// 自带卸载器的程序最终还是要回到系统面板里卸载，给个直达入口比让用户自己找快得多。
func handleOpenSettings(id int64) {
	if err := launchDetached("explorer", "ms-settings:installed-apps"); err != nil {
		respondError(id, -1, "打开系统设置失败: "+err.Error())
		return
	}
	respond(id, map[string]interface{}{"ok": true})
}

// launchDetached 启动后立刻返回：卸载程序往往要跑几十秒，插件不能等它
// （宿主 20s 超时，且等待期间插件 busy 会挡住其它命令）。
func launchDetached(name string, args ...string) error {
	cmd := sysutil.Hide(exec.Command(name, args...))
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				qdLogPanic("launchDetached wait", r)
			}
		}()
		_ = cmd.Wait()
	}()
	return nil
}

func modeOf(uninstall string) string {
	if uninstall == "" {
		return ""
	}
	if msiRe.MatchString(uninstall) {
		return "msi"
	}
	return "manual"
}

// registryPath 把 PowerShell 的 PSPath 还原成注册表路径。
// 形如 Microsoft.PowerShell.Core\Registry::HKEY_LOCAL_MACHINE\Software\...
func registryPath(psPath string) string {
	if psPath == "" {
		return ""
	}
	i := strings.Index(psPath, "::")
	if i >= 0 {
		return psPath[i+2:]
	}
	return psPath
}

func scopeOf(psPath string) string {
	switch {
	case strings.Contains(psPath, "WOW6432Node"):
		return "系统(32位)"
	case strings.Contains(psPath, "HKEY_CURRENT_USER"):
		return "当前用户"
	case strings.Contains(psPath, "HKEY_LOCAL_MACHINE"):
		return "所有用户"
	default:
		return ""
	}
}

// toUint64 注册表里的 EstimatedSize 可能是数字、字符串或 null（PowerShell 的
// JSON 转换不保证类型），统一兜成 uint64。
func toUint64(v interface{}) uint64 {
	switch n := v.(type) {
	case float64:
		if n < 0 {
			return 0
		}
		return uint64(n)
	case string:
		var out uint64
		for _, c := range n {
			if c < '0' || c > '9' {
				break
			}
			out = out*10 + uint64(c-'0')
		}
		return out
	default:
		return 0
	}
}
