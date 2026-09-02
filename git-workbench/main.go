// Git Workbench - Git 仓库一体化工作台（native 插件，JSON-RPC 2.0 over stdin/stdout）
//
// 合并能力（原 git-browser 只读浏览 + 5 个新工具）：
//   repo.open          打开本地仓库：分支、提交、工作区状态、分支列表
//   repo.remote        GitHub 远程仓库：默认分支、提交、分支、描述、star
//   bisect.*           可视化二分定位引入 bug 的提交（自动 checkout，good/bad/skip 标记）
//   conflict.*         三方合并冲突可视化：列出冲突文件、取 base/ours/theirs、写回结果
//   timeline.file      代码演化时间轴（blame）：逐行作者/时间/提交，按提交聚合
//   rewrite.*          历史改写：改作者邮箱、删除敏感文件（dry-run 预览 + 应用）
//   stats.*            仓库体检：作者/时间热力/文件变更频率/代码行数趋势/知识孤岛
//
// 底层：github.com/go-git/go-git/v5（纯 Go，不依赖系统 git 命令）
// 选目录：前端走宿主原生对话框 qdPickFolder，插件内不 spawn 子进程

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
		respond(req.ID, map[string]interface{}{"status": "ready", "name": "QuickDock Git Workbench"})
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
	cmd := strings.ToLower(strings.TrimSpace(p.Command))
	switch cmd {
	// 浏览
	case "repo.open", "open":
		handleRepoOpen(id, p.Input)
	case "repo.remote", "remote":
		handleRepoRemote(id, p.Input)
	// 二分定位
	case "bisect.start":
		handleBisectStart(id, p.Input)
	case "bisect.mark":
		handleBisectMark(id, p.Input)
	case "bisect.status":
		handleBisectStatus(id, p.Input)
	case "bisect.reset":
		handleBisectReset(id, p.Input)
	// 合并冲突
	case "conflict.list":
		handleConflictList(id, p.Input)
	case "conflict.load":
		handleConflictLoad(id, p.Input)
	case "conflict.resolve":
		handleConflictResolve(id, p.Input)
	// 时间轴
	case "timeline.file", "timeline.start":
		handleTimeline(id, p.Input)
	// 历史改写
	case "rewrite.preview":
		handleRewritePreview(id, p.Input)
	case "rewrite.apply":
		handleRewriteApply(id, p.Input)
	// 仓库体检
	case "stats.start":
		handleStatsStart(id, p.Input)
	// 通用
	case "task.status", "task-status":
		handleTaskStatus(id, p.Input)
	case "task.cancel":
		handleTaskCancel(id, p.Input)
	default:
		respondError(id, -32601, "unknown command: "+p.Command)
	}
}

func main() {
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
