// 电脑活动痕迹 — QuickDock native 插件
// JSON-RPC 2.0 over stdin/stdout；全程只读，不修改系统设置、不开启审计。
//
// 命令：timeline.query（日期段时间轴）/ source.status（证据完整度体检）/
//       report.export（CSV/HTML）/ audit.advice（只返回建议命令，不执行）/
//       activity-scan / activity-status（Ctrl+K 入口别名）/ backend-ping
//
// 日志走 templates/qdlog.go 的 qdLog*，不要自己 fmt.Println 到 stdout。

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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
					qdLogPanic("activity-timeline dispatch panic", r)
					respondError(req.ID, -32000, fmt.Sprintf("internal error: %v", r))
				}
			}()
			handleRequest(req)
		}()
	}
}

func handleRequest(req rpcRequest) {
	switch req.Method {
	case "initialize":
		respond(req.ID, map[string]interface{}{"status": "ready", "name": "电脑活动痕迹"})
	case "host.ping", "health.ping":
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

// ---------------------------------------------------------------- 输入解析

func inputStr(in map[string]interface{}, key string) string {
	if in == nil {
		return ""
	}
	v, ok := in[key]
	if !ok || v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		if x {
			return "true"
		}
	}
	return fmt.Sprint(v)
}

func inputTypes(in map[string]interface{}) []string {
	if in == nil {
		return nil
	}
	v, ok := in["types"]
	if !ok || v == nil {
		return nil
	}
	out := []string{}
	add := func(s string) {
		s = strings.TrimSpace(strings.ToLower(s))
		if s != "" && s != "all" && s != "全部" {
			out = append(out, s)
		}
	}
	switch x := v.(type) {
	case string:
		for _, p := range strings.Split(x, ",") {
			add(p)
		}
	case []interface{}:
		for _, it := range x {
			if s, ok := it.(string); ok {
				add(s)
			}
		}
	}
	return out
}

func buildContext(in map[string]interface{}) (CollectContext, string) {
	now := time.Now()
	to := now
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).AddDate(0, 0, -6)
	if s := inputStr(in, "to"); s != "" {
		if t := parseDate(s, true); !t.IsZero() {
			to = t
		}
	}
	if s := inputStr(in, "from"); s != "" {
		if t := parseDate(s, false); !t.IsZero() {
			from = t
		}
	}
	// Ctrl+K 传进来的纯文本支持 "2026-10-28~2026-11-05"
	if text := inputStr(in, "text"); strings.Contains(text, "~") {
		parts := strings.SplitN(text, "~", 2)
		if t := parseDate(parts[0], false); !t.IsZero() {
			from = t
		}
		if t := parseDate(parts[1], true); !t.IsZero() {
			to = t
		}
	}
	if from.After(to) {
		return CollectContext{}, "起始日期不能晚于结束日期"
	}
	profile := os.Getenv("USERPROFILE")
	appdata := os.Getenv("APPDATA")
	if appdata == "" && profile != "" {
		appdata = filepath.Join(profile, "AppData", "Roaming")
	}
	local := os.Getenv("LOCALAPPDATA")
	if local == "" && profile != "" {
		local = filepath.Join(profile, "AppData", "Local")
	}
	sysroot := os.Getenv("SystemRoot")
	if sysroot == "" {
		sysroot = `C:\Windows`
	}
	return CollectContext{
		From: from, To: to, FromMS: from.UnixMilli(), ToMS: to.UnixMilli(),
		Profile: profile, AppData: appdata, LocalAppData: local,
		SystemRoot: sysroot, SID: os.Getenv("USERNAME"), MaxEvents: 3000,
	}, ""
}

func filterTypes(events []TimelineEvent, types []string) []TimelineEvent {
	if len(types) == 0 {
		return events
	}
	out := make([]TimelineEvent, 0, len(events))
	for _, e := range events {
		if passesTypes(types, e) {
			out = append(out, e)
		}
	}
	return out
}

// ---------------------------------------------------------------- 结果缓存

type resultCache struct {
	key     string
	events  []TimelineEvent
	sources []SourceStatus
	ctx     CollectContext
	status  StatusReport
}

var (
	cacheMu sync.Mutex
	cache   resultCache
)

func cacheKey(c CollectContext) string {
	return fmt.Sprintf("%d|%d", c.FromMS, c.ToMS)
}

func cacheSet(key string, events []TimelineEvent, sources []SourceStatus, c CollectContext, st StatusReport) {
	cacheMu.Lock()
	cache = resultCache{key: key, events: events, sources: sources, ctx: c, status: st}
	cacheMu.Unlock()
}

func cacheGet(key string) (resultCache, bool) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if cache.key == key && len(cache.events) > 0 {
		return cache, true
	}
	return resultCache{}, false
}

// ---------------------------------------------------------------- 命令分发

func execute(p executeParams) (map[string]interface{}, string) {
	command := strings.ToLower(strings.TrimSpace(p.Command))
	switch command {
	case "activity-scan", "timeline.query", "timeline-query", "query", "":
		c, msg := buildContext(p.Input)
		if msg != "" {
			return nil, msg
		}
		// 时间轴返回全量，类型过滤交给前端，切换标签不必重新扫描
		events, sources := collectAll(c)
		status := buildStatus(c, events, sources)
		cacheSet(cacheKey(c), events, sources, c, status)
		qdLogInfo("取证扫描完成 from=%s to=%s events=%d sources=%d",
			c.From.Format("2006-01-02"), c.To.Format("2006-01-02"), len(events), len(sources))
		return map[string]interface{}{
			"events": events, "sources": sources, "status": status,
			"from": c.From.Format("2006-01-02"), "to": c.To.Format("2006-01-02"),
			"total": len(events),
		}, ""

	case "activity-status", "source.status", "source-status", "status":
		c, msg := buildContext(p.Input)
		if msg != "" {
			return nil, msg
		}
		return map[string]interface{}{
			"status": buildStatus(c, nil, nil),
			"advice": auditAdvice(),
		}, ""

	case "report.export", "report-export", "export":
		c, msg := buildContext(p.Input)
		if msg != "" {
			return nil, msg
		}
		types := inputTypes(p.Input)
		key := cacheKey(c)
		rc, ok := cacheGet(key)
		if !ok {
			events, sources := collectAll(c)
			rc = resultCache{key: key, events: events, sources: sources, ctx: c,
				status: buildStatus(c, events, sources)}
			cacheSet(key, rc.events, rc.sources, rc.ctx, rc.status)
		}
		outEvents := filterTypes(rc.events, types)
		format := strings.ToLower(inputStr(p.Input, "format"))
		if format == "" {
			format = "csv"
		}
		content := exportCSV(outEvents)
		if format == "html" || format == "htm" {
			content = exportHTML(outEvents, rc.ctx.From.Format("2006-01-02"),
				rc.ctx.To.Format("2006-01-02"), rc.sources)
		} else {
			format = "csv"
		}
		return map[string]interface{}{
			"format": format, "ext": format, "content": content, "count": len(outEvents),
		}, ""

	case "audit.advice", "audit-advice":
		return map[string]interface{}{
			"advice": auditAdvice(),
			"note":   "只读返回命令清单，插件不会执行；开启后也只记录开启之后的行为，无法补录过去",
		}, ""

	case "backend-ping":
		return map[string]interface{}{"ok": true}, ""

	default:
		return nil, "unknown command: " + p.Command
	}
}

// ---------------------------------------------------------------- RPC 输出

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
