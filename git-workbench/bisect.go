package main

import (
	"errors"
	"strings"
	"sync"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// bisectSession 一次二分定位的会话状态。
// Commits 按「由旧到新」排列，lo 指向已知 good 的下标，hi 指向已知 bad 的下标。
// 当 hi-lo==1 时，Commits[hi] 即第一个引入问题的提交。
type bisectSession struct {
	Path       string             `json:"path"`
	Good       string             `json:"good"`
	Bad        string             `json:"bad"`
	OrigBranch string             `json:"origBranch"`
	OrigHash   string             `json:"origHash"`
	Commits    []plumbing.Hash    `json:"-"`
	Hashes     []string           `json:"hashes"`
	Lo         int                `json:"lo"`
	Hi         int                `json:"hi"`
	Current    int                `json:"current"`
	Skipped    map[int]bool       `json:"-"`
	SkipList   []string           `json:"skipList"`
	Steps      int                `json:"steps"`
	Done       bool               `json:"done"`
	Result     string             `json:"result,omitempty"`
	History    []bisectStepRecord `json:"history"`
}

type bisectStepRecord struct {
	Hash  string `json:"hash"`
	Mark  string `json:"mark"`
	Time  string `json:"time"`
	Title string `json:"title"`
}

var (
	bisectMu sync.Mutex
	bisects  = make(map[string]*bisectSession)
)

func commitBrief(c *object.Commit) map[string]interface{} {
	if c == nil {
		return nil
	}
	return map[string]interface{}{
		"hash":    shortHash(c.Hash),
		"full":    c.Hash.String(),
		"author":  c.Author.Name,
		"time":    c.Author.When.Format("2006-01-02 15:04"),
		"message": firstLine(c.Message),
	}
}

// pickNext 在 (lo, hi) 开区间内取未 skip 的中位下标；全被 skip 返回 -1。
func (s *bisectSession) pickNext() int {
	var cand []int
	for i := s.Lo + 1; i < s.Hi; i++ {
		if !s.Skipped[i] {
			cand = append(cand, i)
		}
	}
	if len(cand) == 0 {
		return -1
	}
	return cand[len(cand)/2]
}

func handleBisectStart(id int64, input map[string]interface{}) {
	path := strFrom(input, "path")
	goodRef := strFrom(input, "good")
	badRef := strFrom(input, "bad")
	if path == "" || goodRef == "" || badRef == "" {
		respondError(id, -32602, "需要 path / good / bad 三个参数")
		return
	}
	repo, err := openRepo(path)
	if err != nil {
		respondError(id, -1, "无法打开仓库: "+err.Error())
		return
	}

	// 二分会 checkout 工作区，必须先保证干净，否则会丢失用户改动
	clean, dirty, err := isWorktreeClean(repo)
	if err != nil {
		respondError(id, -1, "无法读取工作区状态: "+err.Error())
		return
	}
	if !clean {
		respond(id, map[string]interface{}{
			"ok": false, "needClean": true,
			"error": "工作区有未提交改动，请先提交或贮藏",
			"dirty": dirty,
		})
		return
	}

	goodC, err := resolveCommit(repo, goodRef)
	if err != nil {
		respondError(id, -1, err.Error())
		return
	}
	badC, err := resolveCommit(repo, badRef)
	if err != nil {
		respondError(id, -1, err.Error())
		return
	}
	if goodC.Hash == badC.Hash {
		respondError(id, -32602, "good 与 bad 是同一个提交")
		return
	}

	// 取 bad..good 区间（不含 good 及其祖先），再翻转为「由旧到新」
	between, err := commitsBetween(repo, badC, goodC)
	if err != nil {
		respondError(id, -1, err.Error())
		return
	}
	if len(between) == 0 {
		respondError(id, -1, "good 与 bad 之间没有可测试的提交")
		return
	}
	commits := make([]*object.Commit, 0, len(between))
	for i := len(between) - 1; i >= 0; i-- { // between 是由新到旧，翻转
		commits = append(commits, between[i])
	}

	s := &bisectSession{
		Path:    path,
		Good:    shortHash(goodC.Hash),
		Bad:     shortHash(badC.Hash),
		Commits: make([]plumbing.Hash, len(commits)),
		Hashes:  make([]string, len(commits)),
		Lo:      -1, // good 是区间之前的那个提交（虚拟下标 -1）
		Hi:      len(commits) - 1,
		Skipped: make(map[int]bool),
		SkipList: []string{},
		History: []bisectStepRecord{},
	}
	for i, c := range commits {
		s.Commits[i] = c.Hash
		s.Hashes[i] = shortHash(c.Hash)
	}

	// 记住原始位置，reset 时恢复
	if head, err := repo.Head(); err == nil {
		s.OrigHash = head.Hash().String()
		if head.Name().IsBranch() {
			s.OrigBranch = head.Name().Short()
		}
	}

	bisectMu.Lock()
	bisects[path] = s
	bisectMu.Unlock()

	// checkout 第一个待测提交
	if err := bisectCheckout(repo, s); err != nil {
		respondError(id, -1, "checkout 失败: "+err.Error())
		return
	}

	respond(id, bisectSnapshot(repo, s))
}

// bisectCheckout 定位下一个待测提交并 checkout（detached HEAD）。
func bisectCheckout(repo *git.Repository, s *bisectSession) error {
	next := s.pickNext()
	if next < 0 {
		if s.Hi-s.Lo == 1 {
			s.Done = true
			s.Result = s.Hashes[s.Hi]
			return nil
		}
		return errors.New("区间内所有提交都被跳过，无法确定结果")
	}
	wt, err := repo.Worktree()
	if err != nil {
		return err
	}
	if err := wt.Checkout(&git.CheckoutOptions{Hash: s.Commits[next]}); err != nil {
		return err
	}
	s.Current = next
	s.Steps++
	return nil
}

func bisectSnapshot(repo *git.Repository, s *bisectSession) map[string]interface{} {
	out := map[string]interface{}{
		"ok":      true,
		"done":    s.Done,
		"good":    s.Good,
		"bad":     s.Bad,
		"steps":   s.Steps,
		"total":   len(s.Commits),
		"remain":  s.Hi - s.Lo - 1,
		"history": s.History,
		"skipList": s.SkipList,
		"result":  s.Result,
		"path":    s.Path,
		"origBranch": s.OrigBranch,
	}
	if s.Done {
		if c, err := repo.CommitObject(s.Commits[s.Hi]); err == nil {
			out["culprit"] = commitBrief(c)
		}
		out["message"] = "二分完成：以下提交首次引入该问题"
		return out
	}
	if s.Current >= 0 && s.Current < len(s.Commits) {
		if c, err := repo.CommitObject(s.Commits[s.Current]); err == nil {
			out["current"] = commitBrief(c)
			out["currentIndex"] = s.Current
		}
	}
	return out
}

func handleBisectMark(id int64, input map[string]interface{}) {
	path := strFrom(input, "path")
	mark := strings.ToLower(strFrom(input, "mark"))
	if path == "" {
		respondError(id, -32602, "缺少 path")
		return
	}
	bisectMu.Lock()
	s, ok := bisects[path]
	bisectMu.Unlock()
	if !ok {
		respondError(id, -1, "该仓库没有进行中的二分，请先 start")
		return
	}
	if s.Done {
		respondError(id, -1, "二分已完成，请 reset 后重新开始")
		return
	}
	if mark != "good" && mark != "bad" && mark != "skip" {
		respondError(id, -32602, "mark 只能是 good / bad / skip")
		return
	}

	repo, err := openRepo(path)
	if err != nil {
		respondError(id, -1, err.Error())
		return
	}

	cur := s.Current
	if cur < 0 || cur >= len(s.Commits) {
		respondError(id, -1, "当前没有待测提交")
		return
	}
	if c, err := repo.CommitObject(s.Commits[cur]); err == nil {
		s.History = append(s.History, bisectStepRecord{
			Hash:  shortHash(c.Hash),
			Mark:  mark,
			Time:  time.Now().Format("15:04:05"),
			Title: firstLine(c.Message),
		})
	}

	switch mark {
	case "good":
		if cur > s.Lo {
			s.Lo = cur
		}
	case "bad":
		if cur < s.Hi {
			s.Hi = cur
		}
	case "skip":
		s.Skipped[cur] = true
		s.SkipList = append(s.SkipList, s.Hashes[cur])
	}

	// 已收敛
	if s.Hi-s.Lo == 1 {
		s.Done = true
		s.Result = s.Hashes[s.Hi]
		respond(id, bisectSnapshot(repo, s))
		return
	}

	if err := bisectCheckout(repo, s); err != nil {
		// 区间全 skip 等不可继续的情况：保留已有结论
		if !s.Done {
			respondError(id, -1, err.Error())
			return
		}
	}
	respond(id, bisectSnapshot(repo, s))
}

func handleBisectStatus(id int64, input map[string]interface{}) {
	path := strFrom(input, "path")
	repo, err := openRepo(path)
	if err != nil {
		respondError(id, -1, err.Error())
		return
	}
	bisectMu.Lock()
	s, ok := bisects[path]
	bisectMu.Unlock()
	if !ok {
		respond(id, map[string]interface{}{"ok": true, "active": false})
		return
	}
	out := bisectSnapshot(repo, s)
	out["active"] = true
	respond(id, out)
}

func handleBisectReset(id int64, input map[string]interface{}) {
	path := strFrom(input, "path")
	repo, err := openRepo(path)
	if err != nil {
		respondError(id, -1, err.Error())
		return
	}
	bisectMu.Lock()
	s, ok := bisects[path]
	if ok {
		delete(bisects, path)
	}
	bisectMu.Unlock()
	if !ok {
		respond(id, map[string]interface{}{"ok": true, "message": "没有进行中的二分"})
		return
	}

	wt, err := repo.Worktree()
	if err != nil {
		respondError(id, -1, err.Error())
		return
	}
	if s.OrigBranch != "" {
		if err := wt.Checkout(&git.CheckoutOptions{
			Branch: plumbing.NewBranchReferenceName(s.OrigBranch),
		}); err != nil {
			respondError(id, -1, "恢复分支失败: "+err.Error())
			return
		}
	} else if s.OrigHash != "" {
		if err := wt.Checkout(&git.CheckoutOptions{Hash: plumbing.NewHash(s.OrigHash)}); err != nil {
			respondError(id, -1, "恢复提交失败: "+err.Error())
			return
		}
	}
	respond(id, map[string]interface{}{
		"ok": true,
		"message": func() string {
			if s.OrigBranch != "" {
				return "已恢复到分支 " + s.OrigBranch
			}
			return "已恢复到原提交"
		}(),
	})
}
