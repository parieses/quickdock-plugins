// WebSocket Tester - 连接 ws/wss 并收发消息（原生 JSON-RPC 子进程，gorilla/websocket）
// 命令：
//   connect  input {url}            建立连接（异步，返回 connId 作为 taskId）
//   send     input {taskId,message} 向连接发送文本
//   close    input {taskId}         关闭连接
//   task-status input {taskId}      轮询连接状态与消息列表
package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

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

type wsConn struct {
	id       string
	conn     *websocket.Conn
	messages []map[string]interface{}
	closed   bool
}

var (
	connsMu sync.Mutex
	conns   = map[string]*wsConn{}
	connSeq int64
)

func strFrom(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
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

func handleConnect(id int64, input map[string]interface{}) {
	url := strings.TrimSpace(strFrom(input, "url"))
	if url == "" {
		respondError(id, -32602, "请输入 ws/wss URL")
		return
	}
	c, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		respondError(id, -1, "连接失败: "+err.Error())
		return
	}
	connSeq++
	cid := fmt.Sprintf("ws-%d", connSeq)
	wc := &wsConn{id: cid, conn: c, messages: []map[string]interface{}{}}
	connsMu.Lock()
	conns[cid] = wc
	connsMu.Unlock()

	go func() {
		for {
			mt, data, err := c.ReadMessage()
			connsMu.Lock()
			if wc.closed {
				connsMu.Unlock()
				return
			}
			if err != nil {
				wc.messages = append(wc.messages, map[string]interface{}{
					"dir":  "recv",
					"type": "close",
					"data": "连接已关闭: " + err.Error(),
					"ts":   time.Now().Format("15:04:05"),
				})
				wc.closed = true
				connsMu.Unlock()
				return
			}
			var payload string
			if mt == websocket.BinaryMessage {
				payload = "data:application/octet-stream;base64," + base64.StdEncoding.EncodeToString(data)
			} else {
				payload = string(data)
			}
			wc.messages = append(wc.messages, map[string]interface{}{
				"dir":  "recv",
				"type": "text",
				"data": payload,
				"ts":   time.Now().Format("15:04:05"),
			})
			connsMu.Unlock()
		}
	}()

	respond(id, map[string]interface{}{"async": true, "taskId": cid})
}

func handleSend(id int64, input map[string]interface{}) {
	cid := strFrom(input, "taskId")
	msg := strFrom(input, "message")
	connsMu.Lock()
	wc, ok := conns[cid]
	if !ok || wc.closed {
		connsMu.Unlock()
		respondError(id, -1, "连接不存在或已关闭")
		return
	}
	err := wc.conn.WriteMessage(websocket.TextMessage, []byte(msg))
	if err == nil {
		wc.messages = append(wc.messages, map[string]interface{}{
			"dir":  "send",
			"type": "text",
			"data": msg,
			"ts":   time.Now().Format("15:04:05"),
		})
	}
	connsMu.Unlock()
	if err != nil {
		respond(id, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	respond(id, map[string]interface{}{"ok": true})
}

func handleClose(id int64, input map[string]interface{}) {
	cid := strFrom(input, "taskId")
	connsMu.Lock()
	wc, ok := conns[cid]
	if !ok {
		connsMu.Unlock()
		respondError(id, -1, "连接不存在")
		return
	}
	wc.closed = true
	c := wc.conn
	delete(conns, cid)
	connsMu.Unlock()
	c.Close()
	respond(id, map[string]interface{}{"ok": true})
}

func handleTaskStatus(id int64, input map[string]interface{}) {
	cid := strFrom(input, "taskId")
	connsMu.Lock()
	wc, ok := conns[cid]
	if !ok {
		connsMu.Unlock()
		respond(id, map[string]interface{}{"status": "missing", "taskId": cid})
		return
	}
	msgs := wc.messages
	state := "open"
	if wc.closed {
		state = "closed"
	}
	count := len(msgs)
	connsMu.Unlock()

	// 仅回传最近 500 条，避免轮询包过大
	if len(msgs) > 500 {
		msgs = msgs[len(msgs)-500:]
	}
	respond(id, map[string]interface{}{
		"status":   "done",
		"state":    state,
		"messages": msgs,
		"count":    count,
	})
}

func dispatch(raw string) {
	var req rpcRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		respondError(0, -32700, "parse error: "+err.Error())
		return
	}
	switch req.Method {
	case "initialize":
		respond(req.ID, map[string]interface{}{"status": "ready", "name": "QuickDock WS Tester"})
	case "host.ping":
		respond(req.ID, map[string]interface{}{"pong": true})
	case "plugin.execute":
		var params executeParams
		if len(req.Params) > 0 {
			_ = json.Unmarshal(req.Params, &params)
		}
		switch strings.ToLower(strings.TrimSpace(params.Command)) {
		case "connect":
			handleConnect(req.ID, params.Input)
		case "send":
			handleSend(req.ID, params.Input)
		case "close":
			handleClose(req.ID, params.Input)
		case "task-status":
			handleTaskStatus(req.ID, params.Input)
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
