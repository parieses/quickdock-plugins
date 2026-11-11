package main

// 加固对比：把一轮巡检的风险清单落盘为基线，修复后重跑自动给出
// 「已拦截 / 仍暴露 / 新增」，并支持只针对基线风险点的单次只读复验。
//
// 存储位置：<用户主目录>/.quickdock/plugin-data/io.github.parieses.server-audit/baselines.json
// 与插件安装目录分离（宿主升级会重命名整个安装目录，写在里面的数据会被卷走）。
// QUICKDOCK_PLUGIN_DATA 可覆盖数据根，用于测试隔离。
//
// 只读边界不变：基线只写本机文件；复验对每个风险点最多发一次只读请求，
// 不递归、不重试、不写入目标、不做任何认证轮询。

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	baselinePluginID    = "io.github.parieses.server-audit"
	baselineFileName    = "baselines.json"
	maxBaselinesPerHost = 6                // 每个主机保留最近 6 条，避免文件无限增长
	maxVerifyItems      = 40               // 快速复验一次最多覆盖 40 个风险点
	verifyTimeout       = 3 * time.Second  // 单项超时
	verifyBudget        = 20 * time.Second // 整体预算：必须小于前端 30s 请求超时
)

var baselineMu sync.Mutex

// BaselineItem 一轮巡检的可对比快照。只保留判定所需字段，不存完整端口表与正文。
type BaselineItem struct {
	ID        string        `json:"id"`
	Host      string        `json:"host"`
	IP        string        `json:"ip"`
	SavedAt   string        `json:"savedAt"`
	Label     string        `json:"label,omitempty"`
	Source    string        `json:"source,omitempty"`
	Summary   Summary       `json:"summary"`
	Findings  []Finding     `json:"findings"`
	OpenPorts []int         `json:"openPorts"`
	Paths     []PathHit     `json:"paths"`
	Unauth    []UnauthCheck `json:"unauth"`
	Services  []ServiceInfo `json:"services"`
}

type BaselineMeta struct {
	ID        string  `json:"id"`
	Host      string  `json:"host"`
	IP        string  `json:"ip"`
	SavedAt   string  `json:"savedAt"`
	Label     string  `json:"label,omitempty"`
	Source    string  `json:"source,omitempty"`
	RiskCount int     `json:"riskCount"`
	Summary   Summary `json:"summary"`
}

type baselineDB struct {
	Version int            `json:"version"`
	Items   []BaselineItem `json:"items"`
}

// CompareRow 单条风险的前后状态。
type CompareRow struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	State  string `json:"state"` // fixed 已拦截 | still 仍暴露 | new 新增 | worse 加重
	Before string `json:"before"`
	After  string `json:"after"`
	Detail string `json:"detail,omitempty"`
	Fix    string `json:"fix,omitempty"`
	Port   int    `json:"port,omitempty"`
}

type CompareResult struct {
	BaselineID string         `json:"baselineId"`
	BaselineAt string         `json:"baselineAt"`
	Host       string         `json:"host"`
	Mode       string         `json:"mode"` // full 完整重扫 | quick 单次只读复验
	Fixed      []CompareRow   `json:"fixed"`
	Still      []CompareRow   `json:"still"`
	New        []CompareRow   `json:"new"`
	Counts     map[string]int `json:"counts"`
	Report     string         `json:"report"`
	Note       string         `json:"note,omitempty"`
}

/* ==================== 落盘 ==================== */

func baselineDir() string {
	base := os.Getenv("QUICKDOCK_PLUGIN_DATA")
	if base == "" {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			base = filepath.Join(home, ".quickdock", "plugin-data")
		} else if cfg, err := os.UserConfigDir(); err == nil && cfg != "" {
			base = filepath.Join(cfg, "QuickDock", "plugin-data")
		} else {
			base = filepath.Join(os.TempDir(), "quickdock-plugin-data")
		}
	}
	return filepath.Join(base, baselinePluginID)
}

func loadDB() baselineDB {
	empty := baselineDB{Version: 1, Items: []BaselineItem{}}
	b, err := os.ReadFile(filepath.Join(baselineDir(), baselineFileName))
	if err != nil {
		return empty
	}
	var db baselineDB
	if err := json.Unmarshal(b, &db); err != nil {
		// 文件损坏不阻断巡检：当作空库重新开始
		qdLogInfo("baseline db unreadable: %v", err)
		return empty
	}
	if db.Items == nil {
		db.Items = []BaselineItem{}
	}
	return db
}

func saveDB(db baselineDB) error {
	dir := baselineDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(db, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, baselineFileName+".tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	dst := filepath.Join(dir, baselineFileName)
	if err := os.Rename(tmp, dst); err != nil {
		// Windows 上目标已存在时先移除再换；失败则清理临时文件
		_ = os.Remove(dst)
		if err2 := os.Rename(tmp, dst); err2 != nil {
			_ = os.Remove(tmp)
			return err2
		}
	}
	return nil
}

func newBaselineID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return "bl-" + strconv.FormatInt(time.Now().UnixMilli(), 36) + "-" + hex.EncodeToString(b)
}

func trimBaselines(items []BaselineItem) []BaselineItem {
	perHost := map[string]int{}
	out := make([]BaselineItem, 0, len(items))
	for _, it := range items {
		if perHost[it.Host] >= maxBaselinesPerHost {
			continue
		}
		perHost[it.Host]++
		out = append(out, it)
	}
	return out
}

func metaOf(it BaselineItem) BaselineMeta {
	return BaselineMeta{
		ID: it.ID, Host: it.Host, IP: it.IP, SavedAt: it.SavedAt,
		Label: it.Label, Source: it.Source, Summary: it.Summary,
		RiskCount: len(riskFindings(it.Findings)),
	}
}

// saveBaselineFromSession 把一次已完成的巡检存为基线（新条目排在最前）。
func saveBaselineFromSession(s *auditSession, label string) (BaselineItem, error) {
	s.mu.Lock()
	item := BaselineItem{
		ID:        newBaselineID(),
		Host:      s.target.Host,
		IP:        s.target.IP,
		SavedAt:   time.Now().Format(time.RFC3339),
		Label:     label,
		Source:    s.ID,
		Summary:   summarize(s.findings),
		Findings:  append([]Finding{}, s.findings...),
		Paths:     append([]PathHit{}, s.paths...),
		Unauth:    append([]UnauthCheck{}, s.unauth...),
		Services:  append([]ServiceInfo{}, s.services...),
		OpenPorts: []int{},
	}
	for _, p := range s.ports {
		if p.State == "open" {
			item.OpenPorts = append(item.OpenPorts, p.Port)
		}
	}
	s.mu.Unlock()

	baselineMu.Lock()
	defer baselineMu.Unlock()
	db := loadDB()
	db.Items = append([]BaselineItem{item}, db.Items...)
	db.Items = trimBaselines(db.Items)
	if err := saveDB(db); err != nil {
		return BaselineItem{}, err
	}
	return item, nil
}

func listBaselines(host string) []BaselineMeta {
	baselineMu.Lock()
	defer baselineMu.Unlock()
	out := []BaselineMeta{}
	for _, it := range loadDB().Items {
		if host != "" && it.Host != host {
			continue
		}
		out = append(out, metaOf(it))
	}
	return out
}

func findBaseline(id string) (BaselineItem, bool) {
	if id == "" {
		return BaselineItem{}, false
	}
	baselineMu.Lock()
	defer baselineMu.Unlock()
	for _, it := range loadDB().Items {
		if it.ID == id {
			return it, true
		}
	}
	return BaselineItem{}, false
}

// previousBaseline 取该主机最近一条基线；在本轮结果落盘之前调用。
func previousBaseline(host string) (BaselineItem, bool) {
	baselineMu.Lock()
	defer baselineMu.Unlock()
	for _, it := range loadDB().Items {
		if it.Host == host {
			return it, true
		}
	}
	return BaselineItem{}, false
}

func deleteBaseline(id string) bool {
	baselineMu.Lock()
	defer baselineMu.Unlock()
	db := loadDB()
	out := make([]BaselineItem, 0, len(db.Items))
	removed := false
	for _, it := range db.Items {
		if it.ID == id {
			removed = true
			continue
		}
		out = append(out, it)
	}
	if !removed {
		return false
	}
	db.Items = out
	return saveDB(db) == nil
}

/* ==================== 对比 ==================== */

// riskFindings 剔除汇总项与中性观察项：它们每轮都在，对比只会制造噪音。
func riskFindings(fs []Finding) []Finding {
	out := []Finding{}
	for _, f := range fs {
		if f.ID == "" || f.ID == "exposure-summary" || f.Severity == "info" {
			continue
		}
		out = append(out, f)
	}
	return out
}

func compareFindings(base, cur []Finding) (fixed, still, newItems []CompareRow) {
	b := map[string]Finding{}
	for _, f := range riskFindings(base) {
		b[f.ID] = f
	}
	c := map[string]Finding{}
	for _, f := range riskFindings(cur) {
		c[f.ID] = f
	}

	fixed, still, newItems = []CompareRow{}, []CompareRow{}, []CompareRow{}
	for id, bf := range b {
		cf, ok := c[id]
		if !ok {
			fixed = append(fixed, CompareRow{
				ID: id, Title: bf.Title, State: "fixed", Before: bf.Severity, After: "-",
				Detail: "本轮未再复现（已拦截 / 已关闭 / 已加固）", Fix: bf.Fix, Port: bf.Port,
			})
			continue
		}
		row := CompareRow{
			ID: id, Title: cf.Title, State: "still", Before: bf.Severity, After: cf.Severity,
			Fix: cf.Fix, Port: cf.Port, Detail: "仍可复现，需继续加固",
		}
		if sevRank(cf.Severity) < sevRank(bf.Severity) {
			row.State = "worse"
			row.Detail = "风险等级升高：" + bf.Severity + " → " + cf.Severity
		} else if cf.Severity != bf.Severity {
			row.Detail = "风险等级变化：" + bf.Severity + " → " + cf.Severity
		}
		still = append(still, row)
	}
	for id, cf := range c {
		if _, ok := b[id]; ok {
			continue
		}
		newItems = append(newItems, CompareRow{
			ID: id, Title: cf.Title, State: "new", Before: "-", After: cf.Severity,
			Detail: "基线之后新出现", Fix: cf.Fix, Port: cf.Port,
		})
	}
	bySeverity := func(rows []CompareRow) {
		sort.SliceStable(rows, func(i, j int) bool {
			ri, rj := sevRank(rows[i].After), sevRank(rows[j].After)
			if ri != rj {
				return ri < rj
			}
			return rows[i].ID < rows[j].ID
		})
	}
	bySeverity(fixed)
	bySeverity(still)
	bySeverity(newItems)
	return fixed, still, newItems
}

func makeCompareResult(base BaselineItem, cur []Finding, mode string) CompareResult {
	fixed, still, newItems := compareFindings(base.Findings, cur)
	cr := CompareResult{
		BaselineID: base.ID, BaselineAt: base.SavedAt, Host: base.Host, Mode: mode,
		Fixed: fixed, Still: still, New: newItems,
		Counts: map[string]int{"fixed": len(fixed), "still": len(still), "new": len(newItems)},
	}
	if mode == "quick" {
		cr.Note = "快速复验只覆盖基线中已有的风险点，每项仅发一次只读请求；新增风险需完整重扫才能发现。"
	}
	cr.Report = compareReport(cr)
	return cr
}

func compareReport(cr CompareResult) string {
	var sb strings.Builder
	mode := "完整重扫"
	if cr.Mode == "quick" {
		mode = "单次只读复验"
	}
	fmt.Fprintf(&sb, "服务器自查 · 加固对比报告\n目标：%s\n基线：%s（%s）\n模式：%s\n结论：已拦截 %d / 仍暴露 %d / 新增 %d\n",
		cr.Host, cr.BaselineAt, cr.BaselineID, mode,
		cr.Counts["fixed"], cr.Counts["still"], cr.Counts["new"])
	section := func(name, mark string, rows []CompareRow) {
		fmt.Fprintf(&sb, "\n【%s】%d 项\n", name, len(rows))
		if len(rows) == 0 {
			sb.WriteString("  （无）\n")
			return
		}
		for _, r := range rows {
			fmt.Fprintf(&sb, "  %s [%s → %s] %s", mark, r.Before, r.After, r.Title)
			if r.Port > 0 {
				fmt.Fprintf(&sb, "（端口 %d）", r.Port)
			}
			sb.WriteString("\n")
			if r.Detail != "" {
				fmt.Fprintf(&sb, "      %s\n", r.Detail)
			}
			if r.State == "worse" || r.State == "new" {
				if r.Fix != "" {
					fmt.Fprintf(&sb, "      处置：%s\n", r.Fix)
				}
			}
		}
	}
	section("已拦截", "✔", cr.Fixed)
	section("仍暴露", "!", cr.Still)
	section("新增", "+", cr.New)
	if cr.Note != "" {
		fmt.Fprintf(&sb, "\n说明：%s\n", cr.Note)
	}
	return sb.String()
}

/* ==================== 单次只读复验 ==================== */

// quickVerify 不重扫端口，只对基线里的每个风险点发一次只读请求，判断「现在还通不通」。
func quickVerify(base BaselineItem) CompareResult {
	items := riskFindings(base.Findings)
	truncated := false
	if len(items) > maxVerifyItems {
		items = items[:maxVerifyItems]
		truncated = true
	}
	cur := []Finding{}
	probe := &auditSession{
		stopCh: make(chan struct{}),
		target: TargetInfo{Host: base.Host, IP: base.IP},
	}
	deadline := time.Now().Add(verifyBudget)
	for i, f := range items {
		if time.Now().After(deadline) {
			// 预算用尽：剩余项一律按「仍暴露」保守计入，绝不误报「已拦截」
			cur = append(cur, items[i:]...)
			truncated = true
			break
		}
		if verifyStillExposed(probe, base, f) {
			cur = append(cur, f)
		}
	}
	cr := makeCompareResult(base, cur, "quick")
	if truncated {
		cr.Note += " 复验受数量/时间上限约束，未完成的项按「仍暴露」保守计入；建议完整重扫确认。"
	}
	return cr
}

// verifyStillExposed 每个分支最多一次网络交互；无法判定时保守返回 true（仍需人工确认）。
func verifyStillExposed(probe *auditSession, base BaselineItem, f Finding) bool {
	switch {
	case strings.HasPrefix(f.ID, "path-"):
		port, rest, ok := splitLeadingPort(strings.TrimPrefix(f.ID, "path-"))
		if !ok {
			return true
		}
		st, _, _, err := httpFetch(base.Host, base.IP, port, serviceUsesTLS(base.Services, port),
			"GET", "/"+strings.TrimPrefix(rest, "/"), verifyTimeout, 512)
		return err != nil || st == 200 // 200 视为仍可读取；401/403/404/410 视为已拦截

	case strings.HasPrefix(f.ID, "unauth-"):
		rest := strings.TrimPrefix(f.ID, "unauth-")
		i := strings.LastIndex(rest, "-")
		if i <= 0 {
			return true
		}
		port, err := strconv.Atoi(rest[i+1:])
		if err != nil {
			return true
		}
		chk, ok := probe.unauthFor(ServiceInfo{Port: port, Service: rest[:i]}, verifyTimeout)
		return !ok || chk.Vulnerable

	case strings.HasPrefix(f.ID, "svc-"):
		port := f.Port
		if port == 0 {
			// svc 的 ID 形如 svc-redis-6379，端口在末尾
			rest := strings.TrimPrefix(f.ID, "svc-")
			i := strings.LastIndex(rest, "-")
			if i <= 0 {
				return true
			}
			port, _ = strconv.Atoi(rest[i+1:])
			if port == 0 {
				return true
			}
		}
		conn, err := net.DialTimeout("tcp", net.JoinHostPort(base.IP, strconv.Itoa(port)), verifyTimeout)
		if err != nil {
			return false // 端口已关闭 → 已拦截
		}
		_ = conn.Close()
		return true

	case strings.HasPrefix(f.ID, "tls-"):
		ti, ok := tlsProbe(base.Host, base.IP, f.Port, verifyTimeout)
		if !ok {
			return true
		}
		switch f.ID {
		case "tls-expired":
			return ti.DaysLeft < 0
		case "tls-expiring":
			return ti.DaysLeft <= 15
		case "tls-selfsigned":
			return ti.SelfSigned
		case "tls-mismatch":
			return ti.HostMismatch
		case "tls-oldproto":
			return strings.Contains(ti.Version, "1.0") || strings.Contains(ti.Version, "1.1")
		case "tls-weakkey":
			return ti.KeyBits > 0 && ti.KeyBits < 2048
		case "tls-sha1":
			return strings.Contains(strings.ToUpper(ti.SigAlg), "SHA1") || strings.Contains(strings.ToUpper(ti.SigAlg), "SHA-1")
		}
		return true

	case strings.HasPrefix(f.ID, "hsts-"):
		port := f.Port
		st, hdr, _, err := httpFetch(base.Host, base.IP, port,
			serviceUsesTLS(base.Services, port), "HEAD", "/", verifyTimeout, 0)
		if err != nil || st == 0 {
			return true
		}
		return strings.TrimSpace(hdr.Get("Strict-Transport-Security")) == ""
	}
	return true
}

// splitLeadingPort 从 "443-/.env" 或 "service-6379" 里取端口。
// 路径型 ID 是 "path-<port>-<path>"，这里返回端口与剩余部分。
func splitLeadingPort(rest string) (int, string, bool) {
	i := strings.Index(rest, "-")
	if i <= 0 {
		return 0, "", false
	}
	port, err := strconv.Atoi(rest[:i])
	if err != nil {
		return 0, "", false
	}
	return port, rest[i+1:], true
}

func serviceUsesTLS(svcs []ServiceInfo, port int) bool {
	for _, svc := range svcs {
		if svc.Port == port {
			return svc.Scheme == "https"
		}
	}
	return port == 443 || port == 8443 || port == 9443
}

/* ==================== RPC 入口 ==================== */

func handleBaselineList(input map[string]interface{}) (map[string]interface{}, string) {
	return map[string]interface{}{"ok": true, "items": listBaselines(strFrom(input, "host"))}, ""
}

func handleBaselineSave(input map[string]interface{}) (map[string]interface{}, string) {
	s := getSession(strFrom(input, "sessionId"))
	if s == nil {
		return nil, "会话不存在或已被回收"
	}
	s.mu.Lock()
	done := !s.running && s.stage == "done"
	s.mu.Unlock()
	if !done {
		return nil, "巡检尚未完成，完成后才能存为基线"
	}
	item, err := saveBaselineFromSession(s, strFrom(input, "label"))
	if err != nil {
		return nil, "保存基线失败：" + err.Error()
	}
	return map[string]interface{}{"ok": true, "baseline": metaOf(item)}, ""
}

func handleBaselineDelete(input map[string]interface{}) (map[string]interface{}, string) {
	id := strFrom(input, "id")
	if id == "" {
		return nil, "缺少基线 id"
	}
	return map[string]interface{}{"ok": deleteBaseline(id)}, ""
}

func handleCompare(input map[string]interface{}) (map[string]interface{}, string) {
	base, ok := findBaseline(strFrom(input, "baselineId"))
	if !ok {
		return nil, "基线不存在（可能已被清理）"
	}
	sid := strFrom(input, "sessionId")
	if sid == "" {
		return map[string]interface{}{"ok": true, "comparison": quickVerify(base)}, ""
	}
	s := getSession(sid)
	if s == nil {
		return nil, "会话不存在或已被回收"
	}
	s.mu.Lock()
	done := !s.running && s.stage == "done"
	cur := append([]Finding{}, s.findings...)
	s.mu.Unlock()
	if !done {
		return nil, "本轮巡检尚未完成，完成后再对比（未完成的清单会把风险误判为已拦截）"
	}
	return map[string]interface{}{"ok": true, "comparison": makeCompareResult(base, cur, "full")}, ""
}

func handleVerify(input map[string]interface{}) (map[string]interface{}, string) {
	base, ok := findBaseline(strFrom(input, "baselineId"))
	if !ok {
		return nil, "基线不存在（可能已被清理）"
	}
	return map[string]interface{}{"ok": true, "comparison": quickVerify(base)}, ""
}
