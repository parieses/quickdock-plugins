package main

// 护栏：这个文件存在的唯一理由是「覆写不可逆」。
// 后端必须独立做一遍硬拒绝——native 插件运行期不经宿主权限校验（已知事实），
// 前端的确认框拦得住误操作，拦不住手滑传错路径，所以后端这一道不是冗余。

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// guardReject 检查目标路径，返回非空表示「拒绝处理」并给出原因。
// 只拦「无论用户怎么确认都不该做」的：设备对象、卷根、系统目录、回收站。
// 用户自己的目录（哪怕整个 Downloads）不在这里拦，交给前端的强制确认。
func guardReject(path string) string {
	p := strings.TrimSpace(path)
	if p == "" {
		return "路径为空"
	}
	// Windows 设备命名空间：\\.\PhysicalDrive0 之类，写下去是整盘
	if strings.HasPrefix(p, `\\.\`) || strings.HasPrefix(p, `\\?\`) {
		return "不接受设备路径（" + p + "）：裸设备写整盘，请用系统自带的重置 / 格式化"
	}
	if runtime.GOOS != "windows" {
		switch {
		case strings.HasPrefix(p, "/dev/"):
			return "不接受块设备路径（" + p + "）：整盘擦除请用 shred / diskutil"
		case p == "/" || p == "/proc" || p == "/sys" || p == "/dev":
			return "不接受系统根与虚拟文件系统路径：" + p
		}
	}

	abs, err := filepath.Abs(p)
	if err != nil {
		return "路径无法解析：" + p
	}
	clean := filepath.Clean(abs)
	lower := strings.ToLower(clean)

	// 卷根：C:\ 这种，删了就是把整个盘抹了
	if isVolumeRoot(clean) {
		return "不接受卷根（" + clean + "）：请选择具体文件或文件夹"
	}

	// 系统目录：无论谁点的都不该由这个工具来删
	for _, sys := range systemDirs() {
		s := strings.ToLower(filepath.Clean(sys))
		if s == "" {
			continue
		}
		if lower == s || strings.HasPrefix(lower, s+string(os.PathSeparator)) {
			return "不接受系统目录：" + clean
		}
	}
	return ""
}

func isVolumeRoot(p string) bool {
	if runtime.GOOS == "windows" {
		// C:\ 或 \\?\C:\ 去掉前缀后只剩盘符加分隔符
		q := strings.TrimPrefix(strings.TrimPrefix(p, `\\?\`), `\\.\`)
		return len(q) <= 3 && len(q) >= 2 && q[1] == ':'
	}
	return p == "/"
}

// systemDirs 需要硬拒绝的目录：系统盘关键位置 + 回收站 + 卷影信息。
func systemDirs() []string {
	var out []string
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v != "" {
			out = append(out, v)
		}
	}
	if runtime.GOOS == "windows" {
		add(os.Getenv("SystemRoot")) // 通常是 C:\Windows
		add(os.Getenv("WINDIR"))
		add(os.Getenv("SystemDrive") + `\Windows`)
		add(os.Getenv("SystemDrive") + `\Program Files`)
		add(os.Getenv("SystemDrive") + `\Program Files (x86)`)
		add(os.Getenv("SystemDrive") + `\ProgramData`)
		add(os.Getenv("SystemDrive") + `\Documents and Settings`)
		add(os.Getenv("SystemDrive") + `\System Volume Information`)
		add(os.Getenv("SystemDrive") + `\$Recycle.Bin`)
		add(os.Getenv("SystemDrive") + `\Recovery`)
		add(os.Getenv("SystemDrive") + `\PerfLogs`)
	} else {
		add("/bin")
		add("/sbin")
		add("/usr")
		add("/etc")
		add("/lib")
		add("/lib64")
		add("/boot")
		add("/var")
		add("/System")
		add("/Library")
		add("/Applications")
		add("/private")
	}
	return out
}

// guardNote 给「允许但值得提醒」的情况：比如目标是整个用户目录、或位于系统盘。
// 返回空串表示没什么可说的。
func guardNote(path string) string {
	clean := filepath.Clean(path)
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if strings.EqualFold(clean, filepath.Clean(home)) {
			return "目标是你的用户目录根：会连桌面、文档、下载一起删掉"
		}
	}
	desktop := filepath.Join(mustHome(), "Desktop")
	if strings.EqualFold(clean, desktop) {
		return "目标是整个桌面"
	}
	downloads := filepath.Join(mustHome(), "Downloads")
	if strings.EqualFold(clean, downloads) {
		return "目标是整个「下载」文件夹"
	}
	if runtime.GOOS == "windows" {
		if len(clean) >= 2 && clean[1] == ':' {
			sysDrive := strings.ToUpper(strings.TrimSpace(os.Getenv("SystemDrive")))
			if sysDrive != "" && strings.EqualFold(clean[:1], sysDrive[:1]) {
				return "目标在系统盘上：请确认没有误选正在使用的文件"
			}
		}
	}
	return ""
}

func mustHome() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
}
