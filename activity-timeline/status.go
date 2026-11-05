package main

// 证据完整度体检：告诉用户「哪些问题这台机器根本答不了」，而不是只给一堆事件。

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

type StatusReport struct {
	Sources            []SourceStatus    `json:"sources"`
	Warnings           []string          `json:"warnings"`
	Audit              map[string]string `json:"audit"`
	Earliest           string            `json:"earliest"`
	Latest             string            `json:"latest"`
	MaxLogMB           int               `json:"maxLogMB"`
	LastAccessDisabled bool              `json:"lastAccessDisabled"`
	Advice             []string          `json:"advice"`
	Total              int               `json:"total"`
}

var maxSizeRe = regexp.MustCompile(`(?i)maxSize:\s*(\d+)`)
var lastAccessRe = regexp.MustCompile(`(?i)disablelastaccess\s*=\s*(\d+)`)
var systemTimeRe = regexp.MustCompile(`SystemTime='([^']+)'`)

func auditSetting(out string, keys ...string) string {
	lines := strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n")
	for _, line := range lines {
		low := strings.ToLower(line)
		hit := false
		for _, k := range keys {
			if strings.Contains(low, strings.ToLower(k)) {
				hit = true
				break
			}
		}
		if !hit {
			continue
		}
		switch {
		case strings.Contains(low, "no auditing") || strings.Contains(line, "无审核"):
			return "未启用"
		case strings.Contains(low, "success and failure") || strings.Contains(line, "成功和失败"):
			return "成功+失败"
		case strings.Contains(low, "success") || strings.Contains(line, "成功"):
			return "仅成功"
		case strings.Contains(low, "failure") || strings.Contains(line, "失败"):
			return "仅失败"
		}
	}
	return "未知"
}

func logBoundary(oldest bool) string {
	args := []string{"qe", "Security", "/f:xml", "/c:1"}
	if oldest {
		args = append(args, "/rd:false")
	} else {
		args = append(args, "/rd:true")
	}
	out, err := execOut(8*time.Second, "wevtutil", args...)
	if err != nil {
		return ""
	}
	m := systemTimeRe.FindSubmatch(out)
	if len(m) < 2 {
		return ""
	}
	t := parseEventTime(string(m[1]))
	if t.IsZero() {
		return ""
	}
	return t.Local().Format("2006-01-02 15:04")
}

func auditAdvice() []string {
	return []string{
		`auditpol /set /subcategory:"Other Logon/Logoff Events" /success:enable /failure:enable`,
		`auditpol /set /subcategory:"Logon" /success:enable /failure:enable`,
		`auditpol /set /subcategory:"Process Creation" /success:enable`,
		`组策略 → 管理模板 → 系统 → 审核进程创建 → 勾选「在创建事件中包含命令行」`,
		`auditpol /set /subcategory:"File System" /success:enable`,
		`wevtutil sl Security /ms:104857600`,
		`安装 Sysmon 后重点看 Event ID 1 / 11 / 15 / 23 / 26`,
	}
}

func buildStatus(c CollectContext, events []TimelineEvent, sources []SourceStatus) StatusReport {
	r := StatusReport{
		Sources: sources,
		Audit:   map[string]string{},
		Advice:  auditAdvice(),
		Total:   len(events),
	}

	if out, err := execOut(10*time.Second, "auditpol", "/get", "/category:*"); err == nil {
		s := decodeConsoleBytes(out)
		r.Audit["登录 Logon"] = auditSetting(s, "Logon")
		r.Audit["锁屏/解锁 Other Logon/Logoff"] = auditSetting(s, "Other Logon/Logoff", "其他登录/注销")
		r.Audit["进程创建 Process Creation"] = auditSetting(s, "Process Creation", "进程创建")
		r.Audit["文件系统 File System"] = auditSetting(s, "File System", "文件系统")
		r.Audit["对象访问 Object Access"] = auditSetting(s, "Object Access", "对象访问")
	} else {
		r.Warnings = append(r.Warnings, "无法读取审核策略（auditpol 需要管理员权限）："+shortErr(err))
	}

	if out, err := execOut(8*time.Second, "wevtutil", "gl", "Security"); err == nil {
		if m := maxSizeRe.FindSubmatch(out); len(m) >= 2 {
			if n, e := strconv.ParseInt(string(m[1]), 10, 64); e == nil {
				r.MaxLogMB = int(n / (1024 * 1024))
			}
		}
	}
	r.Earliest = logBoundary(true)
	r.Latest = logBoundary(false)

	if out, err := execOut(8*time.Second, "fsutil", "behavior", "query", "disablelastaccess"); err == nil {
		if m := lastAccessRe.FindSubmatch([]byte(decodeConsoleBytes(out))); len(m) >= 2 {
			r.LastAccessDisabled = string(m[1]) == "1"
		}
	}

	w := []string{}
	needAdmin := false
	for _, s := range sources {
		if s.Available {
			continue
		}
		low := strings.ToLower(s.Note)
		if strings.Contains(low, "denied") || strings.Contains(s.Note, "拒绝") ||
			strings.Contains(s.Note, "失败") {
			needAdmin = true
		}
	}
	if needAdmin {
		w = append(w, "部分证据源读取失败：Security 日志 / 注册表 / Prefetch 需要管理员权限，请以管理员身份运行 QuickDock 后重试")
	}
	if v := r.Audit["锁屏/解锁 Other Logon/Logoff"]; v == "未启用" || v == "未知" {
		w = append(w, "4800/4801 锁屏·解锁事件默认不记录；4624 LogonType=7（解锁成功）仍是默认可用的证据")
	}
	if v := r.Audit["进程创建 Process Creation"]; v == "未启用" || v == "未知" {
		w = append(w, "4688 进程创建审核未开启，程序运行只能靠 Prefetch / UserAssist / BAM 等残留痕迹还原")
	}
	if v := r.Audit["文件系统 File System"]; v == "未启用" || v == "未知" {
		w = append(w, "4663 文件访问审计未开启：复制、移动了哪个文件默认查不到，只能靠 USN/回收站间接推断")
	}
	if r.LastAccessDisabled {
		w = append(w, "NTFS LastAccessTime 默认不更新，不能拿文件的「访问时间」当证据")
	}
	if r.MaxLogMB > 0 && r.MaxLogMB <= 20 {
		w = append(w, "Security 日志上限约 "+strconv.Itoa(r.MaxLogMB)+" MB，繁忙机器几天就会滚动覆盖旧记录")
	}
	tamper := 0
	for _, e := range events {
		if e.Type == TypeTamper {
			tamper++
		}
	}
	if tamper > 0 {
		w = append(w, "检测到 "+strconv.Itoa(tamper)+" 条反取证事件（日志被清空 / 审计策略被改），时间线可能不完整")
	}
	w = append(w, "所有需开启的审计都只记录开启之后的行为，无法补录过去")
	w = append(w, "插了 U 盘 ≠ 拷了文件；打开过文件 ≠ 读过内容 ≠ 外传；标注「推断」的条目不能当确证")
	r.Warnings = append(w, r.Warnings...)
	return r
}
