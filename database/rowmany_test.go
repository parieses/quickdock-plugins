package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// captureStdout 捕获 respond() 打印的 JSON-RPC 响应行
func captureStdout(t *testing.T, fn func()) map[string]interface{} {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	fn()
	w.Close()
	os.Stdout = old
	data, _ := io.ReadAll(r)
	var out map[string]interface{}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		var m map[string]interface{}
		if json.Unmarshal([]byte(lines[i]), &m) == nil {
			if _, ok := m["result"]; ok {
				return m
			}
			if _, ok := m["error"]; ok {
				return m
			}
		}
	}
	t.Fatalf("未捕获到响应: %q", string(data))
	return out
}

func mustExec(t *testing.T, db *sql.DB, q string) {
	t.Helper()
	if _, err := db.Exec(q); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

func TestRowPkListAndDeleteMany(t *testing.T) {
	dir := t.TempDir()
	os.Setenv("QUICKDOCK_PLUGIN_DATA", dir)
	defer os.Unsetenv("QUICKDOCK_PLUGIN_DATA")

	// 目标业务库：25 行，其中 9 行 city=zz（i=5,10,15,20,25 与 i=1..4 中的 5 的倍数）
	dbFile := filepath.Join(dir, "target.db")
	tdb, err := sql.Open("sqlite", sqliteDSN(dbFile))
	if err != nil {
		t.Fatal(err)
	}
	defer tdb.Close()
	mustExec(t, tdb, "CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT, city TEXT)")
	for i := 1; i <= 25; i++ {
		city := "ly"
		if i%5 == 0 {
			city = "zz"
		}
		mustExec(t, tdb, fmt.Sprintf("INSERT INTO users (id,name,city) VALUES (%d,'n%d','%s')", i, i, city))
	}

	// 插件自己的 store + 一条 sqlite 连接
	initStore()
	if err := storeOK(); err != nil {
		t.Fatalf("store 未初始化: %v", err)
	}
	if err := createConnection(&DbConnection{ID: "t1", Name: "t", DbType: "sqlite", FilePath: dbFile, CreatedAt: now()}); err != nil {
		t.Fatal(err)
	}

	// 1) pkList：city LIKE '%zz%' → 5 行（5/10/15/20/25）
	res := captureStdout(t, func() {
		handleRowPkList(1, map[string]interface{}{
			"id": "t1", "table": "users",
			"filters": map[string]interface{}{"city": "zz"},
			"limit":   float64(50000),
		})
	})
	result, _ := res["result"].(map[string]interface{})
	if result == nil {
		t.Fatalf("pkList 响应异常: %v", res)
	}
	vals, _ := result["values"].([]interface{})
	if len(vals) != 5 {
		t.Fatalf("pkList values = %d (%v), want 5", len(vals), vals)
	}
	if result["pkColumn"] != "id" {
		t.Fatalf("pkColumn = %v, want id", result["pkColumn"])
	}
	if total, _ := result["total"].(float64); total != 5 {
		t.Fatalf("total = %v, want 5", total)
	}
	if result["truncated"] != false {
		t.Fatalf("truncated = %v, want false", result["truncated"])
	}

	// 2) deleteMany：删这 5 行
	pks := make([]interface{}, 0, len(vals))
	for _, v := range vals {
		pks = append(pks, v)
	}
	res2 := captureStdout(t, func() {
		handleRowDeleteMany(2, map[string]interface{}{
			"id": "t1", "tableName": "users", "pkColumn": "id", "pkValues": pks,
		})
	})
	r2, _ := res2["result"].(map[string]interface{})
	if r2 == nil {
		t.Fatalf("deleteMany 响应异常: %v", res2)
	}
	if aff, _ := r2["affected"].(float64); aff != 5 {
		t.Fatalf("affected = %v, want 5", aff)
	}
	var left, zz int
	if err := tdb.QueryRow("SELECT COUNT(*) FROM users").Scan(&left); err != nil {
		t.Fatal(err)
	}
	if err := tdb.QueryRow("SELECT COUNT(*) FROM users WHERE city='zz'").Scan(&zz); err != nil {
		t.Fatal(err)
	}
	if left != 20 || zz != 0 {
		t.Fatalf("删除后 left=%d zz=%d, want 20/0", left, zz)
	}

	// 3) pkList 无筛选 + limit=7 → 截断标志
	res3 := captureStdout(t, func() {
		handleRowPkList(3, map[string]interface{}{
			"id": "t1", "table": "users", "limit": float64(7),
		})
	})
	r3, _ := res3["result"].(map[string]interface{})
	vals3, _ := r3["values"].([]interface{})
	if len(vals3) != 7 {
		t.Fatalf("limit=7 values = %d, want 7", len(vals3))
	}
	if r3["truncated"] != true {
		t.Fatalf("truncated = %v, want true", r3["truncated"])
	}

	// 4) deleteMany 分片事务：删剩余 20 行中的前 12 个主键（跨 500 上限内的单片，验证事务原子性）
	res4 := captureStdout(t, func() {
		handleRowPkList(4, map[string]interface{}{
			"id": "t1", "table": "users", "limit": float64(12),
		})
	})
	r4, _ := res4["result"].(map[string]interface{})
	vals4, _ := r4["values"].([]interface{})
	if len(vals4) != 12 {
		t.Fatalf("pkList(12) values = %d, want 12", len(vals4))
	}
	res5 := captureStdout(t, func() {
		handleRowDeleteMany(5, map[string]interface{}{
			"id": "t1", "tableName": "users", "pkColumn": "id", "pkValues": vals4,
		})
	})
	r5, _ := res5["result"].(map[string]interface{})
	if aff, _ := r5["affected"].(float64); aff != 12 {
		t.Fatalf("deleteMany affected = %v, want 12", aff)
	}
	var left2 int
	if err := tdb.QueryRow("SELECT COUNT(*) FROM users").Scan(&left2); err != nil {
		t.Fatal(err)
	}
	if left2 != 8 {
		t.Fatalf("最终剩余 %d 行, want 8", left2)
	}

	// 5) 参数校验：空 pkValues 必须报错而不是静默成功
	res6 := captureStdout(t, func() {
		handleRowDeleteMany(6, map[string]interface{}{
			"id": "t1", "tableName": "users", "pkColumn": "id", "pkValues": []interface{}{},
		})
	})
	if _, ok := res6["error"]; !ok {
		t.Fatalf("空 pkValues 应报错，实际: %v", res6)
	}

	// 收尾：释放缓存连接与 store（Windows 下不关句柄 t.TempDir 清理会失败）
	closeAllConnCache()
	storeMu.Lock()
	if store != nil {
		_ = store.Close()
		store = nil
	}
	storeMu.Unlock()
}
