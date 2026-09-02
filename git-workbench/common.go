package main

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

var (
	errStop  = errors.New("stop")  // 整体终止遍历
	errPrune = errors.New("prune") // 剪枝：不展开该提交的父节点
)

// ---- 仓库打开 / 引用解析 ----

// openRepo 打开仓库。DetectDotGit 允许传入仓库的子目录（向上查找 .git）。
func openRepo(path string) (*git.Repository, error) {
	if path == "" {
		return nil, errors.New("路径为空")
	}
	return git.PlainOpenWithOptions(path, &git.PlainOpenOptions{DetectDotGit: true})
}

// resolveCommit 把分支名 / 标签 / 短 hash / 完整 hash 解析为 commit。
func resolveCommit(repo *git.Repository, ref string) (*object.Commit, error) {
	if ref == "" {
		ref = "HEAD"
	}
	h, err := repo.ResolveRevision(plumbing.Revision(ref))
	if err == nil {
		return repo.CommitObject(*h)
	}
	// ResolveRevision 对某些形式（如 remotes/origin/x）支持有限，退回到引用遍历
	matches := make([]plumbing.Hash, 0, 2)
	refs, rerr := repo.References()
	if rerr == nil {
		_ = refs.ForEach(func(r *plumbing.Reference) error {
			name := r.Name().String()
			if name == "refs/heads/"+ref || name == "refs/tags/"+ref ||
				name == "refs/remotes/"+ref || strings.HasSuffix(name, "/"+ref) {
				matches = append(matches, r.Hash())
			}
			return nil
		})
	}
	for _, m := range matches {
		if c, cerr := repo.CommitObject(m); cerr == nil {
			return c, nil
		}
	}
	// 最后尝试按前缀匹配完整 hash
	if len(ref) >= 4 {
		if all, cerr := repo.CommitObjects(); cerr == nil {
			var found *object.Commit
			_ = all.ForEach(func(c *object.Commit) error {
				if strings.HasPrefix(c.Hash.String(), ref) {
					found = c
					return errStop
				}
				return nil
			})
			if found != nil {
				return found, nil
			}
		}
	}
	return nil, fmt.Errorf("无法解析提交 %q: %v", ref, err)
}

// fileContentAt 从某次提交的 tree 中读取文件内容（文件不存在返回空串）。
func fileContentAt(c *object.Commit, path string) (string, bool, error) {
	if c == nil || path == "" {
		return "", false, nil
	}
	f, err := c.File(path)
	if err != nil {
		return "", false, nil
	}
	content, err := f.Contents()
	if err != nil {
		return "", false, err
	}
	return content, true, nil
}

// worktreeRoot 取工作区根目录的绝对路径。
// wt.Filesystem 是字段而非方法，且其具体类型（osfs.BoundOS）才提供 Root()，
// 这里用接口断言安全取值，取不到时返回空串由调用方报错。
func worktreeRoot(wt *git.Worktree) string {
	if f, ok := wt.Filesystem.(interface{ Root() string }); ok {
		return f.Root()
	}
	return ""
}

// isWorktreeClean 判断工作区是否干净（无未提交改动）。破坏性操作前必须检查。
func isWorktreeClean(repo *git.Repository) (bool, []string, error) {
	wt, err := repo.Worktree()
	if err != nil {
		return false, nil, err
	}
	st, err := wt.Status()
	if err != nil {
		return false, nil, err
	}
	if st.IsClean() {
		return true, nil, nil
	}
	dirty := make([]string, 0, len(st))
	for f, s := range st {
		if s.Staging != git.Unmodified || s.Worktree != git.Unmodified {
			dirty = append(dirty, f)
		}
	}
	return false, dirty, nil
}

func shortHash(h plumbing.Hash) string {
	s := h.String()
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
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

// ---- 异步任务（带进度与取消）----

type asyncTask struct {
	ID       string                 `json:"id"`
	Status   string                 `json:"status"` // running | done | error | cancelled
	Progress int                    `json:"progress"`
	Message  string                 `json:"message"`
	Result   map[string]interface{} `json:"result,omitempty"`
	Error    string                 `json:"error,omitempty"`
	cancel   chan struct{}
	finished time.Time
}

var (
	tasksMu sync.Mutex
	tasks   = make(map[string]*asyncTask)
	taskSeq int64
)

func startTaskID(prefix string) *asyncTask {
	tasksMu.Lock()
	defer tasksMu.Unlock()
	taskSeq++
	t := &asyncTask{
		ID:      fmt.Sprintf("%s-%d", prefix, taskSeq),
		Status:  "running",
		cancel:  make(chan struct{}),
	}
	tasks[t.ID] = t
	return t
}

func getTask(id string) (*asyncTask, bool) {
	tasksMu.Lock()
	defer tasksMu.Unlock()
	t, ok := tasks[id]
	return t, ok
}

func (t *asyncTask) setProgress(p int, msg string) {
	tasksMu.Lock()
	t.Progress = clampInt(p, 0, 100)
	t.Message = msg
	tasksMu.Unlock()
}

func (t *asyncTask) cancelled() bool {
	select {
	case <-t.cancel:
		return true
	default:
		return false
	}
}

func (t *asyncTask) done(result map[string]interface{}) {
	tasksMu.Lock()
	t.Status = "done"
	t.Progress = 100
	t.Result = result
	t.finished = time.Now()
	tasksMu.Unlock()
}

func (t *asyncTask) fail(err error) {
	tasksMu.Lock()
	t.Status = "error"
	t.Error = err.Error()
	t.finished = time.Now()
	tasksMu.Unlock()
}

func handleTaskStatus(id int64, input map[string]interface{}) {
	taskID := strFrom(input, "taskId")
	if taskID == "" {
		taskID = strFrom(input, "id")
	}
	t, ok := getTask(taskID)
	if !ok {
		respond(id, map[string]interface{}{"status": "missing", "taskId": taskID})
		return
	}
	tasksMu.Lock()
	resp := map[string]interface{}{
		"status":   t.Status,
		"taskId":   t.ID,
		"progress": t.Progress,
		"message":  t.Message,
	}
	if t.Error != "" {
		resp["error"] = t.Error
	}
	if t.Result != nil {
		resp["result"] = t.Result
	}
	tasksMu.Unlock()
	respond(id, resp)
}

func handleTaskCancel(id int64, input map[string]interface{}) {
	taskID := strFrom(input, "taskId")
	if taskID == "" {
		taskID = strFrom(input, "id")
	}
	t, ok := getTask(taskID)
	if !ok {
		respondError(id, -1, "任务不存在: "+taskID)
		return
	}
	close(t.cancel)
	tasksMu.Lock()
	t.Status = "cancelled"
	t.finished = time.Now()
	tasksMu.Unlock()
	respond(id, map[string]interface{}{"ok": true, "taskId": taskID})
}

// ---- 线性化提交区间（bisect 用）----

// commitsBetween 返回 newC 可达、但 oldC 不可达的提交集合（即 oldC..newC 区间的补集），
// 顺序为「由新到旧」。bisect 用它确定待测区间。
func commitsBetween(repo *git.Repository, newC, oldC *object.Commit) ([]*object.Commit, error) {
	if newC == nil || oldC == nil {
		return nil, errors.New("需要两个提交")
	}
	// oldC 及其全部祖先都不在待测区间内
	exclude := make(map[plumbing.Hash]bool)
	if err := walkAncestors(oldC, func(c *object.Commit) error {
		exclude[c.Hash] = true
		return nil
	}); err != nil {
		return nil, err
	}
	if exclude[newC.Hash] {
		return nil, errors.New("good 提交是 bad 提交的祖先，请检查方向（good 应该更旧）")
	}

	var out []*object.Commit
	// 遇到 exclude 中的提交即剪枝：它的祖先必然也在 exclude 内，无需深入
	err := walkAncestors(newC, func(c *object.Commit) error {
		if exclude[c.Hash] {
			return errPrune
		}
		out = append(out, c)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// walkAncestors 深度优先遍历提交及其全部祖先。
// 回调返回 errPrune 表示不再展开该提交的父节点（剪枝）；返回 errStop 表示整体终止。
func walkAncestors(start *object.Commit, fn func(*object.Commit) error) error {
	seen := make(map[plumbing.Hash]bool)
	stack := []*object.Commit{start}
	for len(stack) > 0 {
		n := len(stack) - 1
		c := stack[n]
		stack = stack[:n]
		if c == nil || seen[c.Hash] {
			continue
		}
		seen[c.Hash] = true
		if err := fn(c); err != nil {
			if errors.Is(err, errPrune) {
				continue // 不展开父节点，继续处理栈中其余分支
			}
			if errors.Is(err, errStop) {
				return nil
			}
			return err
		}
		// 关键：Parents() 每次调用都会新建迭代器，必须复用同一个 iter
		iter := c.Parents()
		for {
			pc, err := iter.Next()
			if err != nil {
				break
			}
			stack = append(stack, pc)
		}
	}
	return nil
}
