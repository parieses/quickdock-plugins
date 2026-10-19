package main

// 插件数据目录：与插件本体（安装目录）彻底分离。
//
// 宿主的更新方式是「重命名式」——升级时把 <plugins>/<id>/ 整个改名成 <id>.bak.<旧版本>，
// 再把新版本解压进去，写在安装目录里的任何文件都会被卷走（.bak 之后还会被清理）。
// 本插件唯一的落盘数据是诊断日志，写到宿主数据根下：
//
//	<用户主目录>/.quickdock/plugin-data/io.github.parieses.pdf-toolkit/logs/pdf-toolkit.log
//
// QUICKDOCK_PLUGIN_DATA 可显式覆盖（测试隔离 / 宿主将来统一指定时用）。

import (
	"os"
	"path/filepath"
)

const pluginID = "io.github.parieses.pdf-toolkit"

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

// diagLogPath 诊断日志路径（<dataRoot>/logs/pdf-toolkit.log）。
func diagLogPath() string {
	dir := filepath.Join(dataRoot(), "logs")
	_ = os.MkdirAll(dir, 0o755)
	return filepath.Join(dir, "pdf-toolkit.log")
}
