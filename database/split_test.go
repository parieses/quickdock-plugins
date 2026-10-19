package main

import (
	"reflect"
	"testing"
)

func TestSplitSQLStatements(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"select 1; select 2; select 3", []string{"select 1", "select 2", "select 3"}},
		{"select 1;", []string{"select 1"}},
		{"select 'a;b' as x; select 2", []string{"select 'a;b' as x", "select 2"}},
		{"select `weird;col` from t; update t set a=1", []string{"select `weird;col` from t", "update t set a=1"}},
		{"-- comment; not a statement\nselect 1; # another; comment\nselect 2", []string{"select 1", "select 2"}},
		{"/* this; is ; a ; comment */ select 1; select 2", []string{"select 1", "select 2"}},
		{"  ;  ; select 1 ; ; ", []string{"select 1"}},
		{"insert into t values ('x;y', \"a;b\")", []string{`insert into t values ('x;y', "a;b")`}},
	}
	for i, c := range cases {
		got := splitSQLStatements(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("case %d: got %#v want %#v", i, got, c.want)
		}
	}
}

func TestInlineSQLArgs(t *testing.T) {
	cases := []struct {
		q    string
		args []interface{}
		want string
	}{
		{"SELECT * FROM t", nil, "SELECT * FROM t"},
		{"SELECT * FROM `t` WHERE `a` LIKE ? AND `b` LIKE ?",
			[]interface{}{"%x%", "it's"}, "SELECT * FROM `t` WHERE `a` LIKE '%x%' AND `b` LIKE 'it''s'"},
		// 引号内的 ? 不能被替换
		{"SELECT '?' AS q WHERE a = ?", []interface{}{"1"}, "SELECT '?' AS q WHERE a = '1'"},
		{"SELECT * FROM t WHERE a = ? AND b = ? LIMIT 100 OFFSET 200",
			[]interface{}{nil, 12}, "SELECT * FROM t WHERE a = NULL AND b = 12 LIMIT 100 OFFSET 200"},
	}
	for i, c := range cases {
		got := inlineSQLArgs(c.q, c.args)
		if got != c.want {
			t.Errorf("case %d: got %q want %q", i, got, c.want)
		}
	}
}
