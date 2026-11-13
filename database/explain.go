package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ExplainResult 是 query.explain 的返回值：只解释语句、不执行它。
// 与 query.run 分开是为了让前端能明确区分「执行结果」和「执行计划」，
// 也便于对不支持的数据库 / 语句类型给出人话提示而不是裸驱动错误。
type ExplainResult struct {
	Success    bool          `json:"success"`
	Kind       string        `json:"kind"` // mysql | sqlite
	SQL        string        `json:"sql"`  // 实际下发的 EXPLAIN 语句
	Columns    []string      `json:"columns"`
	Rows       [][]string    `json:"rows"`
	Nulls      [][]bool      `json:"nulls"`
	RowCount   int           `json:"rowCount"`
	Hints      []ExplainHint `json:"hints"`
	DurationMs int64         `json:"durationMs"`
	Error      string        `json:"error"`
}

// ExplainHint 一条可读性诊断（全表扫描、未命中索引、额外排序等）。
type ExplainHint struct {
	Level string `json:"level"` // warn | info | ok
	Text  string `json:"text"`
}

// explainableStatements 白名单：EXPLAIN 只解释这些 DML / 查询。
// 拒绝 DDL / SET / USE / SHOW / 事务控制，也避免有人把多条语句拼进来绕过检查。
var explainableStatements = []string{"SELECT", "WITH", "UPDATE", "DELETE", "INSERT", "REPLACE"}

func explainable(sql string) bool {
	upper := strings.ToUpper(strings.TrimLeft(strings.TrimSpace(sql), "("))
	for _, p := range explainableStatements {
		if upper == p || strings.HasPrefix(upper, p+" ") || strings.HasPrefix(upper, p+"\n") ||
			strings.HasPrefix(upper, p+"\t") || strings.HasPrefix(upper, p+"/*") {
			return true
		}
	}
	return false
}

// handleQueryExplain 生成并读取 EXPLAIN 执行计划。
// MySQL 用 EXPLAIN，SQLite 用 EXPLAIN QUERY PLAN；Redis 没有执行计划概念，直接说明。
// EXPLAIN 只对语句做计划，不会真的执行 UPDATE / DELETE / INSERT，因此是只读安全的。
func handleQueryExplain(id int64, input map[string]interface{}) {
	cid := strFrom(input, "id")
	query := rawStrFrom(input, "query")
	if strings.TrimSpace(query) == "" {
		query = rawStrFrom(input, "text")
	}
	if cid == "" || strings.TrimSpace(query) == "" {
		respondError(id, -32602, "缺少连接 id 或查询内容")
		return
	}

	saved, conn, err := openSavedWithDB(cid, strFrom(input, "database"))
	if err != nil {
		respond(id, &ExplainResult{Error: err.Error()})
		return
	}
	defer conn.close()
	if conn.redis != nil {
		respond(id, &ExplainResult{Kind: "redis", Error: "Redis 命令没有执行计划。可用查询页直接执行 SLOWLOG / INFO commandstats 观察命令开销。"})
		return
	}

	stmts := splitSQLStatements(query)
	if len(stmts) == 0 {
		respond(id, &ExplainResult{Kind: conn.kind, Error: "查询内容为空"})
		return
	}
	if len(stmts) > 1 {
		respond(id, &ExplainResult{Kind: conn.kind, Error: "执行计划一次只解释一条语句：请只保留一条 SQL，或选中其中一条再试。"})
		return
	}
	stmt := strings.TrimSpace(stmts[0])
	upper := strings.ToUpper(stmt)
	var planSQL string
	switch {
	case strings.HasPrefix(upper, "EXPLAIN"):
		// 用户自己写了 EXPLAIN / EXPLAIN QUERY PLAN：原样下发，不再套一层
		planSQL = stmt
	case explainable(stmt):
		if conn.kind == "sqlite" {
			planSQL = "EXPLAIN QUERY PLAN " + stmt
		} else {
			planSQL = "EXPLAIN " + stmt
		}
	default:
		respond(id, &ExplainResult{
			Kind:  conn.kind,
			Error: "该语句不支持执行计划。仅支持 SELECT / WITH / INSERT / UPDATE / DELETE / REPLACE（以及自己写的 EXPLAIN）。",
		})
		return
	}

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), dbQueryTimeout)
	defer cancel()
	res, err := queryRows(ctx, conn.sqlDB, planSQL, 200)
	out := &ExplainResult{
		Kind:       conn.kind,
		SQL:        planSQL,
		DurationMs: time.Since(start).Milliseconds(),
	}
	if err != nil {
		// 老版本 MySQL 不支持 EXPLAIN UPDATE / INSERT（5.6 之前），把驱动错误原样带回来，
		// 前端会展示在计划面板里，而不是变成一次「插件超时」。
		out.Error = err.Error()
		if saved != nil && saved.DbType == "mysql" {
			out.Error += "\n\n提示：MySQL 5.6 以前只支持解释 SELECT；升级 MySQL 或改成等价的 SELECT 再试。"
		}
		respond(id, out)
		return
	}
	out.Success = true
	out.Columns = res.Columns
	out.Rows = res.Rows
	out.Nulls = res.Nulls
	out.RowCount = res.RowCount
	out.Hints = explainHints(conn.kind, res.Columns, res.Rows)
	respond(id, out)
}

// explainHints 把执行计划里最值得人看的几件事翻成中文提示。
// 只做轻量文本判断，不试图替代完整的优化器分析。
func explainHints(kind string, cols []string, rows [][]string) []ExplainHint {
	hints := []ExplainHint{}
	idx := map[string]int{}
	for i, c := range cols {
		idx[strings.ToLower(c)] = i
	}
	cell := func(row []string, key string) string {
		i, ok := idx[key]
		if !ok || i >= len(row) {
			return ""
		}
		return row[i]
	}
	add := func(level, text string) {
		// 同类提示只出一次，避免多表 JOIN 时刷屏
		for _, h := range hints {
			if h.Text == text {
				return
			}
		}
		hints = append(hints, ExplainHint{Level: level, Text: text})
	}
	for _, row := range rows {
		if kind == "mysql" {
			typ := strings.ToUpper(strings.TrimSpace(cell(row, "type")))
			table := strings.TrimSpace(cell(row, "table"))
			where := ""
			if table != "" {
				where = "（" + table + "）"
			}
			if typ == "ALL" {
				add("warn", "全表扫描"+where+"：type=ALL，数据量大时考虑给 WHERE / JOIN 条件加索引")
			} else if typ == "INDEX" {
				add("info", "全索引扫描"+where+"：type=index，虽走索引但仍遍历了整棵索引树")
			}
			if strings.TrimSpace(cell(row, "key")) == "" && strings.TrimSpace(cell(row, "possible_keys")) != "" {
				add("warn", "有可用索引却没用上"+where+"：possible_keys 非空但 key 为空，检查条件是否对列做了函数/隐式转换")
			}
			extra := strings.ToLower(cell(row, "extra"))
			if strings.Contains(extra, "using filesort") {
				add("warn", "额外排序"+where+"：Using filesort，ORDER BY 未命中索引顺序")
			}
			if strings.Contains(extra, "using temporary") {
				add("warn", "使用临时表"+where+"：Using temporary，常见于 GROUP BY / DISTINCT 与排序不一致")
			}
			if n, e := strconv.ParseInt(strings.TrimSpace(cell(row, "rows")), 10, 64); e == nil && n >= 100000 {
				add("info", fmt.Sprintf("预估扫描约 %d 行%s，可用 LIMIT / 更窄的 WHERE 缩小范围", n, where))
			}
		} else {
			detail := strings.ToUpper(cell(row, "detail"))
			if strings.Contains(detail, "SCAN ") && !strings.Contains(detail, "USING") && !strings.Contains(detail, "CONSTANT ROW") {
				add("warn", "全表扫描："+cell(row, "detail"))
			}
			if strings.Contains(detail, "USE TEMP B-TREE") {
				add("warn", "额外排序 / 去重：USE TEMP B-TREE，ORDER BY / GROUP BY 未命中索引")
			}
			if strings.Contains(detail, "SEARCH") && strings.Contains(detail, "USING") {
				add("ok", "命中索引："+cell(row, "detail"))
			}
		}
		if len(hints) >= 8 {
			break
		}
	}
	if len(hints) == 0 {
		hints = append(hints, ExplainHint{Level: "ok", Text: "未发现明显的全表扫描 / 额外排序告警"})
	}
	return hints
}
