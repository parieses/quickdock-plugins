package main

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

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

var (
	store    *sql.DB
	storeMu  sync.Mutex
	storeErr error
)

func now() string { return time.Now().Format(time.RFC3339) }

// pluginDir 返回插件可执行文件所在目录（宿主把插件解压到独立目录，数据随插件走）
func pluginDir() string {
	exe, err := os.Executable()
	if err != nil {
		wd, _ := os.Getwd()
		return wd
	}
	return filepath.Dir(exe)
}

func dataDir() string { return filepath.Join(pluginDir(), "data") }

// sqliteDSN 构造 SQLite file: DSN；% ? # 会干扰解析，需转义
func sqliteDSN(path string) string {
	esc := strings.NewReplacer("%", "%25", "?", "%3F", "#", "%23").Replace(filepath.ToSlash(path))
	return "file:" + esc + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
}

// initStore 打开（必要时创建）插件自己的 SQLite，建表，并完成自身表结构迁移。
func initStore() {
	dir := dataDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		storeErr = fmt.Errorf("创建数据目录失败: %w", err)
		return
	}
	dbPath := filepath.Join(dir, "io.github.parieses.database.db")
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
	store = d
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
