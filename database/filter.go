package main

import (
	"fmt"
	"sort"
	"strings"
)

// ---- 数据页列级筛选（Navicat / DataGrip 式，AND 拼接，全部参数化） ----

// filterCond 单个列的筛选条件。前端把用户在筛选框里输入的文本智能解析成
// 结构化条件下发（参见前端 parseFilterExpr），后端只认白名单内的 op，
// 不拼任何用户原始文本进 SQL，杜绝注入。
type filterCond struct {
	Op string `json:"op"` // like | nlike | eq | ne | gt | gte | lt | lte | between | in | isnull | notnull
	V  string `json:"v"`  // 主值（in 为逗号分隔多值）
	V2 string `json:"v2"` // between 右值
}

// active 判断该条件是否真的能过滤（空值 / 半截 between 视为未启用）。
func (c filterCond) active() bool {
	switch c.Op {
	case "isnull", "notnull":
		return true
	case "between":
		return strings.TrimSpace(c.V) != "" && strings.TrimSpace(c.V2) != ""
	case "in":
		return len(splitInValues(c.V)) > 0
	case "like", "nlike", "eq", "ne", "gt", "gte", "lt", "lte":
		return strings.TrimSpace(c.V) != ""
	}
	return false
}

// parseFilters 解析 input["filters"]，兼容两种协议：
//
//	{"city":"zz"}                 → city LIKE '%zz%'（旧协议：纯字符串，默认包含匹配）
//	{"age":{"op":"gt","v":"18"}}  → age > 18（新协议：结构化条件）
//
// 无法识别的形状与未激活的条件直接丢弃。
func parseFilters(input map[string]interface{}) map[string]filterCond {
	raw, ok := input["filters"].(map[string]interface{})
	if !ok {
		return nil
	}
	out := make(map[string]filterCond, len(raw))
	for k, v := range raw {
		switch t := v.(type) {
		case string:
			if strings.TrimSpace(t) == "" {
				continue
			}
			out[k] = filterCond{Op: "like", V: t}
		case map[string]interface{}:
			c := filterCond{
				Op: strings.ToLower(strings.TrimSpace(anyToStr(t["op"]))),
				V:  anyToStr(t["v"]),
				V2: anyToStr(t["v2"]),
			}
			if c.Op == "" {
				c.Op = "like"
			}
			if c.active() {
				out[k] = c
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// anyToStr 把 JSON 值转成字符串（数字 / nil 安全）。
func anyToStr(v interface{}) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return ""
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", t))
	}
}

// buildWhere 由列筛选条件拼接 WHERE 子句（多列 AND，列按名称排序保证
// SQL 预览稳定可复现）。返回 ("", nil) 表示没有生效条件。
func buildWhere(kind string, filters map[string]filterCond) (string, []interface{}) {
	if len(filters) == 0 {
		return "", nil
	}
	cols := make([]string, 0, len(filters))
	for col := range filters {
		cols = append(cols, col)
	}
	sort.Strings(cols)

	var conds []string
	var args []interface{}
	for _, col := range cols {
		c := filters[col]
		if !c.active() {
			continue
		}
		ident := quoteIdent(kind, col)
		switch c.Op {
		case "like":
			conds = append(conds, ident+" LIKE ?")
			args = append(args, "%"+c.V+"%")
		case "nlike":
			conds = append(conds, ident+" NOT LIKE ?")
			args = append(args, "%"+c.V+"%")
		case "eq":
			conds = append(conds, ident+" = ?")
			args = append(args, c.V)
		case "ne":
			conds = append(conds, ident+" <> ?")
			args = append(args, c.V)
		case "gt":
			conds = append(conds, ident+" > ?")
			args = append(args, c.V)
		case "gte":
			conds = append(conds, ident+" >= ?")
			args = append(args, c.V)
		case "lt":
			conds = append(conds, ident+" < ?")
			args = append(args, c.V)
		case "lte":
			conds = append(conds, ident+" <= ?")
			args = append(args, c.V)
		case "between":
			conds = append(conds, ident+" BETWEEN ? AND ?")
			args = append(args, c.V, c.V2)
		case "in":
			items := splitInValues(c.V)
			if len(items) == 0 {
				continue
			}
			conds = append(conds, ident+" IN ("+strings.TrimSuffix(strings.Repeat("?,", len(items)), ",")+")")
			for _, it := range items {
				args = append(args, it)
			}
		case "isnull":
			conds = append(conds, ident+" IS NULL")
		case "notnull":
			conds = append(conds, ident+" IS NOT NULL")
		}
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// splitInValues 拆分 IN 的多值（逗号分隔），上限 200 个，防止语句过长。
func splitInValues(v string) []string {
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) > 200 {
		out = out[:200]
	}
	return out
}
