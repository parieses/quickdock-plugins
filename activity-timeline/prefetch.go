package main

// Prefetch (.pf) 解析。
//
// .pf 的文件名是「可执行文件名（大写，截断到 29 字符）」+「-」+「完整路径的 8 位哈希」，
// 例如 LENOVO.MODERN.IMCONTROLLER.PL-4447516E.pf。只按 "-" 切开把前半截当程序名展示，
// 长文件名必然显示成半截单词（连 .EXE 都被截掉），用户根本看不出跑的是什么。
//
// 真实程序名、完整路径其实就在 .pf 内部：
//   - Win7/8：明文 SCCA 结构（0x10 起 60 字节 UTF-16LE 名字 + 字符串区里的设备路径）
//   - Win10/11：外层 MAM(Xpress-Huffman) 压缩，用 ntdll!RtlDecompressBufferEx 解开后同上
//   - 0x80 起是最多 8 次运行的 FILETIME，比 .pf 的修改时间精确得多
//
// 全程只读；任何一步失败都返回可读的降级说明，绝不把推断伪装成确证。

import (
	"encoding/binary"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const pfMaxBytes = 8 << 20

type prefetchInfo struct {
	ExeName  string      // 头部存的（可能仍是截断的）程序名
	FullPath string      // 字符串区里的完整可执行路径，已尽量转成盘符形式
	RunTimes []time.Time // 最近最多 8 次运行时间，新 → 旧
	Parsed   bool        // .pf 内部结构是否成功解开
	Note     string      // 降级原因，允许为空
}

// splitPFName 拆出「名字片段」与「路径哈希」。名字里本身可能带 '-'，所以从最后一个 '-' 切。
func splitPFName(name string) (prefix, hash string) {
	stem := strings.TrimSuffix(name, ".pf")
	stem = strings.TrimSuffix(stem, ".PF")
	if i := strings.LastIndex(stem, "-"); i > 0 {
		h := stem[i+1:]
		if len(h) == 8 && isHex8(h) {
			return stem[:i], strings.ToUpper(h)
		}
	}
	return stem, ""
}

func isHex8(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func readPrefetch(path, fallbackPrefix string) prefetchInfo {
	b, err := readFileCapped(path, pfMaxBytes)
	if err != nil {
		return prefetchInfo{Note: "pf 文件不可读：" + shortErr(err)}
	}
	if len(b) < 8 {
		return prefetchInfo{Note: "pf 文件过短"}
	}
	if string(b[:3]) == "MAM" {
		plain, derr := decompressMAM(b)
		if derr != nil {
			return prefetchInfo{Note: "MAM 压缩解开失败：" + shortErr(derr)}
		}
		b = plain
	}
	return parseSCCA(b, fallbackPrefix)
}

func readFileCapped(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, max+1))
}

// parseSCCA 解析解压后的（或本来就未压缩的）SCCA 结构。
// 头部字段只做参考：字符串区偏移随版本漂移，所以始终再做一次全文件 UTF-16 扫描兜底。
func parseSCCA(b []byte, fallbackPrefix string) prefetchInfo {
	if len(b) < 0x90 || string(b[4:8]) != "SCCA" {
		return prefetchInfo{Note: "不是可识别的 SCCA 结构"}
	}
	ver := binary.LittleEndian.Uint32(b[0:4])
	info := prefetchInfo{Parsed: true, ExeName: utf16leToString(b[0x10:0x4C])}
	prefix := info.ExeName
	if prefix == "" {
		prefix = fallbackPrefix
	}

	// Win8+ 在 0x80 存 8 个 FILETIME；Win7 及更早只有一个。
	n := 1
	if ver >= 26 {
		n = 8
	}
	for i := 0; i < n && 0x80+i*8+8 <= len(b); i++ {
		ft := binary.LittleEndian.Uint64(b[0x80+i*8 : 0x80+i*8+8])
		if t := filetimeToTime(ft); !t.IsZero() && t.Year() >= 2000 && t.Year() <= 2100 {
			info.RunTimes = append(info.RunTimes, t)
		}
	}
	sort.Slice(info.RunTimes, func(i, j int) bool { return info.RunTimes[i].After(info.RunTimes[j]) })

	cands := []string{}
	if ver >= 30 && len(b) >= 0x68 {
		if off := binary.LittleEndian.Uint32(b[0x64:0x68]); off >= 0x84 && int(off) < len(b) {
			cands = append(cands, scanPFPaths(b[off:])...)
		}
	}
	cands = append(cands, scanPFPaths(b)...)
	if p := pickExePath(cands, prefix); p != "" {
		info.FullPath = devicePathToWin32(p)
	}
	if info.FullPath == "" {
		info.Note = "pf 内未找到可执行文件路径"
	}
	return info
}

func isPathRune(v uint16) bool {
	return (v >= 0x20 && v <= 0x7e) ||
		(v >= 0xa0 && v <= 0x2fff) ||
		(v >= 0x4e00 && v <= 0x9fff) ||
		(v >= 0xff00 && v <= 0xffef)
}

// utf16Runs 提取以 UTF-16 非路径字符（通常是 0x0000）分隔的可打印串。
func utf16Runs(b []byte, minLen int) []string {
	out := []string{}
	run := make([]rune, 0, 128)
	flush := func() {
		if len(run) >= minLen {
			out = append(out, string(run))
		}
		run = run[:0]
	}
	for i := 0; i+1 < len(b); i += 2 {
		v := uint16(b[i]) | uint16(b[i+1])<<8
		if isPathRune(v) {
			run = append(run, rune(v))
		} else {
			flush()
		}
	}
	flush()
	return out
}

// scanPFPaths 两个字节相位都扫一遍：字符串区未必从偶数偏移开始。
func scanPFPaths(b []byte) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, phase := range []int{0, 1} {
		if phase >= len(b) {
			continue
		}
		for _, s := range utf16Runs(b[phase:], 8) {
			if !strings.ContainsAny(s, `\/`) || seen[s] {
				continue
			}
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func pfBaseName(p string) string {
	if i := strings.LastIndexAny(p, `\/`); i >= 0 {
		return p[i+1:]
	}
	return p
}

// pickExePath 从字符串区里挑出「这个 .pf 对应的那个 exe」，排除同区的 DLL / 目录记录。
func pickExePath(paths []string, prefix string) string {
	pre := strings.TrimSuffix(strings.ToUpper(prefix), ".EXE")
	for _, p := range paths {
		up := strings.ToUpper(pfBaseName(p))
		if strings.HasSuffix(up, ".EXE") && pre != "" && strings.HasPrefix(up, pre) {
			return p
		}
	}
	// 放宽一档：pf 头部名字可能是空的或与字符串区大小写/写法不同。
	if len(pre) >= 6 {
		key := pre[:8]
		for _, p := range paths {
			up := strings.ToUpper(p)
			if strings.HasSuffix(up, ".EXE") && strings.Contains(up, key) {
				return p
			}
		}
	}
	return ""
}

var (
	exeIndexOnce sync.Once
	exeIndex     map[string][]string
)

var skipScanDirs = map[string]bool{
	"node_modules": true, ".git": true, ".cache": true, "cache": true,
	"cache2": true, "temp": true, "tmp": true, "crashdumps": true,
	"winsxs": true, "servicing": true, "installer": true, "windowsapps": true,
	"packages": true, "$recycle.bin": true, "system volume information": true,
}

// lookupExeByPrefix 是 .pf 解析失败时的兜底：在常见安装目录里按截断前缀找回完整 exe 名和路径。
// 索引在一次采集里只建一次，且限制深度与总条目数，避免为几百个 .pf 反复全盘扫。
func lookupExeByPrefix(prefix string) string {
	exeIndexOnce.Do(buildExeIndex)
	pre := strings.ToUpper(strings.TrimSuffix(prefix, ".EXE"))
	if pre == "" {
		return ""
	}
	best := ""
	for name, paths := range exeIndex {
		if !strings.HasPrefix(name, pre) {
			continue
		}
		for _, p := range paths {
			if best == "" || len(p) < len(best) {
				best = p
			}
		}
	}
	return best
}

func buildExeIndex() {
	exeIndex = make(map[string][]string)
	roots := []string{}
	add := func(p string) {
		if p != "" {
			roots = append(roots, p)
		}
	}
	add(os.Getenv("ProgramFiles"))
	add(os.Getenv("ProgramFiles(x86)"))
	add(os.Getenv("ProgramData"))
	if sr := os.Getenv("SystemRoot"); sr != "" {
		add(filepath.Join(sr, "System32"))
		add(filepath.Join(sr, "SysWOW64"))
		add(filepath.Join(sr, "Lenovo")) // 联想 ImController 等 OEM 组件常驻此处
	}
	add(os.Getenv("LOCALAPPDATA"))
	add(os.Getenv("APPDATA"))
	if home := os.Getenv("USERPROFILE"); home != "" {
		add(filepath.Join(home, "Downloads"))
		add(filepath.Join(home, "Desktop"))
	}
	seenRoot := map[string]bool{}
	visited := 0
	stop := errors.New("scan limit")
	for _, root := range roots {
		key := strings.ToUpper(root)
		if seenRoot[key] {
			continue
		}
		seenRoot[key] = true
		if st, err := os.Stat(root); err != nil || !st.IsDir() {
			continue
		}
		rootDepth := strings.Count(filepath.Clean(root), string(os.PathSeparator))
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			visited++
			if visited > 40000 {
				return stop
			}
			if d.IsDir() {
				if strings.Count(filepath.Clean(p), string(os.PathSeparator))-rootDepth > 4 {
					return filepath.SkipDir
				}
				if skipScanDirs[strings.ToLower(d.Name())] {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.EqualFold(filepath.Ext(d.Name()), ".exe") {
				up := strings.ToUpper(d.Name())
				exeIndex[up] = append(exeIndex[up], p)
			}
			return nil
		})
	}
}

func appendNote(cur, add string) string {
	if cur == "" {
		return add
	}
	return cur + "；" + add
}
