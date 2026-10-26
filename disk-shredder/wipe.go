package main

// 空闲空间覆写：清的是「已经删掉的文件」留下的数据残片。
//
// 原理很土：在目标卷上建一个大临时文件，一直写到盘几乎满，
// 这些新数据必然落到之前释放出来的簇上，然后再把临时文件删掉。
// Windows 的 cipher /w 干的是同一件事，区别在三点：
//   - 我们可中断（临时文件是自己建的，取消时能删干净）；
//   - 我们留安全余量，不把盘写到 0——写满会让系统和别的程序一起遭殃；
//   - 我们不限 NTFS，FAT32 / exFAT 的 U 盘同样能跑。
//
// 语义上必须说清楚：覆写的是「整个卷的空闲空间」，不是用户选的那个文件夹。
// 这一点 cipher 的文档里也是这么写的，UI 文案必须照抄，否则就是误导。

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
)

const wipeChunk = 4 << 20 // 4 MiB

// defaultReserve 默认保留余量：写到盘满会把系统写死，留 2 GB 兜底。
func defaultReserve(total uint64) uint64 {
	const minReserve = 2 << 30 // 2 GB
	r := total / 100           // 1%
	if r < minReserve {
		r = minReserve
	}
	return r
}

// pickTempDir 选一个位于目标卷上、且当前用户能写目录来放临时文件。
// 优先卷根（C:\ 通常需要管理员，会失败）→ 用户目录 → 系统临时目录。
func pickTempDir(vol *Volume) (string, error) {
	cands := []string{vol.Path}
	if home, err := os.UserHomeDir(); err == nil && home != "" && onVolume(home, vol) {
		cands = append(cands, filepath.Join(home, "Downloads"), home)
	}
	if tmp := os.TempDir(); tmp != "" && onVolume(tmp, vol) {
		cands = append(cands, tmp)
	}
	for _, c := range cands {
		if probeWritable(c) {
			return c, nil
		}
	}
	return "", fmt.Errorf("在 %s 上找不到可写目录（卷根通常需要管理员权限），请手动指定一个该卷上的文件夹", vol.Path)
}

func onVolume(path string, vol *Volume) bool {
	v := volumeOf(path)
	return v != nil && strings.EqualFold(filepath.Clean(v.Path), filepath.Clean(vol.Path))
}

func isDiskFull(err error) bool {
	if err == nil {
		return false
	}
	var pe *os.PathError
	if errors.As(err, &pe) {
		err = pe.Err
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		if errno == syscall.ENOSPC {
			return true
		}
		if runtime.GOOS == "windows" && errno == 112 { // ERROR_DISK_FULL
			return true
		}
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no space") || strings.Contains(msg, "not enough space") ||
		strings.Contains(err.Error(), "磁盘空间不足") || strings.Contains(err.Error(), "空间不足")
}

// wipeFree 覆写卷上的空闲空间，返回实际写入字节数与补充说明。
func wipeFree(t *task, vol *Volume, passes int, reserve uint64, tempDir string) (uint64, []string) {
	var notes []string
	total := uint64(0)
	plan := passPlan(passes)

	for i, kind := range plan {
		if t.canceled() {
			notes = append(notes, fmt.Sprintf("第 %d/%d 遍开始前已取消", i+1, len(plan)))
			break
		}
		t.setPhase(fmt.Sprintf("第 %d/%d 遍 · %s", i+1, len(plan), passName(kind)))

		v, err := currentVolume(vol.Path)
		if err != nil {
			notes = append(notes, err.Error())
			break
		}
		free := v.FreeBytes
		if free <= reserve {
			notes = append(notes, fmt.Sprintf("可用空间 %s 已不足保留余量 %s，停止", humanBytes(free), humanBytes(reserve)))
			break
		}
		target := free - reserve

		wrote, note := writeBalloon(t, tempDir, target, kind)
		total += wrote
		if note != "" {
			notes = append(notes, note)
			if wrote == 0 {
				break
			}
		}
	}
	return total, notes
}

// writeBalloon 建一个临时文件写到 target 字节（或盘满），再 Sync + 删除。
func writeBalloon(t *task, dir string, target uint64, kind passKind) (uint64, string) {
	var nameBuf [8]byte
	if _, err := rand.Read(nameBuf[:]); err != nil {
		return 0, "生成临时文件名失败：" + err.Error()
	}
	name := filepath.Join(dir, ".qd-wipe-"+hex.EncodeToString(nameBuf[:])+".tmp")

	f, err := os.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, "创建临时文件失败：" + err.Error()
	}

	buf := make([]byte, wipeChunk)
	rnd := newPRNG()
	switch kind {
	case passZero:
		for i := range buf {
			buf[i] = 0
		}
	case passOne:
		for i := range buf {
			buf[i] = 0xFF
		}
	default:
		rnd.fill(buf)
	}

	var written uint64
	var note string
	for written < target {
		if t.canceled() {
			note = "已取消，正在清理临时文件"
			break
		}
		n := uint64(len(buf))
		if target-written < n {
			n = target - written
		}
		w, err := f.Write(buf[:n])
		if w > 0 {
			written += uint64(w)
			t.addBytes(int64(w))
		}
		if err != nil {
			if isDiskFull(err) {
				note = fmt.Sprintf("盘已写满（写到 %s 时空间耗尽），本遍到此为止", humanBytes(written))
				break
			}
			f.Close()
			os.Remove(name)
			return written, "写入失败：" + err.Error()
		}
		// 随机遍每块重新生成，别 4 MiB 一个花样刷到底
		if kind == passRand {
			rnd.fill(buf)
		}
	}

	// 落盘再删：只写页缓存不 Sync，等于没覆写
	if err := f.Sync(); err != nil && note == "" {
		note = "Sync 失败（数据可能未完全落盘）：" + err.Error()
	}
	f.Close()
	if err := os.Remove(name); err != nil {
		if note == "" {
			note = "临时文件删除失败，请手动删除：" + name
		} else {
			note += "；且临时文件删除失败：" + name
		}
	}
	return written, note
}

// currentVolume 重新取一次卷信息（每一遍之间可用空间可能变了）。
func currentVolume(path string) (*Volume, error) {
	for _, v := range listVolumes() {
		if strings.EqualFold(filepath.Clean(v.Path), filepath.Clean(path)) {
			return &v, nil
		}
	}
	return nil, fmt.Errorf("卷 %s 已不存在（可能已被拔出）", path)
}
