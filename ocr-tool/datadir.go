package main

// 插件数据目录：与插件本体（安装目录）彻底分离。
//
// 宿主是「重命名式更新」——升级时把 <plugins>/<id>/ 整个改名成 <id>.bak.<旧版本>，
// 再把新版本解压进去，所以写在安装目录里的任何文件都会被一起卷走（.bak 之后还会被清理）。
// 本插件的持久化数据（PaddleOCR 模型 ~178MB、日志）一律落到：
//
//	<用户主目录>/.quickdock/plugin-data/io.github.parieses.ocr-tool/
//	  ├─ models/   下载的 det/rec 模型、字典、onnxruntime 动态库
//	  └─ logs/     ocr-tool.log
//
// 与 plugins/、logs/、sites/、quickdock.db 同级，升级换版本不用重新下模型。
// QUICKDOCK_PLUGIN_DATA 可显式覆盖（测试隔离 / 宿主将来统一指定时用）。

import (
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const pluginID = "io.github.parieses.ocr-tool"

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

// ensureDataDir 返回 <dataRoot>/name 目录；目标不存在时把历史位置的老目录搬过来
// （优先 rename——同盘瞬间完成，失败再退化成逐文件复制）。
func ensureDataDir(name string) string {
	base := dataRoot()
	_ = os.MkdirAll(base, 0o755)
	dst := filepath.Join(base, name)
	if _, err := os.Stat(dst); err == nil {
		return dst
	}
	for _, src := range legacyCandidates(name) {
		fi, err := os.Stat(src)
		if err != nil || !fi.IsDir() {
			continue
		}
		if err := os.Rename(src, dst); err == nil {
			return dst
		}
		if err := copyTree(src, dst); err == nil {
			return dst
		}
	}
	return dst
}

// ensureDataFile 返回 <dataRoot>/name 的文件路径，父目录按需创建。
func ensureDataFile(name string) string {
	base := dataRoot()
	_ = os.MkdirAll(base, 0o755)
	return filepath.Join(base, name)
}

// copyTree 递归复制目录（仅用于跨盘迁移：源目录保留不动）。
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(p, target)
	})
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
