package main

import (
	"reflect"
	"testing"
)

func TestParseFiltersCompatAndNew(t *testing.T) {
	// 旧协议：纯字符串 → LIKE %值%
	old := parseFilters(map[string]interface{}{
		"filters": map[string]interface{}{"city": "zz", "empty": "  "},
	})
	if len(old) != 1 || old["city"].Op != "like" || old["city"].V != "zz" {
		t.Fatalf("旧协议解析异常: %#v", old)
	}
	if _, ok := old["empty"]; ok {
		t.Fatal("空字符串条件应被丢弃")
	}

	// 新协议：结构化条件
	got := parseFilters(map[string]interface{}{
		"filters": map[string]interface{}{
			"age":  map[string]interface{}{"op": "gt", "v": float64(18)},
			"name": map[string]interface{}{"op": "nlike", "v": "test"},
			"half": map[string]interface{}{"op": "between", "v": "1"}, // 缺 v2，未激活
		},
	})
	if len(got) != 2 {
		t.Fatalf("新协议应只保留 2 个激活条件: %#v", got)
	}
	if got["age"].Op != "gt" || got["age"].V != "18" {
		t.Fatalf("gt 条件异常: %#v", got["age"])
	}
}

func TestBuildWhereOps(t *testing.T) {
	cases := []struct {
		name   string
		cond   map[string]filterCond
		where  string
		args   []interface{}
	}{
		{"like", map[string]filterCond{"city": {Op: "like", V: "zz"}}, " WHERE `city` LIKE ?", []interface{}{"%zz%"}},
		{"nlike", map[string]filterCond{"city": {Op: "nlike", V: "zz"}}, " WHERE `city` NOT LIKE ?", []interface{}{"%zz%"}},
		{"eq", map[string]filterCond{"age": {Op: "eq", V: "18"}}, " WHERE `age` = ?", []interface{}{"18"}},
		{"ne", map[string]filterCond{"age": {Op: "ne", V: "18"}}, " WHERE `age` <> ?", []interface{}{"18"}},
		{"gte", map[string]filterCond{"age": {Op: "gte", V: "18"}}, " WHERE `age` >= ?", []interface{}{"18"}},
		{"lte", map[string]filterCond{"age": {Op: "lte", V: "18"}}, " WHERE `age` <= ?", []interface{}{"18"}},
		{"between", map[string]filterCond{"age": {Op: "between", V: "10", V2: "20"}}, " WHERE `age` BETWEEN ? AND ?", []interface{}{"10", "20"}},
		{"in", map[string]filterCond{"id": {Op: "in", V: "1, 2 ,3"}}, " WHERE `id` IN (?,?,?)", []interface{}{"1", "2", "3"}},
		{"isnull", map[string]filterCond{"x": {Op: "isnull"}}, " WHERE `x` IS NULL", nil},
		{"notnull", map[string]filterCond{"x": {Op: "notnull"}}, " WHERE `x` IS NOT NULL", nil},
		// 多列 AND，且按列名排序保证 SQL 预览稳定
		{"multi", map[string]filterCond{"city": {Op: "like", V: "zz"}, "age": {Op: "gt", V: "18"}},
			" WHERE `age` > ? AND `city` LIKE ?", []interface{}{"18", "%zz%"}},
		// 未激活条件被跳过
		{"inactive", map[string]filterCond{"age": {Op: "gt", V: "  "}}, "", nil},
		{"unknown-op", map[string]filterCond{"age": {Op: "drop", V: "1"}}, "", nil},
	}
	for _, c := range cases {
		where, args := buildWhere("mysql", c.cond)
		if where != c.where {
			t.Errorf("%s: where = %q, want %q", c.name, where, c.where)
		}
		if len(args) == 0 && len(c.args) == 0 {
			continue
		}
		if !reflect.DeepEqual(args, c.args) {
			t.Errorf("%s: args = %#v, want %#v", c.name, args, c.args)
		}
	}
}

func TestBuildWhereNil(t *testing.T) {
	if where, args := buildWhere("sqlite", nil); where != "" || args != nil {
		t.Fatalf("nil filters 应返回空 WHERE, got %q / %#v", where, args)
	}
}
