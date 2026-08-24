// API LoadTest - 功能丰富的 HTTP 接口压测工具
// JSON-RPC 2.0 over stdin/stdout（对齐 QuickDock 宿主约定，参考 disk-analyzer）
//
// 协议：
//   - initialize       握手
//   - host.ping        健康检查
//   - plugin.execute   业务入口，params = {command, input{text: JSON.stringify(cfg)}}
//
// 命令：
//   - bench-start   启动压测（后台 goroutine），立即返回 {started, runId}
//   - bench-status  轮询统计快照（累计值，前端据此算实时 QPS）
//   - bench-stop    中止当前压测
//   - bench-export  导出完整结果 JSON（供复制/存档）

package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ---- JSON-RPC 结构 ----

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// hostResponse 宿主对 host.* 调用的响应（无 method 字段，仅含 id/result/error）
type hostResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type executeParams struct {
	Command string                 `json:"command"`
	Input   map[string]interface{} `json:"input"`
}

// ---- 配置 ----

type benchConfig struct {
	URL          string            `json:"url"`
	Method       string            `json:"method"`
	Headers      map[string]string `json:"headers"`
	HeaderText   string            `json:"headerText"` // 备选：多行 "Key: Value"
	Body         string            `json:"body"`
	Concurrency  int               `json:"concurrency"`
	Requests     int64             `json:"requests"`     // 请求数模式
	DurationSec  int               `json:"durationSec"`  // 时长模式（>0 优先生效）
	TimeoutMs    int               `json:"timeoutMs"`
	RampUpSec    int               `json:"rampUpSec"`
}

// 延迟直方图桶上界（毫秒），最后一个为 +inf
var latBounds = []int64{1, 2, 5, 10, 20, 50, 100, 200, 500, 1000, 2000, 5000, 10000}

// ---- 压测运行态 ----

type benchRun struct {
	cfg        benchConfig
	startTime  time.Time
	stopTime   time.Time
	done       atomic.Bool
	stopped    atomic.Bool
	mu         sync.Mutex
	seq        int64 // 运行序号，用于结果导出标识
	target     int64 // 请求数模式的目标
	mode       string // "requests" | "duration"
	sent       int64
	success    int64
	failed     int64
	sumLatency int64 // 累计延迟(ms)，用于均值
	maxLatency int64
	buckets    []int64                 // 长度=len(latBounds)
	statusCodes map[int]int64
	errSamples []string
	lastErr    string
}

var (
	runsMu sync.Mutex
	run    *benchRun
	runSeq int64
)

// 并发写保护与 host 回调响应路由
var (
	writeMu       sync.Mutex       // 保护 stdout 并发写（dispatch 在独立 goroutine 执行）
	hostRespChans sync.Map         // int64 -> chan *hostResponse，用于 host.* 调用回传
	hostReqID     int64
)

func newBenchRun(cfg benchConfig) *benchRun {
	return &benchRun{
		cfg:        cfg,
		startTime:  time.Now(),
		buckets:    make([]int64, len(latBounds)),
		statusCodes: map[int]int64{},
		errSamples:  make([]string, 0, 20),
		target:      cfg.Requests,
		mode:       "requests",
	}
}

// record 记录一次完成的请求结果
func (r *benchRun) record(latencyMs int64, statusCode int, isErr bool, errMsg string) {
	atomic.AddInt64(&r.sent, 1)
	atomic.AddInt64(&r.sumLatency, latencyMs)
	// 最大延迟
	for {
		old := atomic.LoadInt64(&r.maxLatency)
		if latencyMs <= old || atomic.CompareAndSwapInt64(&r.maxLatency, old, latencyMs) {
			break
		}
	}
	if isErr {
		atomic.AddInt64(&r.failed, 1)
	} else {
		atomic.AddInt64(&r.success, 1)
	}
	// 直方图
	idx := len(latBounds) - 1
	for i, b := range latBounds {
		if latencyMs <= b {
			idx = i
			break
		}
	}
	atomic.AddInt64(&r.buckets[idx], 1)
	// 状态码 + 错误样本（加锁，低频写）
	r.mu.Lock()
	if statusCode > 0 {
		r.statusCodes[statusCode]++
	}
	if isErr && errMsg != "" {
		r.lastErr = errMsg
		if len(r.errSamples) < 20 {
			r.errSamples = append(r.errSamples, errMsg)
		}
	}
	r.mu.Unlock()
}

// snapshot 返回当前统计快照（前端据此算实时 QPS）
func (r *benchRun) snapshot() map[string]interface{} {
	sent := atomic.LoadInt64(&r.sent)
	success := atomic.LoadInt64(&r.success)
	failed := atomic.LoadInt64(&r.failed)
	sumLat := atomic.LoadInt64(&r.sumLatency)
	elapsed := time.Since(r.startTime).Seconds()
	var avg float64
	if sent > 0 {
		avg = float64(sumLat) / float64(sent)
	}
	r.mu.Lock()
	statusCodes := make(map[string]int64, len(r.statusCodes))
	for k, v := range r.statusCodes {
		statusCodes[fmt.Sprintf("%d", k)] = v
	}
	errSamples := append([]string{}, r.errSamples...)
	lastErr := r.lastErr
	r.mu.Unlock()

	total := sent
	var errRate float64
	if total > 0 {
		errRate = float64(failed) / float64(total) * 100
	}
	p50, p90, p95, p99 := r.percentiles()

	hist := make([]int64, len(r.buckets))
	copy(hist, r.buckets)

	return map[string]interface{}{
		"sent":        sent,
		"success":     success,
		"failed":      failed,
		"target":      r.target,
		"mode":        r.mode,
		"elapsedSec":  float64(int(elapsed*100)) / 100,
		"avgLatency":  float64(int(avg*100)) / 100,
		"maxLatency":  atomic.LoadInt64(&r.maxLatency),
		"errRate":     float64(int(errRate*100)) / 100,
		"p50":         p50,
		"p90":         p90,
		"p95":         p95,
		"p99":         p99,
		"statusCodes": statusCodes,
		"errSamples":  errSamples,
		"lastErr":     lastErr,
		"done":        r.done.Load(),
		"stopped":     r.stopped.Load(),
		// 延迟直方图（桶计数 + 桶上界，单位 ms），供前端绘图
		"latencyHistogram": hist,
		"latencyBounds":    latBounds,
	}
}

// percentiles 通过直方图累计估算 p50/p90/p95/p99（线性插值）
func (r *benchRun) percentiles() (p50, p90, p95, p99 int64) {
	total := int64(0)
	for _, c := range r.buckets {
		total += c
	}
	if total == 0 {
		return 0, 0, 0, 0
	}
	maxLat := atomic.LoadInt64(&r.maxLatency)
	calc := func(pct float64) int64 {
		rank := int64(pct * float64(total))
		if rank <= 0 {
			rank = 1
		}
		cum := int64(0)
		for i := 0; i < len(r.buckets); i++ {
			lo := int64(0)
			if i > 0 {
				lo = latBounds[i-1]
			}
			hi := latBounds[i]
			cnt := r.buckets[i]
			if cum+cnt >= rank {
				if cnt == 0 {
					return hi
				}
				frac := float64(rank-cum) / float64(cnt)
				val := lo + int64(frac*float64(hi-lo))
				// 落在最后(+inf)桶时用真实最大延迟更准
				if i == len(r.buckets)-1 && maxLat > hi {
					return maxLat
				}
				return val
			}
			cum += cnt
		}
		return maxLat
	}
	return calc(0.50), calc(0.90), calc(0.95), calc(0.99)
}

// ---- main / RPC 分发 ----

var stdout = bufio.NewWriter(os.Stdout)

func main() {
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
		go dispatch(data)
	}
}

// dispatch 解析一行 JSON-RPC，区分宿主响应（无 method）与请求（有 method）
func dispatch(data string) {
	var probe struct{ Method string `json:"method"` }
	if json.Unmarshal([]byte(data), &probe) == nil && probe.Method == "" {
		var hr hostResponse
		if json.Unmarshal([]byte(data), &hr) == nil && hr.ID > 0 {
			if chI, ok := hostRespChans.Load(hr.ID); ok {
				chI.(chan *hostResponse) <- &hr
				return
			}
		}
	}
	var req rpcRequest
	if err := json.Unmarshal([]byte(data), &req); err != nil {
		respondError(0, -32700, "parse error: "+err.Error())
		return
	}
	handleRequest(req)
}

func handleRequest(req rpcRequest) {
	switch req.Method {
	case "initialize":
		respond(req.ID, map[string]interface{}{"status": "ready", "name": "QuickDock API LoadTest"})
	case "host.ping", "ping":
		respond(req.ID, map[string]interface{}{"pong": true})
	case "plugin.execute":
		handleExecute(req)
	default:
		respondError(req.ID, -32601, "unknown method: "+req.Method)
	}
}

func handleExecute(req rpcRequest) {
	var params executeParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			respondError(req.ID, -32602, "invalid params: "+err.Error())
			return
		}
	}
	input := params.Input
	if input == nil {
		input = map[string]interface{}{}
	}
	// 兼容前端 pluginExec 打包：input.text = JSON.stringify(cfg)
	if textRaw, ok := input["text"].(string); ok && textRaw != "" {
		trimmed := strings.TrimSpace(textRaw)
		if strings.HasPrefix(trimmed, "{") {
			var nested map[string]interface{}
			if err := json.Unmarshal([]byte(trimmed), &nested); err == nil {
				for k, v := range nested {
					if _, exists := input[k]; !exists {
						input[k] = v
					}
				}
			}
		}
	}

	cmd := strings.ToLower(strings.TrimSpace(params.Command))
	cmd = strings.ReplaceAll(cmd, ".", "-")
	switch cmd {
	case "bench-start", "start":
		handleStart(req.ID, input)
	case "bench-status", "status":
		handleStatus(req.ID)
	case "bench-stop", "stop":
		handleStop(req.ID)
	case "bench-export", "export":
		handleExport(req.ID)
	case "bench-copy", "copy":
		handleCopy(req.ID, input)
	default:
		respondError(req.ID, -32601, "unknown command: "+params.Command)
	}
}

// ---- 命令处理 ----

func handleStart(id int64, input map[string]interface{}) {
	runsMu.Lock()
	if run != nil && !run.done.Load() && !run.stopped.Load() {
		runsMu.Unlock()
		respond(id, map[string]interface{}{"started": false, "alreadyRunning": true})
		return
	}

	cfg := benchConfig{
		URL:         strFrom(input, "url"),
		Method:      strings.ToUpper(strFrom(input, "method")),
		Body:        strFrom(input, "body"),
		Concurrency: intFrom(input, "concurrency", 10),
		Requests:    int64(intFrom(input, "requests", 100)),
		DurationSec: intFrom(input, "durationSec", 0),
		TimeoutMs:   intFrom(input, "timeoutMs", 10000),
		RampUpSec:   intFrom(input, "rampUpSec", 0),
	}
	// Header 解析：优先 map，其次多行文本
	if hm, ok := input["headers"].(map[string]interface{}); ok {
		cfg.Headers = map[string]string{}
		for k, v := range hm {
			cfg.Headers[k] = fmt.Sprintf("%v", v)
		}
	} else if ht := strFrom(input, "headerText"); ht != "" {
		cfg.Headers = parseHeaderText(ht)
	}
	if cfg.Method == "" {
		cfg.Method = "GET"
	}
	if cfg.Concurrency < 1 {
		cfg.Concurrency = 1
	}
	if cfg.TimeoutMs < 1 {
		cfg.TimeoutMs = 10000
	}

	if cfg.URL == "" {
		runsMu.Unlock()
		respondError(id, -32602, "缺少 url 参数")
		return
	}
	if !strings.HasPrefix(cfg.URL, "http://") && !strings.HasPrefix(cfg.URL, "https://") {
		runsMu.Unlock()
		respondError(id, -32602, "url 必须以 http:// 或 https:// 开头")
		return
	}

	r := newBenchRun(cfg)
	if cfg.DurationSec > 0 {
		r.mode = "duration"
		r.target = 0
	} else {
		if r.target < 1 {
			r.target = 100
		}
	}
	runSeq++
	r.seq = runSeq
	run = r
	runsMu.Unlock()

	go r.run()
	respond(id, map[string]interface{}{
		"started":  true,
		"runId":    r.seq,
		"mode":     r.mode,
		"target":   r.target,
		"url":      cfg.URL,
		"method":   cfg.Method,
		"rampUpSec": cfg.RampUpSec,
	})
}

// seq 字段加到 benchRun（export 用）

func handleStatus(id int64) {
	runsMu.Lock()
	r := run
	runsMu.Unlock()
	if r == nil {
		respond(id, map[string]interface{}{"done": true, "idle": true})
		return
	}
	respond(id, r.snapshot())
}

func handleStop(id int64) {
	runsMu.Lock()
	r := run
	runsMu.Unlock()
	if r == nil {
		respond(id, map[string]interface{}{"ok": false, "reason": "no active run"})
		return
	}
	r.stopped.Store(true)
	respond(id, map[string]interface{}{"ok": true, "stopped": true})
}

func handleExport(id int64) {
	runsMu.Lock()
	r := run
	runsMu.Unlock()
	if r == nil {
		respondError(id, -32603, "没有可导出的压测结果")
		return
	}
	snap := r.snapshot()
	snap["config"] = r.cfg
	snap["runId"] = r.seq
	snap["stopTime"] = r.stopTime.Format(time.RFC3339)
	respond(id, snap)
}

// handleCopy 将文本写入系统剪贴板（经宿主 host.clipboard.write，需 clipboard 权限）
func handleCopy(id int64, input map[string]interface{}) {
	text := strFrom(input, "text")
	if text == "" {
		respondError(id, -32602, "缺少 text 参数")
		return
	}
	if _, err := callHostMethod("host.clipboard.write", map[string]interface{}{"text": text}); err != nil {
		respondError(id, -32001, "复制失败: "+err.Error())
		return
	}
	respond(id, map[string]interface{}{"ok": true})
}

// callHostMethod 向宿主发起 host.* 调用并等待响应（在 dispatch goroutine 内阻塞，
// 响应由 main 读取循环经 hostRespChans 路由回来，不会死锁）
func callHostMethod(method string, params interface{}) (json.RawMessage, error) {
	id := atomic.AddInt64(&hostReqID, 1)
	payload, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	raw := json.RawMessage(payload) // 必须 RawMessage，否则 []byte 会被 base64 编码
	ch := make(chan *hostResponse, 1)
	hostRespChans.Store(id, ch)
	defer hostRespChans.Delete(id)
	sendJSON(map[string]interface{}{"jsonrpc": "2.0", "id": id, "method": method, "params": raw})
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case resp := <-ch:
		if resp.Error != nil {
			return nil, fmt.Errorf("%s", resp.Error.Message)
		}
		return resp.Result, nil
	case <-timer.C:
		return nil, fmt.Errorf("host 调用超时: %s", method)
	}
}

// ---- 压测执行 ----

func (r *benchRun) run() {
	defer func() {
		r.stopTime = time.Now()
		r.done.Store(true)
	}()

	client := &http.Client{
		Timeout: time.Duration(r.cfg.TimeoutMs) * time.Millisecond,
		Transport: &http.Transport{
			TLSClientConfig:     &tls.Config{InsecureSkipVerify: true},
			DisableKeepAlives:   false,
			MaxIdleConns:        r.cfg.Concurrency,
			MaxIdleConnsPerHost: r.cfg.Concurrency,
		},
	}
	ctx := context.Background()

	// 时长模式：到期自动停止
	if r.mode == "duration" {
		go func() {
			time.Sleep(time.Duration(r.cfg.DurationSec) * time.Second)
			r.stopped.Store(true)
		}()
	}

	// ramp-up：按节奏启动 worker，从 1 逐步增到 concurrency
	concurrency := r.cfg.Concurrency
	var wg sync.WaitGroup
	alreadyStarted := 0
	if r.cfg.RampUpSec <= 0 {
		for i := 0; i < concurrency; i++ {
			wg.Add(1)
			go r.worker(client, ctx, &wg)
			alreadyStarted++
		}
	} else {
		// 每秒启动 step 个，直到满
		step := max(1, concurrency/r.cfg.RampUpSec)
		deadline := time.After(time.Duration(r.cfg.RampUpSec) * time.Second)
		for alreadyStarted < concurrency {
			wg.Add(1)
			go r.worker(client, ctx, &wg)
			alreadyStarted++
			if alreadyStarted >= concurrency {
				break
			}
			select {
			case <-deadline:
				// ramp-up 时间到，补齐剩余 worker
				for alreadyStarted < concurrency {
					wg.Add(1)
					go r.worker(client, ctx, &wg)
					alreadyStarted++
				}
				goto launched
			case <-time.After(time.Duration(step) * time.Second / time.Duration(max(1, concurrency/r.cfg.RampUpSec))):
			}
		}
	}
launched:
	wg.Wait()
}

func (r *benchRun) worker(client *http.Client, ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()
	for {
		if r.stopped.Load() {
			return
		}
		if r.mode == "requests" && atomic.LoadInt64(&r.sent) >= r.target {
			return
		}
		latency, statusCode, isErr, errMsg := doRequest(client, ctx, r.cfg)
		r.record(latency, statusCode, isErr, errMsg)
	}
}

func doRequest(client *http.Client, ctx context.Context, cfg benchConfig) (latencyMs int64, statusCode int, isErr bool, errMsg string) {
	var bodyReader io.Reader
	method := cfg.Method
	if method == "" {
		method = "GET"
	}
	if method != "GET" && method != "HEAD" && cfg.Body != "" {
		bodyReader = strings.NewReader(cfg.Body)
	}
	req, err := http.NewRequestWithContext(ctx, method, cfg.URL, bodyReader)
	if err != nil {
		return 0, 0, true, "构造请求失败: " + err.Error()
	}
	req.Header.Set("User-Agent", "QuickDock-APILoadTest/0.1")
	for k, v := range cfg.Headers {
		req.Header.Set(k, v)
	}

	start := time.Now()
	resp, err := client.Do(req)
	elapsed := time.Since(start)
	latencyMs = elapsed.Milliseconds()
	if err != nil {
		return latencyMs, 0, true, err.Error()
	}
	// 读完并丢弃 body，释放连接
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	statusCode = resp.StatusCode
	isErr = statusCode < 200 || statusCode >= 400
	return latencyMs, statusCode, isErr, ""
}

// ---- 响应 ----

func respond(id int64, result interface{}) {
	payload, err := json.Marshal(result)
	if err != nil {
		respondError(id, -32603, "marshal result failed: "+err.Error())
		return
	}
	writeResponse(rpcResponse{JSONRPC: "2.0", ID: id, Result: payload})
}

func respondError(id int64, code int, msg string) {
	writeResponse(rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}})
}

func writeResponse(resp rpcResponse) {
	data, err := json.Marshal(resp)
	if err != nil {
		return
	}
	writeMu.Lock()
	defer writeMu.Unlock()
	stdout.Write(data)
	stdout.WriteByte('\n')
	stdout.Flush()
}

// sendJSON 直接写一条 JSON-RPC 消息（用于向宿主发起 host.* 调用）
func sendJSON(v interface{}) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	writeMu.Lock()
	defer writeMu.Unlock()
	stdout.Write(data)
	stdout.WriteByte('\n')
	stdout.Flush()
}

// ---- 工具 ----

func strFrom(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func boolFrom(m map[string]interface{}, key string) bool {
	if v, ok := m[key].(bool); ok {
		return v
	}
	return false
}

func intFrom(m map[string]interface{}, key string, def int) int {
	switch v := m[key].(type) {
	case float64:
		if v > 0 {
			return int(v)
		}
	case int:
		if v > 0 {
			return v
		}
	case string:
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 0 {
			return n
		}
	}
	return def
}

// parseHeaderText 解析多行 "Key: Value" 头
func parseHeaderText(text string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, ":") {
			continue
		}
		idx := strings.Index(line, ":")
		k := strings.TrimSpace(line[:idx])
		v := strings.TrimSpace(line[idx+1:])
		if k != "" {
			out[k] = v
		}
	}
	return out
}
