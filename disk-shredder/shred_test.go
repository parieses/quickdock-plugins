package main

// 覆写是这个插件唯一的承诺，必须能证明它真的改写了内容——
// 文件一旦被删就无从验证，所以测的是 overwriteAndRename（覆写 + 改名，不删）。

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestOverwriteDestroysContent(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "secret.txt")
	orig := bytes.Repeat([]byte("TOP-SECRET-PASSWORD-"), 4000) // ~80 KB
	if err := os.WriteFile(p, orig, 0o600); err != nil {
		t.Fatal(err)
	}

	final, err := overwriteAndRename(p, passPlan(1), 3, newPRNG(), nil)
	if err != nil {
		t.Fatalf("覆写失败: %v", err)
	}
	defer os.Remove(final)

	got, err := os.ReadFile(final)
	if err != nil {
		t.Fatalf("读回失败（文件不该被删）: %v", err)
	}
	if bytes.Equal(got, orig) {
		t.Fatal("内容与原文件完全一致，覆写没生效")
	}
	if len(got) != len(orig) {
		t.Fatalf("大小变了：%d → %d（覆写必须保持原大小，否则会分配到新簇）", len(orig), len(got))
	}
	// 随机遍不应留下任何一段原文
	if bytes.Contains(got, []byte("TOP-SECRET-PASSWORD-")) {
		t.Fatal("原文片段仍残留")
	}
	if filepath.Base(final) == "secret.txt" {
		t.Fatal("没有改名，MFT 里的旧文件名会留下")
	}
}

func TestOverwriteZeroPassAndMultiPass(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.bin")
	orig := bytes.Repeat([]byte{0xAB}, 4096)
	if err := os.WriteFile(p, orig, 0o600); err != nil {
		t.Fatal(err)
	}
	// 最后一遍是 0x00：0x00 → 0xFF → 随机 里最后一遍是随机，
	// 这里用 2 遍（0x00 → 随机）验证不了结尾，改用全 0 单遍组合验证
	final, err := overwriteAndRename(p, []passKind{passZero}, 0, newPRNG(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(final)
	got, _ := os.ReadFile(final)
	if !bytes.Equal(got, bytes.Repeat([]byte{0x00}, 4096)) {
		t.Fatal("全 0 遍没有把内容写成全 0")
	}

	// 3 遍（cipher 兼容档）跑完仍保持原大小
	q := filepath.Join(dir, "b.bin")
	if err := os.WriteFile(q, orig, 0o600); err != nil {
		t.Fatal(err)
	}
	f2, err := overwriteAndRename(q, passPlan(3), 2, newPRNG(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f2)
	if st, err := os.Stat(f2); err != nil || st.Size() != int64(len(orig)) {
		t.Fatalf("多遍后大小不对: %v", err)
	}
}

func TestPassPlan(t *testing.T) {
	cases := map[int][]passKind{
		1: {passRand},
		2: {passZero, passRand},
		3: {passZero, passOne, passRand},
		7: {passZero, passOne, passRand, passRand, passZero, passOne, passRand},
	}
	for n, want := range cases {
		got := passPlan(n)
		if len(got) != len(want) {
			t.Fatalf("passes=%d 得到 %d 遍", n, len(got))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("passes=%d 第 %d 遍不符", n, i)
			}
		}
	}
	if len(passPlan(100)) != 7 { // 上限封顶
		t.Fatal("遍数没有封顶")
	}
}

func TestGuardRejects(t *testing.T) {
	bad := []string{"", `\\.\PhysicalDrive0`, `\\?\C:\`, filepath.Join(os.TempDir())}
	// 卷根与系统目录一定被拒
	if guardReject("") == "" {
		t.Fatal("空路径应被拒")
	}
	if guardReject(`\\.\PhysicalDrive0`) == "" {
		t.Fatal("设备路径应被拒")
	}
	for _, b := range bad[:3] {
		if guardReject(b) == "" {
			t.Fatalf("%q 应被拒", b)
		}
	}
	// 临时目录下的普通文件不该被拦
	f := filepath.Join(os.TempDir(), "qd-guard-test.txt")
	os.WriteFile(f, []byte("x"), 0o600)
	defer os.Remove(f)
	if reason := guardReject(f); reason != "" {
		t.Fatalf("临时文件不该被拒: %s", reason)
	}
}

func TestRandomNameStaysInDir(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 50; i++ {
		name, err := randomNameIn(dir)
		if err != nil {
			t.Fatalf("第 %d 次生成失败: %v", i, err)
		}
		if filepath.Dir(name) != dir {
			t.Fatal("随机名跑出目录了")
		}
		os.WriteFile(name, []byte("x"), 0o600) // 制造冲突，逼它重试
	}
}
