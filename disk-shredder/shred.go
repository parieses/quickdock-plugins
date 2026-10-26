package main

// 文件粉碎：覆写内容 → 随机重命名 → 删除。
//
// 三条设计取舍，都写在代码里免得后来人改回去：
//
//  1. 覆写必须保持原文件大小。先截断再写会让 NTFS 分配到新簇，
//     旧簇里的原数据一个字节都没动——那就白覆写了。
//  2. 随机重命名不是花活。NTFS 把文件名放在 MFT 记录里，覆写文件内容清不掉它；
//     改名会让这条记录里的名字被覆盖，多改几次旧名字基本不留痕。
//  3. 写完必须 Sync 再删。OS 写缓存没落盘就删除，可能只改了页缓存。
//
// 一遍随机就够（NIST SP 800-88 Clear）。多遍是给用户心理安慰，
// 也确实能覆盖一些边缘情况（比如坏扇区重映射），代价只是时间。

import (
	"crypto/rand"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type passKind int

const (
	passZero passKind = iota
	passOne
	passRand
)

// passName 给用户看的遍次说明。
func passName(k passKind) string {
	switch k {
	case passZero:
		return "全 0"
	case passOne:
		return "全 1(0xFF)"
	default:
		return "随机"
	}
}

// passPlan 把「几遍」翻译成具体遍次序列。
// 3 遍刻意与 Windows cipher /w 一致（0x00 → 0xFF → 随机），方便对得上口径。
func passPlan(n int) []passKind {
	switch {
	case n >= 7: // DoD 5220.22-M 的 7 遍写法
		return []passKind{passZero, passOne, passRand, passRand, passZero, passOne, passRand}
	case n == 3:
		return []passKind{passZero, passOne, passRand}
	case n == 2:
		return []passKind{passZero, passRand}
	default:
		return []passKind{passRand}
	}
}

// prng 用于填充随机遍的字节流。用 xorshift64*：速度够快，
// 且覆写不关心密码学强度——攻击者只要读不到原数据就行，猜得出序列也无所谓。
type prng struct{ s uint64 }

func newPRNG() *prng {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// 拿不到系统熵就退化成时间种子：覆写安全性不受影响
		return &prng{s: uint64(time.Now().UnixNano()) | 1}
	}
	return &prng{s: binary.LittleEndian.Uint64(b[:]) | 1}
}

func (p *prng) next() uint64 {
	p.s ^= p.s >> 12
	p.s ^= p.s << 25
	p.s ^= p.s >> 27
	return p.s * 0x2545F4914F6CDD1D
}

func (p *prng) fill(b []byte) {
	i := 0
	for ; i+8 <= len(b); i += 8 {
		binary.LittleEndian.PutUint64(b[i:], p.next())
	}
	if i < len(b) {
		var tail [8]byte
		binary.LittleEndian.PutUint64(tail[:], p.next())
		copy(b[i:], tail[:])
	}
}

const shredBufSize = 1 << 20 // 1 MiB

// shredFile 覆写并删除单个文件。progress 每写一段回调一次已写字节数。
func shredFile(path string, plan []passKind, renames int, rnd *prng, progress func(int64)) error {
	final, err := overwriteAndRename(path, plan, renames, rnd, progress)
	if err != nil {
		return err
	}
	return os.Remove(final)
}

// overwriteAndRename 只做覆写与改名，返回改名后的最终路径（删除留给调用方，
// 这样单测能验证「覆写到底有没有把内容改掉」——文件被删了就无从验证了）。
func overwriteAndRename(path string, plan []passKind, renames int, rnd *prng, progress func(int64)) (string, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return "", err
	}
	defer func() {
		// 兜底：即便中途出错也别把句柄攥着（Windows 上句柄不关 = 文件删不掉）
		f.Close()
	}()

	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	size := st.Size()

	if size > 0 {
		buf := make([]byte, shredBufSize)
		for _, kind := range plan {
			if _, err := f.Seek(0, 0); err != nil {
				return "", err
			}
			var left int64 = size
			for left > 0 {
				n := int64(len(buf))
				if left < n {
					n = left
				}
				chunk := buf[:n]
				switch kind {
				case passZero:
					for i := range chunk {
						chunk[i] = 0
					}
				case passOne:
					for i := range chunk {
						chunk[i] = 0xFF
					}
				default:
					rnd.fill(chunk)
				}
				w, err := f.Write(chunk)
				if err != nil {
					return "", err
				}
				left -= int64(w)
				if progress != nil {
					progress(int64(w))
				}
			}
			// 每一遍结束都落盘：避免多遍只留在页缓存里实际只写了一次
			if err := f.Sync(); err != nil {
				return "", err
			}
		}
	}
	if err := f.Sync(); err != nil {
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}

	// 随机改名：清掉 MFT 记录里的旧文件名
	cur := path
	for i := 0; i < renames; i++ {
		next, err := randomNameIn(filepath.Dir(cur))
		if err != nil {
			break
		}
		if err := os.Rename(cur, next); err != nil {
			break // 改名失败不致命：调用方会删当前路径
		}
		cur = next
	}
	return cur, nil
}

// randomNameIn 在同目录下生成一个不冲突的随机名（长度也随机，覆盖 MFT 里不同长度的名字）。
func randomNameIn(dir string) (string, error) {
	const alphabet = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	rnd := newPRNG()
	// 注意别写 int(rnd.next())%n：uint64 转 int 有一半概率是负数，
	// 索引会直接越界 panic（踩过一次，进程当场崩）
	const nlen = uint64(len(alphabet))
	for attempt := 0; attempt < 20; attempt++ {
		n := 6 + int(rnd.next()%20) // 6~25 个字符
		b := make([]byte, n)
		for i := range b {
			b[i] = alphabet[rnd.next()%nlen]
		}
		candidate := filepath.Join(dir, string(b))
		if _, err := os.Lstat(candidate); err != nil {
			return candidate, nil
		}
	}
	return "", os.ErrExist
}

// collectFiles 展开待处理目标：目录递归，文件直接收。
// 不跟随符号链接 / 挂载点（Windows junction 同理），免得顺着链接跑到别处去删。
func collectFiles(paths []string, maxFiles int) (files []string, dirs []string, skipped []string, truncated bool) {
	seenFile := map[string]bool{}
	seenDir := map[string]bool{}

	addFile := func(p string) bool {
		k := filepath.Clean(p)
		if seenFile[k] {
			return false
		}
		if len(files) >= maxFiles {
			truncated = true
			return false
		}
		seenFile[k] = true
		files = append(files, k)
		return true
	}

	for _, raw := range paths {
		p := strings.TrimSpace(raw)
		if p == "" {
			continue
		}
		st, err := os.Lstat(p)
		if err != nil {
			continue // 预览阶段已经报过错，这里不重复
		}
		if st.Mode()&os.ModeSymlink != 0 {
			skipped = append(skipped, p+"（符号链接，未跟随）")
			continue
		}
		if !st.IsDir() {
			if !st.Mode().IsRegular() {
				skipped = append(skipped, p+"（不是普通文件）")
				continue
			}
			addFile(p)
			continue
		}
		// 目录：先记下来，最后按「深度从大到小」删空壳
		root := filepath.Clean(p)
		if !seenDir[root] {
			seenDir[root] = true
			dirs = append(dirs, root)
		}
		_ = filepath.WalkDir(root, func(sub string, d os.DirEntry, err error) error {
			if err != nil {
				return nil // 无权限的子目录跳过，不中断整体
			}
			if d.IsDir() {
				if sub != root && !seenDir[sub] {
					seenDir[sub] = true
					dirs = append(dirs, sub)
				}
				return nil
			}
			if d.Type()&os.ModeSymlink != 0 {
				skipped = append(skipped, sub+"（符号链接，未跟随）")
				return nil
			}
			if !d.Type().IsRegular() {
				skipped = append(skipped, sub+"（不是普通文件）")
				return nil
			}
			addFile(sub)
			return nil
		})
	}
	// 深层目录先删
	sortDirsDeepFirst(dirs)
	return files, dirs, skipped, truncated
}

func sortDirsDeepFirst(dirs []string) {
	// 路径分隔符越多层级越深，先处理深的
	for i := 1; i < len(dirs); i++ {
		for j := i; j > 0; j-- {
			if depth(dirs[j]) > depth(dirs[j-1]) {
				dirs[j], dirs[j-1] = dirs[j-1], dirs[j]
			}
		}
	}
}

func depth(p string) int {
	return strings.Count(filepath.Clean(p), string(os.PathSeparator))
}
