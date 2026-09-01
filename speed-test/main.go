// 网速测试 — 流式下载测速 + 延迟测量
//
// 流程：单次流式 GET 目标测速节点，记录首字节时间(TTFB)作为延迟，
// 随后持续读取流并实时计算下载速率(MB/s、Mbps)。
// 因大文件下载常超宿主 20s 执行限制，采用「start 立即返回 + 前端轮询」的异步会话模型。
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// 默认测速节点：Cloudflare 的下载端点，?bytes=N 精确返回 N 字节，Content-Length 即总大小。
const defaultNodeURL = "https://speed.cloudflare.com/__down?bytes=25000000"

/* ==================== JSON-RPC ==================== */

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

func strFrom(m map[string]interface{}, k string) string {
	if v, ok := m[k].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func intFrom(m map[string]interface{}, k string, def int) int {
	switch v := m[k].(type) {
	case float64:
		n := int(v)
		if n > 0 {
			return n
		}
	case string:
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func respond(id int64, result interface{}) {
	out, _ := json.Marshal(map[string]interface{}{"jsonrpc": "2.0", "id": id, "result": result})
	fmt.Println(string(out))
}

func respondError(id int64, code int, msg string) {
	out, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0", "id": id,
		"error": map[string]interface{}{"code": code, "message": msg},
	})
	fmt.Println(string(out))
}

/* ==================== 会话 ==================== */

type session struct {
	ID   string
	mu   sync.Mutex
	running bool
	stopCh  chan struct{}

	URL      string
	NodeName string

	latencyMs  float64
	downloaded int64
	total      int64
	elapsedMs  int64
	speedMBps  float64
	speedMbps  float64
	errMsg     string
}

var (
	sessMu   sync.Mutex
	sessions = map[string]*session{}
	seqID    int
)

func (s *session) isRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

func (s *session) snapshot() map[string]interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return map[string]interface{}{
		"sessionId": s.ID,
		"running":   s.running,
		"url":       s.URL,
		"nodeName":  s.NodeName,
		"latencyMs": s.latencyMs,
		"downloaded": s.downloaded,
		"total":      s.total,
		"elapsedMs": s.elapsedMs,
		"speedMBps": s.speedMBps,
		"speedMbps": s.speedMbps,
		"error":     s.errMsg,
	}
}

func (s *session) run() {
	defer func() {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-s.stopCh:
			cancel()
		case <-ctx.Done():
		}
	}()

	client := &http.Client{Timeout: 90 * time.Second}
	req, err := http.NewRequestWithContext(ctx, "GET", s.URL, nil)
	if err != nil {
		s.mu.Lock()
		s.errMsg = err.Error()
		s.mu.Unlock()
		return
	}
	req.Header.Set("User-Agent", "QuickDock-SpeedTest/1.0")

	t0 := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		s.mu.Lock()
		s.errMsg = err.Error()
		s.mu.Unlock()
		return
	}
	defer resp.Body.Close()
	if resp.ContentLength > 0 {
		s.mu.Lock()
		s.total = resp.ContentLength
		s.mu.Unlock()
	}

	buf := make([]byte, 64*1024)
	// 首块用于计算延迟（TTFB）
	n, _ := resp.Body.Read(buf)
	ttfb := float64(time.Since(t0).Microseconds()) / 1000.0
	s.mu.Lock()
	s.latencyMs = ttfb
	if n > 0 {
		s.downloaded = int64(n)
	}
	s.mu.Unlock()

	dl := int64(n)
	for {
		select {
		case <-s.stopCh:
			break
		default:
		}
		nr, rerr := resp.Body.Read(buf)
		if nr > 0 {
			dl += int64(nr)
		}
		now := time.Now()
		el := now.Sub(t0).Seconds()
		s.mu.Lock()
		s.downloaded = dl
		s.elapsedMs = int64(now.Sub(t0).Milliseconds())
		if el > 0.3 {
			s.speedMBps = float64(dl) / 1024 / 1024 / el
			s.speedMbps = s.speedMBps * 8
		}
		s.mu.Unlock()
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			break
		}
	}

	// 收尾定速
	s.mu.Lock()
	el := time.Since(t0).Seconds()
	if el > 0 {
		s.speedMBps = float64(dl) / 1024 / 1024 / el
		s.speedMbps = s.speedMBps * 8
	}
	s.downloaded = dl
	s.elapsedMs = int64(time.Since(t0).Milliseconds())
	s.running = false
	s.mu.Unlock()
}

/* ==================== 命令处理 ==================== */

func handleStart(id int64, input map[string]interface{}) {
	url := strFrom(input, "url")
	if url == "" {
		url = defaultNodeURL
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		respondError(id, -32602, "URL 必须以 http:// 或 https:// 开头")
		return
	}
	node := strFrom(input, "node")
	if node == "" {
		node = "默认节点"
	}

	sessMu.Lock()
	seqID++
	sid := fmt.Sprintf("s%d", seqID)
	s := &session{
		ID:        sid,
		URL:       url,
		NodeName:  node,
		running:   true,
		stopCh:    make(chan struct{}),
	}
	sessions[sid] = s
	sessMu.Unlock()

	go s.run()

	respond(id, map[string]interface{}{
		"sessionId": sid,
		"url":       url,
		"nodeName":  node,
		"running":   true,
	})
}

func getSession(id int64, input map[string]interface{}) *session {
	sid := strFrom(input, "sessionId")
	if sid == "" {
		respondError(id, -32602, "缺少 sessionId")
		return nil
	}
	sessMu.Lock()
	s := sessions[sid]
	sessMu.Unlock()
	if s == nil {
		respondError(id, -32002, "会话不存在或已结束")
		return nil
	}
	return s
}

func handlePoll(id int64, input map[string]interface{}) {
	s := getSession(id, input)
	if s == nil {
		return
	}
	respond(id, s.snapshot())
}

func handleStop(id int64, input map[string]interface{}) {
	s := getSession(id, input)
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.running {
		s.running = false
		close(s.stopCh)
	}
	s.mu.Unlock()

	snap := s.snapshot()
	sessMu.Lock()
	delete(sessions, s.ID)
	sessMu.Unlock()

	snap["stopped"] = true
	respond(id, snap)
}

func dispatch(raw string) {
	var req rpcRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		respondError(0, -32700, "parse error: "+err.Error())
		return
	}
	switch req.Method {
	case "initialize":
		respond(req.ID, map[string]interface{}{"status": "ready", "name": "QuickDock Speed Test"})
	case "host.ping":
		respond(req.ID, map[string]interface{}{"pong": true})
	case "plugin.execute":
		var params executeParams
		if len(req.Params) > 0 {
			_ = json.Unmarshal(req.Params, &params)
		}
		switch strings.ToLower(strings.TrimSpace(params.Command)) {
		case "start":
			handleStart(req.ID, params.Input)
		case "poll":
			handlePoll(req.ID, params.Input)
		case "stop":
			handleStop(req.ID, params.Input)
		default:
			respondError(req.ID, -32601, "unknown command: "+params.Command)
		}
	default:
		respondError(req.ID, -32601, "unknown method: "+req.Method)
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
		wg.Add(1)
		go func(raw string) {
			defer wg.Done()
			dispatch(raw)
		}(data)
	}
	wg.Wait()
}
