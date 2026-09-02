package main

import (
	"errors"
	"sort"
	"strings"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage"
)

// rewritePlan 一次历史改写的执行计划。Preview 与 Apply 共用，保证"所见即所改"。
type rewritePlan struct {
	Mode      string             `json:"mode"` // author | deleteFile
	OldEmail  string             `json:"oldEmail,omitempty"`
	NewName   string             `json:"newName,omitempty"`
	NewEmail  string             `json:"newEmail,omitempty"`
	Targets   []string           `json:"targets,omitempty"`
	Commits   []rewriteCommitHit `json:"commits"`
	Total     int                `json:"total"`
	Affected  int                `json:"affected"`
	Refs      []string           `json:"refs"`
	Branch    string             `json:"branch"`
}

type rewriteCommitHit struct {
	Hash     string   `json:"hash"`
	Author   string   `json:"author"`
	Email    string   `json:"email"`
	Time     string   `json:"time"`
	Message  string   `json:"message"`
	AuthorChanged  bool     `json:"authorChanged"`
	RemovedFiles    []string `json:"removedFiles,omitempty"`
}

// ---- 树操作：从 tree 中删除指定路径 ----

func decodeTree(st storage.Storer, h plumbing.Hash) (*object.Tree, error) {
	o, err := st.EncodedObject(plumbing.TreeObject, h)
	if err != nil {
		return nil, err
	}
	return object.DecodeTree(st, o)
}

// removePaths 从以 th 为根的树里删除 targets（相对该树的路径），返回新 tree hash 与是否发生变化。
// 若整棵树被清空，返回 ZeroHash（调用方据此删除该子目录 entry）。
func removePaths(st storage.Storer, th plumbing.Hash, targets map[string]bool) (plumbing.Hash, bool, error) {
	tree, err := decodeTree(st, th)
	if err != nil {
		return th, false, err
	}
	direct := make(map[string]bool)
	sub := make(map[string]map[string]bool)
	for p := range targets {
		p = strings.Trim(p, "/")
		if p == "" {
			continue
		}
		i := strings.IndexByte(p, '/')
		if i < 0 {
			direct[p] = true
		} else {
			dir := p[:i]
			if sub[dir] == nil {
				sub[dir] = make(map[string]bool)
			}
			sub[dir][p[i+1:]] = true
		}
	}

	changed := false
	entries := make([]object.TreeEntry, 0, len(tree.Entries))
	for _, e := range tree.Entries {
		if direct[e.Name] {
			changed = true
			continue
		}
		if childTargets, ok := sub[e.Name]; ok && e.Mode == filemode.Dir {
			nh, ch, err := removePaths(st, e.Hash, childTargets)
			if err != nil {
				return th, false, err
			}
			if ch {
				changed = true
				if nh.IsZero() {
					continue // 子目录已空，删除该 entry
				}
				e.Hash = nh
			}
		}
		entries = append(entries, e)
	}
	if !changed {
		return th, false, nil
	}
	if len(entries) == 0 {
		return plumbing.ZeroHash, true, nil
	}
	tree.Entries = entries
	mem := &plumbing.MemoryObject{}
	mem.SetType(plumbing.TreeObject)
	if err := tree.Encode(mem); err != nil {
		return th, false, err
	}
	nh := mem.Hash()
	// 关键：改写后的树可能与历史中已有的树完全一样（例如删掉某个提交唯一新增的文件后，
	// 该提交的树退化成与父提交相同）。此时对象已存在于 .git/objects 且文件为只读，
	// 直接 SetEncodedObject 在 Windows 上会 "Access is denied"，必须先查重复用。
	if _, err := st.EncodedObject(plumbing.TreeObject, nh); err == nil {
		return nh, true, nil
	}
	if _, err := st.SetEncodedObject(mem); err != nil {
		return th, false, err
	}
	return nh, true, nil
}

// ---- 计划计算 ----

func buildPlan(repo *git.Repository, input map[string]interface{}) (*rewritePlan, error) {
	mode := strings.ToLower(strFrom(input, "mode"))
	if mode != "author" && mode != "deletefile" {
		return nil, errors.New("mode 只能是 author 或 deleteFile")
	}
	plan := &rewritePlan{
		Mode:     mode,
		OldEmail: strFrom(input, "oldEmail"),
		NewName:  strFrom(input, "newName"),
		NewEmail: strFrom(input, "newEmail"),
		Targets:  strSliceFrom(input, "targets"),
		Commits:  []rewriteCommitHit{},
	}
	if mode == "author" && plan.OldEmail == "" && plan.NewName == "" && plan.NewEmail == "" {
		return nil, errors.New("author 模式需提供 oldEmail 或 newName/newEmail")
	}
	if mode == "deletefile" && len(plan.Targets) == 0 {
		return nil, errors.New("deleteFile 模式需提供 targets 文件列表")
	}

	if head, err := repo.Head(); err == nil && head.Name().IsBranch() {
		plan.Branch = head.Name().Short()
	}

	iter, err := repo.Log(&git.LogOptions{Order: git.LogOrderCommitterTime})
	if err != nil {
		return nil, err
	}
	var recs []*object.Commit
	_ = iter.ForEach(func(c *object.Commit) error {
		if len(recs) >= 5000 {
			return errStop
		}
		recs = append(recs, c)
		return nil
	})
	// 翻转为「由旧到新」，保证重写时 parent 已处理完
	sort.SliceStable(recs, func(i, j int) bool {
		return recs[i].Author.When.Before(recs[j].Author.When)
	})

	targetSet := make(map[string]bool)
	for _, t := range plan.Targets {
		targetSet[strings.Trim(t, "/")] = true
	}

	for _, c := range recs {
		hit := rewriteCommitHit{
			Hash:    shortHash(c.Hash),
			Author:  c.Author.Name,
			Email:   c.Author.Email,
			Time:    c.Author.When.Format("2006-01-02 15:04"),
			Message: firstLine(c.Message),
		}
		if mode == "author" {
			if plan.OldEmail == "" || strings.EqualFold(strings.TrimSpace(c.Author.Email), strings.TrimSpace(plan.OldEmail)) {
				hit.AuthorChanged = true
			}
		} else {
			// 统计该提交中实际包含的目标文件
			var found []string
			if tree, err := c.Tree(); err == nil {
				for t := range targetSet {
					if _, err := tree.FindEntry(t); err == nil {
						found = append(found, t)
					} else if f, err := c.File(t); err == nil && f != nil {
						found = append(found, t)
					}
				}
			}
			sort.Strings(found)
			if len(found) > 0 {
				hit.RemovedFiles = found
			}
		}
		if hit.AuthorChanged || len(hit.RemovedFiles) > 0 {
			plan.Affected++
		}
		plan.Commits = append(plan.Commits, hit)
	}
	plan.Total = len(recs)

	branches, err := repo.Branches()
	if err == nil {
		_ = branches.ForEach(func(r *plumbing.Reference) error {
			plan.Refs = append(plan.Refs, r.Name().Short())
			return nil
		})
	}
	return plan, nil
}

func handleRewritePreview(id int64, input map[string]interface{}) {
	path := strFrom(input, "path")
	if path == "" {
		respondError(id, -32602, "缺少 path")
		return
	}
	repo, err := openRepo(path)
	if err != nil {
		respondError(id, -1, "无法打开仓库: "+err.Error())
		return
	}
	plan, err := buildPlan(repo, input)
	if err != nil {
		respondError(id, -1, err.Error())
		return
	}
	// 预览时一并报告工作区状态，apply 前会再次校验
	clean, dirty, _ := isWorktreeClean(repo)
	respond(id, map[string]interface{}{
		"ok": true, "plan": plan, "clean": clean,
		"dirty": func() []string {
			if len(dirty) > 20 {
				return dirty[:20]
			}
			return dirty
		}(),
	})
}

func handleRewriteApply(id int64, input map[string]interface{}) {
	path := strFrom(input, "path")
	if path == "" {
		respondError(id, -32602, "缺少 path")
		return
	}
	if !boolFrom(input, "confirm", false) {
		respondError(id, -32602, "危险操作：需要 confirm=true 才会执行")
		return
	}
	repo, err := openRepo(path)
	if err != nil {
		respondError(id, -1, "无法打开仓库: "+err.Error())
		return
	}
	clean, dirty, err := isWorktreeClean(repo)
	if err != nil {
		respondError(id, -1, err.Error())
		return
	}
	if !clean {
		respondError(id, -1, "工作区有未提交改动，请先提交或贮藏（共 "+itoa(len(dirty))+" 个文件）")
		return
	}

	mode := strings.ToLower(strFrom(input, "mode"))
	oldEmail := strings.TrimSpace(strFrom(input, "oldEmail"))
	newName := strFrom(input, "newName")
	newEmail := strFrom(input, "newEmail")
	targets := strSliceFrom(input, "targets")

	head, err := repo.Head()
	if err != nil {
		respondError(id, -1, "无法读取 HEAD: "+err.Error())
		return
	}
	backupRef := plumbing.NewReferenceFromStrings("refs/rewrite-backup/"+time.Now().Format("20060102150405"), head.Hash().String())
	if err := repo.Storer.SetReference(backupRef); err != nil {
		respondError(id, -1, "创建备份引用失败: "+err.Error())
		return
	}

	iter, err := repo.Log(&git.LogOptions{Order: git.LogOrderCommitterTime})
	if err != nil {
		respondError(id, -1, err.Error())
		return
	}
	var recs []*object.Commit
	_ = iter.ForEach(func(c *object.Commit) error {
		if len(recs) >= 5000 {
			return errStop
		}
		recs = append(recs, c)
		return nil
	})
	sort.SliceStable(recs, func(i, j int) bool {
		return recs[i].Author.When.Before(recs[j].Author.When)
	})

	targetSet := make(map[string]bool)
	for _, t := range targets {
		targetSet[strings.Trim(t, "/")] = true
	}

	mapping := make(map[plumbing.Hash]plumbing.Hash, len(recs))
	st := repo.Storer
	changedCommits := 0
	removedTotal := 0

	for _, c := range recs {
		treeHash := c.TreeHash
		treeChanged := false

		if mode == "deletefile" && len(targetSet) > 0 {
			nh, ch, err := removePaths(st, c.TreeHash, targetSet)
			if err != nil {
				respondError(id, -1, "改写树失败("+shortHash(c.Hash)+"): "+err.Error())
				return
			}
			if ch {
				treeChanged = true
				treeHash = nh
				removedTotal++
			}
		}

		newAuthor := c.Author
		newCommitter := c.Committer
		authorChanged := false
		if mode == "author" {
			if oldEmail == "" || strings.EqualFold(strings.TrimSpace(c.Author.Email), oldEmail) {
				if newName != "" {
					newAuthor.Name = newName
					newCommitter.Name = newName
				}
				if newEmail != "" {
					newAuthor.Email = newEmail
					newCommitter.Email = newEmail
				}
				authorChanged = true
			}
		}

		newParents := make([]plumbing.Hash, 0, len(c.ParentHashes))
		for _, p := range c.ParentHashes {
			if np, ok := mapping[p]; ok {
				newParents = append(newParents, np)
			} else {
				newParents = append(newParents, p) // 未被重写的祖先（超出 5000 上限）
			}
		}

		if !treeChanged && !authorChanged {
			mapping[c.Hash] = c.Hash
			continue
		}

		nc := &object.Commit{
			Author:       newAuthor,
			Committer:    newCommitter,
			Message:      c.Message,
			TreeHash:     treeHash,
			ParentHashes: newParents,
		}
		mem := &plumbing.MemoryObject{}
		mem.SetType(plumbing.CommitObject)
		if err := nc.Encode(mem); err != nil {
			respondError(id, -1, "编码提交失败: "+err.Error())
			return
		}
		nh := mem.Hash()
		// 与树同理：对象可能已存在（只读文件），先查重再写入，避免 Windows 下的 Access denied
		if _, err := st.EncodedObject(plumbing.CommitObject, nh); err != nil {
			if _, err := st.SetEncodedObject(mem); err != nil {
				respondError(id, -1, "写入提交失败: "+err.Error())
				return
			}
		}
		mapping[c.Hash] = nh
		changedCommits++
	}

	// 更新分支引用
	updatedRefs := []string{}
	branches, err := repo.Branches()
	if err == nil {
		var updates []*plumbing.Reference
		_ = branches.ForEach(func(r *plumbing.Reference) error {
			old := r.Hash()
			if nh, ok := mapping[old]; ok && nh != old {
				updates = append(updates, plumbing.NewHashReference(r.Name(), nh))
			}
			return nil
		})
		for _, u := range updates {
			if err := repo.Storer.SetReference(u); err == nil {
				updatedRefs = append(updatedRefs, u.Name().Short())
			}
		}
	}

	// 让工作区与新的 HEAD 一致
	if head.Name().IsBranch() {
		wt, err := repo.Worktree()
		if err == nil {
			_ = wt.Reset(&git.ResetOptions{Mode: git.HardReset})
		}
	}

	respond(id, map[string]interface{}{
		"ok":             true,
		"changed":        changedCommits,
		"total":          len(recs),
		"removedTrees":   removedTotal,
		"updatedRefs":    updatedRefs,
		"backupRef":      backupRef.Name().String(),
		"backupCommit":   shortHash(head.Hash()),
		"finishedAt":     time.Now().Format("15:04:05"),
	})
}
