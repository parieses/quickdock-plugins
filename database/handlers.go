package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

// handleConnList 列出全部连接，password 脱敏（返回空串，避免泄露密文）。
func handleConnList(id int64, input map[string]interface{}) {
	list, err := listConnections()
	if err != nil {
		respondError(id, -32000, err.Error())
		return
	}
	for i := range list {
		list[i].Password = ""
	}
	respond(id, map[string]interface{}{"connections": list})
}

// handleConnSave 新建或更新连接。密码明文入参，落库前加密；更新时空密码表示保留原值。
func handleConnSave(id int64, input map[string]interface{}) {
	in := connInputFrom(input)
	if in.DbType == "" {
		in.DbType = "mysql"
	}
	switch in.DbType {
	case "mysql", "redis", "sqlite":
	default:
		respondError(id, -32602, "不支持的数据库类型: "+in.DbType)
		return
	}
	if in.Name == "" {
		in.Name = in.Host
	}
	if in.Name == "" && in.DbType == "sqlite" {
		in.Name = in.FilePath
	}
	if in.Port == 0 {
		in.Port = defaultPort(in.DbType)
	}

	rec := &DbConnection{
		ID:       in.ID,
		Name:     strings.TrimSpace(in.Name),
		DbType:   in.DbType,
		Host:     strings.TrimSpace(in.Host),
		Port:     in.Port,
		Username: in.Username,
		Database: strings.TrimSpace(in.Database),
		FilePath: strings.TrimSpace(in.FilePath),
	}

	if in.ID != "" {
		existing, err := getConnection(in.ID)
		if err != nil {
			respondError(id, -32000, err.Error())
			return
		}
		rec.Password = existing.Password
		rec.CreatedAt = existing.CreatedAt
		if in.Password != "" {
			enc, e := encryptSecret(in.Password)
			if e != nil {
				respondError(id, -32000, "密码加密失败: "+e.Error())
				return
			}
			rec.Password = enc
		}
		if err := updateConnection(rec); err != nil {
			respondError(id, -32000, err.Error())
			return
		}
		// 主机 / 账号 / 密码 / 默认库可能已改，丢弃旧连接缓存
		invalidateConnCache(rec.ID)
	} else {
		if in.Password != "" {
			enc, e := encryptSecret(in.Password)
			if e != nil {
				respondError(id, -32000, "密码加密失败: "+e.Error())
				return
			}
			rec.Password = enc
		}
		if err := createConnection(rec); err != nil {
			respondError(id, -32000, err.Error())
			return
		}
		invalidateConnCache(rec.ID)
	}
	rec.Password = ""
	respond(id, map[string]interface{}{"connection": rec})
}

// handleConnDelete 删除连接。
func handleConnDelete(id int64, input map[string]interface{}) {
	cid := strFrom(input, "id")
	if cid == "" {
		respondError(id, -32602, "缺少连接 id")
		return
	}
	if err := deleteConnection(cid); err != nil {
		respondError(id, -32000, err.Error())
		return
	}
	invalidateConnCache(cid)
	respond(id, map[string]interface{}{"ok": true})
}

// handleConnTest 用传入配置（未保存也可）测试连通性；也支持只传 id 测已保存连接。
func handleConnTest(id int64, input map[string]interface{}) {
	in := connInputFrom(input)
	// 只给了 id（或编辑时留空密码）→ 用已保存配置补齐
	if in.ID != "" {
		if saved, err := getConnection(in.ID); err == nil {
			if in.DbType == "" {
				in = inputFromSaved(saved)
			} else if in.Password == "" {
				in.Password = inputFromSaved(saved).Password
			}
		}
	}
	conn, err := toDbConn(in)
	if err != nil {
		respond(id, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	defer conn.close()
	if err := conn.ping(); err != nil {
		respond(id, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	respond(id, map[string]interface{}{"ok": true})
}

// handleQueryRun 对已保存连接执行 SQL / Redis 命令，返回列 + 行（+ 可编辑元数据）。
func handleQueryRun(id int64, input map[string]interface{}) {
	cid := strFrom(input, "id")
	query := rawStrFrom(input, "query")
	if strings.TrimSpace(query) == "" {
		respondError(id, -32602, "查询内容为空")
		return
	}
	if cid == "" {
		respondError(id, -32602, "缺少连接 id")
		return
	}
	// 库上下文：前端选中的库优先，未选则回落到连接自身保存的默认库。
	// 会话被限定在该库内后，查询只写表名即可，无需「库名.表名」。
	dbCtx := strFrom(input, "database")
	saved, conn, err := openSavedWithDB(cid, dbCtx)
	if err != nil {
		if conn == nil && saved != nil {
			respond(id, &DbQueryResult{Success: false, Error: err.Error()})
			return
		}
		respondError(id, -32000, err.Error())
		return
	}
	defer conn.close()
	if dbCtx == "" {
		dbCtx = saved.Database
	}

	start := time.Now()
	multi, err := conn.execMulti(query, dbCtx)
	if err != nil {
		respond(id, &DbQueryMultiResult{
			Success: false,
			Error:   err.Error(),
		})
		return
	}
	multi.DurationMs = time.Since(start).Milliseconds()
	respond(id, multi)
}

// isNoDatabaseErr 判断是否为 MySQL「未选择数据库」错误（Error 1046）。
func isNoDatabaseErr(err error) bool {
	if err == nil {
		return false
	}
	if strings.Contains(strings.ToLower(err.Error()), "no database selected") {
		return true
	}
	var me *mysql.MySQLError
	return errors.As(err, &me) && me.Number == 1046
}

// handleTreeList 返回连接下的库列表（SQL）或键树（Redis）。
func handleTreeList(id int64, input map[string]interface{}) {
	cid := strFrom(input, "id")
	if cid == "" {
		respondError(id, -32602, "缺少连接 id")
		return
	}
	saved, conn, err := openSaved(cid)
	if err != nil {
		respondError(id, -32000, err.Error())
		return
	}
	defer conn.close()

	switch conn.kind {
	case "mysql", "sqlite":
		tree, e := listSqlDatabases(conn.sqlDB, conn.kind, saved.Database)
		if e != nil {
			respondError(id, -32000, "读取库列表失败: "+e.Error())
			return
		}
		respond(id, map[string]interface{}{"nodes": tree, "dbType": conn.kind})
	case "redis":
		dbIndex := parseRedisDB(saved.Database)
		if v, ok := input["dbIndex"]; ok {
			if n, ok := v.(float64); ok && int(n) >= 0 {
				dbIndex = int(n)
			}
		}
		keys, e := listRedisKeys(conn.redis, dbIndex,
			strFrom(input, "pattern"), intFrom(input, "limit", 300))
		if e != nil {
			respondError(id, -32000, "读取键失败: "+e.Error())
			return
		}
		respond(id, map[string]interface{}{"nodes": keys, "dbType": conn.kind})
	default:
		respond(id, map[string]interface{}{"nodes": []DbTreeNode{}, "dbType": conn.kind})
	}
}

// handleTreeObjects 返回某个库下的表/视图（含字段），用于树展开时按需加载。
func handleTreeObjects(id int64, input map[string]interface{}) {
	cid := strFrom(input, "id")
	dbName := strFrom(input, "database")
	if cid == "" {
		respondError(id, -32602, "缺少连接 id")
		return
	}
	_, conn, err := openSaved(cid)
	if err != nil {
		respondError(id, -32000, err.Error())
		return
	}
	defer conn.close()
	if conn.kind == "mysql" || conn.kind == "sqlite" {
		tree, e := listSchemaTree(conn.sqlDB, conn.kind, dbName)
		if e != nil {
			respondError(id, -32000, "读取表结构失败: "+e.Error())
			return
		}
		respond(id, map[string]interface{}{"nodes": tree})
		return
	}
	respond(id, map[string]interface{}{"nodes": []DbTreeNode{}})
}

// handleRowUpdate 以主键定位单行并提交修改（参数化，标识符按库类型安全引用）。
func handleRowUpdate(id int64, input map[string]interface{}) {
	cid := strFrom(input, "id")
	table := strFrom(input, "tableName")
	pkCol := strFrom(input, "pkColumn")
	pkVal := rawStrFrom(input, "pkValue")
	sets := strMapFrom(input, "sets")
	nulls := strSliceFrom(input, "nulls")

	if cid == "" {
		respondError(id, -32602, "缺少连接 id")
		return
	}
	if table == "" || pkCol == "" {
		respondError(id, -32602, "缺少表名或主键列")
		return
	}
	// 与 query.run 用同一「库上下文」，保证未带库名前缀的表名能落到正确的库里。
	_, conn, err := openSavedWithDB(cid, strFrom(input, "database"))
	if err != nil {
		respondError(id, -32000, err.Error())
		return
	}
	defer conn.close()
	if conn.kind == "redis" {
		respondError(id, -32000, "Redis 不支持行编辑")
		return
	}

	var setSQL []string
	var args []interface{}
	for col, val := range sets {
		setSQL = append(setSQL, quoteIdent(conn.kind, col)+" = ?")
		args = append(args, val)
	}
	for _, col := range nulls {
		setSQL = append(setSQL, quoteIdent(conn.kind, col)+" = NULL")
	}
	if len(setSQL) == 0 {
		respond(id, map[string]interface{}{"affected": 0, "message": "no changes"})
		return
	}
	q := "UPDATE " + quoteIdent(conn.kind, table) +
		" SET " + strings.Join(setSQL, ", ") +
		" WHERE " + quoteIdent(conn.kind, pkCol) + " = ?"
	args = append(args, pkVal)

	ctx, cancel := context.WithTimeout(context.Background(), dbQueryTimeout)
	defer cancel()
	r, err := conn.sqlDB.ExecContext(ctx, q, args...)
	if err != nil {
		respondError(id, -32000, "更新失败: "+err.Error())
		return
	}
	n, _ := r.RowsAffected()
	respond(id, map[string]interface{}{
		"affected": n,
		"message":  fmt.Sprintf("OK, %d row(s) affected", n),
	})
}

// handleRowInsert 整行插入：仅写入非空的字段（其余交给数据库默认值 / 自增）。
func handleRowInsert(id int64, input map[string]interface{}) {
	cid := strFrom(input, "id")
	table := strFrom(input, "tableName")
	values := strMapFrom(input, "values")
	if cid == "" || table == "" {
		respondError(id, -32602, "缺少连接 id 或表名")
		return
	}
	_, conn, err := openSavedWithDB(cid, strFrom(input, "database"))
	if err != nil {
		respondError(id, -32000, err.Error())
		return
	}
	defer conn.close()
	if conn.kind == "redis" {
		respondError(id, -32000, "Redis 不支持行编辑")
		return
	}
	var cols []string
	var args []interface{}
	for col, val := range values {
		cols = append(cols, quoteIdent(conn.kind, col))
		args = append(args, val)
	}
	if len(cols) == 0 {
		respondError(id, -32602, "没有可插入的字段（所有字段都留空了）")
		return
	}
	ph := strings.TrimSuffix(strings.Repeat("?, ", len(cols)), ", ")
	q := "INSERT INTO " + quoteIdent(conn.kind, table) +
		" (" + strings.Join(cols, ", ") + ") VALUES (" + ph + ")"

	ctx, cancel := context.WithTimeout(context.Background(), dbQueryTimeout)
	defer cancel()
	r, err := conn.sqlDB.ExecContext(ctx, q, args...)
	if err != nil {
		respondError(id, -32000, "插入失败: "+err.Error())
		return
	}
	n, _ := r.RowsAffected()
	respond(id, map[string]interface{}{
		"affected": n,
		"message":  fmt.Sprintf("OK, %d row(s) inserted", n),
	})
}

// handleRowDelete 以主键定位单行并删除。
func handleRowDelete(id int64, input map[string]interface{}) {
	cid := strFrom(input, "id")
	table := strFrom(input, "tableName")
	pkCol := strFrom(input, "pkColumn")
	pkVal := rawStrFrom(input, "pkValue")
	if cid == "" || table == "" || pkCol == "" {
		respondError(id, -32602, "缺少连接 id / 表名 / 主键列")
		return
	}
	_, conn, err := openSavedWithDB(cid, strFrom(input, "database"))
	if err != nil {
		respondError(id, -32000, err.Error())
		return
	}
	defer conn.close()
	if conn.kind == "redis" {
		respondError(id, -32000, "Redis 不支持行编辑")
		return
	}
	q := "DELETE FROM " + quoteIdent(conn.kind, table) +
		" WHERE " + quoteIdent(conn.kind, pkCol) + " = ?"

	ctx, cancel := context.WithTimeout(context.Background(), dbQueryTimeout)
	defer cancel()
	r, err := conn.sqlDB.ExecContext(ctx, q, pkVal)
	if err != nil {
		respondError(id, -32000, "删除失败: "+err.Error())
		return
	}
	n, _ := r.RowsAffected()
	respond(id, map[string]interface{}{
		"affected": n,
		"message":  fmt.Sprintf("OK, %d row(s) deleted", n),
	})
}

// handleRowPkList 按数据页当前的筛选 / 排序收集「全部匹配行」的主键值，
// 供前端「全选匹配筛选的行」使用（只取主键一列，避免把整表数据拉回来）。
func handleRowPkList(id int64, input map[string]interface{}) {
	cid := strFrom(input, "id")
	dbName := strFrom(input, "database")
	table := strFrom(input, "table")
	if cid == "" || table == "" {
		respondError(id, -32602, "缺少连接 id / 表名")
		return
	}
	limit := intFrom(input, "limit", 50000)
	if limit < 1 {
		limit = 1
	}
	if limit > 100000 {
		limit = 100000
	}
	orderBy := strFrom(input, "orderBy")
	orderDir := strings.ToLower(strFrom(input, "orderDir"))
	if orderDir != "desc" {
		orderDir = "asc"
	}

	_, conn, err := openSavedWithDB(cid, dbName)
	if err != nil {
		respondError(id, -32000, err.Error())
		return
	}
	defer conn.close()
	if conn.kind == "redis" {
		respondError(id, -32602, "Redis 不支持表数据")
		return
	}
	effDB := dbName
	if conn.kind == "mysql" && strings.TrimSpace(effDB) == "" {
		if saved, e := getConnection(cid); e == nil {
			effDB = saved.Database
		}
	}

	// 与 table.rows 完全一致的列级筛选：col LIKE '%值%'，多列 AND
	filters := strMapFrom(input, "filters")
	var conds []string
	var fargs []interface{}
	for col, val := range filters {
		if strings.TrimSpace(val) == "" {
			continue
		}
		conds = append(conds, quoteIdent(conn.kind, col)+" LIKE ?")
		fargs = append(fargs, "%"+val+"%")
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	tbl := quoteIdent(conn.kind, table)

	pk, pkReason := primaryKeyOf(conn.sqlDB, conn.kind, effDB, table)
	if pk == "" {
		respondError(id, -32000, "无法定位主键，不能全选删除: "+pkReason)
		return
	}

	ordCol, ordDir := orderBy, strings.ToUpper(orderDir)
	if ordCol == "" {
		ordCol, ordDir = pk, "ASC"
	}
	// 多取一行用于判断是否触顶截断
	q := "SELECT " + quoteIdent(conn.kind, pk) + " FROM " + tbl + where +
		" ORDER BY " + quoteIdent(conn.kind, ordCol) + " " + ordDir +
		fmt.Sprintf(" LIMIT %d", limit+1)

	ctx, cancel := context.WithTimeout(context.Background(), dbQueryTimeout)
	defer cancel()
	rows, err := conn.sqlDB.QueryContext(ctx, q, fargs...)
	if err != nil {
		respondError(id, -32000, "查询失败: "+err.Error())
		return
	}
	defer rows.Close()
	values := make([]string, 0, 256)
	for rows.Next() {
		var v interface{}
		if e := rows.Scan(&v); e != nil {
			respondError(id, -32000, "读取失败: "+e.Error())
			return
		}
		switch x := v.(type) {
		case nil:
			values = append(values, "")
		case []byte:
			values = append(values, string(x))
		case string:
			values = append(values, x)
		default:
			values = append(values, fmt.Sprintf("%v", x))
		}
	}
	if e := rows.Err(); e != nil {
		respondError(id, -32000, "读取失败: "+e.Error())
		return
	}
	truncated := false
	if len(values) > limit {
		values = values[:limit]
		truncated = true
	}

	// 匹配总数：单独短超时，慢表就放弃（前端显示「?」）
	var total int64 = -1
	cctx, ccancel := context.WithTimeout(context.Background(), 8*time.Second)
	if e := conn.sqlDB.QueryRowContext(cctx, "SELECT COUNT(*) FROM "+tbl+where, fargs...).Scan(&total); e != nil {
		total = -1
	}
	ccancel()

	respond(id, map[string]interface{}{
		"pkColumn":  pk,
		"values":    values,
		"total":     total,
		"truncated": truncated,
	})
}

// handleRowDeleteMany 按主键批量删除（分片 IN + 单事务），
// 供前端「删除所选 / 全选匹配筛选的行」使用。
func handleRowDeleteMany(id int64, input map[string]interface{}) {
	cid := strFrom(input, "id")
	table := strFrom(input, "tableName")
	pkCol := strFrom(input, "pkColumn")
	if cid == "" || table == "" || pkCol == "" {
		respondError(id, -32602, "缺少连接 id / 表名 / 主键列")
		return
	}
	raw, _ := input["pkValues"].([]interface{})
	values := make([]string, 0, len(raw))
	for _, v := range raw {
		if v == nil {
			continue
		}
		switch x := v.(type) {
		case string:
			values = append(values, x)
		case []byte:
			values = append(values, string(x))
		default:
			values = append(values, fmt.Sprintf("%v", x))
		}
	}
	if len(values) == 0 {
		respondError(id, -32602, "pkValues 为空")
		return
	}
	if len(values) > 20000 {
		respondError(id, -32602, "单次最多删除 20000 行，请分批")
		return
	}

	_, conn, err := openSavedWithDB(cid, strFrom(input, "database"))
	if err != nil {
		respondError(id, -32000, err.Error())
		return
	}
	defer conn.close()
	if conn.kind == "redis" {
		respondError(id, -32000, "Redis 不支持行编辑")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	tx, err := conn.sqlDB.BeginTx(ctx, nil)
	if err != nil {
		respondError(id, -32000, "开启事务失败: "+err.Error())
		return
	}
	var affected int64
	const chunk = 500 // SQLite 默认变量上限 999，留足余量
	for i := 0; i < len(values); i += chunk {
		end := i + chunk
		if end > len(values) {
			end = len(values)
		}
		part := values[i:end]
		ph := make([]string, len(part))
		args := make([]interface{}, len(part))
		for j, v := range part {
			ph[j] = "?"
			args[j] = v
		}
		q := "DELETE FROM " + quoteIdent(conn.kind, table) +
			" WHERE " + quoteIdent(conn.kind, pkCol) + " IN (" + strings.Join(ph, ",") + ")"
		r, err := tx.ExecContext(ctx, q, args...)
		if err != nil {
			tx.Rollback()
			respondError(id, -32000, "删除失败: "+err.Error())
			return
		}
		n, _ := r.RowsAffected()
		affected += n
	}
	if err := tx.Commit(); err != nil {
		respondError(id, -32000, "提交失败: "+err.Error())
		return
	}
	respond(id, map[string]interface{}{
		"affected": affected,
		"message":  fmt.Sprintf("OK, %d row(s) deleted", affected),
	})
}
