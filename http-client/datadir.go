package main

// 插件数据目录：与插件本体（安装目录）彻底分离。
//
// 宿主是「重命名式更新」——升级时把 <plugins>/<id>/ 整个改名成 <id>.bak.<旧版本>，
// 再把新版本解压进去，所以写在安装目录里的任何文件都会被一起卷走（.bak 之后还会被清理）。
// 插件数据一律落到宿主数据根下：
//
//	<用户主目录>/.quickdock/plugin-data/io.github.parieses.http-client/
//
// 与 plugins/、logs/、sites/、quickdock.db 同级，升级不受影响。
// QUICKDOCK_PLUGIN_DATA 可显式覆盖（测试隔离 / 宿主将来统一指定时用）。
//
// 为什么不放 %APPDATA%：它常被「已知文件夹移动」重定向到 OneDrive / 网盘，
// SQLite（尤其 WAL）在重定向目录下会文件锁失效甚至损坏。

import (
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const pluginID = "io.github.parieses.http-client"

// pluginInstallDir 插件安装目录（<plugins>/<id>/，易失，只在迁移历史数据时读取）。
func pluginInstallDir() string {
	if exe, err := os.Executable(); err == nil {
		return filepath.Dir(exe)
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "."
}

// pluginsRoot 宿主插件根目录（默认 <用户主目录>/.quickdock/plugins）。
func pluginsRoot() string {
	if root := os.Getenv("QUICKDOCK_PLUGINS"); root != "" {
		return root
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".quickdock", "plugins")
	}
	return filepath.Dir(pluginInstallDir())
}

// dataRoot 插件稳定数据目录（可用 QUICKDOCK_PLUGIN_DATA 覆盖）。
func dataRoot() string {
	if p := os.Getenv("QUICKDOCK_PLUGIN_DATA"); p != "" {
		return p
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".quickdock", "plugin-data", pluginID)
	}
	if cfg, err := os.UserConfigDir(); err == nil && cfg != "" {
		return filepath.Join(cfg, "QuickDock", "plugin-data", pluginID)
	}
	return filepath.Join(os.TempDir(), "quickdock-plugin-data", pluginID)
}

// legacyCandidates 列出历史数据可能所在的位置（安装目录内 / 更新留下的 .bak 备份里）。
// .bak 按修改时间从新到旧排列，优先迁移最新的那份。
func legacyCandidates(name string) []string {
	install := pluginInstallDir()
	root := pluginsRoot()
	out := []string{
		filepath.Join(install, name),
		filepath.Join(install, "data", name),
		filepath.Join(root, pluginID, name),
		filepath.Join(root, pluginID, "data", name),
	}
	baks, _ := filepath.Glob(filepath.Join(root, pluginID+".bak.*"))
	sort.Slice(baks, func(i, j int) bool { return modTime(baks[i]).After(modTime(baks[j])) })
	for _, b := range baks {
		out = append(out, filepath.Join(b, name), filepath.Join(b, "data", name))
	}
	return out
}

func modTime(p string) time.Time {
	if fi, err := os.Stat(p); err == nil {
		return fi.ModTime()
	}
	return time.Time{}
}

// ensureDataFile 返回 <dataRoot>/name 的绝对路径；目标不存在时先把历史位置的老文件搬过来
// （复制而非移动：原始备份保持原样，失败也不影响启动）。
func ensureDataFile(name string) string {
	dir := dataRoot()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return filepath.Join(dir, name)
	}
	dst := filepath.Join(dir, name)
	if _, err := os.Stat(dst); err == nil {
		return dst
	}
	for _, src := range legacyCandidates(name) {
		if _, err := os.Stat(src); err != nil {
			continue
		}
		if err := copySiblings(src, dst); err != nil {
			continue
		}
		break
	}
	return dst
}

// copySiblings 复制主文件及 SQLite 的 -wal / -shm 伴随文件
// （最新数据可能还停在 -wal 里，只拷主文件会丢最近的写入）。
func copySiblings(src, dst string) error {
	copied := false
	for _, suf := range []string{"", "-wal", "-shm"} {
		if _, err := os.Stat(src + suf); err != nil {
			continue
		}
		if err := copyFile(src+suf, dst+suf); err != nil {
			return err
		}
		copied = true
	}
	if !copied {
		return os.ErrNotExist
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}
