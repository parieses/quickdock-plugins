package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Match 一条命中记录。Value 是原始命中值（用于定位），Masked 是脱敏后的预览，
// 二者都只在结果里回传前端，绝不写进日志（日志只记统计计数）。
type Match struct {
	Rule     string `json:"rule"`
	Name     string `json:"name"`
	Severity string `json:"severity"`
	Category string `json:"category"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	Col      int    `json:"col"`
	Value    string `json:"value"`
	Masked   string `json:"masked"`
}

// Session 一次扫描任务的状态与结果。
type Session struct {
	ID      string
	mu      sync.Mutex
	running bool
	done    bool
	canceled bool
	scanned int64
	totalEst int64 // 边遍历边增长，仅作进度参考
	current string
	matches []Match
	seen    map[string]struct{}
	trunc   bool
	started time.Time
	filter  map[string]bool
	maxRes  int

	// 扫描选项
	excludes []string
	maxSize  int64
	gitHist  bool
}

var (
	sessMu   sync.Mutex
	sessions = map[string]*Session{}
)

// scanOpts 由 scan 命令解析得到（已校验过的参数）。
type scanOpts struct {
	root       string
	excludes   []string
	maxSize    int64
	gitHistory bool
	workers    int
}

func randID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func defaultExcludes() []string {
	return []string{".git", "node_modules", "vendor", "target", "dist", "build",
		".idea", ".vscode", "__pycache__", ".venv", "venv", "bin", "obj"}
}

// isBinaryExt 常见二进制扩展名直接跳过（省一次读文件）。
func isBinaryExt(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".exe", ".dll", ".so", ".dylib", ".bin", ".png", ".jpg", ".jpeg", ".gif",
		".bmp", ".ico", ".webp", ".mp3", ".mp4", ".wav", ".avi", ".mov", ".zip",
		".gz", ".tar", ".rar", ".7z", ".pdf", ".doc", ".docx", ".xls", ".xlsx",
		".ppt", ".pptx", ".otf", ".woff", ".woff2", ".eot", ".db",
		".sqlite", ".sqlitedb", ".class", ".o", ".a", ".pyc", ".lock", ".ttf":
		return true
	}
	return false
}

// enabledRules 按本次会话的 filter 过滤出要用的规则（避免每文件重复过滤）。
func (s *Session) enabledRules() []Rule {
	out := make([]Rule, 0, len(rulesAll))
	for _, r := range rulesAll {
		if s.filter[r.ID] {
			out = append(out, r)
		}
	}
	return out
}

func (s *Session) finish() {
	s.mu.Lock()
	s.running = false
	s.done = true
	s.mu.Unlock()
}

func (s *Session) addMatch(m Match) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.maxRes > 0 && len(s.matches) >= s.maxRes {
		s.trunc = true
		return
	}
	// 去重：同一文件、同一行、同一个值的密钥只记一次（避免一条 AKIA 被 aws/generic/env
	// 多条规则重复命中，看起来像 bug）。取最先匹配、最具体的规则名。
	key := m.File + "\x00" + itoa(m.Line) + "\x00" + m.Value
	if _, ok := s.seen[key]; ok {
		return
	}
	if len(s.seen) < 50000 {
		s.seen[key] = struct{}{}
	}
	m.Masked = maskValue(m.Value)
	s.matches = append(s.matches, m)
}

func maskValue(v string) string {
	r := []rune(v)
	if len(r) <= 4 {
		return "****"
	}
	if len(r) <= 10 {
		return string(r[:2]) + "****" + string(r[len(r)-1:])
	}
	return string(r[:4]) + "****" + string(r[len(r)-2:])
}

// runScan 遍历目录并扫描，结束后标记 done。
func runScan(s *Session, opts scanOpts) {
	defer s.finish()
	s.mu.Lock()
	s.excludes = opts.excludes
	s.maxSize = opts.maxSize
	s.gitHist = opts.gitHistory
	s.mu.Unlock()

	exSet := map[string]bool{}
	for _, e := range append(defaultExcludes(), opts.excludes...) {
		exSet[e] = true
	}

	fileCh := make(chan string, 1024)
	go func() {
		_ = filepath.Walk(opts.root, func(p string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if s.isCanceled() {
				return filepath.SkipAll
			}
			if info.IsDir() {
				if exSet[info.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			if info.Size() > opts.maxSize {
				return nil
			}
			if isBinaryExt(p) {
				return nil
			}
			s.bumpTotal()
			fileCh <- p
			return nil
		})
		close(fileCh)
	}()

	enabled := s.enabledRules()
	if len(enabled) == 0 {
		enabled = nil
	}
	var wg sync.WaitGroup
	for i := 0; i < opts.workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range fileCh {
				if s.isCanceled() {
					return
				}
				s.scanFile(p, enabled)
			}
		}()
	}
	wg.Wait()

	if opts.gitHistory && !s.isCanceled() {
		s.scanGitHistory(opts.root, enabled)
	}
}

func (s *Session) isCanceled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.canceled
}

func (s *Session) bumpTotal() {
	atomic.AddInt64(&s.totalEst, 1)
}

// scanFile 读文件、跳过二进制、按行与多行两类规则扫描。
func (s *Session) scanFile(path string, enabled []Rule) {
	s.mu.Lock()
	s.current = path
	s.mu.Unlock()

	data, err := os.ReadFile(path)
	if err != nil {
		atomic.AddInt64(&s.scanned, 1)
		return
	}
	if len(data) == 0 {
		atomic.AddInt64(&s.scanned, 1)
		return
	}
	head := data
	if len(head) > 8192 {
		head = head[:8192]
	}
	if bytes.IndexByte(head, 0) != -1 { // 含 NUL = 二进制
		atomic.AddInt64(&s.scanned, 1)
		return
	}
	atomic.AddInt64(&s.scanned, 1)

	for _, r := range enabled {
		if r.MultiLine {
			s.scanMultiline(path, data, r)
		} else {
			s.scanLines(path, data, r)
		}
	}
}

func (s *Session) scanLines(path string, data []byte, r Rule) {
	// 按行扫描：保留行号。
	start := 0
	lineNo := 1
	n := len(data)
	for i := 0; i < n; i++ {
		if data[i] != '\n' {
			continue
		}
		line := string(data[start:i])
		s.matchOnLine(path, line, lineNo, r)
		start = i + 1
		lineNo++
	}
	if start < n {
		s.matchOnLine(path, string(data[start:]), lineNo, r)
	}
}

func (s *Session) matchOnLine(path, line string, lineNo int, r Rule) {
	locs := r.Re.FindAllStringSubmatchIndex(line, -1)
	if len(locs) == 0 {
		return
	}
	for _, loc := range locs {
		var vs, ve int
		if r.ValueGroup > 0 && r.ValueGroup*2+1 < len(loc) {
			vs, ve = loc[r.ValueGroup*2], loc[r.ValueGroup*2+1]
		} else {
			vs, ve = loc[0], loc[1]
		}
		if vs < 0 || ve <= vs {
			continue
		}
		val := line[vs:ve]
		// 去掉值两侧的引号（赋值类规则捕获组常把引号也带进来）
		if len(val) >= 2 && (val[0] == '"' || val[0] == '\'') && val[len(val)-1] == val[0] {
			val = val[1 : len(val)-1]
		}
		if r.Validate != nil && !r.Validate(val) {
			continue
		}
		s.addMatch(Match{
			Rule: r.ID, Name: r.Name, Severity: r.Severity, Category: r.Category,
			File: path, Line: lineNo, Col: vs + 1, Value: val,
		})
	}
}

func (s *Session) scanMultiline(path string, data []byte, r Rule) {
	content := string(data)
	locs := r.Re.FindAllStringSubmatchIndex(content, -1)
	if len(locs) == 0 {
		return
	}
	for _, loc := range locs {
		if loc[1] <= loc[0] {
			continue
		}
		// 行号 = 匹配起点前的换行数 + 1
		lineNo := 1 + strings.Count(content[:loc[0]], "\n")
		val := content[loc[0]:loc[1]]
		if r.Validate != nil && !r.Validate(val) {
			continue
		}
		s.addMatch(Match{
			Rule: r.ID, Name: r.Name, Severity: r.Severity, Category: r.Category,
			File: path, Line: lineNo, Col: 1, Value: val,
		})
	}
}

// scanGitHistory 遍历 git 历史补丁，捕捉已经被提交进仓库的敏感信息。
// 输出无文件/行号语义，统一标记来源为 <git history>。
func (s *Session) scanGitHistory(root string, enabled []Rule) {
	if len(enabled) == 0 {
		return
	}
	cmd := exec.Command("git", "-C", root, "log", "--all", "-p", "--format=", "--no-color")
	out, err := cmd.StdoutPipe()
	if err != nil {
		return
	}
	if err := cmd.Start(); err != nil {
		return
	}
	defer cmd.Wait()
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	lines := 0
	for sc.Scan() {
		lines++
		if lines > 2000000 { // 防止超大仓库跑飞
			break
		}
		line := sc.Text()
		for _, r := range enabled {
			if r.MultiLine {
				continue
			}
			s.matchOnLine("<git history>", line, 0, r)
		}
	}
	s.mu.Lock()
	s.current = "<git history>"
	s.mu.Unlock()
}

// itoa 仅用于去重 key，避免引入 strconv 依赖噪音。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	buf := [12]byte{}
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
