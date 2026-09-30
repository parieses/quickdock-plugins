package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

func handleHostsCommand(id int64, cmd string, input map[string]interface{}) {
	switch cmd {
	case "hosts-list":
		hostsList(id)
	case "hosts-toggle":
		hostsToggle(id, input)
	case "hosts-add":
		hostsAdd(id, input)
	case "hosts-save":
		hostsSave(id, input)
	default:
		respondError(id, -32601, "unknown hosts command: "+cmd)
	}
}

func hostsPath() string {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows` // SystemRoot 未设置时的兜底，避免拼出相对路径误读写
	}
	return filepath.Join(root, "System32", "drivers", "etc", "hosts")
}

// uncommentHostsLine 移除一行动态 hosts 条目最前面的单个 '#' 及其后紧跟的空白，
// 恢复为可用条目。只删第一个 '#'（+ 紧随的空格/制表符），保留行内其余内容原样，
// 避免用 strings.TrimLeft(line, "# \t") 把行首有含义的 '#' 也一并剥掉。
func uncommentHostsLine(line string) string {
	s := strings.TrimLeft(line, " \t") // 去掉行首仅空白
	if strings.HasPrefix(s, "#") {
		s = s[1:] // 去掉第一个 '#'
	}
	return strings.TrimLeft(s, " \t") // 去掉 '#' 后的空白
}

// writeHostsFile 以"备份 + 原子替换"的方式写回 hosts（系统关键文件，禁止裸 os.WriteFile）。
// 流程：先把现有内容备份成 hosts.bak，再写入同目录临时文件、sync 落盘，最后 os.Rename 原子覆盖。
// 这样即使中途崩溃/断电，也不会把 hosts 写成半截损坏内容。
func writeHostsFile(path, content string) error {
	// 1. 备份原文件（尽力而为，失败不阻断——首次可能不存在 .bak 目录权限等）
	if data, rerr := os.ReadFile(path); rerr == nil {
		_ = os.WriteFile(path+".bak", data, 0644)
	}
	// 2. 同目录临时文件，保证 rename 在同一卷内是原子操作
	tmp := path + ".qdtmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	if _, err = f.WriteString(content); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err = f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	// 3. 原子替换
	if err = os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

type HostsEntry struct {
	Line    int    `json:"line"`
	IP      string `json:"ip"`
	Host    string `json:"host"`
	Enabled bool   `json:"enabled"`
	Comment string `json:"comment,omitempty"`
}

func hostsList(id int64) {
	path := hostsPath()
	data, err := os.ReadFile(path)
	if err != nil {
		respondError(id, -1, "读取 hosts 文件失败: "+err.Error())
		return
	}

	lines := strings.Split(string(data), "\n")
	var entries []HostsEntry
	var rawLines []string

	re := regexp.MustCompile(`^\s*(#?)\s*(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}|[0-9a-fA-F]*:[0-9a-fA-F:]*)\s+(\S+)`)

	for i, line := range lines {
		rawLines = append(rawLines, line)
		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimSpace(line)

		if trimmed == "" || strings.HasPrefix(trimmed, "#") && !re.MatchString(trimmed) {
			continue
		}

		m := re.FindStringSubmatch(trimmed)
		if len(m) >= 4 {
			entry := HostsEntry{
				Line:    i,
				IP:      m[2],
				Host:    m[3],
				Enabled: m[1] != "#",
			}
			if m[1] == "#" {
				entry.Comment = "disabled"
			}
			entries = append(entries, entry)
		}
	}

	respond(id, map[string]interface{}{
		"path":    path,
		"entries": entries,
		"raw":     strings.Join(rawLines, "\n"),
	})
}

func hostsToggle(id int64, input map[string]interface{}) {
	lv, ok := input["line"]
	if !ok {
		respondError(id, -1, "缺少 line 参数")
		return
	}
	lineIdx, _ := lv.(float64)

	path := hostsPath()
	data, err := os.ReadFile(path)
	if err != nil {
		respondError(id, -1, "读取 hosts 文件失败: "+err.Error())
		return
	}

	lines := strings.Split(string(data), "\n")
	idx := int(lineIdx)
	if idx < 0 || idx >= len(lines) {
		respondError(id, -1, fmt.Sprintf("行号 %d 超出范围", idx))
		return
	}

	line := lines[idx]
	trimmed := strings.TrimSpace(line)

	if strings.HasPrefix(trimmed, "#") {
		lines[idx] = uncommentHostsLine(line)
	} else {
		lines[idx] = "# " + line
	}

	result := strings.Join(lines, "\n")
	if err := writeHostsFile(path, result); err != nil {
		respondError(id, -1, "写入 hosts 文件失败: "+err.Error())
		return
	}

	respond(id, map[string]interface{}{
		"success": true,
		"line":    idx,
	})
}

func hostsAdd(id int64, input map[string]interface{}) {
	ip, _ := input["ip"].(string)
	host, _ := input["host"].(string)

	if ip == "" || host == "" {
		respondError(id, -1, "需要 ip 和 host 参数")
		return
	}

	// 输入验证：IP 必须是合法 IP 地址
	if net.ParseIP(ip) == nil {
		respondError(id, -1, "无效的 IP 地址: "+ip)
		return
	}
	// host 不能包含换行/制表符等注入字符
	if strings.ContainsAny(host, "\n\r\t#|&") || strings.HasPrefix(host, "#") {
		respondError(id, -1, "host 包含非法字符")
		return
	}
	// 拒绝本地回环/广播地址等异常 DNS 指向
	parsedIP := net.ParseIP(ip)
	if parsedIP.IsUnspecified() || parsedIP.IsMulticast() {
		respondError(id, -1, "拒绝操作：不允许的 IP 类型")
		return
	}

	path := hostsPath()
	data, err := os.ReadFile(path)
	if err != nil {
		respondError(id, -1, "读取 hosts 文件失败: "+err.Error())
		return
	}
	existing := string(data)
	nl := "\n"
	if strings.Contains(existing, "\r\n") {
		nl = "\r\n" // 保持与原文件一致的 Windows 行尾，避免混排 CRLF/LF
	}
	if existing != "" && !strings.HasSuffix(existing, "\n") {
		existing += nl // 追加前先确保原文件以换行结尾
	}
	existing += ip + "\t" + host + nl

	if err := writeHostsFile(path, existing); err != nil {
		respondError(id, -1, "写入 hosts 文件失败: "+err.Error())
		return
	}

	respond(id, map[string]interface{}{"success": true, "ip": ip, "host": host})
}

func hostsSave(id int64, input map[string]interface{}) {
	entriesRaw, ok := input["entries"].([]interface{})
	if !ok {
		respondError(id, -1, "缺少 entries 参数")
		return
	}

	path := hostsPath()
	// 读取原始 hosts 文件
	data, err := os.ReadFile(path)
	if err != nil {
		respondError(id, -1, "读取 hosts 文件失败: "+err.Error())
		return
	}
	lines := strings.Split(string(data), "\n")

	// 遍历 entries 更新 hosts 文件的行状态
	for _, raw := range entriesRaw {
		entry, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		lineFloat, _ := entry["line"].(float64)
		lineIdx := int(lineFloat)
		enabled, _ := entry["enabled"].(bool)
		if lineIdx < 0 || lineIdx >= len(lines) {
			continue
		}
		trimmed := strings.TrimSpace(lines[lineIdx])
		if enabled && strings.HasPrefix(trimmed, "#") {
			lines[lineIdx] = uncommentHostsLine(lines[lineIdx])
		} else if !enabled && !strings.HasPrefix(trimmed, "#") {
			lines[lineIdx] = "# " + lines[lineIdx]
		}
	}

	result := strings.Join(lines, "\n")
	if err := writeHostsFile(path, result); err != nil {
		respondError(id, -1, "写入 hosts 文件失败: "+err.Error())
		return
	}

	respond(id, map[string]interface{}{"success": true})
}
