package main

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-sql-driver/mysql"
	_ "modernc.org/sqlite"

	"github.com/redis/go-redis/v9"
)

// DbConnectionInput 前端传入的连接配置。Password 为明文；落库前加密。
type DbConnectionInput struct {
	ID       string
	Name     string
	DbType   string
	Host     string
	Port     int
	Username string
	Password string
	Database string
	FilePath string
}

// connInputFrom 从命令 input 解析连接配置
func connInputFrom(input map[string]interface{}) DbConnectionInput {
	return DbConnectionInput{
		ID:       strFrom(input, "id"),
		Name:     strFrom(input, "name"),
		DbType:   strings.ToLower(strFrom(input, "dbType")),
		Host:     strFrom(input, "host"),
		Port:     intFrom(input, "port", 0),
		Username: strFrom(input, "username"),
		Password: rawStrFrom(input, "password"),
		Database: strFrom(input, "database"),
		FilePath: strFrom(input, "filePath"),
	}
}

// inputFromSaved 把已保存记录（密文密码）还原成可用于建连的明文配置
func inputFromSaved(saved *DbConnection) DbConnectionInput {
	plain, err := decryptSecret(saved.Password)
	if err != nil {
		plain = ""
	}
	return DbConnectionInput{
		DbType:   saved.DbType,
		Host:     saved.Host,
		Port:     saved.Port,
		Username: saved.Username,
		Password: plain,
		Database: saved.Database,
		FilePath: saved.FilePath,
	}
}

// ---- 内部：统一连接抽象 ----

type dbConnAdapter struct {
	sqlDB  *sql.DB
	redis  *redis.Client
	kind   string
	shared bool // true = 由连接缓存持有：close() 是空操作，真正回收由缓存负责
}

// close 供各 handler 的 defer 调用。共享连接只「归还」不关闭，
// 这样同一个连接的多次操作可以复用已建立的 TCP + 认证会话。
func (c *dbConnAdapter) close() {
	if c.shared {
		return
	}
	c.closeReal()
}

// closeReal 真正关闭底层连接（缓存淘汰 / 连接变更 / 进程退出时使用）。
func (c *dbConnAdapter) closeReal() {
	if c.sqlDB != nil {
		_ = c.sqlDB.Close()
	}
	if c.redis != nil {
		_ = c.redis.Close()
	}
}

func (c *dbConnAdapter) ping() error {
	ctx, cancel := context.WithTimeout(context.Background(), dbQueryTimeout)
	defer cancel()
	if c.sqlDB != nil {
		return c.sqlDB.PingContext(ctx)
	}
	if c.redis != nil {
		return c.redis.Ping(ctx).Err()
	}
	return fmt.Errorf("未知连接类型")
}

func (c *dbConnAdapter) exec(query string) (*DbQueryResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dbQueryTimeout)
	defer cancel()
	if c.redis != nil {
		return execRedis(ctx, c.redis, query)
	}
	return execSQL(ctx, c.sqlDB, query)
}

// execMulti 把以分号分隔的多条语句逐条执行，返回多结果（遇第一条出错即停止，
// 但仍把出错那条的结果一并带上）。defaultDB 仅用于「可编辑」探测时的库上下文。
func (c *dbConnAdapter) execMulti(query, defaultDB string) (*DbQueryMultiResult, error) {
	stmts := splitSQLStatements(query)
	res := &DbQueryMultiResult{Statements: len(stmts), Success: true}
	if len(stmts) == 0 {
		res.Error = "查询内容为空"
		res.Success = false
		return res, nil
	}
	// 超时随语句数增长，但封顶在宿主命令超时之内（见 dbMultiTotalTimeout）
	timeout := dbQueryTimeout * time.Duration(len(stmts))
	if timeout < dbQueryTimeout {
		timeout = dbQueryTimeout
	}
	if timeout > dbMultiTotalTimeout {
		timeout = dbMultiTotalTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	for i, s := range stmts {
		r := &DbQueryResult{Index: i + 1, Statement: s}
		var e error
		if c.redis != nil {
			var rr *DbQueryResult
			rr, e = execRedis(ctx, c.redis, s)
			if rr != nil {
				*r = *rr
			}
		} else {
			var rr *DbQueryResult
			rr, e = execSQL(ctx, c.sqlDB, s)
			if rr != nil {
				*r = *rr
			}
			if e == nil {
				enrichEditable(c, defaultDB, s, r)
			}
		}
		if e != nil {
			r.Success = false
			r.Error = e.Error()
			// MySQL 未选定库时的原始报错很晦涩（Error 1046），补一句可操作的提示。
			if c.kind == "mysql" && strings.TrimSpace(defaultDB) == "" && isNoDatabaseErr(e) {
				r.Error += "\n\n提示：当前连接没有默认库，也没有选中「库上下文」。" +
					"请在左侧展开一个数据库并点击它（或在连接配置里填写「默认数据库」），" +
					"之后直接写 SELECT * FROM 表名 即可，无需带库名前缀。"
			}
			res.Success = false
			res.Error = r.Error
			res.Results = append(res.Results, *r)
			break
		}
		res.Results = append(res.Results, *r)
	}
	return res, nil
}

// toDbConn 按 db_type 用对应驱动建连（每次查询新建连接，用完即关）。
func toDbConn(input DbConnectionInput) (*dbConnAdapter, error) {
	input.DbType = strings.ToLower(strings.TrimSpace(input.DbType))
	switch input.DbType {
	case "mysql":
		port := input.Port
		if port == 0 {
			port = 3306
		}
		// 用 mysql.Config.FormatDSN 构建 DSN：密码/库名含 @ : / 等特殊字符时自动转义，避免拼错
		mc := mysql.NewConfig()
		mc.User = input.Username
		mc.Passwd = input.Password
		mc.Net = "tcp"
		mc.Addr = net.JoinHostPort(input.Host, strconv.Itoa(port))
		mc.DBName = input.Database
		mc.ParseTime = true
		mc.Timeout = 10 * time.Second
		mc.ReadTimeout = 30 * time.Second
		mc.AllowNativePasswords = true
		d, err := sql.Open("mysql", mc.FormatDSN())
		if err != nil {
			return nil, err
		}
		// MySQL 可由服务端并发处理，开放一个小的连接池（4）提升多查询吞吐；
		// SQLite 保持单连接（WAL + 事务一致性需要）。
		d.SetMaxOpenConns(4)
		d.SetMaxIdleConns(4)
		d.SetConnMaxLifetime(5 * time.Minute)
		return &dbConnAdapter{sqlDB: d, kind: "mysql"}, nil
	case "sqlite":
		path := strings.TrimSpace(input.FilePath)
		if path == "" {
			return nil, fmt.Errorf("SQLite 需要指定数据库文件路径")
		}
		// Windows/含特殊字符路径：% ? # 会干扰 SQLite 的 file: DSN 解析，做转义；/ 与盘符冒号保留
		esc := strings.NewReplacer("%", "%25", "?", "%3F", "#", "%23").Replace(filepath.ToSlash(path))
		dsn := "file:" + esc + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
		d, err := sql.Open("sqlite", dsn)
		if err != nil {
			return nil, err
		}
		d.SetMaxOpenConns(1)
		return &dbConnAdapter{sqlDB: d, kind: "sqlite"}, nil
	case "redis":
		port := input.Port
		if port == 0 {
			port = 6379
		}
		opts := &redis.Options{
			Addr:        fmt.Sprintf("%s:%d", input.Host, port),
			Password:    input.Password,
			DB:          parseRedisDB(input.Database),
			DialTimeout: 10 * time.Second,
			ReadTimeout: dbQueryTimeout,
		}
		return &dbConnAdapter{redis: redis.NewClient(opts), kind: "redis"}, nil
	}
	return nil, fmt.Errorf("不支持的数据库类型: %s", input.DbType)
}

// ---- 连接复用缓存 ----
//
// 之前每条命令都新建一次 TCP + 认证连接再 close：本地库还好，连远程 MySQL 时
// 每翻一页 / 看一眼结构都要重新握手，卡顿感很明显。这里按「连接 id + 实际库名」
// 缓存底层 *sql.DB / redis.Client，空闲超过 connIdleTimeout 再回收。
// sql.DB 本身就是连接池，go-sql-driver / go-redis 遇到坏连接会自动重连，无需额外保活。
const connIdleTimeout = 5 * time.Minute

type cachedConn struct {
	adapter  *dbConnAdapter
	lastUsed time.Time
}

var (
	connCacheMu sync.Mutex
	connCache   = map[string]*cachedConn{}
)

func connKey(id, db string) string { return id + "\x00" + strings.TrimSpace(db) }

// getSharedConn 取（或建立）一个可复用的连接。返回 adapter 的 close() 是空操作。
func getSharedConn(id, dbOverride string) (*DbConnection, *dbConnAdapter, error) {
	saved, err := getConnection(id)
	if err != nil {
		return nil, nil, err
	}
	in := inputFromSaved(saved)
	if db := strings.TrimSpace(dbOverride); db != "" {
		in.Database = db
	}
	key := connKey(id, in.Database)
	now := time.Now()

	connCacheMu.Lock()
	// 顺手清理空闲过久的连接，避免长期开着占资源
	for k, c := range connCache {
		if now.Sub(c.lastUsed) > connIdleTimeout {
			c.adapter.closeReal()
			delete(connCache, k)
		}
	}
	if c, ok := connCache[key]; ok {
		c.lastUsed = now
		adapter := c.adapter
		connCacheMu.Unlock()
		return saved, adapter, nil
	}
	// sql.Open / redis.NewClient 都是惰性的（不立刻建连），在锁内创建开销很小
	adapter, e := toDbConn(in)
	if e != nil {
		connCacheMu.Unlock()
		return saved, nil, e
	}
	adapter.shared = true
	connCache[key] = &cachedConn{adapter: adapter, lastUsed: now}
	connCacheMu.Unlock()
	return saved, adapter, nil
}

// invalidateConnCache 在连接被保存 / 删除后调用：关闭并移除该 id 下的所有缓存
// （主机、账号、密码、默认库可能已变，旧连接不可再用）。
func invalidateConnCache(id string) {
	id = strings.TrimSpace(id)
	if id == "" {
		return
	}
	prefix := id + "\x00"
	connCacheMu.Lock()
	for k, c := range connCache {
		if strings.HasPrefix(k, prefix) {
			c.adapter.closeReal()
			delete(connCache, k)
		}
	}
	connCacheMu.Unlock()
}

// closeAllConnCache 进程退出前释放全部缓存连接（避免锁住 SQLite 文件）。
func closeAllConnCache() {
	connCacheMu.Lock()
	for k, c := range connCache {
		c.adapter.closeReal()
		delete(connCache, k)
	}
	connCacheMu.Unlock()
}

// openSavedWithDB 取出已保存连接并复用底层连接；dbOverride（库上下文）非空时
// 覆盖连接的默认库。这样查询里只写表名即可，无需写「库名.表名」。
func openSavedWithDB(id, dbOverride string) (*DbConnection, *dbConnAdapter, error) {
	return getSharedConn(id, dbOverride)
}

// openSaved 取出已保存连接并建连（使用连接自身保存的默认库）
func openSaved(id string) (*DbConnection, *dbConnAdapter, error) {
	return openSavedWithDB(id, "")
}

func defaultPort(t string) int {
	switch t {
	case "mysql":
		return 3306
	case "redis":
		return 6379
	case "sqlite":
		return 0
	}
	return 0
}

// parseRedisDB 从连接配置的 Database 字段解析 Redis 库索引（0-15，默认 0）。
func parseRedisDB(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0
	}
	if n > 15 {
		n = 15
	}
	return n
}
