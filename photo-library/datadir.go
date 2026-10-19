package main

// 插件数据目录：与插件本体（安装目录）彻底分离。
//
// 宿主的更新方式是「重命名式」——升级时把 <plugins>/<id>/ 整个改名成 <id>.bak.<旧版本>，
// 再把新版本解压进去，写在安装目录里的任何文件都会被卷走（.bak 之后还会被清理）。
// 本插件的持久化数据（收藏 favorites.json、资料库 library.json、地图配置 config.json、
// 上次打开位置 session.json、缩略图缓存）一律落到宿主数据根下：
//
//	<用户主目录>/.quickdock/plugin-data/io.github.parieses.photo-library/
//	  ├─ favorites.json / library.json / config.json / session.json
//	  └─ thumbs/   缩略图缓存（可随时重建，单独子目录，避免被 LRU 误删配置）
//
// 与 plugins/、logs/、sites/、quickdock.db 同级，升级换版本不受影响。
// QUICKDOCK_PLUGIN_DATA 可显式覆盖（测试隔离 / 宿主将来统一指定时用）。

import (
	"io"
	"os"
	"path/filepath"
)

const pluginID = "io.github.parieses.photo-library"

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

// pluginDataDir 确保数据根存在并返回（调用方已按需创建子目录）。
func pluginDataDir() string {
	d := dataRoot()
	_ = os.MkdirAll(d, 0o755)
	return d
}

// legacyDataDir 早期版本把收藏 / 资料库 / 配置放在用户缓存目录，首次运行搬过来。
func legacyDataDir() string {
	if base, err := os.UserCacheDir(); err == nil && base != "" {
		return filepath.Join(base, "quickdock-photo-library")
	}
	return ""
}

// migrateLegacyJSON 把老位置里的 JSON 数据补进新目录（目标已存在则保持不动）。
// 缩略图不搬——纯缓存，重新生成即可。
func migrateLegacyJSON(dst string) {
	src := legacyDataDir()
	if src == "" || src == dst {
		return
	}
	for _, name := range []string{"favorites.json", "library.json", "config.json", "session.json"} {
		from := filepath.Join(src, name)
		to := filepath.Join(dst, name)
		if _, err := os.Stat(from); err != nil {
			continue
		}
		if _, err := os.Stat(to); err == nil {
			continue
		}
		_ = copyFile(from, to)
	}
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
