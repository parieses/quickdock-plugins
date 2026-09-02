// Database - 数据库客户端（native 插件，JSON-RPC 2.0 over stdin/stdout）
//
// 命令：
//   conn.list      列出已保存连接（密码脱敏）
//   conn.save      新建 / 更新连接（密码 DPAPI 加密后落库）
//   conn.delete    删除连接
//   conn.test      用传入配置测试连通性（无需先保存）
//   query.run      执行 SQL（MySQL/SQLite）或 Redis 命令，返回列 + 行 + 可编辑元数据
//   tree.list      库表浏览器根节点（SQL：库；Redis：键）
//   tree.objects   按需展开某个库下的 表/视图/字段
//   row.update     以主键定位单行提交 UPDATE（MySQL/SQLite）
//
// 存储：插件自带 SQLite（modernc.org/sqlite，纯 Go），DB 文件在 <插件目录>/data/。
// 数据独立存储，不读取旧主程序 quickdock.db。
// 外库驱动：go-sql-driver/mysql、modernc.org/sqlite、redis/go-redis/v9。

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
)

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

// rawStrFrom 不做 TrimSpace（SQL / 密码等需保留原文）
func rawStrFrom(input map[string]interface{}, key string) string {
	if v, ok := input[key].(string); ok {
		return v
	}
	return ""
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

func strMapFrom(input map[string]interface{}, key string) map[string]string {
	raw, ok := input[key].(map[string]interface{})
	if !ok {
		return nil
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		switch t := v.(type) {
		case string:
			out[k] = t
		case nil:
			out[k] = ""
		default:
			out[k] = fmt.Sprintf("%v", t)
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
		respond(req.ID, map[string]interface{}{"status": "ready", "name": "QuickDock Database"})
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

func handleCommand(id int64, p executeParams) {
	// handler 内 panic 不应拖垮整个插件进程（否则宿主健康检查会重启，用户丢失状态）
	defer func() {
		if r := recover(); r != nil {
			respondError(id, -32000, fmt.Sprintf("internal error: %v", r))
		}
	}()
	cmd := strings.ToLower(strings.TrimSpace(p.Command))
	switch cmd {
	// 连接管理
	case "conn.list", "list":
		handleConnList(id, p.Input)
	case "conn.save", "save":
		handleConnSave(id, p.Input)
	case "conn.delete", "delete":
		handleConnDelete(id, p.Input)
	case "conn.test", "test":
		handleConnTest(id, p.Input)
	// 查询
	case "query.run", "query":
		handleQueryRun(id, p.Input)
	// 库表树
	case "tree.list", "tree":
		handleTreeList(id, p.Input)
	case "tree.objects":
		handleTreeObjects(id, p.Input)
	// 行编辑
	case "row.update":
		handleRowUpdate(id, p.Input)
	default:
		respondError(id, -32601, "unknown command: "+p.Command)
	}
}

func main() {
	// 存储初始化失败不阻塞进程启动：后续命令会各自返回错误，host.ping 仍可用
	initStore()

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
		// 每个请求独立 goroutine：任何 handler 都不阻塞 stdin 读循环，
		// host.ping 永远秒回，杜绝"同步 handler 卡住 → 健康检查超时 → 被误杀"。
		wg.Add(1)
		go func(raw string) {
			defer wg.Done()
			dispatch(raw)
		}(data)
	}
	wg.Wait()
}
