// EXIF Viewer - 解析图片 EXIF 元数据（原生 JSON-RPC 子进程，goexif）
// 命令：read  input {path}  返回拍摄时间、相机/镜头、参数、GPS 等
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/rwcarlsen/goexif/exif"
	"github.com/rwcarlsen/goexif/tiff"
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

// visitor 实现 exif.Walker，遍历所有 EXIF 字段并收集为可读字符串
type visitor struct {
	tags map[string]interface{}
}

func (v *visitor) Walk(name exif.FieldName, tag *tiff.Tag) error {
	v.tags[string(name)] = tag.String()
	return nil
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

func handleRead(id int64, input map[string]interface{}) {
	path := strings.TrimSpace(strFrom(input, "path"))
	if path == "" {
		respondError(id, -32602, "请选择图片文件")
		return
	}
	f, err := os.Open(path)
	if err != nil {
		respondError(id, -1, "打开失败: "+err.Error())
		return
	}
	defer f.Close()

	x, err := exif.Decode(f)
	if err != nil {
		respond(id, map[string]interface{}{
			"ok":    false,
			"error": "无法解析 EXIF（可能无 EXIF 或非 JPEG）: " + err.Error(),
		})
		return
	}

	vis := &visitor{tags: map[string]interface{}{}}
	_ = x.Walk(vis)

	out := map[string]interface{}{"ok": true, "tags": vis.tags}
	if tm, e := x.DateTime(); e == nil {
		out["datetime"] = tm.Format("2006-01-02 15:04:05")
	}
	if lat, lon, e := x.LatLong(); e == nil {
		out["gps"] = map[string]interface{}{"lat": lat, "lng": lon}
	}
	respond(id, out)
}

func dispatch(raw string) {
	var req rpcRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		respondError(0, -32700, "parse error: "+err.Error())
		return
	}
	switch req.Method {
	case "initialize":
		respond(req.ID, map[string]interface{}{"status": "ready", "name": "QuickDock EXIF Viewer"})
	case "host.ping":
		respond(req.ID, map[string]interface{}{"pong": true})
	case "plugin.execute":
		var params executeParams
		if len(req.Params) > 0 {
			_ = json.Unmarshal(req.Params, &params)
		}
		switch strings.ToLower(strings.TrimSpace(params.Command)) {
		case "read":
			handleRead(req.ID, params.Input)
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
