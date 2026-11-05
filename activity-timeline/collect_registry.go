package main

// 注册表取证：USBSTOR 设备历史、UserAssist / BAM 执行痕迹、MRU 体检。
// 全部走只读 `reg query`，键的最后写入时间由 winapi_windows.go 的
// RegQueryInfoKeyW 取得（reg query 本身不提供时间戳）。

import (
	"encoding/binary"
	"fmt"
	"strings"
	"time"
)

func regQueryLines(key string) ([]string, error) {
	out, err := execOut(12*time.Second, "reg", "query", key, "/s")
	if err != nil {
		return nil, err
	}
	return strings.Split(strings.ReplaceAll(decodeConsoleBytes(out), "\r\n", "\n"), "\n"), nil
}

// parseRegLine 解析 "    ValueName    REG_SZ    value" 这类输出。
func parseRegLine(line string) (name, typ, val string, ok bool) {
	t := strings.TrimSpace(line)
	idx := strings.Index(t, "REG_")
	if idx < 0 {
		return "", "", "", false
	}
	name = strings.TrimSpace(t[:idx])
	rest := t[idx:]
	sp := strings.IndexAny(rest, " \t")
	if sp < 0 {
		return "", "", "", false
	}
	typ = rest[:sp]
	val = strings.TrimSpace(rest[sp:])
	return name, typ, val, name != ""
}

func lastPathSeg(p string) string {
	i := strings.LastIndexByte(p, '\\')
	if i < 0 {
		return p
	}
	return p[i+1:]
}

func parseUSBModelSeg(seg string) (vendor, product string) {
	parts := strings.Split(seg, "&")
	for _, p := range parts {
		low := strings.ToLower(p)
		if strings.HasPrefix(low, "ven_") {
			vendor = strings.ReplaceAll(p[4:], "_", " ")
		} else if strings.HasPrefix(low, "prod_") {
			product = strings.ReplaceAll(p[5:], "_", " ")
		}
	}
	return
}

func collectUSBRegistry(c CollectContext, emit func(TimelineEvent)) SourceStatus {
	lines, err := regQueryLines(`HKLM\SYSTEM\CurrentControlSet\Enum\USBSTOR`)
	if err != nil {
		return src("U 盘设备历史（USBSTOR）", 0, err, "")
	}
	friendly := map[string]string{}
	modelKeys := map[string]string{} // 型号键路径 -> 键值中的描述
	instanceKeys := []string{}
	cur := ""
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "HKEY_") {
			cur = strings.TrimSpace(line)
			seg := lastPathSeg(cur)
			low := strings.ToLower(seg)
			if strings.HasPrefix(low, "disk&ven_") {
				modelKeys[cur] = seg
			} else if strings.Contains(seg, "&") {
				instanceKeys = append(instanceKeys, cur)
			}
			continue
		}
		if cur == "" {
			continue
		}
		if name, typ, val, ok := parseRegLine(line); ok && name == "FriendlyName" && typ == "REG_SZ" {
			friendly[cur] = val
		}
	}

	count := 0
	for _, inst := range instanceKeys {
		seg := lastPathSeg(inst)
		serial := seg
		if i := strings.LastIndexByte(serial, '&'); i > 0 {
			serial = serial[:i]
		}
		parent := inst[:strings.LastIndexByte(inst, '\\')]
		modelSeg := modelKeys[parent]
		vendor, product := parseUSBModelSeg(modelSeg)
		name := friendly[parent]
		if name == "" {
			name = strings.TrimSpace(vendor + " " + product)
		}
		if name == "" {
			name = friendly[inst]
		}
		if name == "" {
			name = seg
		}
		t := regKeyLastWrite(inst)
		if t.IsZero() {
			continue
		}
		detail := fmt.Sprintf("设备 %s · 序列号 %s", truncStr(name, 90), truncStr(serial, 60))
		if vendor != "" || product != "" {
			detail += fmt.Sprintf(" · %s %s", vendor, product)
		}
		detail += " · 时间为注册表键最后写入时刻，近似最近一次连接"
		count++
		emit(ev(t, TypeDevice, LevelCorroborated, "U 盘接入记录（注册表历史）", detail,
			`HKLM\SYSTEM\CurrentControlSet\Enum\USBSTOR + 键 LastWriteTime`, inst))
	}
	st := src("U 盘设备历史（USBSTOR）", count, nil, "没有插过 USB 存储设备的记录")
	if count > 0 {
		st.Note = "设备即使已拔除仍会保留；接入 ≠ 拷贝过文件"
	}
	return st
}

func collectUserAssist(c CollectContext, emit func(TimelineEvent)) SourceStatus {
	lines, err := regQueryLines(`HKCU\Software\Microsoft\Windows\CurrentVersion\Explorer\UserAssist`)
	if err != nil {
		return src("程序运行痕迹（UserAssist）", 0, err, "")
	}
	count := 0
	cur := ""
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "HKEY_") {
			cur = strings.TrimSpace(line)
			continue
		}
		if !strings.HasSuffix(strings.ToLower(cur), `\count`) {
			continue
		}
		name, typ, val, ok := parseRegLine(line)
		if !ok || typ != "REG_BINARY" {
			continue
		}
		b := parseHexBytes(val)
		if len(b) < 16 {
			continue
		}
		path := rot13(name)
		if !strings.Contains(path, `\`) {
			continue
		}
		t := filetimeFromBytes(b, 8)
		if t.IsZero() || t.Year() < 2000 {
			continue
		}
		runs := binary.LittleEndian.Uint32(b[4:8])
		count++
		emit(ev(t, TypeProgram, LevelCorroborated, "程序运行（UserAssist）",
			fmt.Sprintf("%s · 累计启动 %d 次", truncStr(path, 180), runs),
			`HKCU\...\Explorer\UserAssist\{GUID}\Count`, "值名 ROT13 解码 · " + path))
	}
	return src("程序运行痕迹（UserAssist）", count, nil,
		"无 GUI 程序启动记录（可能已清理，或只用了命令行启动）")
}

func collectBAM(c CollectContext, emit func(TimelineEvent)) SourceStatus {
	lines, err := regQueryLines(`HKLM\SYSTEM\CurrentControlSet\Services\bam\State\UserSettings`)
	if err != nil {
		return src("程序最后执行（BAM/DAM）", 0, err, "")
	}
	count := 0
	cur := ""
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "HKEY_") {
			cur = strings.TrimSpace(line)
			continue
		}
		if cur == "" {
			continue
		}
		name, typ, val, ok := parseRegLine(line)
		if !ok || typ != "REG_BINARY" || !strings.Contains(name, `\`) {
			continue
		}
		b := parseHexBytes(val)
		if len(b) < 16 {
			continue
		}
		t := filetimeFromBytes(b, 8)
		if t.IsZero() || t.Year() < 2000 {
			continue
		}
		count++
		emit(ev(t, TypeProgram, LevelCorroborated, "程序最后执行（BAM/DAM）",
			truncStr(name, 180) + " · 按用户 SID 记录",
			`HKLM\SYSTEM\...\Services\bam\State\UserSettings`, cur+" · "+name))
	}
	return src("程序最后执行（BAM/DAM）", count, nil, "无 BAM 记录（需管理员权限读取 HKLM）")
}

func collectRecentDocsStatus(c CollectContext, emit func(TimelineEvent)) SourceStatus {
	keys := []struct {
		name, key string
	}{
		{"RecentDocs（最近文档）", `HKCU\Software\Microsoft\Windows\CurrentVersion\Explorer\RecentDocs`},
		{"OpenSavePidlMRU（打开/保存路径）", `HKCU\Software\Microsoft\Windows\CurrentVersion\Explorer\ComDlg32\OpenSavePidlMRU`},
		{"ShellBags（浏览过的文件夹）", `HKCU\Software\Microsoft\Windows\Shell\BagMRU`},
	}
	total := 0
	missing := []string{}
	parts := []string{}
	for _, k := range keys {
		lines, err := regQueryLines(k.key)
		if err != nil {
			missing = append(missing, k.name)
			continue
		}
		n := 0
		for _, line := range lines {
			if _, typ, _, ok := parseRegLine(line); ok && typ == "REG_BINARY" {
				n++
			}
		}
		total += n
		parts = append(parts, fmt.Sprintf("%s %d 条", k.name, n))
	}
	st := src("MRU / ShellBags 体检", total, nil, "无 MRU 记录")
	body := strings.Join(parts, " · ")
	if body == "" {
		body = "无可读 MRU 记录"
	}
	st.Note = body + " · MRU 无可靠时间戳，仅用于体检，不进时间轴"
	if len(missing) > 0 {
		st.Note += " · 读取失败：" + strings.Join(missing, "、")
		st.Level = "partial"
	}
	return st
}
