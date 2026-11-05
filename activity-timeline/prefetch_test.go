package main

// 用合成的 SCCA / MAM 样本验证解析链路（真实 C:\Windows\Prefetch 需要管理员权限，
// CI 上拿不到，所以这里自己造结构，覆盖头部字段、UTF-16 字符串区与运行时间戳）。

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

func utf16leBytes(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := make([]byte, len(u)*2)
	for i, v := range u {
		binary.LittleEndian.PutUint16(b[i*2:], v)
	}
	return b
}

// makeSCCA 造一个 v30 的明文 prefetch 结构。
func makeSCCA(name string, paths []string, times []time.Time) []byte {
	b := make([]byte, 0x400)
	binary.LittleEndian.PutUint32(b[0:4], 30) // Win10
	copy(b[4:8], "SCCA")
	binary.LittleEndian.PutUint32(b[0x0C:0x10], uint32(len(b)))
	copy(b[0x10:0x4C], utf16leBytes(name))
	binary.LittleEndian.PutUint32(b[0x4C:0x50], 0x4447516E)
	binary.LittleEndian.PutUint32(b[0x64:0x68], 0x100) // 字符串区偏移
	for i, t := range times {
		if i >= 8 {
			break
		}
		ft := uint64(t.UTC().UnixNano()/100 + 116444736000000000)
		binary.LittleEndian.PutUint64(b[0x80+i*8:0x88+i*8], ft)
	}
	off := 0x100
	for _, p := range paths {
		w := utf16leBytes(p)
		copy(b[off:], w)
		off += len(w) + 2 // UTF-16 null 结尾
	}
	return b
}

func TestSplitPFName(t *testing.T) {
	cases := []struct{ in, prefix, hash string }{
		{"LENOVO.MODERN.IMCONTROLLER.PL-4447516E.pf", "LENOVO.MODERN.IMCONTROLLER.PL", "4447516E"},
		{"NOTEPAD.EXE-D8414F97.pf", "NOTEPAD.EXE", "D8414F97"},
		{"SOME-APP.EXE-0FE8F3A9.pf", "SOME-APP.EXE", "0FE8F3A9"},
		{"BROKEN.pf", "BROKEN", ""},
	}
	for _, c := range cases {
		p, h := splitPFName(c.in)
		if p != c.prefix || h != c.hash {
			t.Errorf("splitPFName(%q) = (%q,%q), want (%q,%q)", c.in, p, h, c.prefix, c.hash)
		}
	}
}

func TestParseSCCA(t *testing.T) {
	exe := `C:\Windows\Lenovo\ImController\PluginHost\Lenovo.Modern.ImController.PluginHost.exe`
	t1 := time.Date(2026, 11, 3, 8, 5, 0, 0, time.UTC)
	t2 := time.Date(2026, 11, 5, 2, 3, 39, 0, time.UTC)
	b := makeSCCA("LENOVO.MODERN.IMCONTROLLER.PL", []string{
		exe,
		`\DEVICE\HARDDISKVOLUME3\WINDOWS\SYSTEM32\KERNEL32.DLL`,
		`\DEVICE\HARDDISKVOLUME3\WINDOWS\SYSTEM32\NTDLL.DLL`,
	}, []time.Time{t1, t2})

	info := parseSCCA(b, "LENOVO.MODERN.IMCONTROLLER.PL")
	if !info.Parsed {
		t.Fatalf("应该识别为 SCCA，Note=%q", info.Note)
	}
	if info.ExeName != "LENOVO.MODERN.IMCONTROLLER.PL" {
		t.Errorf("头部程序名 = %q", info.ExeName)
	}
	if !strings.HasSuffix(info.FullPath, "Lenovo.Modern.ImController.PluginHost.exe") {
		t.Errorf("完整路径未提取到: %q", info.FullPath)
	}
	if len(info.RunTimes) != 2 {
		t.Fatalf("运行时间条数 = %d, want 2", len(info.RunTimes))
	}
	if !info.RunTimes[0].Equal(t2) || !info.RunTimes[1].Equal(t1) {
		t.Errorf("运行时间未按新→旧排列: %v", info.RunTimes)
	}
}

func TestParseSCCAVolumeGUIDPath(t *testing.T) {
	exe := `\VOLUME{12345678-1234-1234-1234-123456789ABC}\PROGRAM FILES\CONTOSO\CONTOSO-HELPER.EXE`
	b := makeSCCA("CONTOSO-HELPER.EXE", []string{exe}, nil)
	info := parseSCCA(b, "CONTOSO-HELPER.EXE")
	if info.FullPath == "" {
		t.Fatalf("Volume{GUID} 路径也应保留，Note=%q", info.Note)
	}
	if !strings.Contains(info.FullPath, "CONTOSO-HELPER.EXE") {
		t.Errorf("提取到的路径不对: %q", info.FullPath)
	}
}

func TestPickExePathIgnoresDLL(t *testing.T) {
	got := pickExePath([]string{
		`\DEVICE\HARDDISKVOLUME3\WINDOWS\SYSTEM32\KERNEL32.DLL`,
		`\DEVICE\HARDDISKVOLUME3\WINDOWS\SYSTEM32\NOTEPAD.EXE`,
	}, "NOTEPAD.EXE")
	if !strings.HasSuffix(got, "NOTEPAD.EXE") {
		t.Errorf("应挑出 exe 而不是 DLL，实际 %q", got)
	}
}

func TestParseSCCARejectsGarbage(t *testing.T) {
	info := parseSCCA(make([]byte, 0x200), "WHATEVER")
	if info.Parsed || info.FullPath != "" {
		t.Errorf("垃圾数据不应被当成有效 pf: %+v", info)
	}
}

// 端到端：把合成 .pf 放进临时 Prefetch 目录，走一遍真正的 collectPrefetch，
// 检查时间轴事件里展示的是完整 exe 名 + 完整路径，而不是半截文件名。
func TestCollectPrefetchShowsRealProgram(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Windows")
	dir := filepath.Join(root, "Prefetch")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := `C:\Windows\Lenovo\ImController\PluginHost\Lenovo.Modern.ImController.PluginHost.exe`
	plain := makeSCCA("LENOVO.MODERN.IMCONTROLLER.PL", []string{exe},
		[]time.Time{time.Date(2026, 11, 3, 8, 5, 0, 0, time.UTC)})
	pfName := "LENOVO.MODERN.IMCONTROLLER.PL-4447516E.pf"
	if err := os.WriteFile(filepath.Join(dir, pfName), plain, 0o644); err != nil {
		t.Fatal(err)
	}

	var got []TimelineEvent
	st := collectPrefetch(CollectContext{SystemRoot: root}, func(e TimelineEvent) { got = append(got, e) })
	if len(got) != 1 {
		t.Fatalf("事件数 = %d, want 1 (status=%+v)", len(got), st)
	}
	d := got[0].Detail
	wantTime := time.Date(2026, 11, 3, 8, 5, 0, 0, time.UTC).Local().Format("2006-01-02 15:04:05")
	for _, want := range []string{
		"Lenovo.Modern.ImController.PluginHost.exe",
		`C:\Windows\Lenovo\ImController\PluginHost`,
		wantTime,
	} {
		if !strings.Contains(d, want) {
			t.Errorf("文案里缺少 %q\n实际: %s", want, d)
		}
	}
	if strings.Contains(d, "不是程序全名") {
		t.Errorf("解析成功时不应出现降级说明: %s", d)
	}
	t.Logf("展示文案: %s", d)
}

// 解析失败时必须讲清命名规则，不能把截断片段当成程序名糊弄过去。
func TestCollectPrefetchDegradesHonestly(t *testing.T) {
	// 用一个本机绝不会存在的名字，避免降级分支真的反查到某个 exe。
	root := filepath.Join(t.TempDir(), "Windows")
	dir := filepath.Join(root, "Prefetch")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	pfName := "ZZQX-NOT-INSTALLED-APP-AB12CD34.pf"
	if err := os.WriteFile(filepath.Join(dir, pfName), make([]byte, 256), 0o644); err != nil {
		t.Fatal(err)
	}
	var got []TimelineEvent
	collectPrefetch(CollectContext{SystemRoot: root}, func(e TimelineEvent) { got = append(got, e) })
	if len(got) != 1 {
		t.Fatalf("事件数 = %d, want 1", len(got))
	}
	d := got[0].Detail
	for _, want := range []string{"不是程序全名", "29 个字符", "AB12CD34", "路径"} {
		if !strings.Contains(d, want) {
			t.Errorf("降级文案里缺少 %q\n实际: %s", want, d)
		}
	}
	t.Logf("降级文案: %s", d)
}
