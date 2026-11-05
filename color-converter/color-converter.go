// Color Converter - 颜色格式互转
// JSON-RPC 2.0 over stdin/stdout
//
// 协议对齐 QuickDock 宿主约定：
//   - initialize      启动握手，必须尽快响应，否则超时判定加载失败
//   - host.ping       健康检查
//   - plugin.execute  唯一业务入口，params = {command, input}
//
// 命令：
//   - color-convert   HEX / RGB / HSL 互转（纯计算，面板本身不依赖后端）
//   - backend-ping    供宿主 / 前端探测后端可用性
//
// 屏幕取色已由宿主截图覆盖层提供，插件不再实现：原 picker_*.go 平台实现、
// 取色系列命令与热键等待状态机均已移除。界面内的原生拾色器与三栏
// 互转都在前端完成。
//
// 注意：宿主 Manager.ExecuteCommand 固定发送 "plugin.execute"，
// 前端 postMessage 的 command 落在 params.command。
//
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
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

type executeParams struct {
	Command string                 `json:"command"`
	Input   map[string]interface{} `json:"input"`
}

var stdout = bufio.NewWriter(os.Stdout)

// stdoutMu 保护 stdout：宿主请求处理与插件主动回调可能并发写出，
// JSON-RPC 每行一个对象，交错写入会损坏协议
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
			respondError(0, -32700, "parse error: "+err.Error())
			continue
		}
		if req.Method == "" {
			// 无 method 的帧 = 宿主对插件回调请求（host.*）的响应，fire-and-forget，静默丢弃
			continue
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					qdLogPanic("color-converter dispatch panic", r)
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
		respond(req.ID, map[string]interface{}{"status": "ready", "name": "QuickDock Color Tools"})
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

// execute 分发命令。command 兼容点号写法（内部统一替换为 -）。
func execute(p executeParams) (map[string]interface{}, string) {
	command := strings.ReplaceAll(p.Command, ".", "-")

	switch command {
	case "color-convert", "convert", "color":
		// HEX / RGB / HSL 互转（纯计算，AI 可直接调用；面板不传参时也不影响原行为）
		text := inputStr(p.Input, "text")
		if text == "" {
			text = inputStr(p.Input, "value")
		}
		if text == "" {
			text = inputStr(p.Input, "color")
		}
		if text == "" {
			return nil, "缺少颜色值：请传入 text，如 #ff0000、rgb(255,0,0)、hsl(0,100%,50%)"
		}
		r, g, b, ok := parseColor(text)
		if !ok {
			return nil, "无法识别颜色：" + text + "（支持 #rgb / #rrggbb / rgb(r,g,b) / hsl(h,s%,l%)）"
		}
		h, s, l := rgbToHSL(r, g, b)
		return map[string]interface{}{
			"hex": fmt.Sprintf("#%02x%02x%02x", r, g, b),
			"rgb": fmt.Sprintf("rgb(%d, %d, %d)", r, g, b),
			"hsl": fmt.Sprintf("hsl(%d, %d%%, %d%%)", h, s, l),
			"r":   r, "g": g, "b": b,
			"h": h, "s": s, "l": l,
		}, ""
	case "color-panel":
		// 面板入口：宿主负责打开转换界面，本身不做计算（与 control-receiver 的
		// file.browse 同约定）。AI 客户端经 plugin_execute 调到这里时给明确指路，
		// 避免误以为插件仍提供屏幕取色。
		return nil, "命令 color-panel 只打开颜色转换面板（不返回计算结果）；颜色格式互转请直接换算，屏幕取色请用宿主截图覆盖层的取色器"
	case "backend-ping": // 供前端探测后端可用性
		return map[string]interface{}{"ok": true}, ""
	default:
		return nil, "unknown command: " + p.Command
	}
}

// ---- 颜色解析与转换（color-convert 用；算法与前端 hexToRgb / rgbToHsl / hslToRgb 对齐）----

func inputStr(input map[string]interface{}, key string) string {
	if input == nil {
		return ""
	}
	if v, ok := input[key]; ok {
		if s, ok := v.(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// parseColor 解析 #rgb / #rrggbb / rgb(r,g,b) / hsl(h,s%,l%)，返回 RGB 分量。
func parseColor(s string) (int, int, int, bool) {
	t := strings.ToLower(strings.TrimSpace(s))
	if t == "" {
		return 0, 0, 0, false
	}
	if i := strings.Index(t, "("); i >= 0 {
		body := strings.TrimSuffix(t[i+1:], ")")
		parts := strings.Split(body, ",")
		switch {
		case strings.HasPrefix(t, "rgb") && len(parts) >= 3:
			var vals [3]int
			for k := 0; k < 3; k++ {
				n, err := strconv.Atoi(strings.TrimSpace(parts[k]))
				if err != nil {
					return 0, 0, 0, false
				}
				vals[k] = clampInt(n, 0, 255)
			}
			return vals[0], vals[1], vals[2], true
		case strings.HasPrefix(t, "hsl") && len(parts) >= 3:
			h, err1 := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
			sv, err2 := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(parts[1]), "%"), 64)
			lv, err3 := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(parts[2]), "%"), 64)
			if err1 != nil || err2 != nil || err3 != nil {
				return 0, 0, 0, false
			}
			r, g, b := hslToRGB(h, sv, lv)
			return r, g, b, true
		}
	}
	h := strings.TrimPrefix(t, "#")
	if len(h) == 3 {
		h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
	}
	if len(h) == 6 {
		if n, err := strconv.ParseUint(h, 16, 32); err == nil {
			return int(n>>16) & 255, int(n>>8) & 255, int(n) & 255, true
		}
	}
	return 0, 0, 0, false
}

// rgbToHSL 与前端 rgbToHsl 一致：h∈[0,360)、s/l 为百分比整数。
func rgbToHSL(r, g, b int) (int, int, int) {
	rf, gf, bf := float64(r)/255, float64(g)/255, float64(b)/255
	mx := math.Max(rf, math.Max(gf, bf))
	mn := math.Min(rf, math.Min(gf, bf))
	var h, s float64
	l := (mx + mn) / 2
	if mx != mn {
		d := mx - mn
		if l > 0.5 {
			s = d / (2 - mx - mn)
		} else {
			s = d / (mx + mn)
		}
		switch mx {
		case rf:
			h = (gf - bf) / d
			if gf < bf {
				h += 6
			}
		case gf:
			h = (bf-rf)/d + 2
		default:
			h = (rf-gf)/d + 4
		}
		h *= 60
	}
	return int(math.Round(h)), int(math.Round(s * 100)), int(math.Round(l * 100))
}

// hslToRGB 与前端 hslToRgb 一致（h 度，s/l 百分比）。
func hslToRGB(h, s, l float64) (int, int, int) {
	h = math.Mod(math.Mod(h, 360)+360, 360)
	s = math.Min(100, math.Max(0, s)) / 100
	l = math.Min(100, math.Max(0, l)) / 100
	f := func(n float64) float64 {
		k := math.Mod(n+h/30, 12)
		a := s * math.Min(l, 1-l)
		return l - a*math.Max(-1, math.Min(k-3, math.Min(9-k, 1)))
	}
	return int(math.Round(f(0) * 255)), int(math.Round(f(8) * 255)), int(math.Round(f(4) * 255))
}

func respond(id int64, result interface{}) {
	defer qdFinishRequest(id, 0, "")
	payload, err := json.Marshal(result)
	if err != nil {
		respondError(id, -32603, "marshal result failed: "+err.Error())
		return
	}
	writeResponse(rpcResponse{JSONRPC: "2.0", ID: id, Result: payload})
}

func respondError(id int64, code int, msg string) {
	defer qdFinishRequest(id, code, msg)
	writeResponse(rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}})
}

// writeResponse 统一出口：成功与失败都必须 Flush，
// 否则响应滞留在 bufio 缓冲区，宿主收不到任何字节，只会等到超时。
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
