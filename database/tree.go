package main

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/redis/go-redis/v9"
)

// DbTreeNode 库表浏览器树节点（SQL：库→表/视图→字段；Redis：目录→键）。
type DbTreeNode struct {
	Name     string       `json:"name"`
	Kind     string       `json:"kind"`             // database | folder | table | view | column | key
	Detail   string       `json:"detail"`           // 字段类型 / Redis 类型 / 计数
	Label    string       `json:"label,omitempty"`  // 显示名（Redis 键在目录层级下只显示末段，Name 仍为完整 key）
	Children []DbTreeNode `json:"children,omitempty"`
}

// listSqlDatabases 列出 SQL 连接下「当前有权限的库」。
func listSqlDatabases(d *sql.DB, kind, defaultDb string) ([]DbTreeNode, error) {
	if kind == "mysql" {
		ctx, cancel := context.WithTimeout(context.Background(), dbQueryTimeout)
		defer cancel()
		rows, err := d.QueryContext(ctx, "SHOW DATABASES")
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		tree := []DbTreeNode{}
		for rows.Next() {
			var dbName string
			if err := rows.Scan(&dbName); err != nil {
				continue
			}
			tree = append(tree, DbTreeNode{Name: dbName, Kind: "database"})
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return tree, nil
	}
	// sqlite：单文件通常只有一个 main 库
	name := strings.TrimSpace(defaultDb)
	if name == "" {
		name = "main"
	}
	children, err := listSchemaTree(d, "sqlite", name)
	if err != nil {
		return nil, err
	}
	return []DbTreeNode{{Name: name, Kind: "database", Children: children}}, nil
}

// listSchemaTree 列出某个库下的 表/视图 及其字段（不含库层），用于按需加载。
func listSchemaTree(d *sql.DB, kind, dbName string) ([]DbTreeNode, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dbQueryTimeout)
	defer cancel()

	var tables, views []DbTreeNode
	if kind == "mysql" {
		q := "SHOW FULL TABLES FROM " + quoteIdent("mysql", dbName)
		rows, err := d.QueryContext(ctx, q)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var tbl, ttype string
			if err := rows.Scan(&tbl, &ttype); err != nil {
				return nil, err
			}
			if strings.EqualFold(ttype, "VIEW") {
				views = append(views, DbTreeNode{Name: tbl, Kind: "view"})
			} else {
				tables = append(tables, DbTreeNode{Name: tbl, Kind: "table"})
			}
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	} else {
		rows, err := d.QueryContext(ctx, "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var tbl string
			if err := rows.Scan(&tbl); err != nil {
				return nil, err
			}
			tables = append(tables, DbTreeNode{Name: tbl, Kind: "table"})
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		vrows, verr := d.QueryContext(ctx, "SELECT name FROM sqlite_master WHERE type='view' ORDER BY name")
		if verr == nil {
			defer vrows.Close()
			for vrows.Next() {
				var v string
				if err := vrows.Scan(&v); err != nil {
					continue
				}
				views = append(views, DbTreeNode{Name: v, Kind: "view"})
			}
		}
	}

	for i := range tables {
		tables[i].Children = columnsOf(d, kind, dbName, tables[i].Name)
	}
	for i := range views {
		views[i].Children = columnsOf(d, kind, dbName, views[i].Name)
	}

	tree := []DbTreeNode{}
	if len(tables) > 0 {
		tree = append(tree, DbTreeNode{Name: "tables", Kind: "folder", Children: tables})
	}
	if len(views) > 0 {
		tree = append(tree, DbTreeNode{Name: "views", Kind: "folder", Children: views})
	}
	return tree, nil
}

// columnsOf 列出某张表的字段（含类型）。
func columnsOf(d *sql.DB, kind, dbName, table string) []DbTreeNode {
	ctx, cancel := context.WithTimeout(context.Background(), dbQueryTimeout)
	defer cancel()
	var cols []DbTreeNode
	if kind == "mysql" {
		q := "SHOW COLUMNS FROM " + quoteIdent("mysql", dbName) + "." + quoteIdent("mysql", table)
		rows, err := d.QueryContext(ctx, q)
		if err != nil {
			return cols
		}
		defer rows.Close()
		for rows.Next() {
			var field, ctype, nullFlag string
			var key, def sql.NullString
			if err := rows.Scan(&field, &ctype, &nullFlag, &key, &def); err != nil {
				continue
			}
			cols = append(cols, DbTreeNode{Name: field, Kind: "column", Detail: ctype})
		}
	} else {
		rows, err := d.QueryContext(ctx, `PRAGMA table_info(`+quoteIdent("sqlite", table)+`)`)
		if err != nil {
			return cols
		}
		defer rows.Close()
		for rows.Next() {
			var cid int
			var name, ctype string
			var notnull int
			var dflt sql.NullString
			var pk int
			if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
				continue
			}
			cols = append(cols, DbTreeNode{Name: name, Kind: "column", Detail: ctype})
		}
	}
	return cols
}

// listRedisKeys 用 SCAN 列出键并按 ':' 分隔的前缀组成目录层级（大库上限保护）。
// pattern 为 SCAN 匹配模式（默认 *，前端可传 "user:*" 之类做前缀过滤）。
func listRedisKeys(client *redis.Client, dbIndex int, pattern string, limit int) ([]DbTreeNode, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dbQueryTimeout)
	defer cancel()
	if strings.TrimSpace(pattern) == "" {
		pattern = "*"
	}
	if limit <= 0 || limit > 3000 {
		limit = 300
	}

	type keyInfo struct{ name, kind string }
	var keys []keyInfo
	iter := client.Scan(ctx, 0, pattern, 200).Iterator()
	truncated := false
	for iter.Next(ctx) {
		if len(keys) >= limit {
			truncated = true
			break
		}
		k := iter.Val()
		kt, err := client.Type(ctx, k).Result()
		if err != nil {
			kt = "?"
		}
		keys = append(keys, keyInfo{name: k, kind: kt})
	}
	if err := iter.Err(); err != nil && len(keys) == 0 {
		return nil, err
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].name < keys[j].name })

	// 按 ':' 层级建目录树：user:1:name → user → 1 → name（叶子保留完整 key 便于点击查询）
	root := &redisNode{children: map[string]*redisNode{}}
	for _, k := range keys {
		segs := strings.Split(k.name, ":")
		cur := root
		for i, seg := range segs {
			if i == len(segs)-1 {
				cur.leaves = append(cur.leaves, DbTreeNode{Name: k.name, Kind: "key", Detail: k.kind})
				break
			}
			nx, ok := cur.children[seg]
			if !ok {
				nx = &redisNode{children: map[string]*redisNode{}}
				cur.children[seg] = nx
			}
			cur = nx
		}
	}

	label := fmt.Sprintf("DB %d", dbIndex)
	detail := fmt.Sprintf("%d keys", len(keys))
	if truncated {
		detail += "+（已截断）"
	}
	return []DbTreeNode{{
		Name: label, Kind: "folder", Detail: detail,
		Children: root.flatten(""),
	}}, nil
}

type redisNode struct {
	children map[string]*redisNode
	leaves   []DbTreeNode
}

// flatten 把前缀树转成 DbTreeNode 列表：叶子键显示完整 key 的最后一段以省空间。
func (n *redisNode) flatten(prefix string) []DbTreeNode {
	out := []DbTreeNode{}
	names := make([]string, 0, len(n.children))
	for k := range n.children {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, name := range names {
		child := n.children[name]
		kids := child.flatten(prefix + name + ":")
		out = append(out, DbTreeNode{
			Name: name, Kind: "folder", Detail: fmt.Sprintf("%d", len(kids)), Children: kids,
		})
	}
	for _, leaf := range n.leaves {
		// Name 保留完整 key（点击即以全名查询），Label 只显示当前层级下的末段
		short := strings.TrimPrefix(leaf.Name, prefix)
		out = append(out, DbTreeNode{Name: leaf.Name, Kind: "key", Detail: leaf.Detail, Label: short})
	}
	return out
}
