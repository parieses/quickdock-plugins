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
