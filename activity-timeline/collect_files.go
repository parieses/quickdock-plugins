package main

// 文件系统侧痕迹：Prefetch、Recent LNK、Jump Lists、回收站 $I、SetupAPI 设备日志。
// 只读，不修改任何文件；所有时间都按本地时区归一化。
// Prefetch 的真实程序名 / 完整路径解析见 prefetch.go。

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func formatSize(n int64) string {
	if n <= 0 {
		return "0 B"
	}
	units := []string{"B", "KB", "MB", "GB", "TB"}
	v := float64(n)
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}

func collectPrefetch(c CollectContext, emit func(TimelineEvent)) SourceStatus {
	dir := filepath.Join(c.SystemRoot, "Prefetch")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return src("程序运行痕迹（Prefetch）", 0, err, "")
	}
	count := 0
	resolved := 0
	for _, de := range entries {
		if de.IsDir() || !strings.HasSuffix(strings.ToLower(de.Name()), ".pf") {
			continue
		}
		info, ierr := de.Info()
		if ierr != nil {
			continue
		}
		prefix, hash := splitPFName(de.Name())
		pf := readPrefetch(filepath.Join(dir, de.Name()), prefix)

		// 优先用 .pf 内部字符串区里的完整路径；读不到时再按前缀在本机反查 exe。
		exe, where := "", ""
		if pf.FullPath != "" {
			exe, where = pfBaseName(pf.FullPath), pf.FullPath
			resolved++
		} else if hit := lookupExeByPrefix(prefix); hit != "" {
			exe, where = filepath.Base(hit), hit
			pf.Note = appendNote(pf.Note, "完整路径来自本机 exe 反查")
		}

		first := fileCreationTime(info)
		runNote := ".pf 修改时间 ≈ 最近一次运行"
		if len(pf.RunTimes) > 0 {
			runNote = "最近运行 " + pf.RunTimes[0].Local().Format("2006-01-02 15:04:05") +
				"（.pf 内 FILETIME，精确）"
			if len(pf.RunTimes) > 1 {
				runNote += fmt.Sprintf(" · 另有 %d 次历史运行记录", len(pf.RunTimes)-1)
			}
			runNote += " · 事件时间仍取 .pf 修改时间（近似）"
		}

		var detail string
		if exe != "" {
			detail = "程序 " + exe
			detail += " · " + truncStr(where, 170)
			if !first.IsZero() {
				detail += " · 首次记录 " + first.Local().Format("2006-01-02 15:04")
			}
			detail += " · " + runNote
		} else {
			// 解析不出来时绝不把截断片段伪装成程序名，把命名规则讲清楚。
			detail = "程序名片段「" + truncStr(prefix, 40) + "…」不是程序全名：" +
				"Windows 把 .pf 文件名截断到 29 个字符（连 .EXE 都被截掉）"
			if hash != "" {
				detail += "；" + hash + " 是「完整 exe 路径」的 8 位哈希，同名程序从不同目录运行会生成不同 .pf"
			}
			if !first.IsZero() {
				detail += " · 首次记录 " + first.Local().Format("2006-01-02 15:04")
			}
			detail += " · " + runNote
			detail += " · 本机未反查到对应 exe（可能已卸载/被清理）"
			if pf.Note != "" {
				detail += "；pf 解析：" + pf.Note
			}
		}

		raw := de.Name()
		if hash != "" {
			raw += " · 路径哈希 " + hash
		}
		if where != "" {
			raw += " · 完整路径 " + where
		}
		if pf.ExeName != "" {
			raw += " · pf 头部名 " + pf.ExeName
		}
		count++
		emit(ev(info.ModTime(), TypeProgram, LevelCorroborated, "程序运行（Prefetch）", detail,
			`C:\Windows\Prefetch\*.pf`, raw))
	}
	st := src("程序运行痕迹（Prefetch）", count, nil, "无 Prefetch 记录（可能被清理或已禁用）")
	if count > 0 {
		st.Note = fmt.Sprintf("Prefetch 目录上限约 1024 个文件，旧记录会被挤掉；%d/%d 条解析出完整 exe 路径", resolved, count)
	}
	return st
}

// lnkTarget 从 .lnk 二进制里启发式提取目标路径（ASCII / UTF-16LE 两段扫描）。
// 不解析完整 [MS-SHLLINK]，够用于展示；解析不出来时前端回退显示 LNK 文件名。
func lnkTarget(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if len(b) > 256*1024 {
		b = b[:256*1024]
	}
	cands := []string{}
	start := -1
	for i := 0; i <= len(b); i++ {
		printable := i < len(b) && b[i] >= 32 && b[i] < 127
		if printable {
			if start < 0 {
				start = i
			}
		} else if start >= 0 {
			if i-start >= 5 {
				cands = append(cands, string(b[start:i]))
			}
			start = -1
		}
	}
	run := []rune{}
	for i := 0; i+1 < len(b); i += 2 {
		if b[i+1] == 0 && b[i] >= 32 && b[i] < 127 {
			run = append(run, rune(b[i]))
		} else {
			if len(run) >= 5 {
				cands = append(cands, string(run))
			}
			run = run[:0]
		}
	}
	if len(run) >= 5 {
		cands = append(cands, string(run))
	}
	best := ""
	for _, s := range cands {
		if !strings.Contains(s, `:\`) && !strings.HasPrefix(s, `\\`) {
			continue
		}
		if len(s) > len(best) {
			best = s
		}
	}
	return strings.TrimSpace(best)
}

func collectRecentLNK(c CollectContext, emit func(TimelineEvent)) SourceStatus {
	dir := filepath.Join(c.AppData, "Microsoft", "Windows", "Recent")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return src("最近打开的文件（Recent LNK）", 0, err, "")
	}
	count := 0
	for _, de := range entries {
		if de.IsDir() || !strings.HasSuffix(strings.ToLower(de.Name()), ".lnk") {
			continue
		}
		info, ierr := de.Info()
		if ierr != nil {
			continue
		}
		full := filepath.Join(dir, de.Name())
		target := lnkTarget(full)
		label := strings.TrimSuffix(de.Name(), ".lnk")
		if target == "" {
			target = label
		}
		detail := target + " · LNK 内含卷序列号，可把文件关联到具体 U 盘"
		count++
		emit(ev(info.ModTime(), TypeFile, LevelCorroborated, "打开过文件（Recent）", detail,
			`%APPDATA%\Microsoft\Windows\Recent\*.lnk`, de.Name()+" · "+target))
	}
	return src("最近打开的文件（Recent LNK）", count, nil,
		"无最近文件记录（可能被「清除最近使用的项目」清掉）")
}

func collectJumpLists(c CollectContext, emit func(TimelineEvent)) SourceStatus {
	base := filepath.Join(c.AppData, "Microsoft", "Windows", "Recent")
	count := 0
	for _, sub := range []string{"AutomaticDestinations", "CustomDestinations"} {
		entries, err := os.ReadDir(filepath.Join(base, sub))
		if err != nil {
			continue
		}
		for _, de := range entries {
			if de.IsDir() || count >= 60 {
				continue
			}
			info, ierr := de.Info()
			if ierr != nil {
				continue
			}
			count++
			emit(ev(info.ModTime(), TypeFile, LevelInferred, "跳转列表更新（推断）",
				"应用 AppID " + de.Name() + " 的最近文档列表被写入 · 仅表示列表更新，不等于打开过某个文件",
				`%APPDATA%\...\Recent\`+sub, de.Name()))
		}
	}
	st := src("跳转列表 Jump Lists", count, nil, "无跳转列表记录")
	if count > 0 {
		st.Note = "复合文档内的逐条访问时间未解析，只取文件更新时间，均为推断级"
	}
	return st
}

func collectRecycleBin(c CollectContext, emit func(TimelineEvent)) SourceStatus {
	count := 0
	hadDir := false
	for drive := byte('A'); drive <= byte('Z'); drive++ {
		root := string(drive) + `:\$Recycle.Bin`
		if st, err := os.Stat(root); err != nil || !st.IsDir() {
			continue
		}
		hadDir = true
		sids, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, sid := range sids {
			if !sid.IsDir() {
				continue
			}
			items, err := os.ReadDir(filepath.Join(root, sid.Name()))
			if err != nil {
				continue
			}
			for _, it := range items {
				if !strings.HasPrefix(it.Name(), "$I") || count >= 500 {
					continue
				}
				path := filepath.Join(root, sid.Name(), it.Name())
				b, err := os.ReadFile(path)
				if err != nil || len(b) < 28 {
					continue
				}
				var ft uint64
				var size int64
				var pathLen uint32
				var rest []byte
				ver := binary.LittleEndian.Uint64(b[0:8])
				if ver == 2 && len(b) >= 28 {
					size = int64(binary.LittleEndian.Uint64(b[8:16]))
					ft = binary.LittleEndian.Uint64(b[16:24])
					pathLen = binary.LittleEndian.Uint32(b[24:28])
					rest = b[28:]
				} else if len(b) >= 20 {
					size = int64(binary.LittleEndian.Uint64(b[0:8]))
					ft = binary.LittleEndian.Uint64(b[8:16])
					pathLen = binary.LittleEndian.Uint32(b[16:20])
					rest = b[20:]
				} else {
					continue
				}
				t := filetimeToTime(ft)
				if t.IsZero() {
					continue
				}
				maxLen := int(pathLen) * 2
				if maxLen > len(rest) {
					maxLen = len(rest)
				}
				orig := utf16leToString(rest[:maxLen])
				if orig == "" && len(rest) > 0 {
					orig = strings.TrimSpace(string(rest[:maxLen]))
				}
				if orig == "" {
					orig = "(路径无法解析)"
				}
				count++
				emit(ev(t, TypeDelete, LevelConfirmed, "删除了文件（回收站）",
					orig + " · 原大小 " + formatSize(size),
					root+`\...\$I*`, it.Name()+" · "+orig))
			}
		}
	}
	st := src("回收站删除记录", count, nil, "该时段没有回收站删除记录")
	if !hadDir {
		st.Available = false
		st.Level = "missing"
		st.Note = "没有找到任何盘符的 $Recycle.Bin"
	}
	return st
}

func collectSetupAPI(c CollectContext, emit func(TimelineEvent)) SourceStatus {
	path := filepath.Join(c.SystemRoot, "INF", "setupapi.dev.log")
	f, err := os.Open(path)
	if err != nil {
		return src("设备首次安装（SetupAPI）", 0, err, "")
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 32<<20))
	if err != nil {
		return src("设备首次安装（SetupAPI）", 0, err, "")
	}
	lines := strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
	var section time.Time
	perSection := 0
	seen := map[string]bool{}
	count := 0
	for _, line := range lines {
		if idx := strings.Index(line, "Section start"); idx >= 0 {
			s := strings.TrimSpace(line[idx+len("Section start"):])
			fields := strings.Fields(s)
			if len(fields) >= 2 {
				if t, terr := time.ParseInLocation("2006/01/02 15:04:05.000",
					fields[0]+" "+fields[1], time.Local); terr == nil {
					section = t
					perSection = 0
					continue
				}
			}
			section = time.Time{}
			continue
		}
		if section.IsZero() || perSection >= 2 || count >= 120 {
			continue
		}
		low := strings.ToLower(line)
		if !strings.Contains(low, "usb\\vid_") && !strings.Contains(low, "usbstor") &&
			!strings.Contains(low, "ven_") {
			continue
		}
		dev := strings.TrimSpace(line)
		if dev == "" || len(dev) > 180 || seen[dev] {
			continue
		}
		seen[dev] = true
		perSection++
		count++
		emit(ev(section, TypeDevice, LevelCorroborated, "USB 设备首次安装（SetupAPI）",
			truncStr(dev, 160), `C:\Windows\INF\setupapi.dev.log`, dev))
	}
	return src("设备首次安装（SetupAPI）", count, nil, "该时段没有设备安装记录")
}
