// 巡检会话：装配目标 → 端口扫描 → 指纹 → 敏感路径 → 未授权校验 → 风险报告。
//
// 采用「start 立即返回 + 前端轮询」的异步会话模型（同 netdiag / dir-buster）：
// 扫描阶段的中间结果实时可见，用户可随时停止。
package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defConcurrency = 500  // 默认并发
	maxConcurrency = 1000 // 硬上限，调参也只能到这里
	defTimeoutMs   = 800  // 单端口 connect 超时
	maxTimeoutMs   = 5000
	maxPathHits    = 200 // 路径命中上限，防爆炸
	maxWebTargets  = 3   // 最多对 3 个 web 端口做路径探测
)

type auditOptions struct {
	Host         string
	Mode         string // common（内置常用端口表） | full（追加 1-65535 全量）
	PortsSpec    string // 自定义端口，给了就只扫这些
	TimeoutMs    int
	Concurrency  int
	CheckPaths   bool
	CheckUnauth  bool
	LookupGeo    bool
	AllowPrivate bool   // 仅单元测试内部构造；RPC 入口恒为 false
	PortHint     int    // 输入形如 host:8080 时只扫这一个端口
	CompareWith  string // 指定对比的基线 id；留空则自动用该主机上一轮基线
}

type TargetInfo struct {
	Input       string   `json:"input"`
	Host        string   `json:"host"`
	IP          string   `json:"ip"`
	IPs         []string `json:"ips"`
	StartedAt   string   `json:"startedAt"`
	Mode        string   `json:"mode"`
	PortCount   int      `json:"portCount"`
	TimeoutMs   int      `json:"timeoutMs"`
	Concurrency int      `json:"concurrency"`
	Geo         *GeoInfo `json:"geo,omitempty"`
	ElapsedMs   int64    `json:"elapsedMs"`
}

type GeoInfo struct {
	OK     bool                `json:"ok"`
	Error  string              `json:"error,omitempty"`
	Fields []map[string]string `json:"fields,omitempty"`
}

type PortStats struct {
	Open     int `json:"open"`
	Closed   int `json:"closed"`
	Filtered int `json:"filtered"`
	Total    int `json:"total"`
}

type Progress struct {
	Phase   string `json:"phase"`
	Scanned int    `json:"scanned"`
	Total   int    `json:"total"`
	Pct     int    `json:"pct"`
}

type Summary struct {
	Critical int    `json:"critical"`
	High     int    `json:"high"`
	Medium   int    `json:"medium"`
	Low      int    `json:"low"`
	Info     int    `json:"info"`
	Exposed  int    `json:"exposed"`
	Verdict  string `json:"verdict"`
}

type auditSession struct {
	ID string

	mu         sync.Mutex
	running    bool
	stage      string
	stageText  string
	errMsg     string
	startedAt  time.Time
	finishedAt time.Time
	opts       auditOptions
	target     TargetInfo
	ports      []PortResult
	stats      PortStats
	progress   Progress
	services   []ServiceInfo
	paths      []PathHit
	pathsCheck int
	unauth     []UnauthCheck
	findings   []Finding
	summary    Summary
	comparison *CompareResult // 本轮与基线的加固对比结果，快照里带给前端

	scanned int64 // atomic，供 worker 高频更新
	total   int64 // atomic

	stopOnce sync.Once
	stopCh   chan struct{}
}

var (
	sessMu sync.Mutex
	sesss  = map[string]*auditSession{}
)

func newSessionID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("sa-%d", time.Now().UnixNano())
	}
	return "sa-" + hex.EncodeToString(b)
}

func putSession(s *auditSession) *auditSession {
	sessMu.Lock()
	defer sessMu.Unlock()
	sesss[s.ID] = s
	// 只留最近 8 个会话，避免长跑内存堆积
	if len(sesss) > 8 {
		var oldest *auditSession
		for _, v := range sesss {
			if oldest == nil || v.startedAt.Before(oldest.startedAt) {
				oldest = v
			}
		}
		if oldest != nil && oldest.ID != s.ID {
			oldest.stop()
			delete(sesss, oldest.ID)
		}
	}
	return s
}

func getSession(id string) *auditSession {
	sessMu.Lock()
	defer sessMu.Unlock()
	return sesss[id]
}

func (s *auditSession) stop() {
	s.stopOnce.Do(func() { close(s.stopCh) })
}

func (s *auditSession) stopped() bool {
	select {
	case <-s.stopCh:
		return true
	default:
		return false
	}
}

func (s *auditSession) setStage(code, text string) {
	s.mu.Lock()
	s.stage, s.stageText = code, text
	s.mu.Unlock()
}

func (s *auditSession) fail(err error) {
	s.mu.Lock()
	s.errMsg = err.Error()
	s.running = false
	s.finishedAt = time.Now()
	s.mu.Unlock()
	qdLogWarn("audit %s aborted: %v", s.ID, err)
}

func (s *auditSession) finish() {
	s.mu.Lock()
	s.running = false
	s.finishedAt = time.Now()
	s.stage = "done"
	s.stageText = "完成"
	s.summary = summarize(s.findings)
	if s.errMsg != "" {
		s.summary.Verdict = s.errMsg
	}
	s.mu.Unlock()
}

/* ==================== 命令入口 ==================== */

func handleAuditStart(input map[string]interface{}) (map[string]interface{}, string) {
	host := strFrom(input, "host")
	if host == "" {
		return nil, "请填写要自查的目标（域名或公网 IP）"
	}
	opts := auditOptions{
		Host:         host,
		Mode:         strings.ToLower(strFrom(input, "mode")),
		PortsSpec:    strFrom(input, "ports"),
		TimeoutMs:    intFrom(input, "timeout", defTimeoutMs),
		Concurrency:  intFrom(input, "concurrency", defConcurrency),
		CheckPaths:   boolFrom(input, "checkPaths", true),
		CheckUnauth:  boolFrom(input, "checkUnauth", true),
		LookupGeo:    boolFrom(input, "geo", false),
		AllowPrivate: false, // 绝不从 RPC 入参放开：内网/回环地址一律拒绝，防误扫他人内网
		CompareWith:  strFrom(input, "compareWith"),
	}
	if opts.TimeoutMs <= 0 {
		opts.TimeoutMs = defTimeoutMs
	}
	if opts.TimeoutMs > maxTimeoutMs {
		opts.TimeoutMs = maxTimeoutMs
	}
	opts.Concurrency = clampConcurrency(opts.Concurrency)
	if opts.Mode == "" {
		opts.Mode = "common"
	}

	s := &auditSession{
		ID:        newSessionID(),
		stopCh:    make(chan struct{}),
		startedAt: time.Now(),
		running:   true,
		stage:     "resolve",
		stageText: "解析预检",
		opts:      opts,
	}
	putSession(s)
	go s.run()

	// 解析是同步前置条件：放在 run 里做但这里先给前端一个立即回执，
	// 真正的解析结论由第一次 poll 带回，失败也会以 error 字段返回。
	return map[string]interface{}{
		"sessionId": s.ID,
		"stage":     "resolve",
		"message":   "已接收目标，开始解析预检",
	}, ""
}

func handleAuditPoll(input map[string]interface{}) (map[string]interface{}, string) {
	s := getSession(strFrom(input, "sessionId"))
	if s == nil {
		return nil, "会话不存在或已结束，请重新开始"
	}
	return s.snapshot(), ""
}

func handleAuditStop(input map[string]interface{}) (map[string]interface{}, string) {
	s := getSession(strFrom(input, "sessionId"))
	if s == nil {
		return map[string]interface{}{"ok": true}, ""
	}
	s.stop()
	return map[string]interface{}{"ok": true}, ""
}

// handleAuditReport 返回纯文本报告，便于复制存档 / 贴到工单。
func handleAuditReport(input map[string]interface{}) (map[string]interface{}, string) {
	s := getSession(strFrom(input, "sessionId"))
	if s == nil {
		return nil, "会话不存在或已结束"
	}
	return map[string]interface{}{"ok": true, "report": s.textReport()}, ""
}

func handleIPLookup(input map[string]interface{}) (map[string]interface{}, string) {
	ip := strFrom(input, "ip")
	if ip == "" {
		return nil, "请填写 IP"
	}
	geo := lookupGeo(ip)
	return map[string]interface{}{"ok": geo.OK, "error": geo.Error, "fields": geo.Fields}, ""
}

/* ==================== 主流程 ==================== */

func (s *auditSession) run() {
	defer func() {
		if r := recover(); r != nil {
			s.fail(fmt.Errorf("巡检异常：%v", r))
			qdLogPanic("server-audit run", r)
		}
	}()

	// 1. 解析预检
	s.setStage("resolve", "解析预检")
	target, err := s.resolveTarget()
	s.mu.Lock()
	s.target = target // 失败时也带上已解析出的主机信息，前端好显示
	s.mu.Unlock()
	if err != nil {
		s.fail(err)
		s.finish()
		return
	}
	qdLogInfo("audit %s target=%s ip=%s mode=%s", s.ID, target.Host, target.IP, s.opts.Mode)

	// 可选：HTTP 查询 IP 归属（不查 whois）
	if s.opts.LookupGeo {
		geo := lookupGeo(target.IP)
		s.mu.Lock()
		s.target.Geo = &geo
		s.mu.Unlock()
	}

	// 2. 端口扫描（两阶段：常用端口表 → 可选全量）
	ports := buildPortList(s.opts)
	s.mu.Lock()
	s.target.PortCount = len(ports)
	s.progress = Progress{Phase: "ports", Total: len(ports)}
	s.mu.Unlock()
	s.setStage("ports", "端口扫描")
	atomic.StoreInt64(&s.total, int64(len(ports)))
	atomic.StoreInt64(&s.scanned, 0)
	open := s.scanPorts(ports)
	if s.stopped() {
		s.setStage("done", "已停止")
		s.finish()
		return
	}

	// 3. 服务指纹
	s.setStage("finger", "服务指纹")
	s.mu.Lock()
	s.progress = Progress{Phase: "finger", Total: len(open)}
	s.mu.Unlock()
	atomic.StoreInt64(&s.total, int64(len(open)))
	atomic.StoreInt64(&s.scanned, 0)
	s.fingerprint(open)

	// 4. 敏感路径（只读 GET/HEAD）
	if s.opts.CheckPaths {
		s.setStage("paths", "敏感路径")
		s.checkPaths()
	}

	// 5. 未授权访问 / 匿名口令单次校验
	if s.opts.CheckUnauth {
		s.setStage("unauth", "未授权校验")
		s.checkUnauth()
	}

	// 6. 汇总报告
	s.setStage("report", "生成报告")
	findings := buildFindings(s)
	s.mu.Lock()
	s.findings = findings
	s.mu.Unlock()

	// 7. 加固对比：先与该主机上一轮基线比对，再把本轮结果存为新基线。
	//    顺序不能反，否则「上一轮」会拿到刚写的这一轮，永远零差异。
	if s.errMsg == "" && !s.stopped() {
		base, ok := previousBaseline(target.Host)
		if s.opts.CompareWith != "" {
			if b, found := findBaseline(s.opts.CompareWith); found && b.Host == target.Host {
				base, ok = b, true
			}
		}
		if ok {
			cmp := makeCompareResult(base, findings, "full")
			s.mu.Lock()
			s.comparison = &cmp
			s.mu.Unlock()
		}
		if _, err := saveBaselineFromSession(s, ""); err != nil {
			// 落盘失败只记日志，不能让一次写文件问题毁掉整轮巡检结果
			qdLogInfo("baseline save failed: %v", err)
		}
	}
	s.finish()
}

/* ==================== 快照 ==================== */

func (s *auditSession) snapshot() map[string]interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()

	elapsed := int64(0)
	if s.finishedAt.IsZero() {
		elapsed = time.Since(s.startedAt).Milliseconds()
	} else {
		elapsed = s.finishedAt.Sub(s.startedAt).Milliseconds()
	}
	target := s.target
	target.ElapsedMs = elapsed

	prog := s.progress
	if s.stage != "done" && s.stage != "report" && prog.Phase != "" {
		prog.Scanned = int(atomic.LoadInt64(&s.scanned))
		prog.Total = int(atomic.LoadInt64(&s.total))
		if prog.Total > 0 {
			prog.Pct = prog.Scanned * 100 / prog.Total
		}
	}

	// 一律返回非 nil 切片：前端按数组遍历，null 会让渲染逻辑多一层判断
	ports := append([]PortResult{}, s.ports...)
	services := append([]ServiceInfo{}, s.services...)
	paths := append([]PathHit{}, s.paths...)
	unauth := append([]UnauthCheck{}, s.unauth...)
	findings := append([]Finding{}, s.findings...)
	if target.IPs == nil {
		target.IPs = []string{}
	}

	summary := summarize(findings)
	if s.errMsg != "" {
		summary.Verdict = s.errMsg
	}

	return map[string]interface{}{
		"sessionId": s.ID,
		"running":   s.running,
		"stage":     s.stage,
		"stageText": s.stageText,
		"error":     s.errMsg,
		"startedAt": s.startedAt.Format(time.RFC3339),
		"elapsedMs": elapsed,
		"target":    target,
		"options": map[string]interface{}{
			"mode":        s.opts.Mode,
			"timeoutMs":   s.opts.TimeoutMs,
			"concurrency": s.opts.Concurrency,
			"checkPaths":  s.opts.CheckPaths,
			"checkUnauth": s.opts.CheckUnauth,
		},
		"progress":     prog,
		"portStats":    s.stats,
		"ports":        ports,
		"services":     services,
		"paths":        paths,
		"pathsChecked": s.pathsCheck,
		"unauth":       unauth,
		"findings":     findings,
		"summary":      summary,
		"comparison":   s.comparison,
	}
}

/* ==================== 小工具 ==================== */

func clampConcurrency(n int) int {
	if n <= 0 {
		return defConcurrency
	}
	if n > maxConcurrency {
		return maxConcurrency
	}
	return n
}

func strFrom(m map[string]interface{}, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func intFrom(m map[string]interface{}, key string, def int) int {
	if m == nil {
		return def
	}
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		var n int
		if _, err := fmt.Sscanf(strings.TrimSpace(v), "%d", &n); err == nil {
			return n
		}
	}
	return def
}

func boolFrom(m map[string]interface{}, key string, def bool) bool {
	if m == nil {
		return def
	}
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

func sortServices(list []ServiceInfo) {
	sort.Slice(list, func(i, j int) bool { return list[i].Port < list[j].Port })
}
