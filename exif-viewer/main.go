// EXIF Viewer - 解析图片 EXIF 元数据（原生 JSON-RPC 子进程，goexif）
// 命令：read  input {path}  返回拍摄时间、相机/镜头、参数、GPS 等
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/rwcarlsen/goexif/exif"
	"github.com/rwcarlsen/goexif/tiff"
)

// detectImageFormat 按文件头魔数判断图片格式，并在读取后把文件指针复位到开头，
// 以便后续 exif.Decode 仍从起始位置解析。空串表示无法读取/非图片。
func detectImageFormat(f *os.File) string {
	var head [8]byte
	if _, err := io.ReadFull(f, head[:]); err != nil {
		return ""
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return ""
	}
	if bytes.HasPrefix(head[:3], []byte{0xFF, 0xD8, 0xFF}) { // JPEG: FF D8 FF
		return "jpeg"
	}
	if bytes.Equal(head[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1A, '\n'}) { // PNG
		return "png"
	}
	return "other"
}

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

	// 先按魔数识别格式：PNG / 无 EXIF 的 JPEG 是极常见情况，
	// 不能直接把 goexif 的底层错误 "failed to find exif intro marker" 丢给用户。
	format := detectImageFormat(f)
	switch format {
	case "":
		respond(id, map[string]interface{}{
			"ok":    false,
			"level": "error",
			"error": "无法读取文件，可能不是有效的图片文件",
		})
		return
	case "other":
		respond(id, map[string]interface{}{
			"ok":    false,
			"level": "info",
			"error": "不支持的图片格式（当前仅支持 JPEG / PNG）",
		})
		return
	case "png":
		respond(id, map[string]interface{}{
			"ok":    false,
			"level": "info",
			"error": "PNG 图片通常不包含 JPEG 标准 EXIF 信息（PNG 的 eXIf 块暂不支持）",
		})
		return
	}

	x, err := exif.Decode(f)
	if err != nil {
		// 绝大多数情况是 JPEG 本身就没有 EXIF 段，而非文件损坏
		respond(id, map[string]interface{}{
			"ok":    false,
			"level": "info",
			"error": "该 JPEG 图片未包含 EXIF 元数据",
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
