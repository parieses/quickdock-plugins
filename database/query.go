package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// sqlQuote 对 SQL 标识符（表名/列名）做基本合法化，防止标识符注入。
// 真正的值参数应使用 ? 占位符进行参数化查询。
func sqlQuote(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "_"
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

const dbQueryTimeout = 30 * time.Second
const dbMaxRows = 1000

// dbMultiTotalTimeout 是多条语句执行的总超时上限。
// 必须小于 plugin.json 里的 backend.timeout（120s），否则宿主会先掐断请求，
// 前端只能看到「插件响应超时」，拿不到具体的 SQL 错误。
const dbMultiTotalTimeout = 110 * time.Second

// DbQueryResult 单条 SQL 的执行结果（不落库）。
type DbQueryResult struct {
	Success    bool       `json:"success"`
	Columns    []string   `json:"columns"`
	Rows       [][]string `json:"rows"`
	Nulls      [][]bool   `json:"nulls"` // 与 Rows 同维度，标记该单元格是否为 SQL NULL
	RowCount   int        `json:"rowCount"`
	Truncated  bool       `json:"truncated"` // 结果是否因超过 dbMaxRows 被截断
	Affected   int64      `json:"affected"`
	Message    string     `json:"message"`
	Error      string     `json:"error"`
	DurationMs int64      `json:"durationMs"`
	// 可编辑元数据：当结果为「单表 SELECT 且含主键列」时填充，前端据此渲染可编辑网格。
	Editable   bool   `json:"editable"`
	TableName  string `json:"tableName"`
	PrimaryKey string `json:"primaryKey"`
	EditReason string `json:"editReason"` // 不可编辑时的原因（复合主键 / 无主键 / 非单表查询）
	// 分页元数据：table.rows 使用；Total = -1 表示未统计到总行数
	Total    int64  `json:"total"`
	Page     int    `json:"page"`
	PageSize int    `json:"pageSize"`
	OrderBy  string `json:"orderBy"`
	OrderDir string `json:"orderDir"`
	// 多语句模式下填充：第几条语句（从 1 开始）与原始语句文本
	Index     int    `json:"index"`
	Statement string `json:"statement"`
	// SQL 预览：table.rows 返回实际执行的 SQL（参数已内联），便于前端展示/复制
	SQL     string `json:"sql"`     // 含 LIMIT/OFFSET，即本页真实执行的语句
	SQLFull string `json:"sqlFull"` // 去掉分页，便于直接拿去复用
}

// DbQueryMultiResult 一次「query.run」可能包含多条以分号分隔的语句，每条对应一个 DbQueryResult。
// 前端据此渲染「结果 1 / 结果 2 / …」可切换标签。
type DbQueryMultiResult struct {
	Statements int              `json:"statements"` // 切分出的语句总数
	Results    []DbQueryResult `json:"results"`    // 每条语句的结果（遇错即止，含出错那条）
	Success    bool             `json:"success"`    // 是否全部成功
	DurationMs int64            `json:"durationMs"` // 整体耗时
	Error      string           `json:"error"`      // 整体级错误（如全部执行失败时的摘要）
}

// enrichEditable 探测结果是否来自「单表 SELECT 且含主键列」，若是则填充可编辑元数据。
func enrichEditable(c *dbConnAdapter, defaultDB, query string, res *DbQueryResult) {
	if !isSelectLike(query) {
		res.EditReason = "非 SELECT 查询"
		return
	}
	if res.RowCount == 0 || len(res.Columns) == 0 {
		res.EditReason = "查询无结果或无可显示列"
		return
	}
	table, dbName := detectEditableTable(query)
	if table == "" {
		res.EditReason = "非单表 SELECT（含 JOIN / 子查询 / UNION / 多表 / 聚合）"
		return
	}
	if c.kind == "mysql" && dbName == "" {
		if cur := currentDatabase(c.sqlDB); cur != "" {
			dbName = cur
		}
	}
	pk, reason := primaryKeyOf(c.sqlDB, c.kind, orDefault(dbName, defaultDB), table)
	if pk == "" {
		res.EditReason = reason
		return
	}
	hasPK := false
	for _, col := range res.Columns {
		if strings.EqualFold(col, pk) {
			hasPK = true
			break
		}
	}
	if !hasPK {
		res.EditReason = "主键列「" + pk + "」未包含在结果中"
		return
	}
	res.Editable = true
	res.TableName = table
	res.PrimaryKey = pk
}

// detectEditableTable 从简单 SELECT 中提取「单一表名」。
func detectEditableTable(q string) (table, dbName string) {
	s := strings.TrimSpace(q)
	if !strings.HasPrefix(strings.ToUpper(s), "SELECT") {
		return "", ""
	}
	fromIdx := strings.Index(strings.ToUpper(s), " FROM ")
	if fromIdx < 0 {
		return "", ""
	}
	rest := s[fromIdx+6:]
	up := strings.ToUpper(rest)
	if strings.Contains(up, "JOIN") || strings.Contains(up, "UNION") || strings.Contains(up, "(") || strings.Contains(up, ",") {
		return "", ""
	}
	for _, kw := range []string{" WHERE ", " GROUP ", " ORDER ", " HAVING ", " LIMIT ", " PROCEDURE "} {
		if i := strings.Index(strings.ToUpper(rest), kw); i >= 0 {
			rest = rest[:i]
		}
	}
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return "", ""
	}
	if sp := strings.IndexAny(rest, " \t"); sp >= 0 {
		rest = rest[:sp]
	}
	rest = strings.Trim(rest, "`\"[]")
	if rest == "" {
		return "", ""
	}
	if dot := strings.Index(rest, "."); dot >= 0 {
		dbName = strings.Trim(rest[:dot], "`\"[]")
		rest = strings.Trim(rest[dot+1:], "`\"[]")
	}
	return rest, dbName
}

// primaryKeyOf 返回表的主键列（仅支持单列主键）。
func primaryKeyOf(d *sql.DB, kind, dbName, table string) (string, string) {
	ctx, cancel := context.WithTimeout(context.Background(), dbQueryTimeout)
	defer cancel()
	if d == nil {
		return "", "数据库连接不可用"
	}
	if kind == "mysql" {
		q := "SELECT COLUMN_NAME FROM information_schema.KEY_COLUMN_USAGE " +
			"WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? AND CONSTRAINT_NAME = 'PRIMARY' " +
			"ORDER BY ORDINAL_POSITION"
		rows, err := d.QueryContext(ctx, q, dbName, table)
		if err != nil {
			return "", "无法读取主键信息：" + err.Error()
		}
		defer rows.Close()
		var cols []string
		for rows.Next() {
			var c string
			if err := rows.Scan(&c); err != nil {
				continue
			}
			cols = append(cols, c)
		}
		if len(cols) == 1 {
			return cols[0], ""
		}
		if len(cols) == 0 {
			return "", "表无主键（PRIMARY KEY）"
		}
		return "", "复合主键暂不支持编辑"
	}
	q := `PRAGMA table_info("` + strings.ReplaceAll(table, `"`, `""`) + `")`
	rows, err := d.QueryContext(ctx, q)
	if err != nil {
		return "", "无法读取主键信息：" + err.Error()
	}
	defer rows.Close()
	var pk string
	n := 0
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull int
		var dflt sql.NullString
		var isPk int
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &isPk); err != nil {
			continue
		}
		if isPk > 0 {
			if n == 0 {
				pk = name
			}
			n++
		}
	}
	if n == 1 {
		return pk, ""
	}
	if n == 0 {
		return "", "表无主键（PRIMARY KEY）"
	}
	return "", "复合主键暂不支持编辑"
}

// currentDatabase 返回 MySQL 连接当前所在的数据库。
func currentDatabase(d *sql.DB) string {
	ctx, cancel := context.WithTimeout(context.Background(), dbQueryTimeout)
	defer cancel()
	var name sql.NullString
	if err := d.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&name); err != nil {
		return ""
	}
	return name.String
}

func orDefault(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// quoteIdent 按数据库类型安全引用标识符。
func quoteIdent(kind, name string) string {
	name = strings.TrimSpace(name)
	if kind == "sqlite" {
		return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
	}
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

// isSelectLike 判断 SQL 是否返回结果集。
func isSelectLike(q string) bool {
	t := strings.TrimSpace(q)
	t = strings.TrimLeft(t, "(")
	upper := strings.ToUpper(t)
	for _, p := range []string{"SELECT", "SHOW", "DESCRIBE", "DESC", "EXPLAIN", "PRAGMA", "WITH"} {
		if strings.HasPrefix(upper, p) {
			return true
		}
	}
	return false
}

// splitSQLStatements 按分号切分多条 SQL 语句，尊重单/双引号字符串、反引号标识符与
// 行注释（-- / #）块注释（/* */）。空语句会被丢弃。返回的每条语句不含结尾的分号。
func splitSQLStatements(sql string) []string {
	var stmts []string
	var b strings.Builder
	inSingle, inDouble, inBacktick := false, false, false
	inLineComment, inBlockComment := false, false
	runes := []rune(sql)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		switch {
		case inLineComment:
			if c == '\n' {
				inLineComment = false
				b.WriteRune(c)
			}
			continue
		case inBlockComment:
			if c == '*' && i+1 < len(runes) && runes[i+1] == '/' {
				inBlockComment = false
				i++ // 跳过 /
			}
			continue
		case c == '-' && i+1 < len(runes) && runes[i+1] == '-':
			inLineComment = true
			i++ // 跳过第二个 -
			continue
		case c == '#' && (i == 0 || runes[i-1] == '\n' || runes[i-1] == ' ' || runes[i-1] == '\t'):
			inLineComment = true
			continue
		case c == '/' && i+1 < len(runes) && runes[i+1] == '*':
			inBlockComment = true
			i++ // 跳过 *
			continue
		case c == '\'' && !inDouble && !inBacktick:
			inSingle = !inSingle
			b.WriteRune(c)
			continue
		case c == '"' && !inSingle && !inBacktick:
			inDouble = !inDouble
			b.WriteRune(c)
			continue
		case c == '`' && !inSingle && !inDouble:
			inBacktick = !inBacktick
			b.WriteRune(c)
			continue
		case c == ';' && !inSingle && !inDouble && !inBacktick:
			if s := strings.TrimSpace(b.String()); s != "" {
				stmts = append(stmts, s)
			}
			b.Reset()
			continue
		}
		b.WriteRune(c)
	}
	if s := strings.TrimSpace(b.String()); s != "" {
		stmts = append(stmts, s)
	}
	return stmts
}

// sqlLiteral 把单个参数渲染成 SQL 字面量（仅用于「SQL 预览」展示，不参与实际执行）。
func sqlLiteral(v interface{}) string {
	switch x := v.(type) {
	case nil:
		return "NULL"
	case string:
		return "'" + strings.ReplaceAll(x, "'", "''") + "'"
	case []byte:
		return "'" + strings.ReplaceAll(string(x), "'", "''") + "'"
	case time.Time:
		return "'" + x.Format("2006-01-02 15:04:05") + "'"
	default:
		return fmt.Sprint(x)
	}
}

// inlineSQLArgs 把查询里的 ? 占位符替换成实际参数，产出可直接复制的完整 SQL。
// 会跳过引号与反斜杠转义内的 ?，避免误替换字面量里的问号。
func inlineSQLArgs(query string, args []interface{}) string {
	if len(args) == 0 {
		return query
	}
	var b strings.Builder
	ai := 0
	inSingle, inDouble := false, false
	for i := 0; i < len(query); i++ {
		ch := query[i]
		switch {
		case ch == '\\' && (inSingle || inDouble) && i+1 < len(query):
			b.WriteByte(ch)
			i++
			b.WriteByte(query[i])
		case ch == '\'' && !inDouble:
			inSingle = !inSingle
			b.WriteByte(ch)
		case ch == '"' && !inSingle:
			inDouble = !inDouble
			b.WriteByte(ch)
		case ch == '?' && !inSingle && !inDouble && ai < len(args):
			b.WriteString(sqlLiteral(args[ai]))
			ai++
		default:
			b.WriteByte(ch)
		}
	}
	return b.String()
}

func execSQL(ctx context.Context, d *sql.DB, query string, args ...interface{}) (*DbQueryResult, error) {
	res := &DbQueryResult{}
	if isSelectLike(query) {
		rows, err := d.QueryContext(ctx, query, args...)
		if err != nil {
			return res, err
		}
		defer rows.Close()
		cols, err := rows.Columns()
		if err != nil {
			return res, err
		}
		res.Columns = cols
		for rows.Next() {
			raw := make([]sql.RawBytes, len(cols))
			ptrs := make([]interface{}, len(cols))
			for i := range raw {
				ptrs[i] = &raw[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				return res, err
			}
			cells := make([]string, len(cols))
			rowNull := make([]bool, len(cols))
			for i, b := range raw {
				if b == nil {
					cells[i] = ""
					rowNull[i] = true
				} else {
					cells[i] = string(b)
				}
			}
			res.Rows = append(res.Rows, cells)
			res.Nulls = append(res.Nulls, rowNull)
			if len(res.Rows) >= dbMaxRows {
				// 再看一眼是否真的还有行：正好 dbMaxRows 行时不应误报截断
				if rows.Next() {
					res.Truncated = true
				}
				break
			}
		}
		res.RowCount = len(res.Rows)
		res.Success = true
		return res, rows.Err()
	}
	r, err := d.ExecContext(ctx, query, args...)
	if err != nil {
		return res, err
	}
	n, _ := r.RowsAffected()
	res.Affected = n
	res.Message = fmt.Sprintf("OK, %d row(s) affected", n)
	res.Success = true
	return res, nil
}

func execRedis(ctx context.Context, client *redis.Client, query string) (*DbQueryResult, error) {
	res := &DbQueryResult{}
	parts := strings.Fields(query)
	if len(parts) == 0 {
		return res, fmt.Errorf("命令为空")
	}
	args := make([]interface{}, len(parts))
	for i, p := range parts {
		args[i] = p
	}
	cmd := client.Do(ctx, args...)
	val, err := cmd.Result()
	if err != nil {
		return res, err
	}
	res.Message = redisValueToString(val)
	res.Success = true
	return res, nil
}

// redisValueToString 递归把 Redis 回复（RESP）转成可读字符串。
func redisValueToString(v interface{}) string {
	switch t := v.(type) {
	case nil:
		return "(nil)"
	case string:
		return t
	case []byte:
		return string(t)
	case int64:
		return fmt.Sprintf("%d", t)
	case float64:
		return fmt.Sprintf("%v", t)
	case []interface{}:
		var sb strings.Builder
		for i, item := range t {
			if i > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(fmt.Sprintf("%d) %s", i+1, redisValueToString(item)))
		}
		return sb.String()
	case map[string]interface{}:
		var sb strings.Builder
		i := 0
		for k, val := range t {
			if i > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(fmt.Sprintf("%s: %s", k, redisValueToString(val)))
			i++
		}
		return sb.String()
	default:
		return fmt.Sprintf("%v", v)
	}
}
