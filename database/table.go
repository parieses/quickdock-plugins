package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// ---- 对象列表（库下的表 / 视图） ----

// DbObject 库下的一个对象（表或视图）。Rows = -1 表示未知（视图 / SQLite）。
type DbObject struct {
	Name      string `json:"name"`
	Type      string `json:"type"` // table | view
	Engine    string `json:"engine"`
	Rows      int64  `json:"rows"`
	Comment   string `json:"comment"`
	Collation string `json:"collation"`
}

// handleTableList 列出某个库下的全部表 / 视图（含引擎、行数、注释等元信息）。
func handleTableList(id int64, input map[string]interface{}) {
	cid := strFrom(input, "id")
	dbName := strFrom(input, "database")
	if cid == "" {
		respondError(id, -32602, "缺少连接 id")
		return
	}
	saved, conn, err := openSavedWithDB(cid, dbName)
	if err != nil {
		respondError(id, -32000, err.Error())
		return
	}
	defer conn.close()
	if conn.kind == "redis" {
		respondError(id, -32602, "Redis 连接请使用键列表")
		return
	}
	db := orDefault(dbName, saved.Database)
	if conn.kind == "mysql" && strings.TrimSpace(db) == "" {
		respond(id, map[string]interface{}{"objects": []DbObject{}, "database": "", "error": "未选择数据库"})
		return
	}
	list, err := listDbObjects(conn.sqlDB, conn.kind, db)
	if err != nil {
		respond(id, map[string]interface{}{"objects": []DbObject{}, "database": db, "error": err.Error()})
		return
	}
	respond(id, map[string]interface{}{"objects": list, "database": db})
}

func listDbObjects(d *sql.DB, kind, dbName string) ([]DbObject, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dbQueryTimeout)
	defer cancel()
	out := []DbObject{}
	if kind == "sqlite" {
		rows, err := d.QueryContext(ctx,
			"SELECT name, type FROM sqlite_master WHERE type IN ('table','view') AND name NOT LIKE 'sqlite_%' ORDER BY name")
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var name, t string
			if err := rows.Scan(&name, &t); err != nil {
				continue
			}
			out = append(out, DbObject{Name: name, Type: t, Engine: "SQLite", Rows: -1})
		}
		return out, rows.Err()
	}
	q := "SELECT TABLE_NAME, TABLE_TYPE, IFNULL(ENGINE,''), IFNULL(TABLE_ROWS,0), " +
		"IFNULL(TABLE_COMMENT,''), IFNULL(TABLE_COLLATION,'') " +
		"FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? ORDER BY TABLE_NAME"
	rows, err := d.QueryContext(ctx, q, dbName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var name, ttype, engine, comment, coll string
		var n int64
		if err := rows.Scan(&name, &ttype, &engine, &n, &comment, &coll); err != nil {
			continue
		}
		o := DbObject{Name: name, Type: "table", Engine: engine, Rows: n, Comment: comment, Collation: coll}
		if strings.EqualFold(ttype, "VIEW") {
			o.Type = "view"
			o.Rows = -1
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// ---- 表结构 ----

type DbColumn struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Nullable bool   `json:"nullable"`
	Key      string `json:"key"` // PRI / UNI / MUL / ""
	Default  string `json:"default"`
	HasDef   bool   `json:"hasDefault"`
	Extra    string `json:"extra"`
	Comment  string `json:"comment"`
}

type DbIndex struct {
	Name    string   `json:"name"`
	Unique  bool     `json:"unique"`
	Type    string   `json:"type"` // PRIMARY / UNIQUE / INDEX / BTREE…
	Columns []string `json:"columns"`
}

type DbForeignKey struct {
	Column    string `json:"column"`
	RefTable  string `json:"refTable"`
	RefColumn string `json:"refColumn"`
	OnUpdate  string `json:"onUpdate"`
	OnDelete  string `json:"onDelete"`
}

// TableStructure 建表信息：字段 + 索引 + 外键 + 建表语句。
type TableStructure struct {
	Columns   []DbColumn     `json:"columns"`
	Indexes   []DbIndex      `json:"indexes"`
	Fks       []DbForeignKey `json:"fks"`
	Ddl       string         `json:"ddl"`
	Engine    string         `json:"engine"`
	Collation string         `json:"collation"`
	Comment   string         `json:"comment"`
	RowCount  int64          `json:"rowCount"` // -1 未知
}

// handleTableStructure 返回一张表的完整建表信息。
func handleTableStructure(id int64, input map[string]interface{}) {
	cid := strFrom(input, "id")
	dbName := strFrom(input, "database")
	table := strFrom(input, "table")
	if cid == "" {
		respondError(id, -32602, "缺少连接 id")
		return
	}
	if table == "" {
		respondError(id, -32602, "缺少表名")
		return
	}
	saved, conn, err := openSavedWithDB(cid, dbName)
	if err != nil {
		respondError(id, -32000, err.Error())
		return
	}
	defer conn.close()
	if conn.kind == "redis" {
		respondError(id, -32602, "Redis 不支持表结构")
		return
	}
	st, err := tableStructure(conn.sqlDB, conn.kind, orDefault(dbName, saved.Database), table)
	if err != nil {
		respondError(id, -32000, err.Error())
		return
	}
	respond(id, st)
}

func tableStructure(d *sql.DB, kind, dbName, table string) (*TableStructure, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dbQueryTimeout)
	defer cancel()
	st := &TableStructure{Columns: []DbColumn{}, Indexes: []DbIndex{}, Fks: []DbForeignKey{}, RowCount: -1}
	if kind == "mysql" {
		// 字段
		q := `SELECT COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE, IFNULL(COLUMN_KEY,''), COLUMN_DEFAULT,
		             IFNULL(EXTRA,''), IFNULL(COLUMN_COMMENT,'')
		      FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?
		      ORDER BY ORDINAL_POSITION`
		rows, err := d.QueryContext(ctx, q, dbName, table)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var c DbColumn
			var nullable string
			var def sql.NullString
			if err := rows.Scan(&c.Name, &c.Type, &nullable, &c.Key, &def, &c.Extra, &c.Comment); err != nil {
				continue
			}
			c.Nullable = strings.EqualFold(nullable, "YES")
			c.Default, c.HasDef = def.String, def.Valid
			st.Columns = append(st.Columns, c)
		}
		rows.Close()

		// 索引（按 INDEX_NAME 聚合列）
		iq := `SELECT INDEX_NAME, NON_UNIQUE, IFNULL(INDEX_TYPE,''), COLUMN_NAME
		       FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?
		       ORDER BY INDEX_NAME, SEQ_IN_INDEX`
		irows, err := d.QueryContext(ctx, iq, dbName, table)
		if err == nil {
			idx := map[string]*DbIndex{}
			order := []string{}
			for irows.Next() {
				var name, itype, col string
				var nonUnique int
				if err := irows.Scan(&name, &nonUnique, &itype, &col); err != nil {
					continue
				}
				it, ok := idx[name]
				if !ok {
					it = &DbIndex{Name: name, Unique: nonUnique == 0, Type: itype}
					if strings.EqualFold(name, "PRIMARY") {
						it.Type = "PRIMARY"
					}
					idx[name] = it
					order = append(order, name)
				}
				it.Columns = append(it.Columns, col)
			}
			irows.Close()
			for _, n := range order {
				st.Indexes = append(st.Indexes, *idx[n])
			}
		}

		// 外键
		fq := `SELECT k.COLUMN_NAME, k.REFERENCED_TABLE_NAME, k.REFERENCED_COLUMN_NAME,
		              IFNULL(r.UPDATE_RULE,''), IFNULL(r.DELETE_RULE,'')
		       FROM information_schema.KEY_COLUMN_USAGE k
		       JOIN information_schema.REFERENTIAL_CONSTRAINTS r
		         ON r.CONSTRAINT_NAME = k.CONSTRAINT_NAME AND r.CONSTRAINT_SCHEMA = k.CONSTRAINT_SCHEMA
		       WHERE k.TABLE_SCHEMA = ? AND k.TABLE_NAME = ? AND k.REFERENCED_TABLE_NAME IS NOT NULL`
		frows, err := d.QueryContext(ctx, fq, dbName, table)
		if err == nil {
			for frows.Next() {
				var f DbForeignKey
				if err := frows.Scan(&f.Column, &f.RefTable, &f.RefColumn, &f.OnUpdate, &f.OnDelete); err != nil {
					continue
				}
				st.Fks = append(st.Fks, f)
			}
			frows.Close()
		}

		// 建表语句 + 引擎 / 排序规则 / 注释
		qualified := quoteIdent("mysql", table)
		if strings.TrimSpace(dbName) != "" {
			qualified = quoteIdent("mysql", dbName) + "." + qualified
		}
		var tname, ddl string
		if err := d.QueryRowContext(ctx, "SHOW CREATE TABLE "+qualified).Scan(&tname, &ddl); err == nil {
			st.Ddl = ddl
		}
		var engine, coll, comment sql.NullString
		mq := `SELECT IFNULL(ENGINE,''), IFNULL(TABLE_COLLATION,''), IFNULL(TABLE_COMMENT,''), IFNULL(TABLE_ROWS,-1)
		       FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?`
		if err := d.QueryRowContext(ctx, mq, dbName, table).Scan(&engine, &coll, &comment, &st.RowCount); err == nil {
			st.Engine, st.Collation, st.Comment = engine.String, coll.String, comment.String
		}
		return st, nil
	}

	// SQLite
	rows, err := d.QueryContext(ctx, "PRAGMA table_info("+quoteIdent("sqlite", table)+")")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			continue
		}
		c := DbColumn{Name: name, Type: ctype, Default: dflt.String, HasDef: dflt.Valid}
		// SQLite 的 `id INTEGER PRIMARY KEY` 在 PRAGMA 里 notnull 仍为 0（隐式非空），
		// 主键列一律按非空展示，与常见客户端一致
		c.Nullable = (notnull == 0 && pk == 0)
		if pk > 0 {
			c.Key = "PRI"
		}
		st.Columns = append(st.Columns, c)
	}
	rows.Close()

	// 索引：index_list + index_info
	il, err := d.QueryContext(ctx, "PRAGMA index_list("+quoteIdent("sqlite", table)+")")
	if err == nil {
		type idxRef struct {
			name   string
			unique bool
		}
		var refs []idxRef
		for il.Next() {
			var seq, unique, partial int
			var name, origin string
			if err := il.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
				continue
			}
			refs = append(refs, idxRef{name: name, unique: unique == 1})
		}
		il.Close()
		for _, r := range refs {
			it := DbIndex{Name: r.name, Unique: r.unique, Type: "INDEX", Columns: []string{}}
			irows, err := d.QueryContext(ctx, "PRAGMA index_info("+quoteIdent("sqlite", r.name)+")")
			if err != nil {
				continue
			}
			for irows.Next() {
				var seqno, cid int
				var name sql.NullString
				if err := irows.Scan(&seqno, &cid, &name); err != nil {
					continue
				}
				it.Columns = append(it.Columns, name.String)
			}
			irows.Close()
			st.Indexes = append(st.Indexes, it)
		}
	}

	// 外键
	fk, err := d.QueryContext(ctx, "PRAGMA foreign_key_list("+quoteIdent("sqlite", table)+")")
	if err == nil {
		for fk.Next() {
			var id, seq int
			var refTable, from, to, onUpdate, onDelete, match string
			if err := fk.Scan(&id, &seq, &refTable, &from, &to, &onUpdate, &onDelete, &match); err != nil {
				continue
			}
			st.Fks = append(st.Fks, DbForeignKey{Column: from, RefTable: refTable, RefColumn: to, OnUpdate: onUpdate, OnDelete: onDelete})
		}
		fk.Close()
	}

	var ddl sql.NullString
	if err := d.QueryRowContext(ctx,
		"SELECT sql FROM sqlite_master WHERE name = ? AND type IN ('table','view')", table).Scan(&ddl); err == nil {
		st.Ddl = ddl.String
		st.Engine = "SQLite"
	}
	return st, nil
}

// ---- 表数据（分页 + 排序） ----

// handleTableRows 分页读取一张表的全部字段，并附带主键信息（供前端内联编辑）。
func handleTableRows(id int64, input map[string]interface{}) {
	cid := strFrom(input, "id")
	dbName := strFrom(input, "database")
	table := strFrom(input, "table")
	if cid == "" {
		respondError(id, -32602, "缺少连接 id")
		return
	}
	if table == "" {
		respondError(id, -32602, "缺少表名")
		return
	}
	page := intFrom(input, "page", 1)
	if page < 1 {
		page = 1
	}
	size := intFrom(input, "pageSize", 100)
	if size < 10 {
		size = 10
	}
	if size > 1000 {
		size = 1000
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

	tbl := quoteIdent(conn.kind, table)
	q := "SELECT * FROM " + tbl
	if orderBy != "" {
		q += " ORDER BY " + quoteIdent(conn.kind, orderBy) + " " + strings.ToUpper(orderDir)
	}
	q += fmt.Sprintf(" LIMIT %d OFFSET %d", size, (page-1)*size)

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), dbQueryTimeout)
	defer cancel()
	res, err := execSQL(ctx, conn.sqlDB, q)
	res.DurationMs = time.Since(start).Milliseconds()
	res.Page, res.PageSize, res.OrderBy, res.OrderDir = page, size, orderBy, orderDir
	res.Total = -1
	if err != nil {
		res.Success = false
		res.Error = err.Error()
		respond(id, res)
		return
	}
	res.Success = true

	// 总行数：单独短超时，慢表就放弃（前端显示「?」而不是卡住）
	cctx, ccancel := context.WithTimeout(context.Background(), 8*time.Second)
	var total int64
	if e := conn.sqlDB.QueryRowContext(cctx, "SELECT COUNT(*) FROM "+tbl).Scan(&total); e == nil {
		res.Total = total
	}
	ccancel()

	// 主键 → 是否可内联编辑
	pk, reason := primaryKeyOf(conn.sqlDB, conn.kind, effDB, table)
	if pk == "" {
		res.EditReason = reason
	} else {
		has := false
		for _, c := range res.Columns {
			if strings.EqualFold(c, pk) {
				has = true
				break
			}
		}
		if has {
			res.Editable, res.TableName, res.PrimaryKey = true, table, pk
		} else {
			res.EditReason = "主键列「" + pk + "」未包含在结果中"
		}
	}
	respond(id, res)
}
