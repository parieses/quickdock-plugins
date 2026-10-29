// 敏感信息扫描 - 扫描项目目录，找出意外硬编码的密钥、私钥、token、密码字段与身份证号等敏感信息，防止它们被提交进仓库或泄露
// JSON-RPC 2.0 over stdin/stdout（QuickDock native 插件骨架，由 plugin-scaffold 生成）
//
// 与宿主约定：initialize 握手 / host.ping 健康检查 / plugin.execute 业务入口。
// 日志走 templates/qdlog.go 的 qdLog*（落 plugin-YYYYMMDD.log），不要自己 fmt.Println。
// 命令名统一小写（宿主会 strings.ToLower 后再分发）。

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}
type executeParams struct {
	Command string                 `json:"command"`
	Input   map[string]interface{} `json:"input"`
}

var stdout = bufio.NewWriter(os.Stdout)
var stdoutMu sync.Mutex
var cbSeq int64 = 1_000_000_000

func main() {
	qdLogStart()
	defer qdLogExit()
	reader := bufio.NewReader(os.Stdin)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		data := strings.TrimSpace(line)
		if data == "" {
			continue
		}
		if qdObserveLine(data) {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal([]byte(data), &req); err != nil {
			respondError(json.RawMessage("0"), -32700, "parse error: "+err.Error())
			continue
		}
		if req.Method == "" {
			continue
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					qdLogPanic("secrets-scanner dispatch panic", r)
					respondError(req.ID, -32000, fmt.Sprintf("internal error: %v", r))
				}
			}()
			handleRequest(req)
		}()
	}
}

// hostCall 向宿主发起回调（如 db.set / host.notify），fire-and-forget。
func hostCall(method string, params map[string]interface{}) {
	cbSeq++
	payload, err := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0", "id": cbSeq, "method": method, "params": params,
	})
	if err != nil {
		return
	}
	stdoutMu.Lock()
	defer stdoutMu.Unlock()
	stdout.Write(payload)
	stdout.WriteByte('\n')
	stdout.Flush()
}

func handleRequest(req rpcRequest) {
	switch req.Method {
	case "initialize":
		respond(req.ID, map[string]interface{}{"status": "ready", "name": "敏感信息扫描"})
	case "host.ping":
		respond(req.ID, map[string]interface{}{"pong": true})
	case "plugin.execute":
		var p executeParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			respondError(req.ID, -32602, "invalid params: "+err.Error())
			return
		}
		result, errMsg := execute(p)
		if errMsg != "" {
			respondError(req.ID, -32603, errMsg)
			return
		}
		respond(req.ID, result)
	default:
		respondError(req.ID, -32601, "method not found: "+req.Method)
	}
}

// execute 分发命令（命令名统一小写；前端也用小写发送）。
func execute(p executeParams) (map[string]interface{}, string) {
	command := strings.ToLower(strings.ReplaceAll(p.Command, ".", "-"))
	switch command {
	case "rules":
		return map[string]interface{}{"rules": rulesMeta()}, ""
	case "scan":
		return cmdScan(p.Input)
	case "scan-poll":
		return cmdPoll(p.Input)
	case "scan-cancel":
		return cmdCancel(p.Input)
	default:
		return nil, "unknown command: " + p.Command
	}
}

// rulesMeta 返回全部规则元数据，供前端勾选。
func rulesMeta() []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(rulesAll))
	for _, r := range rulesAll {
		out = append(out, map[string]interface{}{
			"id":         r.ID,
			"name":       r.Name,
			"category":   r.Category,
			"severity":   r.Severity,
			"desc":       r.Desc,
			"defaultOn":  r.DefaultOn,
		})
	}
	return out
}

// anyBool / anyFloat：容忍 "true"/"1"/"5" 这类字符串形态（见 cmdScan 注释）。
func anyBool(v interface{}) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		s := strings.ToLower(strings.TrimSpace(t))
		return s == "true" || s == "1" || s == "yes" || s == "on"
	}
	return false
}

func anyFloat(v interface{}) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case int:
		return float64(t)
	case json.Number:
		if f, err := t.Float64(); err == nil {
			return f
		}
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(t), 64); err == nil {
			return f
		}
	}
	return 0
}

func strArr(v interface{}) []string {
	a, ok := v.([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(a))
	for _, x := range a {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func cmdScan(in map[string]interface{}) (map[string]interface{}, string) {
	if in == nil {
		return nil, "缺少参数 path"
	}
	root, _ := in["path"].(string)
	if root == "" {
		return nil, "缺少参数 path"
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return nil, "路径不存在或不是目录: " + root
	}

	excludes := strArr(in["excludeDirs"])
	enabled := strArr(in["rules"])
	// 类型宽容：AI 客户端按 params 声明可能把 true 写成 "true"、把 5 写成 "5"，
	// 能转就转——静默退回默认值会让「扫了但没生效」变得难以察觉。
	gitHist := anyBool(in["gitHistory"])

	maxMB := 2.0
	if v := anyFloat(in["maxFileSizeMB"]); v > 0 {
		maxMB = v
	}
	if maxMB > 64 {
		maxMB = 64
	}

	s := &Session{
		ID:      randID(),
		started: time.Now(),
		running: true,
		filter:  map[string]bool{},
		seen:    make(map[string]struct{}),
		maxRes:  5000,
	}
	if len(enabled) == 0 {
		for _, r := range rulesAll {
			s.filter[r.ID] = r.DefaultOn
		}
	} else {
		set := map[string]bool{}
		for _, e := range enabled {
			set[e] = true
		}
		for _, r := range rulesAll {
			s.filter[r.ID] = set[r.ID]
		}
	}

	sessMu.Lock()
	sessions[s.ID] = s
	sessMu.Unlock()

	opts := scanOpts{
		root:        root,
		excludes:    excludes,
		maxSize:     int64(maxMB * 1024 * 1024),
		gitHistory:  gitHist,
		workers:     8,
	}
	go runScan(s, opts)

	qdLogInfo("开始扫描 path=%s 规则=%d 项 git历史=%v", root, len(s.enabledRules()), gitHist)
	return map[string]interface{}{
		"sessionId": s.ID,
		"message":   "扫描已启动",
	}, ""
}

func findSession(id string) *Session {
	sessMu.Lock()
	defer sessMu.Unlock()
	return sessions[id]
}

func cmdPoll(in map[string]interface{}) (map[string]interface{}, string) {
	id, _ := in["sessionId"].(string)
	s := findSession(id)
	if s == nil {
		return nil, "会话不存在或已结束: " + id
	}
	s.mu.Lock()
	running := s.running
	canceled := s.canceled
	done := s.done
	current := s.current
	matches := s.matches
	truncated := s.trunc
	s.mu.Unlock()

	scanned := atomic.LoadInt64(&s.scanned)
	totalEst := atomic.LoadInt64(&s.totalEst)

	// 复制一份，避免前端持有内部切片被后续写入扰动
	snap := make([]Match, len(matches))
	copy(snap, matches)

	return map[string]interface{}{
		"sessionId":  id,
		"running":    running,
		"done":       done,
		"canceled":   canceled,
		"scanned":    scanned,
		"totalEst":   totalEst,
		"current":    current,
		"elapsedMs":  time.Since(s.started).Milliseconds(),
		"matches":    snap,
		"total":      len(snap),
		"truncated":  truncated,
	}, ""
}

func cmdCancel(in map[string]interface{}) (map[string]interface{}, string) {
	id, _ := in["sessionId"].(string)
	s := findSession(id)
	if s == nil {
		return nil, "会话不存在: " + id
	}
	s.mu.Lock()
	s.canceled = true
	s.mu.Unlock()
	qdLogInfo("用户取消扫描 session=%s", id)
	return map[string]interface{}{"ok": true}, ""
}

func respond(id json.RawMessage, result interface{}) {
	defer qdFinishRequest(idToInt(id), 0, "")
	payload, err := json.Marshal(result)
	if err != nil {
		respondError(json.RawMessage("0"), -32603, "marshal result failed: "+err.Error())
		return
	}
	writeResponse(rpcResponse{JSONRPC: "2.0", ID: id, Result: payload})
}

func respondError(id json.RawMessage, code int, msg string) {
	defer qdFinishRequest(idToInt(id), code, msg)
	writeResponse(rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}})
}

func writeResponse(resp rpcResponse) {
	data, err := json.Marshal(resp)
	if err != nil {
		return
	}
	stdoutMu.Lock()
	defer stdoutMu.Unlock()
	stdout.Write(data)
	stdout.WriteByte('\n')
	stdout.Flush()
}
