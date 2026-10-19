// control-receiver —— QuickDock 插件：手机屏幕镜像「qpm接收端」
//
// 角色：QuestPhoneMirror 协议里的「接收端 + 控制端」。
//   · 作为 TCP 客户端主动连接手机发送端（默认 45556），按协议收 SCREEN_INFO 与 H.264 视频帧；
//   · 把 H.264(AVCC) 帧经本机 WebSocket 推给插件前端，由前端 WebCodecs 解码上屏；
//   · 前端鼠标点击/拖拽 → control.tap / control.swipe 等命令 → 反向写回同一条 TCP 连接，
//     手机端 CastService.readLoop 读到后交给无障碍服务执行。
//
// 宿主契约（QuickDock v3 plugin-dev-guide.md）：
//   initialize / host.ping / plugin.execute；stdout 每行一个 JSON-RPC 响应，必须 Flush。
//   前端 pluginExec(cmd, data) → params.command = cmd，data 被序列化进 params.input.text。
//   本插件对两条投递路径都兼容：既吃 input.text 里的 JSON，也吃宿主直接塞进 input 的键值。
//
// QuestPhoneMirror 协议（严格对齐协议仓 protocol/src/main/java/com/qpm/protocol/Proto.kt）：
//   统一帧格式 [type:u8][length:u32 LE][body]，单条 TCP 双向复用：
//     0x01 VIDEO_FRAME  手机→接收端  body=[pts:u64 LE][flags:u8][h264]，flags bit0=关键帧 bit1=SPS/PPS
//     0x20 SCREEN_INFO  手机→接收端  body=[w:u32][h:u32][dpi:u32]
//     0x10 TAP          接收端→手机  body=[x:f32][y:f32]            （屏幕绝对像素坐标）
//     0x11 SWIPE        接收端→手机  body=[x1][y1][x2][y2][durMs:u32]
//     0x12 TEXT         接收端→手机  body=UTF-8 文本
//     0x13 KEY          接收端→手机  body=[keycode:u32]（4=返回 3=Home 187=多任务）
//   发现：手机每秒向 255.255.255.255:45555 广播 "QPM1|<tcpPort>|<w>x<h>"。
//
// 本地端口设计（见 README「端口建议」）：
//   controlPort（默认 24556）本地控制通道 WebSocket（状态/日志/画面参数推送，JSON）
//   streamPort = controlPort + 1（默认 24557）本地视频流 WebSocket（H.264 AVCC 二进制帧）
//   两个服务都只监听 127.0.0.1，不对外暴露；端口持久化在宿主数据根下的
//   <用户主目录>/.quickdock/plugin-data/io.github.parieses.control-receiver/config.json
//   （与插件安装目录分离，升级不丢；见 datadir.go）。
//
// 前端 ↔ 后端消息格式：
//   控制通道（文本 JSON）：{"t":"status"|"screen"|"config"|"devices"|"log", ...}
//   视频流通道：文本 JSON {"t":"screen"|"config"} + 二进制帧 [flags:u8][pts:u64 LE][avcc…]
//   另提供 stream.pull 轮询兜底：若 iframe 里 WebSocket 被拦，前端改用 RPC 拉流。

package main

import (
	"bufio"
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ============================================================ JSON-RPC 骨架

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

var (
	stdout  = bufio.NewWriter(os.Stdout)
	outMu   sync.Mutex // 保护 stdout：响应与日志都是单行 JSON，必须整行原子写出
	hostSeq int64      // 插件 → 宿主 的回调请求自增 id
)

func writeLine(v interface{}) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	outMu.Lock()
	defer outMu.Unlock()
	_, _ = stdout.Write(data)
	_ = stdout.WriteByte('\n')
	_ = stdout.Flush() // 不 Flush 宿主只会在超时后才放弃
}

func respond(id int64, result interface{}) {
	payload, err := json.Marshal(result)
	if err != nil {
		respondError(id, -32603, "marshal result failed: "+err.Error())
		return
	}
	writeLine(rpcResponse{JSONRPC: "2.0", ID: id, Result: payload})
}

func respondError(id int64, code int, msg string) {
	writeLine(rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}})
}

// hostLog 走 log.info / log.warn / log.error 通道落宿主的 plugin-YYYYMMDD.log。
// 必须带 id：宿主对无 id 的通知会静默丢弃。
func hostLog(level, format string, args ...interface{}) {
	id := atomic.AddInt64(&hostSeq, 1)
	payload, _ := json.Marshal(map[string]string{"message": fmt.Sprintf(format, args...)})
	writeLine(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  "log." + level,
		"params":  json.RawMessage(payload),
	})
}

// ============================================================ 协议常量

const (
	TypeVideoFrame byte = 0x01
	TypeScreenInfo byte = 0x20
	TypeTap        byte = 0x10
	TypeSwipe      byte = 0x11
	TypeText       byte = 0x12
	TypeKey        byte = 0x13
)

const (
	vFlagKey    byte = 1 // bit0：关键帧
	vFlagConfig byte = 2 // bit1：SPS/PPS 参数集
)

const (
	maxBody          = 4 << 20 // Proto.MAX_BODY
	discoveryPort    = 45555
	defaultPhonePort = 45556
	defaultCtlPort   = 24556
	phoneDialTimeout = 5 * time.Second

	// 首帧看门狗：连上手机后迟迟收不到画面时自动重连。
	// 手机端 acceptLoop 每接受一条连接都会 requestKeyFrame()，而 MediaProjection
	// 在息屏/画面完全静止时不会产出新帧——重连是「再要一个关键帧」的唯一手段。
	frameWatchWait = 2500 * time.Millisecond
	frameWatchMax  = 3
)

// 控制指令 → 屏幕坐标语义：手机侧 dispatchGesture 使用屏幕绝对像素，
// 因此前端必须用 SCREEN_INFO 的 w/h 把画布坐标换算成手机像素后再回传。

var commonServicePorts = map[int]string{
	21: "FTP", 22: "SSH", 23: "Telnet", 25: "SMTP", 53: "DNS", 80: "HTTP",
	110: "POP3", 143: "IMAP", 443: "HTTPS", 465: "SMTPS", 587: "SMTP", 993: "IMAPS",
	995: "POP3S", 1521: "Oracle", 3306: "MySQL", 3389: "RDP", 5432: "PostgreSQL",
	5672: "AMQP", 6379: "Redis", 8080: "HTTP-Alt", 8443: "HTTPS-Alt", 9000: "PHP-FPM",
	11211: "Memcached", 27017: "MongoDB",
	45555: "QuestPhoneMirror UDP 广播", 45556: "QuestPhoneMirror 发送端 TCP",
}

func readFrame(r io.Reader) (byte, []byte, error) {
	head := make([]byte, 5)
	if _, err := io.ReadFull(r, head); err != nil {
		return 0, nil, err
	}
	n := binary.LittleEndian.Uint32(head[1:5])
	if n > maxBody {
		return 0, nil, fmt.Errorf("非法帧长度: %d", n)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return 0, nil, err
	}
	return head[0], body, nil
}

func writeFrame(w io.Writer, t byte, body []byte) error {
	head := make([]byte, 5)
	head[0] = t
	binary.LittleEndian.PutUint32(head[1:5], uint32(len(body)))
	if _, err := w.Write(head); err != nil {
		return err
	}
	_, err := w.Write(body)
	return err
}

func encodeTap(x, y float32) []byte {
	b := make([]byte, 8)
	binary.LittleEndian.PutUint32(b[0:4], math.Float32bits(x))
	binary.LittleEndian.PutUint32(b[4:8], math.Float32bits(y))
	return b
}

func encodeSwipe(x1, y1, x2, y2 float32, durMs int32) []byte {
	b := make([]byte, 20)
	binary.LittleEndian.PutUint32(b[0:4], math.Float32bits(x1))
	binary.LittleEndian.PutUint32(b[4:8], math.Float32bits(y1))
	binary.LittleEndian.PutUint32(b[8:12], math.Float32bits(x2))
	binary.LittleEndian.PutUint32(b[12:16], math.Float32bits(y2))
	binary.LittleEndian.PutUint32(b[16:20], uint32(durMs))
	return b
}

func encodeKey(keycode int32) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, uint32(keycode))
	return b
}

func decodeScreenInfo(body []byte) (w, h, dpi int) {
	if len(body) < 12 {
		return 0, 0, 0
	}
	return int(int32(binary.LittleEndian.Uint32(body[0:4]))),
		int(int32(binary.LittleEndian.Uint32(body[4:8]))),
		int(int32(binary.LittleEndian.Uint32(body[8:12])))
}

// ============================================================ H.264 参数集处理
//
// 手机侧是 MediaCodec 硬编：普通输出帧是 AVCC（4 字节大端长度前缀），
// 而 csd-0/csd-1（SPS/PPS）是 Annex-B（00 00 00 01 起始码）。
// 浏览器 WebCodecs 用 codec="avc1.xxxxxx" + description(avcC) 时要求输入 AVCC，
// 所以这里只把参数集转成 avcC，视频帧原样透传。

// splitAnnexB 按 Annex-B 起始码切出 NAL 单元；不含起始码则返回 nil。
func splitAnnexB(b []byte) [][]byte {
	var nals [][]byte
	i := 0
	start := -1
	for i+3 <= len(b) {
		if b[i] == 0 && b[i+1] == 0 && b[i+2] == 1 {
			if start >= 0 && i > start {
				nals = append(nals, b[start:i])
			}
			i += 3
			start = i
			continue
		}
		if i+4 <= len(b) && b[i] == 0 && b[i+1] == 0 && b[i+2] == 0 && b[i+3] == 1 {
			if start >= 0 && i > start {
				nals = append(nals, b[start:i])
			}
			i += 4
			start = i
			continue
		}
		i++
	}
	if start < 0 {
		return nil
	}
	if start < len(b) {
		nals = append(nals, b[start:])
	}
	return nals
}

// splitAVCC 按 4 字节大端长度前缀切出 NAL 单元；结构不合法则返回 nil。
func splitAVCC(b []byte) [][]byte {
	var nals [][]byte
	i := 0
	for i+4 <= len(b) {
		n := int(binary.BigEndian.Uint32(b[i : i+4]))
		i += 4
		if n <= 0 || i+n > len(b) {
			return nil
		}
		nals = append(nals, b[i:i+n])
		i += n
	}
	if i != len(b) || len(nals) == 0 {
		return nil
	}
	return nals
}

// extractParamSets 从参数集载荷里取 SPS/PPS，兼容 Annex-B 与 AVCC 两种封装。
// 手机在 IDR 前会重发一次 SPS/PPS（flags bit1），所以中途连入也能拿到参数集。
func extractParamSets(b []byte) (sps, pps []byte) {
	nals := splitAnnexB(b)
	if len(nals) == 0 {
		nals = splitAVCC(b)
	}
	for _, n := range nals {
		if len(n) < 2 {
			continue
		}
		switch n[0] & 0x1F {
		case 7: // SPS
			if sps == nil {
				sps = n
			}
		case 8: // PPS
			if pps == nil {
				pps = n
			}
		}
	}
	return sps, pps
}

// isAnnexB 只看载荷开头几个字节判断封装格式。
// 实测（Android 14 / 1440x2964 设备）MediaCodec 的 AVC 输出是 Annex-B 起始码，
// 而不是任务书里写的 AVCC；但不同设备/ROM 两种都可能出现，所以这里自动识别。
func isAnnexB(b []byte) bool {
	if len(b) >= 4 && b[0] == 0 && b[1] == 0 && b[2] == 0 && b[3] == 1 {
		return true
	}
	return len(b) >= 3 && b[0] == 0 && b[1] == 0 && b[2] == 1
}

// normalizeToAVCC 把视频载荷统一成 AVCC（4 字节大端长度前缀）。
// 浏览器用 codec="avc1.*" + description(avcC) 配置 VideoDecoder 时要求 AVCC 输入，
// 因此 Annex-B 需要逐 NAL 改写；本来就是 AVCC 的原样返回。
func normalizeToAVCC(b []byte) []byte {
	if !isAnnexB(b) {
		return b
	}
	nals := splitAnnexB(b)
	if len(nals) == 0 {
		return b
	}
	out := make([]byte, 0, len(b))
	var l [4]byte
	for _, n := range nals {
		if len(n) == 0 {
			continue
		}
		binary.BigEndian.PutUint32(l[:], uint32(len(n)))
		out = append(out, l[:]...)
		out = append(out, n...)
	}
	if len(out) == 0 {
		return b
	}
	return out
}

// buildAvcC 由 SPS/PPS 拼出 WebCodecs 需要的 AVCDecoderConfigurationRecord。
func buildAvcC(sps, pps []byte) []byte {
	if len(sps) < 4 || len(pps) < 1 {
		return nil
	}
	out := make([]byte, 0, 11+len(sps)+len(pps))
	out = append(out, 1)      // configurationVersion
	out = append(out, sps[1]) // AVCProfileIndication
	out = append(out, sps[2]) // profile_compatibility
	out = append(out, sps[3]) // AVCLevelIndication
	out = append(out, 0xFF)   // lengthSizeMinusOne = 3（4 字节长度前缀）
	out = append(out, 0xE1)   // numOfSequenceParameterSets = 1
	out = append(out, byte(len(sps)>>8), byte(len(sps)))
	out = append(out, sps...)
	out = append(out, 1) // numOfPictureParameterSets
	out = append(out, byte(len(pps)>>8), byte(len(pps)))
	out = append(out, pps...)
	return out
}

// codecStringFromSPS 生成 "avc1.PPCCLL" 形式的 codec 串。
func codecStringFromSPS(sps []byte) string {
	if len(sps) < 4 {
		return ""
	}
	return fmt.Sprintf("avc1.%02x%02x%02x", sps[1], sps[2], sps[3])
}

// ============================================================ 配置持久化

type config struct {
	ControlPort  int    `json:"controlPort"`
	StreamPort   int    `json:"streamPort"`
	PhoneHost    string `json:"phoneHost"`
	PhonePort    int    `json:"phonePort"`
	AutoDiscover bool   `json:"autoDiscover"`
}

func defaultConfig() config {
	return config{
		ControlPort:  defaultCtlPort,     // 24556：高位、避开特权端口与 QPM 的 45556
		StreamPort:   defaultCtlPort + 1, // 24557：本地视频流 WS
		PhoneHost:    "",
		PhonePort:    defaultPhonePort,
		AutoDiscover: true,
	}
}

func (c config) normalize() config {
	d := defaultConfig()
	if c.ControlPort < 1024 || c.ControlPort > 65534 {
		c.ControlPort = d.ControlPort
	}
	if c.StreamPort != c.ControlPort+1 {
		c.StreamPort = c.ControlPort + 1
	}
	if c.PhonePort < 1 || c.PhonePort > 65535 {
		c.PhonePort = d.PhonePort
	}
	c.PhoneHost = strings.TrimSpace(c.PhoneHost)
	return c
}

func loadConfig(path string) config {
	cfg := defaultConfig()
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		hostLog("warn", "config.json 解析失败，回退默认配置: %v", err)
		return defaultConfig()
	}
	return cfg.normalize()
}

// saveConfig 原子写：同目录临时文件 + rename，避免半截 JSON 破坏下次启动。
func saveConfig(path string, cfg config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ============================================================ WebSocket（标准库手写）
//
// 只需要「文本 JSON + 二进制帧 + 关闭 + ping/pong」这点子集，
// 因此手写握手与帧编解码，不引入任何第三方依赖。

const (
	wsGUID       = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	wsOpCont     = 0x0
	wsOpText     = 0x1
	wsOpBinary   = 0x2
	wsOpClose    = 0x8
	wsOpPing     = 0x9
	wsOpPong     = 0xA
	maxWSPayload = 1 << 20
)

type wsOut struct {
	opcode byte
	data   []byte
}

type wsClient struct {
	name      string
	conn      net.Conn
	br        *bufio.Reader
	hub       *hub
	out       chan wsOut
	done      chan struct{}
	wmu       sync.Mutex // 串行化对 conn 的写：发送协程与关闭帧不能交错
	closed    atomic.Bool
	closeOnce sync.Once
}

func newWSClient(name string, conn net.Conn, br *bufio.Reader, h *hub) *wsClient {
	c := &wsClient{
		name: name, conn: conn, br: br, hub: h,
		out:  make(chan wsOut, h.queue),
		done: make(chan struct{}),
	}
	go c.writeLoop()
	go c.readLoop()
	h.add(c)
	return c
}

func (c *wsClient) writeLoop() {
	for {
		select {
		case <-c.done:
			return
		case m := <-c.out:
			if err := c.write(m.opcode, m.data); err != nil {
				c.close()
				return
			}
		}
	}
}

func (c *wsClient) write(opcode byte, data []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return writeWSFrame(c.conn, opcode, data)
}

func (c *wsClient) readLoop() {
	defer c.close()
	for {
		op, payload, err := readWSMessage(c.br)
		if err != nil {
			return
		}
		switch op {
		case wsOpText, wsOpBinary:
			if c.hub.onText != nil {
				c.hub.onText(c, payload)
			}
		case wsOpPing:
			c.send(wsOpPong, payload)
		case wsOpClose:
			return
		}
	}
}

// send 把消息投进发送队列。视频通道队列满时丢最旧一帧（保延迟），
// 控制通道队列满时直接丢当前消息（保连接）。
// 注意：这里刻意不 close(out)，否则并发 send 会 panic（send on closed channel）；
// 退出由 done 通道通知 writeLoop。
func (c *wsClient) send(opcode byte, data []byte) {
	if c.closed.Load() {
		return
	}
	if c.hub.keepLatest {
		select {
		case c.out <- wsOut{opcode, data}:
		default:
			select {
			case <-c.out:
			default:
			}
			select {
			case c.out <- wsOut{opcode, data}:
			default:
			}
		}
		return
	}
	select {
	case c.out <- wsOut{opcode, data}:
	default:
	}
}

func (c *wsClient) close() {
	c.closeOnce.Do(func() {
		c.closed.Store(true)
		_ = c.write(wsOpClose, nil)
		close(c.done)
		_ = c.conn.Close()
		c.hub.remove(c)
	})
}

func (c *wsClient) sendJSON(v interface{}) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	c.send(wsOpText, data)
}

type hub struct {
	name       string
	queue      int
	keepLatest bool
	onText     func(c *wsClient, payload []byte)
	onAttach   func(c *wsClient)

	mu      sync.Mutex
	clients map[*wsClient]struct{}
}

func newHub(name string, queue int, keepLatest bool, onAttach func(*wsClient)) *hub {
	return &hub{
		name: name, queue: queue, keepLatest: keepLatest,
		onAttach: onAttach, clients: map[*wsClient]struct{}{},
	}
}

func (h *hub) add(c *wsClient) {
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	if h.onAttach != nil {
		h.onAttach(c)
	}
}

func (h *hub) remove(c *wsClient) {
	h.mu.Lock()
	delete(h.clients, c)
	h.mu.Unlock()
}

func (h *hub) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients)
}

func (h *hub) broadcast(opcode byte, data []byte) {
	h.mu.Lock()
	clients := make([]*wsClient, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
	}
	h.mu.Unlock()
	for _, c := range clients {
		c.send(opcode, data)
	}
}

func (h *hub) broadcastJSON(v interface{}) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	h.broadcast(wsOpText, data)
}

func (h *hub) closeAll() {
	h.mu.Lock()
	clients := make([]*wsClient, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
	}
	h.mu.Unlock()
	for _, c := range clients {
		c.close()
	}
}

func readWSMessage(br *bufio.Reader) (byte, []byte, error) {
	var (
		msgOp  byte
		buf    []byte
		inFrag bool
	)
	for {
		fin, op, payload, err := readWSFrame(br)
		if err != nil {
			return 0, nil, err
		}
		switch op {
		case wsOpClose, wsOpPing, wsOpPong:
			return op, payload, nil
		case wsOpText, wsOpBinary:
			if inFrag {
				return 0, nil, errors.New("ws: 分片期间收到新数据帧")
			}
			if fin {
				return op, payload, nil
			}
			inFrag, msgOp = true, op
			buf = append(buf, payload...)
		case wsOpCont:
			if !inFrag {
				return 0, nil, errors.New("ws: 未分片却收到继续帧")
			}
			buf = append(buf, payload...)
			if fin {
				return msgOp, buf, nil
			}
		default:
			return 0, nil, fmt.Errorf("ws: 不支持的 opcode %d", op)
		}
		if len(buf) > maxWSPayload {
			return 0, nil, errors.New("ws: 消息过大")
		}
	}
}

func readWSFrame(br *bufio.Reader) (bool, byte, []byte, error) {
	var h [2]byte
	if _, err := io.ReadFull(br, h[:]); err != nil {
		return false, 0, nil, err
	}
	fin := h[0]&0x80 != 0
	op := h[0] & 0x0F
	masked := h[1]&0x80 != 0
	length := int64(h[1] & 0x7F)

	switch length {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(br, ext[:]); err != nil {
			return false, 0, nil, err
		}
		length = int64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(br, ext[:]); err != nil {
			return false, 0, nil, err
		}
		v := binary.BigEndian.Uint64(ext[:])
		if v > uint64(maxWSPayload) {
			return false, 0, nil, errors.New("ws: 帧过大")
		}
		length = int64(v)
	}
	if length > maxWSPayload {
		return false, 0, nil, errors.New("ws: 帧过大")
	}

	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(br, mask[:]); err != nil {
			return false, 0, nil, err
		}
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(br, payload); err != nil {
		return false, 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return fin, op, payload, nil
}

// writeWSFrame 服务端 → 客户端，按 RFC 6455 不加掩码。
func writeWSFrame(w io.Writer, opcode byte, payload []byte) error {
	head := make([]byte, 0, 10)
	head = append(head, 0x80|opcode) // FIN + opcode
	n := len(payload)
	switch {
	case n < 126:
		head = append(head, byte(n))
	case n <= 0xFFFF:
		head = append(head, 126, byte(n>>8), byte(n))
	default:
		head = append(head, 127)
		var ext [8]byte
		binary.BigEndian.PutUint64(ext[:], uint64(n))
		head = append(head, ext[:]...)
	}
	if _, err := w.Write(head); err != nil {
		return err
	}
	if n == 0 {
		return nil
	}
	_, err := w.Write(payload)
	return err
}

// upgradeWS 完成握手并接管连接（net/http 的 Hijack）。
func upgradeWS(w http.ResponseWriter, r *http.Request) (net.Conn, *bufio.ReadWriter, error) {
	key := r.Header.Get("Sec-WebSocket-Key")
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") || key == "" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusUpgradeRequired)
		_, _ = w.Write([]byte(`{"error":"此地址仅提供 WebSocket 服务"}`))
		return nil, nil, errors.New("非 WebSocket 握手")
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		w.WriteHeader(http.StatusInternalServerError)
		return nil, nil, errors.New("当前 http.Server 不支持 Hijack")
	}
	conn, rw, err := hj.Hijack()
	if err != nil {
		return nil, nil, err
	}
	sum := sha1.Sum([]byte(key + wsGUID))
	resp := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + base64.StdEncoding.EncodeToString(sum[:]) + "\r\n\r\n"
	if _, err := rw.WriteString(resp); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	if err := rw.Flush(); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	return conn, rw, nil
}

// wsEndpoint 一个本地 WS 服务（绑定 127.0.0.1）。
type wsEndpoint struct {
	name string
	hub  *hub
	port int
	ln   net.Listener
	srv  *http.Server
}

func bindWS(name string, port int, h *hub) (*wsEndpoint, error) {
	ln, err := net.Listen("tcp4", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		return nil, err
	}
	ep := &wsEndpoint{name: name, hub: h, port: port, ln: ln}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		conn, rw, err := upgradeWS(w, r)
		if err != nil {
			return
		}
		newWSClient(name, conn, rw.Reader, h)
	})
	ep.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = ep.srv.Serve(ln) }()
	return ep, nil
}

func (ep *wsEndpoint) stop() {
	if ep == nil {
		return
	}
	_ = ep.srv.Close()
	_ = ep.ln.Close()
	ep.hub.closeAll()
}

// ============================================================ 拉流兜底缓冲
//
// 若宿主 iframe 的沙箱环境拦截了 WebSocket，前端可用 stream.pull 轮询拉流。
// 环形缓冲保留最近若干帧，配合 since 游标实现「只发新增帧」。

type pullChunk struct {
	seq   int64
	flags byte
	pts   uint64
	data  []byte
}

type pullBuf struct {
	mu        sync.Mutex
	chunks    []pullChunk
	next      int64
	dropped   int64
	bytes     int
	maxChunks int
	maxBytes  int
}

func (p *pullBuf) push(flags byte, pts uint64, data []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(data) == 0 || len(data) > p.maxBytes {
		return
	}
	p.chunks = append(p.chunks, pullChunk{seq: p.next, flags: flags, pts: pts, data: data})
	p.next++
	p.bytes += len(data)
	for len(p.chunks) > p.maxChunks || p.bytes > p.maxBytes {
		if len(p.chunks) <= 1 {
			break
		}
		p.bytes -= len(p.chunks[0].data)
		p.chunks = p.chunks[1:]
		p.dropped++
	}
}

func (p *pullBuf) reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.bytes = 0
	p.dropped += int64(len(p.chunks))
	p.chunks = nil
}

// 单帧拉流上限：真实码流的关键帧可达 450KB，必须整帧放行，
// 否则兜底轮询永远拿不到 IDR。上限同时受宿主 1MB 单行 stdout 约束
// （512KB 原始数据 → base64 683KB，留足安全余量）。
const pullSingleMax = 512 << 10

// since 返回 seq 之后的帧（有上限），以及当前游标与「是否需要等关键帧」标记。
func (p *pullBuf) since(seq int64, maxChunks, maxBytes int) (out []pullChunk, next int64, head int64, gap bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	next = p.next
	if len(p.chunks) == 0 {
		return nil, next, next, false
	}
	head = p.chunks[0].seq
	if seq < head-1 {
		gap = true // 请求的游标已被丢弃，前端应等下一个关键帧
		seq = head - 1
	}
	total := 0
	for _, c := range p.chunks {
		if c.seq <= seq {
			continue
		}
		first := len(out) == 0
		if !first && (len(out) >= maxChunks || total+len(c.data) > maxBytes) {
			break
		}
		if first && len(c.data) > pullSingleMax {
			break // 极端情况：单帧超预算，跳过这帧
		}
		out = append(out, c)
		total += len(c.data)
	}
	return out, next, head, gap
}

// ============================================================ 主状态

type app struct {
	mu sync.Mutex

	cfg       config
	cfgPath   string
	pluginDir string

	// 手机侧
	conn      net.Conn
	writeMu   sync.Mutex
	connected bool
	remote    string
	screenW   int
	screenH   int
	screenDPI int
	lastErr   string

	// 解码参数集
	avcC  []byte
	codec string
	avcCW int
	avcCH int

	// 统计
	frames     int64
	bytes      int64
	fps        float64
	kbps       float64
	tickFrames int64
	tickBytes  int64
	lastTick   time.Time

	// 发现
	discovering  bool
	discoverStop chan struct{}
	devices      []Device

	// 本地服务
	//   wsMu 只保护端点对象的绑定/释放（RPC 主循环与 WS 客户端协程会并发调用）；
	//   wsCtlPort/wsStreamPort 是「实际在听」的端口快照，由 a.mu 保护，供 snapshot 读取。
	//   两把锁从不同时持有，避免与 a.mu 形成环路。
	wsMu         sync.Mutex
	ctlHub       *hub
	strHub       *hub
	ctlEp        *wsEndpoint
	strEp        *wsEndpoint
	wsCtlPort    int
	wsStreamPort int

	pull pullBuf

	startedAt time.Time

	// 首帧看门狗
	watchGen     int64
	watchAttempt int
	framesAt     int64
}

type Device struct {
	Host string `json:"host"`
	Port int    `json:"port"`
	W    int    `json:"w"`
	H    int    `json:"h"`
	Seen string `json:"seen"`
}

var appState = &app{}

func (a *app) logf(level, format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	hostLog(level, "%s", msg)
	a.ctlHub.broadcastJSON(map[string]interface{}{
		"t":     "log",
		"level": level,
		"msg":   msg,
		"ts":    time.Now().UnixMilli(),
	})
}

// ============================================================ 状态快照与广播

func (a *app) snapshot() map[string]interface{} {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.snapshotLocked()
}

func (a *app) snapshotLocked() map[string]interface{} {
	devices := make([]Device, len(a.devices))
	copy(devices, a.devices)
	st := map[string]interface{}{
		"connected":    a.connected,
		"remote":       a.remote,
		"screenW":      a.screenW,
		"screenH":      a.screenH,
		"dpi":          a.screenDPI,
		"fps":          round1(a.fps),
		"kbps":         round1(a.kbps),
		"frames":       a.frames,
		"bytes":        a.bytes,
		"controlPort":  a.cfg.ControlPort,
		"streamPort":   a.cfg.StreamPort,
		"phoneHost":    a.cfg.PhoneHost,
		"phonePort":    a.cfg.PhonePort,
		"autoDiscover": a.cfg.AutoDiscover,
		"wsRunning":    a.wsCtlPort != 0 && a.wsStreamPort != 0,
		"discovering":  a.discovering,
		"devices":      devices,
		"lastErr":      a.lastErr,
		"avcC":         a.avcCLocked(),
	}
	if a.wsCtlPort != 0 {
		st["wsControlPort"] = a.wsCtlPort
	}
	if a.wsStreamPort != 0 {
		st["wsStreamPort"] = a.wsStreamPort
	}
	st["ctlClients"] = a.ctlHub.count()
	st["streamClients"] = a.strHub.count()
	return st
}

func (a *app) avcCLocked() interface{} {
	if len(a.avcC) == 0 {
		return nil
	}
	return map[string]interface{}{
		"codec":       a.codec,
		"description": base64.StdEncoding.EncodeToString(a.avcC),
		"w":           a.avcCW,
		"h":           a.avcCH,
	}
}

func (a *app) broadcastStatus() {
	snap := a.snapshot()
	snap["t"] = "status" // 控制通道消息都带 t 字段，前端据此分流
	a.ctlHub.broadcastJSON(snap)
}

func round1(f float64) float64 { return math.Round(f*10) / 10 }

// ============================================================ 本地 WS 生命周期

// startWS 绑定本地 WS 端点（只监听 127.0.0.1）。
//
// 既在 initialize 时调用（让前端可以不经宿主桥接、直接连本地端口下发命令），
// 也在 start / 端口变更时调用。幂等：目标端口已在监听就直接返回。
func (a *app) startWS() error {
	a.mu.Lock()
	c := a.cfg
	a.mu.Unlock()

	a.wsMu.Lock()
	if a.ctlEp != nil && a.strEp != nil && a.ctlEp.port == c.ControlPort && a.strEp.port == c.StreamPort {
		a.wsMu.Unlock()
		return nil
	}
	a.stopWSLocked()
	ctl, err := bindWS("ctl", c.ControlPort, a.ctlHub)
	if err != nil {
		a.wsMu.Unlock()
		return fmt.Errorf("控制端口 %d 无法监听：%v", c.ControlPort, err)
	}
	str, err := bindWS("stream", c.StreamPort, a.strHub)
	if err != nil {
		ctl.stop()
		a.wsMu.Unlock()
		return fmt.Errorf("视频流端口 %d 无法监听：%v", c.StreamPort, err)
	}
	a.ctlEp, a.strEp = ctl, str
	a.wsMu.Unlock()

	a.mu.Lock()
	a.wsCtlPort, a.wsStreamPort = c.ControlPort, c.StreamPort
	a.mu.Unlock()
	a.logf("info", "本地服务已就绪：控制通道 ws://127.0.0.1:%d/，视频流 ws://127.0.0.1:%d/（仅本机可连）", c.ControlPort, c.StreamPort)
	return nil
}

func (a *app) stopWS() {
	a.wsMu.Lock()
	a.stopWSLocked()
	a.wsMu.Unlock()
	a.mu.Lock()
	a.wsCtlPort, a.wsStreamPort = 0, 0
	a.mu.Unlock()
}

func (a *app) stopWSLocked() {
	if a.ctlEp != nil {
		a.ctlEp.stop()
		a.ctlEp = nil
	}
	if a.strEp != nil {
		a.strEp.stop()
		a.strEp = nil
	}
}

// wsRunning 返回本地 WS 是否已就绪。
func (a *app) wsRunning() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.wsCtlPort != 0 && a.wsStreamPort != 0
}

// ============================================================ 与手机发送端的连接

func (a *app) connect(host string, port int) error {
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	conn, err := net.DialTimeout("tcp", addr, phoneDialTimeout)
	if err != nil {
		a.mu.Lock()
		a.lastErr = "连接 " + addr + " 失败：" + err.Error()
		a.mu.Unlock()
		return fmt.Errorf("连接 %s 失败: %w", addr, err)
	}
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetNoDelay(true) // 视频流对延迟敏感
		_ = tcp.SetKeepAlive(true)
	}

	a.disconnect()

	a.mu.Lock()
	a.conn = conn
	a.connected = true
	a.remote = conn.RemoteAddr().String()
	a.screenW, a.screenH, a.screenDPI = 0, 0, 0
	a.avcC, a.codec, a.avcCW, a.avcCH = nil, "", 0, 0
	a.lastErr = ""
	a.cfg.PhoneHost = host
	a.cfg.PhonePort = port
	cfg := a.cfg
	a.mu.Unlock()
	a.pull.reset()

	if err := saveConfig(a.cfgPath, cfg); err != nil {
		hostLog("warn", "保存 config.json 失败: %v", err)
	}
	a.logf("info", "已连接手机发送端 %s", a.remote)
	a.broadcastStatus()

	a.mu.Lock()
	a.framesAt = a.frames // 首帧看门狗基线：本次连接后收到的帧数
	a.watchGen++
	gen := a.watchGen
	att := a.watchAttempt
	a.mu.Unlock()

	go a.readLoop(conn)
	if att < frameWatchMax {
		go a.frameWatch(gen, host, port, att+1)
	}
	return nil
}

// frameWatch 在连接后 frameWatchWait 内没有收到任何画面时重连一次，最多 frameWatchMax 次。
// 这是「已连接，等待画面…」长时间不消失的主要对策：手机端每接受一条连接都会
// requestKeyFrame()，而息屏/静止画面下 MediaProjection 根本不产出新帧。
func (a *app) frameWatch(gen int64, host string, port, attempt int) {
	time.Sleep(frameWatchWait)
	a.mu.Lock()
	stale := a.watchGen == gen && a.connected && a.frames == a.framesAt
	a.watchAttempt = attempt
	remote := a.remote
	a.mu.Unlock()
	if !stale {
		return
	}
	a.logf("warn", "已连接 %s，但 %.1f 秒内没有收到任何画面（手机息屏或画面静止时不会编码）；重新连接以再次请求关键帧（第 %d/%d 次）",
		remote, frameWatchWait.Seconds(), attempt, frameWatchMax)
	if err := a.connect(host, port); err != nil {
		a.logf("error", "重新连接失败：%v", err)
	}
}

func (a *app) disconnect() {
	a.mu.Lock()
	conn := a.conn
	a.conn = nil
	wasConnected := a.connected
	a.connected = false
	a.watchGen++ // 让在途的首帧看门狗失效
	a.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
	if wasConnected {
		a.pull.reset()
	}
}

// readLoop 持续解析手机发来的帧，直到连接断开。
func (a *app) readLoop(conn net.Conn) {
	defer func() {
		if r := recover(); r != nil {
			hostLog("error", "readLoop panic: %v\n%s", r, debug.Stack())
		}
		a.mu.Lock()
		if a.conn == conn {
			a.conn = nil
			a.connected = false
		}
		a.mu.Unlock()
		a.logf("warn", "与手机发送端的连接已断开")
		a.broadcastStatus()
	}()

	reader := bufio.NewReaderSize(conn, 1<<20)
	for {
		typ, body, err := readFrame(reader)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				hostLog("warn", "读帧结束: %v", err)
			}
			return
		}
		a.onPhoneFrame(typ, body)
	}
}

func (a *app) onPhoneFrame(typ byte, body []byte) {
	switch typ {
	case TypeScreenInfo:
		w, h, dpi := decodeScreenInfo(body)
		a.setScreen(w, h, dpi)
	case TypeVideoFrame:
		if len(body) < 9 {
			return
		}
		pts := binary.LittleEndian.Uint64(body[0:8])
		flags := body[8]
		data := body[9:]

		if flags&vFlagConfig != 0 {
			// 参数集帧：SPS/PPS（Annex-B）——转成 avcC 下发给前端
			if sps, pps := extractParamSets(data); len(sps) > 0 && len(pps) > 0 {
				a.setAvcC(sps, pps)
			} else {
				hostLog("warn", "收到参数集帧但未能解析出 SPS/PPS（%d 字节）", len(data))
			}
			return
		}
		if flags&vFlagKey != 0 && !a.hasAvcC() {
			// 兜底：个别设备把 SPS/PPS 直接内联在 IDR 帧里
			if sps, pps := extractParamSets(data); len(sps) > 0 && len(pps) > 0 {
				a.setAvcC(sps, pps)
			}
		}
		// 统一成 AVCC 再下发：前端用固定 avcC 描述配置解码器，只需喂 AVCC 帧
		a.pushFrame(flags, pts, normalizeToAVCC(data))
	}
}

func (a *app) hasAvcC() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.avcC) > 0
}

func (a *app) setScreen(w, h, dpi int) {
	a.mu.Lock()
	changed := a.screenW != w || a.screenH != h || a.screenDPI != dpi
	a.screenW, a.screenH, a.screenDPI = w, h, dpi
	a.avcCW, a.avcCH = w, h
	msg := map[string]interface{}{"t": "screen", "w": w, "h": h, "dpi": dpi}
	a.mu.Unlock()
	if !changed {
		return
	}
	a.logf("info", "手机屏幕参数：%dx%d @ %ddpi", w, h, dpi)
	a.ctlHub.broadcastJSON(msg)
	a.strHub.broadcastJSON(msg)
	a.broadcastStatus()
	if a.hasAvcC() {
		a.broadcastConfig()
	}
}

func (a *app) setAvcC(sps, pps []byte) {
	desc := buildAvcC(sps, pps)
	if desc == nil {
		return
	}
	codec := codecStringFromSPS(sps)
	a.mu.Lock()
	changed := a.codec != codec || !bytes.Equal(a.avcC, desc)
	a.avcC, a.codec = desc, codec
	a.mu.Unlock()
	if !changed {
		return
	}
	a.logf("info", "解析到 SPS/PPS（%d+%d 字节）→ codec=%s，avcC %d 字节", len(sps), len(pps), codec, len(desc))
	a.broadcastConfig()
}

func (a *app) configMsg() map[string]interface{} {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.avcC) == 0 {
		return nil
	}
	return map[string]interface{}{
		"t":           "config",
		"codec":       a.codec,
		"description": base64.StdEncoding.EncodeToString(a.avcC),
		"w":           a.avcCW,
		"h":           a.avcCH,
	}
}

func (a *app) broadcastConfig() {
	msg := a.configMsg()
	if msg == nil {
		return
	}
	a.strHub.broadcastJSON(msg) // 视频通道：让新连入的前端立刻能建解码器
	a.ctlHub.broadcastJSON(msg) // 控制通道：兜底拉流模式也能拿到
}

func (a *app) pushFrame(flags byte, pts uint64, data []byte) {
	a.mu.Lock()
	if !a.connected {
		a.mu.Unlock()
		return
	}
	a.frames++
	a.bytes += int64(len(data))
	a.tickFrames++
	a.tickBytes += int64(len(data))
	a.mu.Unlock()

	a.pull.push(flags, pts, data)

	if a.strHub.count() > 0 {
		env := make([]byte, 9+len(data))
		env[0] = flags
		binary.LittleEndian.PutUint64(env[1:9], pts)
		copy(env[9:], data)
		a.strHub.broadcast(wsOpBinary, env)
	}
}

// sendControl 把控制帧写回手机。写操作串行化，避免多命令交错撕裂帧。
func (a *app) sendControl(typ byte, body []byte) error {
	a.mu.Lock()
	conn := a.conn
	a.mu.Unlock()
	if conn == nil {
		return errors.New("未连接手机发送端，请先点击「启动」")
	}
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	_ = conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
	if err := writeFrame(conn, typ, body); err != nil {
		_ = conn.SetWriteDeadline(time.Time{})
		return fmt.Errorf("控制指令发送失败: %w", err)
	}
	_ = conn.SetWriteDeadline(time.Time{})
	return nil
}

func (a *app) tap(x, y float32) error { return a.sendControl(TypeTap, encodeTap(x, y)) }
func (a *app) key(k int32) error      { return a.sendControl(TypeKey, encodeKey(k)) }
func (a *app) text(s string) error    { return a.sendControl(TypeText, []byte(s)) }
func (a *app) swipe(x1, y1, x2, y2 float32, dur int32) error {
	return a.sendControl(TypeSwipe, encodeSwipe(x1, y1, x2, y2, dur))
}

// ============================================================ 自动发现（UDP 45555）

// startDiscovery 监听手机广播；found 回调收到的设备。
// autoConnect=true 时发现第一台设备即自动连接。
func (a *app) startDiscovery(timeout time.Duration, autoConnect bool) error {
	addr := &net.UDPAddr{IP: net.IPv4zero, Port: discoveryPort}
	conn, err := net.ListenUDP("udp4", addr)
	if err != nil {
		return fmt.Errorf("监听 UDP %d 失败（可能有程序已占用）: %w", discoveryPort, err)
	}

	stop := make(chan struct{})
	a.mu.Lock()
	if a.discoverStop != nil {
		close(a.discoverStop)
	}
	a.discoverStop = stop
	a.discovering = true
	a.devices = nil
	a.mu.Unlock()
	a.broadcastStatus()

	go func() {
		defer func() {
			_ = conn.Close()
			a.mu.Lock()
			a.discovering = false
			if a.discoverStop == stop {
				a.discoverStop = nil
			}
			a.mu.Unlock()
			a.broadcastStatus()
		}()

		deadline := time.Now().Add(timeout)
		buf := make([]byte, 256)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if time.Now().After(deadline) {
				a.logf("warn", "自动发现超时（%s），可手动填写手机地址", timeout)
				return
			}
			_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
			n, src, err := conn.ReadFromUDP(buf)
			if err != nil {
				var ne net.Error
				if errors.As(err, &ne) && ne.Timeout() {
					continue
				}
				return
			}
			dev, ok := parseBeacon(string(buf[:n]), src)
			if !ok {
				continue
			}
			a.addDevice(dev)
			a.mu.Lock()
			connected := a.connected
			a.mu.Unlock()
			if autoConnect && !connected {
				a.logf("info", "发现发送端 %s:%d，正在连接…", dev.Host, dev.Port)
				if err := a.connect(dev.Host, dev.Port); err == nil {
					return
				}
			}
		}
	}()
	return nil
}

func (a *app) stopDiscovery() {
	a.mu.Lock()
	stop := a.discoverStop
	a.discoverStop = nil
	a.mu.Unlock()
	if stop != nil {
		close(stop)
	}
}

func (a *app) addDevice(dev Device) {
	a.mu.Lock()
	for i, d := range a.devices {
		if d.Host == dev.Host && d.Port == dev.Port {
			a.devices[i] = dev
			a.mu.Unlock()
			a.ctlHub.broadcastJSON(map[string]interface{}{"t": "devices", "devices": a.deviceList()})
			return
		}
	}
	a.devices = append(a.devices, dev)
	list := make([]Device, len(a.devices))
	copy(list, a.devices)
	a.mu.Unlock()
	a.logf("info", "发现发送端 %s:%d（%dx%d）", dev.Host, dev.Port, dev.W, dev.H)
	a.ctlHub.broadcastJSON(map[string]interface{}{"t": "devices", "devices": list})
	a.broadcastStatus()
}

func (a *app) deviceList() []Device {
	a.mu.Lock()
	defer a.mu.Unlock()
	list := make([]Device, len(a.devices))
	copy(list, a.devices)
	sort.Slice(list, func(i, j int) bool { return list[i].Host < list[j].Host })
	return list
}

func parseBeacon(msg string, src *net.UDPAddr) (Device, bool) {
	const prefix = "QPM1|"
	if !strings.HasPrefix(msg, prefix) {
		return Device{}, false
	}
	parts := strings.Split(strings.TrimSpace(strings.TrimPrefix(msg, prefix)), "|")
	if len(parts) < 2 {
		return Device{}, false
	}
	port, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil || port <= 0 || port > 65535 {
		return Device{}, false
	}
	wh := strings.SplitN(strings.ToLower(parts[1]), "x", 2)
	if len(wh) != 2 {
		return Device{}, false
	}
	w, _ := strconv.Atoi(strings.TrimSpace(wh[0]))
	h, _ := strconv.Atoi(strings.TrimSpace(wh[1]))
	host := ""
	if src != nil {
		host = src.IP.String()
	}
	if host == "" {
		return Device{}, false
	}
	return Device{Host: host, Port: port, W: w, H: h, Seen: time.Now().Format("15:04:05")}, true
}

// ============================================================ 统计心跳

func (a *app) statsLoop() {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for range tick.C {
		a.mu.Lock()
		dt := time.Since(a.lastTick).Seconds()
		if dt <= 0.001 {
			dt = 1
		}
		a.fps = float64(a.tickFrames) / dt
		a.kbps = float64(a.tickBytes*8) / 1000 / dt
		a.tickFrames, a.tickBytes = 0, 0
		a.lastTick = time.Now()
		a.mu.Unlock()
		if a.ctlHub.count() > 0 {
			a.broadcastStatus()
		}
	}
}

// ============================================================ 端口建议

type portCandidate struct {
	Port       int    `json:"port"`
	StreamPort int    `json:"streamPort"`
	Available  bool   `json:"available"`
	Tag        string `json:"tag,omitempty"`
	Reason     string `json:"reason"`
}

func portAvailable(port int) bool {
	if port < 1 || port > 65535 {
		return false
	}
	ln, err := net.Listen("tcp4", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

// pairAvailable 控制端口与配套视频端口必须同时可用（两者都要在本机 bind）。
func pairAvailable(port int) bool {
	return portAvailable(port) && portAvailable(port+1)
}

func excludedReason(port int) string {
	if port >= 1 && port <= 1023 {
		return "特权端口（1–1023），普通权限无法监听"
	}
	if name, ok := commonServicePorts[port]; ok {
		if strings.HasPrefix(name, "QuestPhoneMirror") {
			return "QuestPhoneMirror 已占用：" + name
		}
		return "常见服务端口（" + name + "），避开以免冲突"
	}
	return ""
}

func reasonFor(port, currentCtl int, isDefault bool) string {
	if r := excludedReason(port); r != "" {
		return r
	}
	if port == currentCtl {
		if pairAvailable(port) {
			return "当前配置端口，本机可用（建议保持）"
		}
		return "当前配置端口，但本机端口已被其他程序监听"
	}
	if !portAvailable(port) {
		return "本机已被占用"
	}
	if !portAvailable(port + 1) {
		return fmt.Sprintf("配套视频流端口 %d 已被占用", port+1)
	}
	if isDefault {
		return "推荐默认：高位区间，避开特权端口，且与 QuestPhoneMirror 的 45556 错开"
	}
	return fmt.Sprintf("高位空闲，与默认 %d 错开，便于同时开多个实例", defaultCtlPort)
}

func (a *app) suggestPorts() map[string]interface{} {
	a.mu.Lock()
	current := a.cfg.ControlPort
	a.mu.Unlock()

	base := []int{defaultCtlPort, defaultCtlPort + 10, defaultCtlPort + 1000, 35556, 14556}
	candidates := make([]portCandidate, 0, len(base))
	seen := map[int]bool{}
	for _, p := range base {
		if seen[p] {
			continue
		}
		seen[p] = true
		avail := pairAvailable(p)
		tag := ""
		switch {
		case p == defaultCtlPort:
			tag = "default"
		case avail:
			tag = "backup"
		}
		candidates = append(candidates, portCandidate{
			Port:       p,
			StreamPort: p + 1,
			Available:  avail,
			Tag:        tag,
			Reason:     reasonFor(p, current, p == defaultCtlPort),
		})
	}

	// 推荐值：默认端口成对可用就用默认；否则从默认起顺延找第一对空闲端口（跳过静态排除项）
	recommended := defaultCtlPort
	advanceNote := ""
	defaultOK := pairAvailable(defaultCtlPort)
	if !defaultOK {
		found := 0
		for p := defaultCtlPort + 1; p <= 65534 && p < defaultCtlPort+4000; p++ {
			if excludedReason(p) != "" || p == current {
				continue
			}
			if pairAvailable(p) {
				found = p
				break
			}
		}
		if found == 0 {
			found = current
			advanceNote = "自动顺延未找到连续空闲端口对，暂沿用当前设置"
		} else {
			advanceNote = fmt.Sprintf("默认 %d 当前不可用（被占用或配套端口冲突），已自动顺延到 %d", defaultCtlPort, found)
		}
		recommended = found
		candidates = append(candidates, portCandidate{
			Port:       recommended,
			StreamPort: recommended + 1,
			Available:  pairAvailable(recommended),
			Tag:        "recommended",
			Reason:     advanceNote,
		})
	}

	return map[string]interface{}{
		"current":          current,
		"default":          defaultCtlPort,
		"defaultAvailable": defaultOK,
		"recommended":      recommended,
		"note":             advanceNote,
		"excluded": []string{
			"1–1023 特权端口",
			"45555 QuestPhoneMirror UDP 广播",
			"45556 QuestPhoneMirror 发送端 TCP",
			"21/22/23/25/53/80/110/143/443/3306/5432/6379/8080/8443/9000 等常见服务端口",
		},
		"constraint": fmt.Sprintf("streamPort 固定为 controlPort + 1（当前 %d → 视频流 %d），仅监听 127.0.0.1", current, current+1),
		"candidates": candidates,
	}
}

// ============================================================ 命令处理

func (a *app) handleStart(input map[string]interface{}) (interface{}, error) {
	host := strFrom(input, "host")
	port := intFrom(input, "port", 0)
	discover := boolFrom(input, "discover", false)

	// 前端把「端口/地址」与 start 合并成一次 RPC：先落盘配置，省掉 config.set 的整轮往返。
	// （这是之前「点启动要等两轮 RPC」的直接原因，也是启动慢的一部分。）
	if hasConfigKeys(input) {
		portsChanged, err := a.applyInputConfig(input)
		if err != nil {
			return map[string]interface{}{"started": false, "error": err.Error()}, nil
		}
		if portsChanged {
			a.stopWS() // 端口变了：下一步 startWS 会按新端口重建
		}
	}

	a.mu.Lock()
	a.watchAttempt = 0 // 用户手动启动 → 首帧看门狗重新计数
	autoDiscover := a.cfg.AutoDiscover
	if port <= 0 {
		port = a.cfg.PhonePort
	}
	if host == "" {
		host = a.cfg.PhoneHost
	}
	a.mu.Unlock()

	// 「启动接收」的关键一步留痕：参数、方式、后续每一步耗时都能在宿主日志里对上。
	cfgNow := a.cfgSnapshot()
	how := "手动地址 " + host + ":" + strconv.Itoa(port)
	if host == "" {
		if discover || autoDiscover {
			how = "自动发现（监听 UDP 45555 广播，最多 12 秒）"
		} else {
			how = "未指定地址且未开自动发现"
		}
	}
	a.logf("info", "启动接收：控制端口 %d / 视频流 %d，方式：%s", cfgNow.ControlPort, cfgNow.StreamPort, how)

	// 本地服务必须先起得来，否则前端连不上流
	if err := a.startWS(); err != nil {
		sug := a.suggestPorts()
		a.mu.Lock()
		a.lastErr = err.Error()
		a.mu.Unlock()
		return map[string]interface{}{
			"started":            false,
			"error":              err.Error(),
			"portSuggest":        sug,
			"suggestedPortValue": sug["recommended"],
			"config":             a.cfgSnapshot(),
		}, nil
	}
	a.broadcastStatus()

	if host != "" {
		if err := a.connect(host, port); err != nil {
			a.logf("error", "连接失败：%v", err)
			return map[string]interface{}{"started": false, "error": err.Error(), "status": a.snapshot()}, nil
		}
		return map[string]interface{}{"started": true, "mode": "direct", "config": a.cfgSnapshot(), "status": a.snapshot()}, nil
	}

	if discover || autoDiscover {
		if err := a.startDiscovery(12*time.Second, true); err != nil {
			a.logf("error", "自动发现启动失败：%v", err)
			return map[string]interface{}{"started": false, "error": err.Error()}, nil
		}
		return map[string]interface{}{"started": true, "mode": "discover", "config": a.cfgSnapshot(), "status": a.snapshot()}, nil
	}
	return map[string]interface{}{
		"started": false,
		"error":   "未填写手机地址且未开启自动发现：请填写发送端 IP，或勾选自动发现",
		"config":  a.cfgSnapshot(),
	}, nil
}

// applyInputConfig 把前端传来的端口/地址并入配置并落盘（start 与 config.set 共用）。
// 返回「控制端口是否发生变化」，供调用方决定是否需要重启本地 WS。
func (a *app) applyInputConfig(input map[string]interface{}) (bool, error) {
	a.mu.Lock()
	before := a.cfg
	cfg := a.cfg
	a.mu.Unlock()

	if _, ok := input["controlPort"]; ok {
		cfg.ControlPort = intFrom(input, "controlPort", cfg.ControlPort)
	}
	if _, ok := input["streamPort"]; ok {
		cfg.StreamPort = intFrom(input, "streamPort", cfg.StreamPort)
	}
	if _, ok := input["phoneHost"]; ok {
		cfg.PhoneHost = strFrom(input, "phoneHost")
	}
	if _, ok := input["phonePort"]; ok {
		cfg.PhonePort = intFrom(input, "phonePort", cfg.PhonePort)
	}
	if _, ok := input["autoDiscover"]; ok {
		cfg.AutoDiscover = boolFrom(input, "autoDiscover", cfg.AutoDiscover)
	}

	cfg = cfg.normalize()
	if cfg.ControlPort < 1024 || cfg.ControlPort > 65534 {
		return false, fmt.Errorf("控制端口必须在 1024–65534 之间（收到 %d）", cfg.ControlPort)
	}
	if r := excludedReason(cfg.ControlPort); r != "" && !strings.HasPrefix(r, "常见服务端口") {
		return false, fmt.Errorf("控制端口 %d 不可用：%s", cfg.ControlPort, r)
	}

	a.mu.Lock()
	a.cfg = cfg
	a.mu.Unlock()

	if err := saveConfig(a.cfgPath, cfg); err != nil {
		return false, fmt.Errorf("写入 %s 失败: %w", a.cfgPath, err)
	}
	return before.ControlPort != cfg.ControlPort, nil
}

func hasConfigKeys(m map[string]interface{}) bool {
	for _, k := range []string{"controlPort", "streamPort", "phoneHost", "phonePort", "autoDiscover"} {
		if _, ok := m[k]; ok {
			return true
		}
	}
	return false
}

func (a *app) handleConfigSet(input map[string]interface{}) (interface{}, error) {
	portsChanged, err := a.applyInputConfig(input)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	cfg := a.cfg
	a.mu.Unlock()
	a.logf("info", "端口配置已保存：controlPort=%d，streamPort=%d，phonePort=%d", cfg.ControlPort, cfg.StreamPort, cfg.PhonePort)

	result := map[string]interface{}{
		"ok":      true,
		"savedTo": a.cfgPath,
		"config":  cfg,
	}

	// 端口变了且本地服务在跑 → 就地重启到新端口
	if portsChanged {
		wasRunning := a.wsRunning()
		a.stopWS()
		if wasRunning {
			if err := a.startWS(); err != nil {
				a.logf("error", "切换端口失败：%v", err)
				result["ok"] = false
				result["warning"] = err.Error()
			} else {
				a.broadcastConfig()
			}
		}
	}
	result["status"] = a.snapshot()
	return result, nil
}

// handlePhoneReconnect 断开并重新连接手机。手机端 acceptLoop 每接受一条连接都会
// requestKeyFrame()，所以这是「已连接但迟迟没有画面 / 画面冻结」最有效的恢复手段。
func (a *app) handlePhoneReconnect() (interface{}, error) {
	a.mu.Lock()
	host, port := a.cfg.PhoneHost, a.cfg.PhonePort
	auto := a.cfg.AutoDiscover
	a.mu.Unlock()

	a.disconnect()
	a.mu.Lock()
	a.watchAttempt = 0
	a.mu.Unlock()

	if host != "" {
		if err := a.connect(host, port); err != nil {
			return map[string]interface{}{"ok": false, "error": err.Error(), "status": a.snapshot()}, nil
		}
		return map[string]interface{}{"ok": true, "mode": "direct", "config": a.cfgSnapshot(), "status": a.snapshot()}, nil
	}
	if auto {
		if err := a.startDiscovery(12*time.Second, true); err != nil {
			return map[string]interface{}{"ok": false, "error": err.Error()}, nil
		}
		return map[string]interface{}{"ok": true, "mode": "discover"}, nil
	}
	return map[string]interface{}{"ok": false, "error": "未配置手机地址，且未开启自动发现"}, nil
}

func (a *app) cfgSnapshot() config {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg
}

func (a *app) handleStreamPull(input map[string]interface{}) interface{} {
	since := int64(intFrom(input, "since", -1))
	maxChunks := intFrom(input, "maxChunks", 24)
	if maxChunks <= 0 || maxChunks > 64 {
		maxChunks = 24
	}
	maxBytes := intFrom(input, "maxBytes", 512*1024)
	if maxBytes <= 0 || maxBytes > 512*1024 {
		maxBytes = 512 * 1024
	}
	chunks, next, head, gap := a.pull.since(since, maxChunks, maxBytes)

	out := make([]map[string]interface{}, 0, len(chunks))
	for _, c := range chunks {
		out = append(out, map[string]interface{}{
			"seq":   c.seq,
			"flags": c.flags,
			"pts":   c.pts,
			"data":  base64.StdEncoding.EncodeToString(c.data),
		})
	}

	res := map[string]interface{}{
		"next":   next,
		"head":   head,
		"gap":    gap,
		"chunks": out,
	}
	if cfg := a.configMsg(); cfg != nil {
		res["config"] = cfg
	}
	a.mu.Lock()
	res["screenW"] = a.screenW
	res["screenH"] = a.screenH
	res["connected"] = a.connected
	a.mu.Unlock()
	return res
}

// wsAllowedCmds 允许经「控制通道 WS」下发的命令白名单。
//
// 控制通道只监听 127.0.0.1，且这里只开「控制 / 查询 / 配置」类命令，
// 不开任何文件、进程、shell 能力，因此对端最多也只能操控这台机器的手机镜像。
//
// 有了它，前端就不必依赖宿主的 plugin:execute 桥接：宿主桥接一旦失效
// （例如插件后端被宿主 kill 后重建），前端仍可直连本地端口把命令送进来。
var wsAllowedCmds = map[string]bool{
	"start":           true,
	"stop":            true,
	"status":          true,
	"port.suggest":    true,
	"phone.reconnect": true,
	"config.get":      true,
	"config.set":      true,
	"stream.pull":     true,
	"control.tap":     true,
	"control.swipe":   true,
	"control.text":    true,
	"control.key":     true,
}

// handleWSControl 处理控制通道 WS 上的文本消息。
// 载荷形式：{"t":"命令名","id":任意,"参数...":...}
//   - t=ping            → 回 {"t":"pong"}
//   - t=白名单命令       → 回 {"t":"res","id":..,"ok":true,"data":..}
//   - 其它               → 回 {"t":"error","id":..,"msg":..}
func (a *app) handleWSControl(c *wsClient, payload []byte) {
	var msg map[string]interface{}
	if err := json.Unmarshal(payload, &msg); err != nil {
		c.sendJSON(map[string]interface{}{"t": "error", "msg": "载荷不是 JSON 对象"})
		return
	}
	name, _ := msg["t"].(string)
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" || name == "ping" {
		c.sendJSON(map[string]interface{}{"t": "pong", "ts": time.Now().UnixMilli()})
		return
	}
	cmd := strings.ReplaceAll(name, "_", ".")
	id := msg["id"]
	if !wsAllowedCmds[cmd] {
		c.sendJSON(map[string]interface{}{
			"t": "error", "id": id, "cmd": cmd,
			"msg": "控制通道不支持该命令",
		})
		return
	}
	delete(msg, "t")

	// 记录每一条经本地直连进来的命令（高频命令除外）：
	// 宿主日志里能与 RPC 命令区分开，排查「界面点了没反应」时可直接看出请求有没有到达后端。
	begin := time.Now()
	if !quietCmds[cmd] {
		a.logf("info", "WS 命令: %s（本地直连 id=%v）", cmd, id)
	}

	res, err := a.handleExecute(msg, cmd)
	if err != nil {
		a.logf("warn", "WS 命令 %s 失败（%dms）: %v", cmd, time.Since(begin).Milliseconds(), err)
		c.sendJSON(map[string]interface{}{"t": "error", "id": id, "cmd": cmd, "msg": err.Error()})
		return
	}
	if !quietCmds[cmd] {
		a.logf("info", "WS 命令 %s 完成（%dms）", cmd, time.Since(begin).Milliseconds())
	}
	c.sendJSON(map[string]interface{}{"t": "res", "id": id, "cmd": cmd, "ok": true, "data": res})
}

// quietCmds 是不写日志的命令：status / stream.pull / control.* 出现频率高，
// 写进宿主日志只会把「启动接收」这类关键路径的信息淹没掉。
var quietCmds = map[string]bool{
	"status":        true,
	"stream.pull":   true,
	"control.tap":   true,
	"control.swipe": true,
	"control.text":  true,
	"control.key":   true,
}

func (a *app) handleExecute(input map[string]interface{}, cmd string) (interface{}, error) {
	switch cmd {
	case "start":
		return a.handleStart(input)

	case "stop":
		a.stopDiscovery()
		a.disconnect()
		a.logf("info", "已停止接收")
		return map[string]interface{}{"ok": true, "status": a.snapshot()}, nil

	case "status":
		return a.snapshot(), nil

	case "port.suggest", "portsuggest":
		return a.suggestPorts(), nil

	case "phone.reconnect", "phonereconnect":
		return a.handlePhoneReconnect()

	case "config.get":
		a.mu.Lock()
		cfg := a.cfg
		a.mu.Unlock()
		return map[string]interface{}{
			"config": cfg,
			"path":   a.cfgPath,
			"status": a.snapshot(),
		}, nil

	case "config.set":
		return a.handleConfigSet(input)

	case "control.tap":
		if err := a.tap(float32(floatFrom(input, "x", 0)), float32(floatFrom(input, "y", 0))); err != nil {
			return nil, err
		}
		return map[string]interface{}{"ok": true}, nil

	case "control.swipe":
		dur := intFrom(input, "durationMs", intFrom(input, "durMs", 200))
		if dur <= 0 || dur > 10000 {
			dur = 200
		}
		err := a.swipe(
			float32(floatFrom(input, "x1", 0)), float32(floatFrom(input, "y1", 0)),
			float32(floatFrom(input, "x2", 0)), float32(floatFrom(input, "y2", 0)),
			int32(dur),
		)
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{"ok": true}, nil

	case "control.text":
		text := strFrom(input, "text")
		if text == "" {
			return nil, errors.New("文本为空")
		}
		if err := a.text(text); err != nil {
			return nil, err
		}
		return map[string]interface{}{"ok": true, "len": len(text)}, nil

	case "control.key":
		keycode := intFrom(input, "keycode", 0)
		if keycode == 0 {
			return nil, errors.New("keycode 不能为空（4=返回 3=Home 187=多任务）")
		}
		if err := a.key(int32(keycode)); err != nil {
			return nil, err
		}
		return map[string]interface{}{"ok": true, "keycode": keycode}, nil

	case "stream.pull", "streampull":
		return a.handleStreamPull(input), nil
	}
	return nil, fmt.Errorf("unknown command: %s", cmd)
}

// ============================================================ 入口

func main() {
	appState.cfg = defaultConfig()
	// 配置落在 <用户主目录>/.quickdock/plugin-data/<id>/config.json，与插件安装目录分离；
	// ensureDataFile 首次运行会把安装目录里的老 config.json 迁过来。
	appState.cfgPath = ensureDataFile("config.json")
	appState.startedAt = time.Now()
	appState.lastTick = time.Now()
	appState.pull = pullBuf{maxChunks: 180, maxBytes: 6 << 20}

	appState.ctlHub = newHub("ctl", 256, false, func(c *wsClient) {
		c.sendJSON(map[string]interface{}{"t": "hello", "name": "control-receiver", "channel": "ctl"})
		appState.broadcastStatus()
		appState.ctlHub.broadcastJSON(map[string]interface{}{"t": "devices", "devices": appState.deviceList()})
	})
	appState.ctlHub.onText = appState.handleWSControl
	appState.strHub = newHub("stream", 16, true, func(c *wsClient) {
		// 中途连入的前端也要立刻知道屏幕尺寸与解码参数
		appState.mu.Lock()
		w, h, dpi := appState.screenW, appState.screenH, appState.screenDPI
		appState.mu.Unlock()
		if w > 0 && h > 0 {
			c.sendJSON(map[string]interface{}{"t": "screen", "w": w, "h": h, "dpi": dpi})
		}
		if msg := appState.configMsg(); msg != nil {
			c.sendJSON(msg)
		}
	})

	go appState.statsLoop()

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 1<<20), 1<<20) // 宿主单行上限约 1MB
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			respondError(0, -32700, "parse error: "+err.Error())
			continue
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					hostLog("error", "dispatch panic: %v\n%s", r, debug.Stack())
					if req.ID != 0 {
						respondError(req.ID, -32000, fmt.Sprintf("internal error: %v", r))
					}
				}
			}()
			handleRequest(req)
		}()
	}

	appState.stopDiscovery()
	appState.disconnect()
	appState.stopWS()
}

func handleRequest(req rpcRequest) {
	switch req.Method {
	case "initialize":
		var p struct {
			PluginDir string `json:"pluginDir"`
		}
		if len(req.Params) > 0 {
			_ = json.Unmarshal(req.Params, &p)
		}
		if p.PluginDir != "" {
			// pluginDir 只做展示 / 旧数据迁移来源，配置路径始终固定在 plugin-data 下。
			appState.mu.Lock()
			appState.pluginDir = p.PluginDir
			appState.mu.Unlock()
		}
		appState.mu.Lock()
		appState.cfg = loadConfig(appState.cfgPath).normalize()
		savedPath := appState.cfgPath
		cfgNow := appState.cfg
		appState.mu.Unlock()
		hostLog("info", "配置目录: %s（控制端口 %d，视频流 %d）", savedPath, cfgNow.ControlPort, cfgNow.StreamPort)

		// 立刻绑定本地 WS 控制面：前端随后可以直接连 127.0.0.1 下发命令，
		// 完全绕开宿主的 plugin:execute 桥接（该桥接在插件后端被 kill 重建后可能失效，
		// 表现为界面点「启动接收」后一直没反应）。
		// 绑定失败不致命：端口被占时前端会自动退回宿主 RPC 通道。
		localWS := ""
		if err := appState.startWS(); err != nil {
			hostLog("warn", "本地 WS 预绑定失败（将依赖宿主桥接）：%v", err)
		} else {
			localWS = fmt.Sprintf("ws://127.0.0.1:%d/", cfgNow.ControlPort)
		}

		respond(req.ID, map[string]interface{}{
			"status":   "ready",
			"name":     "QuickDock Control Receiver",
			"pluginId": "io.github.parieses.control-receiver",
			"localWS":  localWS,
			"capabilities": []string{
				"start", "stop", "status", "port.suggest", "phone.reconnect",
				"control.tap", "control.swipe", "control.text", "control.key",
				"config.get", "config.set", "stream.pull",
			},
		})

	case "host.ping":
		respond(req.ID, map[string]interface{}{"pong": true, "time": time.Now().UnixMilli()})

	case "plugin.execute":
		handleExecute(req)

	case "shutdown":
		appState.stopDiscovery()
		appState.disconnect()
		appState.stopWS()
		os.Exit(0)

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
	// 前端 pluginExec(cmd, data) 把 data 序列化进 input.text；这里摊平，
	// 让 handler 能直接按 input["x"] 取值。同时兼容宿主直接塞键值的路径。
	if raw, ok := input["text"].(string); ok && raw != "" {
		trimmed := strings.TrimSpace(raw)
		if strings.HasPrefix(trimmed, "{") {
			var nested map[string]interface{}
			if err := json.Unmarshal([]byte(trimmed), &nested); err == nil {
				for k, v := range nested {
					if _, exists := input[k]; !exists {
						input[k] = v
					}
				}
			}
		} else if _, exists := input["text"]; !exists {
			input["text"] = trimmed
		}
	}

	// 命令名全小写后分发；点号与连字符写法都接受（port.suggest / port-suggest）
	cmd := strings.ToLower(strings.TrimSpace(params.Command))
	cmd = strings.ReplaceAll(cmd, "_", ".")
	if cmd == "" {
		cmd = "status"
	}

	// 关键路径留痕：宿主日志里能看到命令有没有到达、耗时多少。
	begin := time.Now()
	if !quietCmds[cmd] {
		appState.logf("info", "RPC 命令: %s（宿主桥接 id=%d）", cmd, req.ID)
	}

	result, err := appState.handleExecute(input, cmd)
	if err != nil {
		code := -32003
		if strings.HasPrefix(err.Error(), "unknown command") {
			code = -32601
		}
		if !quietCmds[cmd] {
			appState.logf("warn", "RPC 命令 %s 失败（%dms）: %v", cmd, time.Since(begin).Milliseconds(), err)
		}
		respondError(req.ID, code, err.Error())
		return
	}
	if !quietCmds[cmd] {
		appState.logf("info", "RPC 命令 %s 完成（%dms）", cmd, time.Since(begin).Milliseconds())
	}
	respond(req.ID, result)
}

func exeDir() string {
	exe, err := os.Executable()
	if err != nil {
		wd, _ := os.Getwd()
		return wd
	}
	return filepath.Dir(exe)
}

// ============================================================ 取值工具

func strFrom(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func intFrom(m map[string]interface{}, key string, def int) int {
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case json.Number:
		if i, err := v.Int64(); err == nil {
			return int(i)
		}
	case string:
		if i, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return i
		}
	}
	return def
}

func floatFrom(m map[string]interface{}, key string, def float64) float64 {
	switch v := m[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case json.Number:
		if f, err := v.Float64(); err == nil {
			return f
		}
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
			return f
		}
	}
	return def
}

func boolFrom(m map[string]interface{}, key string, def bool) bool {
	switch v := m[key].(type) {
	case bool:
		return v
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "yes", "on":
			return true
		case "0", "false", "no", "off":
			return false
		}
	case float64:
		return v != 0
	}
	return def
}
