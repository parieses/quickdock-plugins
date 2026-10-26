// qdlog.go — native 插件运行期日志（走宿主 log.* 通道，落 plugin-YYYYMMDD.log）
//
// 与宿主主日志同一套分级（I = info / W = warn / E = error）、同一个日志目录、
// 同一条时间线：宿主记「何时拉起 / 停止 / 重启插件」，这里记「插件内部做了什么、
// 哪一步失败、失败在哪个命令上」。行首的 [plugin:<id>] 由宿主补，插件不用自己拼。
//
// 只做四件事：
//  1. 进程启动 / initialize 握手 / 退出，各留一条锚点日志；
//  2. 每条命令的开始与结束（含耗时），高频轮询类命令做节流，避免刷屏；
//  3. 所有 respondError 出口统一记录（错误码分级：参数错误记 W，未知命令记 W，其余记 E），
//     这样每个 handler 不必各写一遍——任何失败路径都不会再静默；
//  4. panic 一律记 ERROR 并带调用栈前几帧（此前只会散落到 stderr 记 WARN，且按行拆碎）。
//
// 协议约束（踩过的坑，别改）：
//   - log.* 必须带 id：宿主会**静默丢弃**无 id 的通知，日志就丢了；
//   - 日志请求的自增 id 从 1<<40 起，与插件自己发起的 host.* 调用 id 不重叠——
//     否则「吞掉宿主回包」时可能误吞插件正在等待的 host.* 响应，直接卡死调用方；
//   - 只吞自己发出过的 log.* 回包（method 为空且 id 命中 qdPending），其余原样交回。
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	qdLogIDBase   = int64(1) << 40 // 与插件自身 host.* 调用 id 的取值区间隔离
	qdMsgMaxRunes = 900            // 单条日志上限（宿主一行一条，过长会污染日志结构）
	qdSlowCommand = 10 * time.Second
)

var (
	qdLogSeq    int64
	qdOutMu     sync.Mutex
	qdStateMu   sync.Mutex
	qdPending   = make(map[int64]struct{}, 32) // 已发出、等待宿主回包的 log.* id
	qdInflight  = make(map[int64]qdCallInfo, 32)
	qdThrottle  = make(map[string]time.Time, 64)
	qdStartedAt = time.Now()
)

type qdCallInfo struct {
	method  string
	command string
	start   time.Time
}

// ---------- 基础输出 ----------

// qdEmit 以 JSON-RPC 请求写一条 log.*（宿主先落盘再回包，不阻塞后续命令）。
func qdEmit(level, msg string) {
	id := atomic.AddInt64(&qdLogSeq, 1) + qdLogIDBase
	line, err := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  "log." + level,
		"params":  map[string]string{"message": qdOneLine(msg)},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "qdlog marshal failed: %v\n", err)
		return
	}
	line = append(line, '\n')

	qdStateMu.Lock()
	qdPending[id] = struct{}{}
	if len(qdPending) > 4096 { // 宿主长期不回包时的兜底，防止无界增长
		qdPending = make(map[int64]struct{}, 32)
	}
	qdStateMu.Unlock()

	qdOutMu.Lock()
	_, werr := os.Stdout.Write(line)
	qdOutMu.Unlock()
	if werr != nil { // stdout 断了（宿主已退出）：退到 stderr，宿主仍会记 WARN
		fmt.Fprintf(os.Stderr, "qdlog write failed: %v\n", werr)
	}
}

func qdLogInfo(format string, args ...interface{})  { qdEmit("info", fmt.Sprintf(format, args...)) }
func qdLogWarn(format string, args ...interface{})  { qdEmit("warn", fmt.Sprintf(format, args...)) }
func qdLogError(format string, args ...interface{}) { qdEmit("error", fmt.Sprintf(format, args...)) }

// qdLogPanic 记录 panic（含调用栈前 6 帧），替代原先散落 stderr 的多行输出。
func qdLogPanic(where string, r interface{}) {
	qdLogError("PANIC where=%s err=%v stack=%s", where, r, qdStack(6))
}

// qdWaitPanic 用于「handler 必回包」的场景：先记日志再让调用方回 JSON-RPC error。
func qdWaitPanic(where string, r interface{}) string {
	qdLogPanic(where, r)
	return fmt.Sprintf("internal error: %v", r)
}

// ---------- 生命周期 ----------

// qdLogStart 在 main 开头调用，留一条进程锚点（与宿主的「按需惰性复活插件」对齐）。
func qdLogStart() {
	exe, _ := os.Executable()
	qdLogInfo("进程启动 pid=%d os=%s/%s exe=%s", os.Getpid(), runtime.GOOS, runtime.GOARCH, exe)
}

// qdLogExit 用 defer 挂在 main 上，记录退出与总运行时长（stdin 关闭 = 宿主主动停止）。
func qdLogExit() {
	qdLogInfo("进程退出 运行时长=%s", qdDur(time.Since(qdStartedAt)))
}

// ---------- stdin 观察 ----------

// qdObserveLine 在读循环拿到一行非空输入后调用，返回 true 表示「这行是本插件日志的回包，
// 直接跳过」；返回 false 时调用方照旧交给 dispatch/handleRequest，不改变任何既有语义。
func qdObserveLine(raw string) bool {
	var probe struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal([]byte(raw), &probe); err != nil {
		// 解析失败：宿主侧协议错乱或插件被写坏，值得留一条 W；后续仍交给原逻辑处理
		qdLogWarn("stdin 行解析失败 err=%v line=%s", err, qdTrunc(raw, 200))
		return false
	}
	// id 归一化为 int64 供日志追踪；前端桥接常用字符串 id（如 "ss_1"），无法转数字时记 0。
	id := idToInt(probe.ID)
	if probe.Method == "" {
		qdStateMu.Lock()
		_, ours := qdPending[id]
		if ours {
			delete(qdPending, id)
		}
		qdStateMu.Unlock()
		return ours // 只有自己 log.* 的回包才吞，host.* 的回包必须交回
	}

	switch probe.Method {
	case "initialize":
		var p struct {
			HostVersion string `json:"hostVersion"`
			PluginDir   string `json:"pluginDir"`
		}
		_ = json.Unmarshal(probe.Params, &p)
		qdTrack(probe.Method, id, "")
		qdLogInfo("initialize 握手 host=%s dir=%s", p.HostVersion, p.PluginDir)
	case "plugin.execute":
		var p struct {
			Command string `json:"command"`
		}
		_ = json.Unmarshal(probe.Params, &p)
		qdTrack(probe.Method, id, p.Command)
		if qdThrottled("start:"+p.Command, 2*time.Second) {
			qdLogInfo("命令开始 command=%s id=%d", p.Command, id)
		}
	case "shutdown", "terminate", "dispose", "exit":
		qdLogInfo("收到 %s，准备退出（已运行 %s）", probe.Method, qdDur(time.Since(qdStartedAt)))
	case "host.ping", "health.ping":
		// 健康检查高频，不记；超时由宿主侧记录
	default:
		qdTrack(probe.Method, id, "")
		if qdThrottled("method:"+probe.Method, 5*time.Second) {
			qdLogInfo("收到请求 method=%s id=%d", probe.Method, id)
		}
	}
	return false
}

// idToInt 把请求 id 归一化为 int64 供日志追踪使用；字符串 id（前端桥接常用）无法转数字时返回 0。
// 内联在本文件而非各插件 main.go，保证 qdlog.go 自包含、可逐字节同步到所有插件。
func idToInt(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	var n float64
	if err := json.Unmarshal(raw, &n); err == nil {
		return int64(n)
	}
	return 0
}

// qdTrack 记一次在途请求，供 respond/respondError 计算耗时与上下文。
// 只保留有限条数：插件若不回包（或回包 id 不匹配），不能让 map 无界增长。
func qdTrack(method string, id int64, command string) {
	if id <= 0 {
		return
	}
	qdStateMu.Lock()
	if len(qdInflight) < 512 {
		qdInflight[id] = qdCallInfo{method: method, command: command, start: time.Now()}
	}
	qdStateMu.Unlock()
}

// qdFinishRequest 由 respond / respondError 调用（defer 形式），一条请求只记一次。
// code == 0 表示成功；其余按错误码分级：参数错 / 未知命令记 W，业务失败记 E。
func qdFinishRequest(id int64, code int, msg string) {
	qdStateMu.Lock()
	call, ok := qdInflight[id]
	if ok {
		delete(qdInflight, id)
	}
	qdStateMu.Unlock()

	if !ok {
		// 没有上下文的错误响应（如解析失败时的 id=0、异步任务的合成 id）仍然要留痕
		if code != 0 {
			qdLogWarn("失败响应（无请求上下文）code=%d msg=%s", code, msg)
		}
		return
	}

	label := call.command
	if label == "" {
		label = call.method
	}
	elapsed := time.Since(call.start)

	switch {
	case code == 0:
		// 高频轮询命令（task-status / progress 之类）只按节流记，避免刷屏
		if elapsed >= time.Second || qdThrottled("done:"+label, 2*time.Second) {
			qdLogInfo("%s 完成 耗时=%s", label, qdDur(elapsed))
		}
		if elapsed >= qdSlowCommand {
			qdLogWarn("%s 耗时偏高 耗时=%s（宿主默认 20s 超时，可调 plugin.json backend.timeout）",
				label, qdDur(elapsed))
		}
	case code == -32601: // method / command not found
		qdLogWarn("%s 未识别 code=%d msg=%s", label, code, msg)
	case code == -32700, code == -32602: // parse error / invalid params
		qdLogWarn("%s 请求不合法 code=%d msg=%s", label, code, msg)
	default:
		qdLogError("%s 失败 code=%d msg=%s 耗时=%s", label, code, msg, qdDur(elapsed))
	}
}

// ---------- 小工具 ----------

// qdThrottled 同一 key 在 d 内只放行一次。
func qdThrottled(key string, d time.Duration) bool {
	now := time.Now()
	qdStateMu.Lock()
	defer qdStateMu.Unlock()
	if t, ok := qdThrottle[key]; ok && now.Sub(t) < d {
		return false
	}
	if len(qdThrottle) > 256 {
		qdThrottle = make(map[string]time.Time, 64)
	}
	qdThrottle[key] = now
	return true
}

// qdDur 人类可读耗时（与宿主日志同风格：38ms / 1.2s）。
func qdDur(d time.Duration) string {
	switch {
	case d < time.Millisecond:
		return fmt.Sprintf("%dµs", d.Microseconds())
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	default:
		return fmt.Sprintf("%.2fs", d.Seconds())
	}
}

// qdOneLine 压成一行：换行会破坏「一行一条日志」的结构，超长按 rune 截断（不切坏中文）。
func qdOneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= qdMsgMaxRunes {
		return s
	}
	return string(r[:qdMsgMaxRunes]) + "…(truncated)"
}

func qdTrunc(s string, maxRunes int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes]) + "…(truncated)"
}

// qdStack 取调用栈前 n 帧（去掉 goroutine 头与参数噪音，够定位即可）。
func qdStack(n int) string {
	lines := strings.Split(string(debug.Stack()), "\n")
	if len(lines) > 1 {
		lines = lines[1:] // 首行是 "goroutine N [running]:"
	}
	if len(lines) > n*2 {
		lines = lines[:n*2]
	}
	return strings.Join(lines, " ")
}
