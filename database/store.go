package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

// dbName 插件私有 SQLite 文件名（连接配置 + 通用 KV 都存这里）。
const dbName = "io.github.parieses.database.db"

// DbConnection 已保存的连接配置。Password 为 DPAPI（Windows）/ base64（其他平台）密文。
type DbConnection struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	DbType    string `json:"dbType"` // mysql | redis | sqlite
	Host      string `json:"host"`
	Port      int    `json:"port"`
	Username  string `json:"username"`
	Password  string `json:"password"`
	Database  string `json:"database"`
	FilePath  string `json:"filePath"`
	CreatedAt string `json:"createdAt"`
}

// schemaDDL 与主程序 internal/db/schema.go 的 db_connections 字段名/默认值保持一致
const schemaDDL = `CREATE TABLE IF NOT EXISTS db_connections (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL DEFAULT '',
	db_type TEXT NOT NULL DEFAULT 'mysql',
	host TEXT NOT NULL DEFAULT '127.0.0.1',
	port INTEGER NOT NULL DEFAULT 3306,
	username TEXT NOT NULL DEFAULT '',
	password TEXT NOT NULL DEFAULT '',
	database TEXT NOT NULL DEFAULT '',
	file_path TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
)`

const connColumns = `id, name, db_type, host, port, username, password, "database", file_path, created_at`

// kvDDL 通用键值表：存放设置、各连接「上次打开的库」等前端需要持久化的小数据。
const kvDDL = `CREATE TABLE IF NOT EXISTS kv (
	k TEXT PRIMARY KEY,
	v TEXT NOT NULL
)`

var (
	store    *sql.DB
	storeMu  sync.Mutex
	storeErr error
)

func now() string { return time.Now().Format(time.RFC3339) }

// pluginID 插件标识：数据目录名与「更新备份目录」前缀都用它。
const pluginID = "io.github.parieses.database"

// pluginDir 返回插件可执行文件所在目录。
// ⚠️ 这是易失目录：宿主更新插件时会把整个 <id>/ 重命名成 <id>.bak.<旧版本>
// （里面的 data/ 被一起卷走），再把新版本解压进来。插件数据绝不能存这里。
func pluginDir() string {
	exe, err := os.Executable()
	if err != nil {
		wd, _ := os.Getwd()
		return wd
	}
	return filepath.Dir(exe)
}

// pluginsRoot 返回宿主插件根目录（默认 <用户主目录>/.quickdock/plugins）。
func pluginsRoot() string {
	if root := os.Getenv("QUICKDOCK_PLUGINS"); root != "" {
		return root
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".quickdock", "plugins")
	}
	return filepath.Dir(pluginDir())
}

// dataRoots 按优先级返回候选数据根目录：第一个作为当前存储，其余作为历史数据
// 来源参与合并回收（见 legacyCandidates）。
//
// 为什么选 <用户主目录>/.quickdock/plugin-data/<id>：
//   - 它在宿主自己的数据根里（plugins/、logs/、sites/、quickdock.db 都在这），
//     与安装目录彻底解耦，宿主重命名式更新碰不到；
//   - .quickdock 是本地真实目录，而 %APPDATA% 常被「已知文件夹移动」重定向到
//     OneDrive / 网盘，SQLite（尤其 WAL）在重定向目录下会文件锁失效甚至损坏；
//   - 里面的密码是 DPAPI 加密的（密钥 = 用户 + 机器），本就不该漫游。
//
// QUICKDOCK_PLUGIN_DATA 可显式覆盖（测试隔离 / 宿主将来统一指定时用）。
func dataRoots() []string {
	if p := os.Getenv("QUICKDOCK_PLUGIN_DATA"); p != "" {
		return []string{p}
	}
	out := []string{}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		out = append(out, filepath.Join(home, ".quickdock", "plugin-data", pluginID))
	}
	// 早期版本用过 %APPDATA%\QuickDock\plugin-data\<id>，保留为回落 / 历史来源
	if cfg, err := os.UserConfigDir(); err == nil && cfg != "" {
		out = append(out, filepath.Join(cfg, "QuickDock", "plugin-data", pluginID))
	}
	out = append(out, filepath.Join(pluginDir(), "data")) // 兜底：极少触发
	return out
}

// dataRoot 当前正在使用的数据目录。
func dataRoot() string { return dataRoots()[0] }

// legacyCandidates 列出所有可能存有历史数据的库文件：
//  1. 当前安装目录 <pluginDir>/data/
//  2. <pluginsRoot>/<id>/data/（exe 被解压到版本子目录时）
//  3. 历史更新留下的备份 <pluginsRoot>/<id>.bak.*/data/ ← 数据最常在这里
//  4. 其它候选数据根（如早期用过的 %APPDATA%\QuickDock\plugin-data\<id>）
func legacyCandidates() []string {
	seen := map[string]bool{}
	out := []string{}
	add := func(p string) {
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		if _, err := os.Stat(p); err == nil {
			out = append(out, p)
		}
	}
	add(filepath.Join(pluginDir(), "data", dbName))
	add(filepath.Join(pluginsRoot(), pluginID, "data", dbName))
	if baks, err := filepath.Glob(filepath.Join(pluginsRoot(), pluginID+".bak.*", "data", dbName)); err == nil {
		for _, b := range baks {
			add(b)
		}
	}
	// 非当前使用的其它数据根：早期版本可能落在 %APPDATA% 下，也一并回收
	for _, r := range dataRoots()[1:] {
		add(filepath.Join(r, dbName))
	}
	return out
}

// findDBInDir 在目录里找一个已存在的 SQLite 主库文件（扩展名恰好为 .db，
// 排除 -wal / -shm / -journal 等伴随文件），排除与 canonical 同名的文件。
// 用于「库文件用了别的名字落在 plugin-data/<id>/ 里」时仍能识别并复用。
// 与 http-client 的 ensureDataFile 加固保持一致，堵上「同目录异名库被忽略」这一小概率缺口。
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

// dbHasData 判断给定 SQLite 文件是否含有效业务数据（db_connections 表有行）。
// 否则（文件损坏 / 不是本插件的库 / 空库）一律视为无数据。
func dbHasData(path string) bool {
	if _, err := os.Stat(path); err != nil {
		return false
	}
	d, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		return false
	}
	defer d.Close()
	d.SetMaxOpenConns(1)
	var n int
	if err := d.QueryRow("SELECT COUNT(*) FROM db_connections").Scan(&n); err != nil {
		return false
	}
	return n > 0
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

// sqliteDSN 构造 SQLite file: DSN；% ? # 会干扰解析，需转义
func sqliteDSN(path string) string {
	esc := strings.NewReplacer("%", "%25", "?", "%3F", "#", "%23").Replace(filepath.ToSlash(path))
	return "file:" + esc + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
}

// initStore 打开（必要时创建）插件自己的 SQLite，建表，并把历史数据迁过来
// （含更新时被宿主卷进 .bak 备份里的库，见 legacyCandidates）。
func initStore() {
	dir := dataRoot()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		storeErr = fmt.Errorf("创建数据目录失败: %w", err)
		return
	}
	dbPath := filepath.Join(dir, dbName)

	// 同目录内若存在其它名字的库文件（如 database.db / data.db / 旧版命名），
	// 且规范文件缺失或为空占位库，则优先复用带数据的那个，避免误建空库导致
	// 「连接配置看不到了」。与 http-client 的 ensureDataFile 加固保持一致。
	status := "created"
	if _, err := os.Stat(dbPath); err == nil {
		if dbHasData(dbPath) {
			status = "exists"
		} else {
			status = "empty-placeholder"
		}
	}
	if status != "exists" {
		if alt := findDBInDir(dir, dbName); alt != "" && dbHasData(alt) {
			if err := copySiblings(alt, dbPath); err == nil {
				status = "reused-alt:" + filepath.Base(alt)
			}
		}
	}

	d, err := sql.Open("sqlite", sqliteDSN(dbPath))
	if err != nil {
		storeErr = fmt.Errorf("打开插件数据库失败: %w", err)
		return
	}
	d.SetMaxOpenConns(1) // SQLite 单写者：串行化避免 database is locked
	if _, err := d.Exec(schemaDDL); err != nil {
		storeErr = fmt.Errorf("初始化表失败: %w", err)
		_ = d.Close()
		return
	}
	if _, err := d.Exec(kvDDL); err != nil {
		storeErr = fmt.Errorf("初始化键值表失败: %w", err)
		_ = d.Close()
		return
	}
	store = d
	qdLogInfo("数据库就绪 path=%s status=%s", dbPath, status)
	// 打开成功后做一次历史数据回收（见 adoptHistoricalData）
	adoptHistoricalData()
}

// adoptMarkerKey 记录「历史数据已回收」，避免用户删掉的连接被老备份反复 import 回来。
const adoptMarkerKey = "__history_adopted"

// adoptHistoricalData 首次运行时把散落在各处的历史数据「合并」进稳定库：
// 候选 = 当前安装目录 <pluginDir>/data/ + 历史更新留下的 <id>.bak.*/data/。
// 宿主是「重命名式更新」——把 <id>/ 整个改成 <id>.bak.<旧版本>，里面的 data/
// 被一起卷走，这正是「更新后连接全消失」的根源；这里把能捞的都捞回来。
// 连接按主键 id 去重（稳定库优先），KV 只补缺失的 key。
// 只做一次（之后被 adoptMarkerKey 挡住），否则用户删掉的连接会被备份「复活」。
func adoptHistoricalData() {
	if err := storeOK(); err != nil {
		return
	}
	var mark string
	if err := store.QueryRow("SELECT v FROM kv WHERE k = ?", adoptMarkerKey).Scan(&mark); err == nil {
		return // 已经回收过
	}
	total := 0
	for _, c := range legacyCandidates() {
		total += adoptOneDB(c)
	}
	_ = kvSet(adoptMarkerKey, "1")
	if total > 0 {
		fmt.Fprintf(os.Stderr, "已从历史位置回收 %d 条连接\n", total)
	}
}

// adoptOneDB 把候选库合并进当前 store，返回新增的连接数。为不触碰原始文件
// （最新数据可能只存在于 -wal 里），先把 db/-wal/-shm 拷到临时目录再打开。
func adoptOneDB(path string) int {
	tmp, err := os.MkdirTemp(dataRoot(), ".adopt-")
	if err != nil {
		return 0
	}
	defer os.RemoveAll(tmp)
	dst := filepath.Join(tmp, dbName)
	for _, suf := range []string{"", "-wal", "-shm"} {
		if _, err := os.Stat(path + suf); err != nil {
			continue
		}
		if err := copyFile(path+suf, dst+suf); err != nil {
			return 0
		}
	}
	d, err := sql.Open("sqlite", sqliteDSN(dst))
	if err != nil {
		return 0
	}
	defer d.Close()
	d.SetMaxOpenConns(1)

	added := 0
	if rows, err := d.Query("SELECT " + connColumns + " FROM db_connections"); err == nil {
		var list []DbConnection
		for rows.Next() {
			var c DbConnection
			if err := rows.Scan(&c.ID, &c.Name, &c.DbType, &c.Host, &c.Port, &c.Username,
				&c.Password, &c.Database, &c.FilePath, &c.CreatedAt); err != nil {
				continue
			}
			list = append(list, c)
		}
		rows.Close()
		for _, c := range list {
			res, err := store.Exec(
				`INSERT OR IGNORE INTO db_connections (`+connColumns+`, updated_at)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				c.ID, c.Name, c.DbType, c.Host, c.Port, c.Username, c.Password,
				c.Database, c.FilePath, c.CreatedAt, now(),
			)
			if err != nil {
				continue
			}
			if n, err := res.RowsAffected(); err == nil && n > 0 {
				added += int(n)
			}
		}
	}
	if krows, err := d.Query("SELECT k, v FROM kv WHERE k <> ?", adoptMarkerKey); err == nil {
		type kvPair struct{ k, v string }
		var pairs []kvPair
		for krows.Next() {
			var p kvPair
			if err := krows.Scan(&p.k, &p.v); err != nil {
				continue
			}
			pairs = append(pairs, p)
		}
		krows.Close()
		for _, p := range pairs {
			_, _ = store.Exec("INSERT OR IGNORE INTO kv(k, v) VALUES(?, ?)", p.k, p.v)
		}
	}
	return added
}

// copyFile 逐字节拷贝文件（用于旧数据迁移）。
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
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

func storeOK() error {
	if storeErr != nil {
		return storeErr
	}
	if store == nil {
		return errors.New("插件数据库未初始化")
	}
	return nil
}

// 注：旧主程序 quickdock.db 的 db_connections 不再迁移（按需求丢弃老数据，新插件库独立存储）。

// ---- CRUD ----

func listConnections() ([]DbConnection, error) {
	if err := storeOK(); err != nil {
		return nil, err
	}
	storeMu.Lock()
	defer storeMu.Unlock()
	rows, err := store.Query("SELECT " + connColumns + " FROM db_connections ORDER BY created_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []DbConnection{}
	for rows.Next() {
		var c DbConnection
		if err := rows.Scan(&c.ID, &c.Name, &c.DbType, &c.Host, &c.Port, &c.Username,
			&c.Password, &c.Database, &c.FilePath, &c.CreatedAt); err != nil {
			return nil, err
		}
		list = append(list, c)
	}
	return list, rows.Err()
}

func getConnection(id string) (*DbConnection, error) {
	if err := storeOK(); err != nil {
		return nil, err
	}
	storeMu.Lock()
	defer storeMu.Unlock()
	row := store.QueryRow("SELECT "+connColumns+" FROM db_connections WHERE id = ?", id)
	var c DbConnection
	if err := row.Scan(&c.ID, &c.Name, &c.DbType, &c.Host, &c.Port, &c.Username,
		&c.Password, &c.Database, &c.FilePath, &c.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("连接不存在: %s", id)
		}
		return nil, err
	}
	return &c, nil
}

func createConnection(c *DbConnection) error {
	if err := storeOK(); err != nil {
		return err
	}
	if c.ID == "" {
		c.ID = uuid.New().String()
	}
	if c.CreatedAt == "" {
		c.CreatedAt = now()
	}
	storeMu.Lock()
	defer storeMu.Unlock()
	_, err := store.Exec(
		`INSERT INTO db_connections (`+connColumns+`, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.Name, c.DbType, c.Host, c.Port, c.Username, c.Password,
		c.Database, c.FilePath, c.CreatedAt, now(),
	)
	return err
}

func updateConnection(c *DbConnection) error {
	if err := storeOK(); err != nil {
		return err
	}
	storeMu.Lock()
	defer storeMu.Unlock()
	_, err := store.Exec(
		`UPDATE db_connections SET name = ?, db_type = ?, host = ?, port = ?, username = ?,
		 password = ?, "database" = ?, file_path = ?, updated_at = ? WHERE id = ?`,
		c.Name, c.DbType, c.Host, c.Port, c.Username, c.Password,
		c.Database, c.FilePath, now(), c.ID,
	)
	return err
}

func deleteConnection(id string) error {
	if err := storeOK(); err != nil {
		return err
	}
	storeMu.Lock()
	defer storeMu.Unlock()
	_, err := store.Exec("DELETE FROM db_connections WHERE id = ?", id)
	return err
}

// ---- 通用 KV（设置 / 各连接上次打开的库 等） ----

// kvGet 返回键对应的 JSON 反序列化值；不存在时 value 为 null。
func kvGet(key string) (interface{}, error) {
	if err := storeOK(); err != nil {
		return nil, err
	}
	storeMu.Lock()
	defer storeMu.Unlock()
	var raw string
	row := store.QueryRow("SELECT v FROM kv WHERE k = ?", key)
	err := row.Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var val interface{}
	if err := json.Unmarshal([]byte(raw), &val); err != nil {
		return raw, nil // 兼容存入的裸字符串
	}
	return val, nil
}

// kvSet 写入任意 JSON 可序列化值（按 key 覆盖）。
func kvSet(key string, value interface{}) error {
	if err := storeOK(); err != nil {
		return err
	}
	b, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("value 序列化失败: %w", err)
	}
	storeMu.Lock()
	defer storeMu.Unlock()
	_, err = store.Exec(
		`INSERT INTO kv(k, v) VALUES(?, ?) ON CONFLICT(k) DO UPDATE SET v = excluded.v`,
		key, string(b),
	)
	return err
}

// kvList 返回全部键名（升序）。
func kvList() ([]string, error) {
	if err := storeOK(); err != nil {
		return nil, err
	}
	storeMu.Lock()
	defer storeMu.Unlock()
	rows, err := store.Query("SELECT k FROM kv ORDER BY k")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := []string{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}
