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
		return strings.TrimSpace(v)
	}
	return ""
}

// rawStrFrom 不做 TrimSpace，用于文本哈希等需要逐字节精确的场景
func rawStrFrom(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

var outMu sync.Mutex

func respond(id int64, result interface{}) {
	defer qdFinishRequest(id, 0, "")
	outMu.Lock()
	defer outMu.Unlock()
	out, _ := json.Marshal(map[string]interface{}{"jsonrpc": "2.0", "id": id, "result": result})
	fmt.Println(string(out))
}

func respondError(id int64, code int, msg string) {
	defer qdFinishRequest(id, code, msg)
	outMu.Lock()
	defer outMu.Unlock()
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

	// 数据源（文件 / 文本）：先拿 reader，算法再决定读几遍。
	// 原来前端为 4 个算法各发一次 calc，后端每次 os.Open + io.Copy 读完整文件 ——
	// 一个 4GB 镜像要读 4 遍。现在 algo=all 用 io.MultiWriter 一次读完算 4 个。
	var reader io.Reader
	target := ""
	if mode == "text" {
		reader = strings.NewReader(rawStrFrom(input, "text"))
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
		reader = f
		target = path
	}

	if algo == "all" {
		names := []string{"md5", "sha1", "sha256", "sha512"}
		sums := map[string]hash.Hash{
			"md5": md5.New(), "sha1": sha1.New(), "sha256": sha256.New(), "sha512": sha512.New(),
		}
		ws := make([]io.Writer, 0, len(names))
		for _, n := range names {
			ws = append(ws, sums[n])
		}
		if _, err := io.Copy(io.MultiWriter(ws...), reader); err != nil {
			respondError(id, -1, "读取失败: "+err.Error())
			return
		}
		out := make(map[string]string, len(names))
		for _, n := range names {
			out[n] = hex.EncodeToString(sums[n].Sum(nil))
		}
		respond(id, map[string]interface{}{
			"target": target,
			"mode":   mode,
			"hashes": out,
		})
		return
	}

	h := pickHash(algo)
	if _, err := io.Copy(h, reader); err != nil {
		respondError(id, -1, "读取失败: "+err.Error())
		return
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
		// 命令别名：plugin.json 的 commands[].id 是 hash-calc，后端历史实现是 calc。
		// 带前端的插件走 plugin:init 打开窗口，命令 id 不会到后端；但纯后端命令路径
		// （宿主 ExecutePluginCommand）会把 id 原样发过来，别名兜住这一条，避免 -32601。
		cmd := strings.ToLower(strings.TrimSpace(params.Command))
		switch cmd {
		case "hash-calc":
			cmd = "calc"
		}
		switch cmd {
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
	qdLogStart()
	defer qdLogExit()
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
		if qdObserveLine(data) {
			continue
		}
		wg.Add(1)
		go func(raw string) {
			defer wg.Done()
			var pre struct {
				ID int64 `json:"id"`
			}
			_ = json.Unmarshal([]byte(raw), &pre)
			reqID := pre.ID
			panicked := true
			defer func() {
				if panicked {
					if r := recover(); r != nil {
						qdLogPanic("[panic] hash-calc dispatch", r)
						respondError(reqID, -32603, "internal error")
					}
				}
			}()
			dispatch(raw)
			panicked = false
		}(data)
	}
	wg.Wait()
}
