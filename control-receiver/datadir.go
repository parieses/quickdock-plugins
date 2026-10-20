package main

// 插件数据目录：与插件本体（安装目录）彻底分离。
//
// 宿主是「重命名式更新」——升级时把 <plugins>/<id>/ 整个改名成 <id>.bak.<旧版本>，
// 再把新版本解压进去，所以写在安装目录里的任何文件都会被一起卷走（.bak 之后还会被清理）。
// 本插件的持久化数据（config.json：控制端口 / 视频流端口 / 上次连接的手机）一律落到：
//
//	<用户主目录>/.quickdock/plugin-data/io.github.parieses.control-receiver/
//
// 与 plugins/、logs/、sites/、quickdock.db 同级，升级换版本不受影响。
// QUICKDOCK_PLUGIN_DATA 可显式覆盖（测试隔离 / 宿主将来统一指定时用）。

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const pluginID = "io.github.parieses.control-receiver"

// pluginInstallDir 插件安装目录（<plugins>/<id>/，易失，只在迁移历史数据时读取）。
func pluginInstallDir() string { return exeDir() }

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

// configFileValid 判断配置文件是否「非空且为合法 JSON 对象」（用于决定能否复用）。
func configFileValid(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return false
	}
	var v map[string]interface{}
	if err := json.Unmarshal(data, &v); err != nil {
		return false
	}
	return true
}

// ensureDataFile 返回 <dataRoot>/name 的绝对路径，以及「数据来自何处」的状态说明
// （落启动日志，便于排查配置丢失）。
//
// 目标已存在且合法 → "exists"；
// 目标缺失 / 空 / 损坏 → 先在历史位置（安装目录 / .bak 备份）找一份合法配置迁过来，
//   → "migrated:<来源>"；
// 都找不到 → 返回空路径，状态 "created"（新建默认配置）。
// 与 http-client 的 ensureDataFile 加固对齐：规范文件损坏时不再静默用空库/默认配置，
// 而是优先复用历史位置里仍有效的数据。
func ensureDataFile(name string) (string, string) {
	dir := dataRoot()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return filepath.Join(dir, name), "created(err-mkdir)"
	}
	dst := filepath.Join(dir, name)

	// 1) 目标本身已是合法配置 → 直接用它。
	if configFileValid(dst) {
		return dst, "exists"
	}

	// 2) 历史位置（安装目录 / .bak 备份）里找一份合法配置迁过来。
	for _, src := range legacyCandidates(name) {
		if configFileValid(src) {
			if err := copyFile(src, dst); err == nil {
				return dst, "migrated:" + src
			}
		}
	}

	return dst, "created"
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
