package main

import (
	"os"
	"path/filepath"
	"strings"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// ---- 冲突标记解析 ----

type conflictBlock struct {
	Index       int      `json:"index"`
	Start       int      `json:"start"` // 起始行（含 <<<<<<<）
	End         int      `json:"end"`   // 结束行（含 >>>>>>>）
	Ours        []string `json:"ours"`
	Theirs      []string `json:"theirs"`
	Base        []string `json:"base"`
	OursLabel   string   `json:"oursLabel"`
	TheirsLabel string   `json:"theirsLabel"`
}

const (
	markOurs   = "<<<<<<<"
	markBase   = "|||||||"
	markSep    = "======="
	markTheirs = ">>>>>>>"
)

// parseConflictBlocks 从带冲突标记的文本中解析出冲突块。
// 同时支持标准两段式（<<< === >>>）与 diff3 三段式（<<< ||| === >>>）。
func parseConflictBlocks(content string) []conflictBlock {
	lines := strings.Split(content, "\n")
	var blocks []conflictBlock
	var cur *conflictBlock
	var section int // 1=ours 2=base 3=theirs
	idx := 0

	flush := func(endLine int) {
		if cur != nil {
			cur.End = endLine
			blocks = append(blocks, *cur)
			cur = nil
		}
	}

	for i, ln := range lines {
		trimmed := ln
		switch {
		case strings.HasPrefix(trimmed, markOurs):
			flush(i - 1)
			idx++
			cur = &conflictBlock{Index: idx, Start: i}
			cur.OursLabel = strings.TrimSpace(strings.TrimPrefix(trimmed, markOurs))
			section = 1
		case strings.HasPrefix(trimmed, markBase) && cur != nil:
			cur.Base = []string{}
			section = 2
		case strings.HasPrefix(trimmed, markSep) && cur != nil:
			cur.Theirs = []string{}
			section = 3
		case strings.HasPrefix(trimmed, markTheirs) && cur != nil:
			cur.TheirsLabel = strings.TrimSpace(strings.TrimPrefix(trimmed, markTheirs))
			flush(i)
			section = 0
		default:
			if cur != nil {
				switch section {
				case 1:
					cur.Ours = append(cur.Ours, ln)
				case 2:
					cur.Base = append(cur.Base, ln)
				case 3:
					cur.Theirs = append(cur.Theirs, ln)
				}
			}
		}
	}
	if cur != nil {
		cur.End = len(lines) - 1
		blocks = append(blocks, *cur)
	}
	return blocks
}

// ---- 命令实现 ----

func conflictFiles(repo *git.Repository) ([]string, error) {
	wt, err := repo.Worktree()
	if err != nil {
		return nil, err
	}
	st, err := wt.Status()
	if err != nil {
		return nil, err
	}
	var out []string
	for f, s := range st {
		if s.Staging == git.UpdatedButUnmerged || s.Worktree == git.UpdatedButUnmerged {
			out = append(out, f)
		}
	}
	return out, nil
}

func handleConflictList(id int64, input map[string]interface{}) {
	path := strFrom(input, "path")
	repo, err := openRepo(path)
	if err != nil {
		respondError(id, -1, "无法打开仓库: "+err.Error())
		return
	}
	files, err := conflictFiles(repo)
	if err != nil {
		respondError(id, -1, err.Error())
		return
	}
	mh, _ := mergingHead(repo)
	out := map[string]interface{}{
		"ok":      true,
		"files":   files,
		"count":   len(files),
		"merging": mh != nil,
	}
	if mh != nil {
		out["mergeHead"] = shortHash(mh.Hash)
		out["mergeSubject"] = firstLine(mh.Message)
	}
	if head, err := repo.Head(); err == nil {
		out["ourRef"] = head.Name().Short()
		out["theirRef"] = func() string {
			if mh != nil {
				return shortHash(mh.Hash)
			}
			return ""
		}()
	}
	respond(id, out)
}

func handleConflictLoad(id int64, input map[string]interface{}) {
	path := strFrom(input, "path")
	file := strFrom(input, "file")
	if path == "" || file == "" {
		respondError(id, -32602, "需要 path / file")
		return
	}
	repo, err := openRepo(path)
	if err != nil {
		respondError(id, -1, err.Error())
		return
	}
	wt, err := repo.Worktree()
	if err != nil {
		respondError(id, -1, err.Error())
		return
	}
	root := worktreeRoot(wt)
	full := filepath.Join(root, file)

	merged := ""
	if b, err := os.ReadFile(full); err == nil {
		merged = string(b)
	} else {
		respondError(id, -1, "读取冲突文件失败: "+err.Error())
		return
	}

	// ours = HEAD，theirs = MERGE_HEAD，base = 两者的 merge-base
	var oursC, theirsC, baseC *object.Commit
	if head, err := repo.Head(); err == nil {
		oursC, _ = repo.CommitObject(head.Hash())
	}
	theirsC, _ = mergingHead(repo)
	if oursC != nil && theirsC != nil {
		if bases, err := oursC.MergeBase(theirsC); err == nil && len(bases) > 0 {
			baseC = bases[0]
		}
	}

	ours, _, _ := fileContentAt(oursC, file)
	theirs, _, _ := fileContentAt(theirsC, file)
	base, _, _ := fileContentAt(baseC, file)
	if base == "" && ours != "" && theirs != "" && ours == theirs {
		base = ours // 无共同祖先时退化，避免误报冲突
	}

	blocks := parseConflictBlocks(merged)
	if blocks == nil {
		blocks = []conflictBlock{}
	}

	respond(id, map[string]interface{}{
		"ok":     true,
		"file":   file,
		"merged": merged,
		"ours":   ours,
		"theirs": theirs,
		"base":   base,
		"blocks": blocks,
		"hasOurs":   oursC != nil,
		"hasTheirs": theirsC != nil,
		"hasBase":   baseC != nil,
		"oursLabel": func() string {
			if head, err := repo.Head(); err == nil {
				return head.Name().Short()
			}
			return "HEAD"
		}(),
		"theirsLabel": func() string {
			if theirsC != nil {
				return shortHash(theirsC.Hash)
			}
			return "MERGE_HEAD"
		}(),
	})
}

func handleConflictResolve(id int64, input map[string]interface{}) {
	path := strFrom(input, "path")
	file := strFrom(input, "file")
	content := strFrom(input, "content")
	action := strings.ToLower(strFrom(input, "action")) // save | takeOurs | takeTheirs
	autoCommit := boolFrom(input, "commit", false)

	if path == "" || file == "" {
		respondError(id, -32602, "需要 path / file")
		return
	}
	repo, err := openRepo(path)
	if err != nil {
		respondError(id, -1, err.Error())
		return
	}
	wt, err := repo.Worktree()
	if err != nil {
		respondError(id, -1, err.Error())
		return
	}

	// 一键采用某一方：直接从对应版本读取内容
	if action == "takeours" || action == "taketheirs" {
		var src *object.Commit
		if action == "takeours" {
			if head, err := repo.Head(); err == nil {
				src, _ = repo.CommitObject(head.Hash())
			}
		} else {
			src, _ = mergingHead(repo)
		}
		if src == nil {
			respondError(id, -1, "无法获取对应版本（仓库可能不在合并状态）")
			return
		}
		c, ok, _ := fileContentAt(src, file)
		if !ok {
			respondError(id, -1, "该版本中不存在文件: "+file)
			return
		}
		content = c
	}
	if content == "" && action == "" {
		respondError(id, -32602, "缺少 content")
		return
	}

	root := worktreeRoot(wt)
	full := filepath.Join(root, file)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		respondError(id, -1, "创建目录失败: "+err.Error())
		return
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		respondError(id, -1, "写入文件失败: "+err.Error())
		return
	}
	if _, err := wt.Add(file); err != nil {
		respondError(id, -1, "git add 失败: "+err.Error())
		return
	}

	remain, _ := conflictFiles(repo)
	out := map[string]interface{}{
		"ok":      true,
		"file":    file,
		"remain":  len(remain),
		"files":   remain,
		"message": "已保存并标记为已解决",
	}

	if autoCommit && len(remain) == 0 {
		msg := strFrom(input, "message")
		if msg == "" {
			msg = "Merge: resolve conflicts"
		}
		if _, err := wt.Commit(msg, &git.CommitOptions{}); err != nil {
			out["commitError"] = err.Error()
		} else {
			out["committed"] = true
			out["message"] = "全部冲突已解决，已提交合并结果"
		}
	}
	respond(id, out)
}
