// Hash Calc - 计算文件或文本的摘要（原生 JSON-RPC 子进程，标准库 crypto）
// 命令：calc  input {mode:"file"|"text", path?, text?, algo:"md5"|"sha1"|"sha256"|"sha512"}
package main

import (
	"bufio"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"os"
	"strings"
	"sync"
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

func pickHash(algo string) hash.Hash {
	switch strings.ToLower(algo) {
	case "md5":
		return md5.New()
	case "sha1":
		return sha1.New()
	case "sha512":
		return sha512.New()
	default:
		return sha256.New()
	}
}

func handleCalc(id int64, input map[string]interface{}) {
	mode := strings.ToLower(strFrom(input, "mode"))
	algo := strings.ToLower(strFrom(input, "algo"))
	if algo == "" {
		algo = "sha256"
	}
	h := pickHash(algo)
	target := ""
	if mode == "text" {
		io.WriteString(h, strFrom(input, "text"))
		target = "(文本输入)"
	} else {
		path := strFrom(input, "path")
		if path == "" {
			respondError(id, -32602, "请选择文件")
			return
		}
		f, err := os.Open(path)
		if err != nil {
			respondError(id, -1, "打开失败: "+err.Error())
			return
		}
		defer f.Close()
		if _, err := io.Copy(h, f); err != nil {
			respondError(id, -1, "读取失败: "+err.Error())
			return
		}
		target = path
	}
	respond(id, map[string]interface{}{
		"target": target,
		"algo":   algo,
		"hash":   hex.EncodeToString(h.Sum(nil)),
		"mode":   mode,
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
		respond(req.ID, map[string]interface{}{"status": "ready", "name": "QuickDock Hash Calc"})
	case "host.ping":
		respond(req.ID, map[string]interface{}{"pong": true})
	case "plugin.execute":
		var params executeParams
		if len(req.Params) > 0 {
			_ = json.Unmarshal(req.Params, &params)
		}
		switch strings.ToLower(strings.TrimSpace(params.Command)) {
		case "calc":
			handleCalc(req.ID, params.Input)
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
