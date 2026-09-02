package main

import (
	"context"
	"fmt"
	"strings"
	"time"
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
	saved, conn, err := openSaved(cid)
	if err != nil {
		if conn == nil && saved != nil {
			respond(id, &DbQueryResult{Success: false, Error: err.Error()})
			return
		}
		respondError(id, -32000, err.Error())
		return
	}
	defer conn.close()

	start := time.Now()
	res, err := conn.exec(query)
	res.DurationMs = time.Since(start).Milliseconds()
	if err != nil {
		res.Success = false
		res.Error = err.Error()
		respond(id, res)
		return
	}
	res.Success = true
	// 单表 SELECT 且含主键列时，标记结果可编辑（供前端网格内联修改）。
	if conn.kind != "redis" {
		enrichEditable(conn, saved.Database, query, res)
	}
	respond(id, res)
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
		keys, e := listRedisKeys(conn.redis, parseRedisDB(saved.Database),
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
	_, conn, err := openSaved(cid)
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
