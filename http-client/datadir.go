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
	"strings"
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

// ensureDataFile 返回 <dataRoot>/name 的绝对路径，以及一条「数据来自何处」的状态说明
// （供启动日志回显，便于排查「数据已在 plugin-data 里却没回显」）。
//
// 目标已存在 → "exists"（数据就位）；
// 目录里有其它名字的库文件被复用 → "reused-alt:<文件名>"（修复文件名不符导致的新建空库）；
// 从历史位置（安装目录 / .bak 备份）迁过来 → "migrated:<来源路径>"；
// 都找不到 → "created"（新建空库，意味着此前的数据没被认出来 —— 排查重点）。
// ensureDataFile 返回 <dataRoot>/name 的绝对路径，以及「数据来自何处」的状态说明（落启动日志）。
//
// 数据定位优先级：
//  1. 目标文件已存在且非空（项目/请求/目录/历史任一行）→ "exists"；
//  2. 目标文件不存在，或已被旧版本误建为空库（0 行）——先在 dataRoot 里找其它名字的库文件
//     （如 http-client.db / data.db / 旧版命名），再找历史位置（安装目录 / .bak 备份），
//     取第一个「有数据」的来源复制复用（覆盖空库）→ "reused-alt:<名>" / "migrated:<来源>"；
//  3. 都找不到 → 返回空库路径，状态 "created"（此时数据确实没被认出来，是排查重点）。
//
// 关键：若旧版本已把目标文件建成了空库，绝不能因为"文件存在"就直接返回，否则会一直卡在空库、
// 表现为"数据已在 plugin-data 却回显不出"。只有当目标是非空库时才短路返回。
func ensureDataFile(name string) (string, string) {
	dir := dataRoot()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return filepath.Join(dir, name), "created(err-mkdir)"
	}
	dst := filepath.Join(dir, name)

	// 1) 目标本身已是有效非空库 → 直接用它。
	if st, err := os.Stat(dst); err == nil && st.Size() > 0 && dbHasData(dst) {
		return dst, "exists"
	}

	// 2) 同目录内其它名字的库文件（修复文件名不符 / 旧版本空库占位）。
	if alt := findDBInDir(dir, name); alt != "" {
		if _, ok := statDB(alt); ok {
			if err := copySiblings(alt, dst); err == nil {
				return dst, "reused-alt:" + filepath.Base(alt)
			}
		}
	}

	// 3) 历史位置（安装目录 / .bak 备份）迁移。
	for _, src := range legacyCandidates(name) {
		if _, ok := statDB(src); !ok {
			continue
		}
		if err := copySiblings(src, dst); err != nil {
			continue
		}
		return dst, "migrated:" + src
	}

	return dst, "created"
}

// statDB 返回文件是否存在且含有效业务数据（用于决定能否复用）。
func statDB(path string) (bool, bool) {
	if _, err := os.Stat(path); err != nil {
		return false, false
	}
	return true, dbHasData(path)
}

// findDBInDir 在目录里找一个已存在的 SQLite 主库文件（扩展名恰好为 .db，
// 排除 -wal / -shm / -journal 等伴随文件），排除与 canonical 同名的文件。
// 用于「库文件用了别的名字落在 plugin-data/<id>/ 里」时仍能识别并复用。
func findDBInDir(dir, canonical string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		fn := e.Name()
		if !strings.HasSuffix(fn, ".db") {
			continue
		}
		if fn == canonical {
			continue
		}
		return filepath.Join(dir, fn)
	}
	return ""
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
