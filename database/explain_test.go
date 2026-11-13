package main

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

func TestExplainable(t *testing.T) {
	ok := []string{
		"SELECT * FROM users",
		"select 1",
		"WITH c AS (SELECT 1) SELECT * FROM c",
		"UPDATE users SET name='a' WHERE id=1",
		"DELETE FROM users WHERE id=1",
		"INSERT INTO users(name) VALUES('a')",
		"REPLACE INTO users(id,name) VALUES(1,'a')",
		"SELECT/* hint */ 1",
	}
	for _, s := range ok {
		if !explainable(s) {
			t.Errorf("应支持解释: %s", s)
		}
	}
	bad := []string{
		"", "SHOW TABLES", "DESCRIBE users", "DROP TABLE users",
		"SET @x=1", "USE db1", "CREATE TABLE t(a int)", "PRAGMA table_info(t)",
		"SELECTED nonsense",
	}
	for _, s := range bad {
		if explainable(s) {
			t.Errorf("不应支持解释: %q", s)
		}
	}
}

func TestExplainHintsMySQL(t *testing.T) {
	cols := []string{"id", "select_type", "table", "type", "possible_keys", "key", "rows", "Extra"}
	rows := [][]string{{
		"1", "SIMPLE", "orders", "ALL", "idx_user", "", "250000", "Using where; Using filesort; Using temporary",
	}}
	hints := explainHints("mysql", cols, rows)
	joined := ""
	for _, h := range hints {
		joined += h.Text + "\n"
	}
	for _, want := range []string{"全表扫描", "有可用索引却没用上", "额外排序", "临时表", "250000"} {
		if !strings.Contains(joined, want) {
			t.Errorf("缺少提示 %q，实际:\n%s", want, joined)
		}
	}
	for _, h := range hints {
		if h.Level != "warn" && h.Level != "info" {
			t.Errorf("意外提示级别: %+v", h)
		}
	}
}

func TestExplainHintsSQLite(t *testing.T) {
	cols := []string{"id", "parent", "notused", "detail"}
	rows := [][]string{
		{"3", "0", "0", "SCAN users"},
		{"8", "0", "0", "SEARCH orders USING INDEX idx_user (user_id=?)"},
		{"12", "0", "0", "USE TEMP B-TREE FOR ORDER BY"},
	}
	joined := ""
	for _, h := range explainHints("sqlite", cols, rows) {
		joined += h.Text + "\n"
	}
	for _, want := range []string{"全表扫描", "命中索引", "额外排序"} {
		if !strings.Contains(joined, want) {
			t.Errorf("缺少提示 %q，实际:\n%s", want, joined)
		}
	}
	clean := explainHints("sqlite", cols, [][]string{{"1", "0", "0", "SEARCH t USING INTEGER PRIMARY KEY (rowid=?)"}})
	if len(clean) != 1 || clean[0].Level != "ok" {
		t.Errorf("走索引的计划应给出 ok 提示，实际: %+v", clean)
	}
}

// queryRows 必须能读到 EXPLAIN 的结果集：这是 query.explain 的基础
// （isSelectLike 不认 "EXPLAIN QUERY PLAN"，所以命令不能复用 execSQL）。
func TestQueryRowsExplainSQLite(t *testing.T) {
	d, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err = d.Exec("CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT)"); err != nil {
		t.Fatal(err)
	}
	res, err := queryRows(context.Background(), d, "EXPLAIN QUERY PLAN SELECT * FROM users WHERE id = 1", 200)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Success || res.RowCount == 0 || len(res.Columns) == 0 {
		t.Fatalf("EXPLAIN 没有返回结果: %+v", res)
	}
	found := false
	for _, c := range res.Columns {
		if strings.EqualFold(c, "detail") {
			found = true
		}
	}
	if !found {
		t.Fatalf("EXPLAIN 结果缺少 detail 列: %v", res.Columns)
	}
	if res.Truncated {
		t.Fatal("计划行数远小于上限，不应被判为截断")
	}
}
