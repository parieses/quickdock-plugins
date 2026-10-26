package main

// 任务会话。覆写是长活（几十秒到几小时），不能挂在一次 plugin.execute 里等——
// 宿主有 20 秒超时。走「start 拿 id → poll 拉进度」的异步范式，和 lan-scanner 一致。

import (
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type taskItem struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
	OK   bool   `json:"ok"`
	Err  string `json:"err"`
	Note string `json:"note"`
}

type task struct {
	ID      string `json:"id"`
	Mode    string `json:"mode"` // shred | wipe-free
	Status  string `json:"-"`    // running | done | canceled | error
	Phase   string `json:"-"`
	Current string `json:"-"`

	TotalBytes int64 `json:"-"`
	DoneBytes  int64 `json:"-"` // 已完成字节（含失败项已写的部分）

	TotalItems int `json:"-"`
	DoneItems  int `json:"-"`
	Failed     int `json:"-"`

	mu        sync.Mutex
	Items     []taskItem
	Notes     []string
	ErrText   string
	StartedAt time.Time
	EndedAt   time.Time
	cancelCh  chan struct{}
	canceled_ int32
}

var (
	tasksMu sync.Mutex
	tasks   = map[string]*task{}
)

func newTask(mode string) *task {
	t := &task{
		ID:        nextTaskID(),
		Mode:      mode,
		Status:    "running",
		StartedAt: time.Now(),
		cancelCh:  make(chan struct{}),
	}
	tasksMu.Lock()
	// 只保留最近 8 个任务：poll 完的结果不必永久驻留
	if len(tasks) >= 8 {
		var oldest *task
		for _, x := range tasks {
			if x.Status == "running" {
				continue
			}
			if oldest == nil || x.StartedAt.Before(oldest.StartedAt) {
				oldest = x
			}
		}
		if oldest != nil {
			delete(tasks, oldest.ID)
		}
	}
	tasks[t.ID] = t
	tasksMu.Unlock()
	return t
}

func getTask(id string) *task {
	tasksMu.Lock()
	defer tasksMu.Unlock()
	return tasks[id]
}

var taskSeq int64

func nextTaskID() string {
	n := atomic.AddInt64(&taskSeq, 1)
	return "sh" + itoa(n) + "-" + itoa(time.Now().Unix()%100000)
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [24]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func (t *task) canceled() bool {
	return atomic.LoadInt32(&t.canceled_) != 0
}

func (t *task) cancel() {
	atomic.StoreInt32(&t.canceled_, 1)
	select {
	case <-t.cancelCh:
	default:
		close(t.cancelCh)
	}
}

func (t *task) setPhase(p string) {
	t.mu.Lock()
	t.Phase = p
	t.mu.Unlock()
}

func (t *task) setCurrent(c string) {
	t.mu.Lock()
	t.Current = c
	t.mu.Unlock()
}

func (t *task) addBytes(n int64) {
	atomic.AddInt64(&t.DoneBytes, n)
}

func (t *task) addItem(it taskItem) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Items = append(t.Items, it)
	t.DoneItems++
	if !it.OK {
		t.Failed++
	}
}

func (t *task) addNote(n string) {
	if strings.TrimSpace(n) == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Notes = append(t.Notes, n)
}

func (t *task) finish(status string) {
	t.mu.Lock()
	t.Status = status
	t.EndedAt = time.Now()
	if status == "done" {
		t.Phase = "完成"
	}
	t.mu.Unlock()
}

// snapshot 给前端的进度快照。items 只回最近 limit 条，避免大目录把 JSON 撑爆。
func (t *task) snapshot(limit int) map[string]interface{} {
	t.mu.Lock()
	defer t.mu.Unlock()

	done := atomic.LoadInt64(&t.DoneBytes)
	total := t.TotalBytes
	pct := 0.0
	if total > 0 {
		pct = float64(done) / float64(total) * 100
		if pct > 100 {
			pct = 100
		}
	}
	elapsed := time.Since(t.StartedAt).Seconds()
	speed := 0.0
	if elapsed > 0.2 {
		speed = float64(done) / elapsed
	}
	eta := 0.0
	if speed > 0 && total > done {
		eta = float64(total-done) / speed
	}

	items := t.Items
	if limit > 0 && len(items) > limit {
		items = items[len(items)-limit:]
	}

	return map[string]interface{}{
		"id":         t.ID,
		"mode":       t.Mode,
		"status":     t.Status,
		"phase":      t.Phase,
		"current":    t.Current,
		"doneBytes":  done,
		"totalBytes": total,
		"percent":    pct,
		"speed":      speed,
		"etaSec":     eta,
		"elapsedSec": elapsed,
		"totalItems": t.TotalItems,
		"doneItems":  t.DoneItems,
		"failed":     t.Failed,
		"items":      items,
		"notes":      t.Notes,
		"error":      t.ErrText,
	}
}
