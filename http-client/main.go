// HTTP 客户端插件（native 插件，JSON-RPC 2.0 over stdin/stdout）
//
// 移植自主程序 services/httpclient_*.go 的 HTTP 客户端功能，作为独立外部原生插件运行。
// 自带 SQLite（modernc.org/sqlite，纯 Go 无 CGO），数据独立存储。
//
// 命令一览：
//   req.send / req.list / req.save / req.get / req.delete / req.curl
//   history.list / history.clear / history.delete
//   project.list / project.save / project.delete
//   folder.list / folder.save / folder.delete / folder.reorder
//   doc.list / doc.save / doc.get / doc.delete
//   env.list / env.save / env.delete / env.resolve
//   project.importPostman

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Server 持有插件级状态（主要是数据库连接）。
type Server struct {
	db *DB
}

var srv *Server

// ---- JSON-RPC ----

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type executeParams struct {
	Command string                 `json:"command"`
	Input   map[string]interface{} `json:"input"`
}

func strFrom(input map[string]interface{}, key string) string {
	if v, ok := input[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func boolFrom(input map[string]interface{}, key string, def bool) bool {
	if v, ok := input[key].(bool); ok {
		return v
	}
	return def
}

func intFrom(input map[string]interface{}, key string, def int) int {
	switch v := input[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			return n
		}
	}
	return def
}

func strSliceFrom(input map[string]interface{}, key string) []string {
	raw, ok := input[key]
	if !ok || raw == nil {
		return nil
	}
	arr, ok := raw.([]interface{})
	if !ok {
		if s, ok := raw.(string); ok && s != "" {
			return []string{s}
		}
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, v := range arr {
		if s, ok := v.(string); ok {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

var outMu sync.Mutex

func respond(id int64, result interface{}) {
	outMu.Lock()
	defer outMu.Unlock()
	out, _ := json.Marshal(map[string]interface{}{"jsonrpc": "2.0", "id": id, "result": result})
	fmt.Println(string(out))
}

func respondError(id int64, code int, msg string) {
	outMu.Lock()
	defer outMu.Unlock()
	out, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0", "id": id,
		"error": map[string]interface{}{"code": code, "message": msg},
	})
	fmt.Println(string(out))
}

// ---- dispatch ----

func dispatch(raw string) {
	var req rpcRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		respondError(0, -32700, "parse error: "+err.Error())
		return
	}
	switch req.Method {
	case "initialize":
		respond(req.ID, map[string]interface{}{"status": "ready", "name": "QuickDock HTTP Client"})
	case "host.ping":
		respond(req.ID, map[string]interface{}{"pong": true})
	case "plugin.execute":
		var params executeParams
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &params); err != nil {
				respondError(req.ID, -32602, "invalid params: "+err.Error())
				return
			}
		}
		handleCommand(req.ID, params)
	default:
		respondError(req.ID, -32601, "unknown method: "+req.Method)
	}
}

func main() {
	// 数据库路径：<可执行文件目录>/data/io.github.parieses.http-client.db
	exe, err := os.Executable()
	if err != nil {
		exe, _ = os.Getwd()
	}
	binDir := filepath.Dir(exe)
	dataDir := filepath.Join(binDir, "data")
	dbPath := filepath.Join(dataDir, "io.github.parieses.http-client.db")

	db, err := openDB(dbPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open db:", err)
		os.Exit(1)
	}
	defer db.Close()

	srv = &Server{db: db}

	reader := bufio.NewReader(os.Stdin)
	var wg sync.WaitGroup
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		data := strings.TrimSpace(line)
		if data == "" {
			continue
		}
		wg.Add(1)
		go func(raw string) {
			defer wg.Done()
			dispatch(raw)
		}(data)
	}
	wg.Wait()
}
