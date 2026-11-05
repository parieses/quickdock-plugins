package main

// 时间轴事件模型与采集调度。
// 设计原则：每条事件都带「来源 + 证据强度」，推断项绝不伪装成确证。

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
)

const (
	TypeSession = "session"
	TypeProgram = "program"
	TypeDevice  = "device"
	TypeFile    = "file"
	TypeDelete  = "delete"
	TypeChange  = "change"
	TypeNetwork = "network"
	TypeTamper  = "tamper"

	LevelConfirmed    = "confirmed"
	LevelCorroborated = "corroborated"
	LevelInferred     = "inferred"
)

type TimelineEvent struct {
	TS     int64  `json:"ts"`
	Time   string `json:"time"`
	Type   string `json:"type"`
	Level  string `json:"level"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
	Source string `json:"source"`
	Raw    string `json:"raw"`
}

type SourceStatus struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
	Count     int    `json:"count"`
	Note      string `json:"note"`
	Level     string `json:"level"`
}

type CollectContext struct {
	From, To     time.Time
	FromMS, ToMS int64
	Profile      string
	AppData      string
	LocalAppData string
	SystemRoot   string
	SID          string
	MaxEvents    int
}

type Collector func(c CollectContext, emit func(TimelineEvent)) SourceStatus

type collectState struct {
	mu      sync.Mutex
	events  []TimelineEvent
	sources []SourceStatus
}

func (s *collectState) emit(c CollectContext, e TimelineEvent) {
	if e.TS <= 0 || e.TS < c.FromMS || e.TS > c.ToMS {
		return
	}
	if e.Time == "" {
		e.Time = time.UnixMilli(e.TS).Local().Format("2006-01-02 15:04:05")
	}
	if e.Level == "" {
		e.Level = LevelCorroborated
	}
	s.mu.Lock()
	s.events = append(s.events, e)
	s.mu.Unlock()
}

func (s *collectState) addSource(st SourceStatus) {
	s.mu.Lock()
	s.sources = append(s.sources, st)
	s.mu.Unlock()
}

// collectAll 并发跑所有只读取证源，按时间倒序汇总。
func collectAll(c CollectContext) ([]TimelineEvent, []SourceStatus) {
	st := &collectState{}
	collectors := []Collector{
		collectSecurityLog, collectSystemLog, collectRDSessionLog,
		collectUSBEventLog, collectPowerShellLog, collectAppInstallLog, collectSysmonLog,
		collectUSBRegistry, collectUserAssist, collectBAM, collectRecentDocsStatus,
		collectPrefetch, collectRecentLNK, collectJumpLists, collectRecycleBin, collectSetupAPI,
	}
	var wg sync.WaitGroup
	for _, fn := range collectors {
		wg.Add(1)
		go func(f Collector) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					qdLogWarn("采集器异常: %v", r)
				}
			}()
			// 只统计真正落入所选日期段的事件，避免把「扫描过的文件数」当成结果条数
			produced := 0
			wrapped := func(e TimelineEvent) {
				if e.TS <= 0 || e.TS < c.FromMS || e.TS > c.ToMS {
					return
				}
				produced++
				st.emit(c, e)
			}
			res := f(c, wrapped)
			res.Count = produced
			if res.Available && produced == 0 {
				res.Level = "partial"
				if res.Note == "" {
					res.Note = "该时段没有落入时间轴的记录"
				}
			}
			st.addSource(res)
		}(fn)
	}
	wg.Wait()

	sort.Slice(st.events, func(i, j int) bool { return st.events[i].TS > st.events[j].TS })
	sort.Slice(st.sources, func(i, j int) bool { return st.sources[i].Name < st.sources[j].Name })
	if c.MaxEvents > 0 && len(st.events) > c.MaxEvents {
		st.events = st.events[:c.MaxEvents]
	}
	return st.events, st.sources
}

// ev 构造时间轴事件；零时间会被 emit 过滤掉。
func ev(t time.Time, typ, level, title, detail, source, raw string) TimelineEvent {
	if t.IsZero() {
		return TimelineEvent{}
	}
	lt := t.Local()
	return TimelineEvent{
		TS:     lt.UnixMilli(),
		Time:   lt.Format("2006-01-02 15:04:05"),
		Type:   typ,
		Level:  level,
		Title:  title,
		Detail: detail,
		Source: source,
		Raw:    raw,
	}
}

func src(name string, count int, err error, emptyNote string) SourceStatus {
	s := SourceStatus{Name: name, Count: count, Level: "ok", Available: true}
	if err != nil {
		s.Available = false
		s.Level = "missing"
		s.Note = "读取失败：" + shortErr(err)
		return s
	}
	if count == 0 {
		s.Level = "partial"
		s.Note = emptyNote
	}
	return s
}

func shortErr(err error) string {
	if err == nil {
		return ""
	}
	s := strings.Join(strings.Fields(decodeConsoleBytes([]byte(err.Error()))), " ")
	if len([]rune(s)) > 120 {
		return string([]rune(s)[:120]) + "…"
	}
	return s
}

// execOut 执行只读系统命令，带超时；stdout 返回，stderr 并入错误信息。
func execOut(timeout time.Duration, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	applyProcAttr(cmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return out, fmt.Errorf("%w: %s", err, truncStr(msg, 180))
		}
		return out, err
	}
	return out, nil
}

func truncStr(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func parseDate(s string, endOfDay bool) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	layouts := []string{"2006-01-02", "2006/01/02", time.RFC3339, "2006-01-02 15:04:05"}
	for _, l := range layouts {
		if t, err := time.ParseInLocation(l, s, time.Local); err == nil {
			if endOfDay && (l == "2006-01-02" || l == "2006/01/02") {
				return t.Add(24*time.Hour - time.Nanosecond)
			}
			return t
		}
	}
	return time.Time{}
}

func filetimeToTime(ft uint64) time.Time {
	if ft == 0 || ft < 116444736000000000 {
		return time.Time{}
	}
	return time.Unix(0, int64(ft-116444736000000000)*100).UTC()
}

func filetimeFromBytes(b []byte, off int) time.Time {
	if len(b) < off+8 {
		return time.Time{}
	}
	return filetimeToTime(binary.LittleEndian.Uint64(b[off : off+8]))
}

func parseHexBytes(s string) []byte {
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, "\t", "")
	s = strings.TrimPrefix(strings.ToLower(s), "0x")
	b, _ := hex.DecodeString(s)
	return b
}

func rot13(s string) string {
	out := []rune(s)
	for i, r := range out {
		switch {
		case r >= 'a' && r <= 'z':
			out[i] = 'a' + (r-'a'+13)%26
		case r >= 'A' && r <= 'Z':
			out[i] = 'A' + (r-'A'+13)%26
		}
	}
	return string(out)
}

func utf16leToString(b []byte) string {
	if len(b) < 2 {
		return ""
	}
	if len(b)%2 != 0 {
		b = b[:len(b)-1]
	}
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		v := uint16(b[i]) | uint16(b[i+1])<<8
		if v == 0 {
			break
		}
		u = append(u, v)
	}
	return strings.TrimSpace(string(utf16.Decode(u)))
}

func rawFrom(d map[string]string, keys ...string) string {
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		if v := strings.TrimSpace(d[k]); v != "" && v != "-" {
			parts = append(parts, k+"="+truncStr(v, 160))
		}
	}
	return strings.Join(parts, " · ")
}

func passesTypes(types []string, e TimelineEvent) bool {
	if len(types) == 0 {
		return true
	}
	for _, t := range types {
		if t == e.Type {
			return true
		}
	}
	return false
}
