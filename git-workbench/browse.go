package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

type commitInfo struct {
	Hash    string `json:"hash"`
	Author  string `json:"author"`
	Time    string `json:"time"`
	Message string `json:"message"`
}

type fileEntry struct {
	File   string `json:"file"`
	Status string `json:"status"`
}

func statusLabel(st git.StatusCode) string {
	switch st {
	case git.Untracked:
		return "未跟踪"
	case git.Modified:
		return "已修改"
	case git.Added:
		return "新增"
	case git.Deleted:
		return "已删除"
	case git.Renamed:
		return "重命名"
	case git.Copied:
		return "复制"
	case git.UpdatedButUnmerged:
		return "冲突"
	default:
		return "修改"
	}
}

// mergingHead 若仓库正处于合并（冲突）状态，返回 MERGE_HEAD 指向的提交。
func mergingHead(repo *git.Repository) (*object.Commit, error) {
	ref, err := repo.Reference(plumbing.ReferenceName("MERGE_HEAD"), false)
	if err != nil {
		return nil, nil // 不在合并状态
	}
	c, err := repo.CommitObject(ref.Hash())
	if err != nil {
		return nil, nil
	}
	return c, nil
}

func handleRepoOpen(id int64, input map[string]interface{}) {
	path := strFrom(input, "path")
	repo, err := openRepo(path)
	if err != nil {
		respondError(id, -1, "无法打开仓库: "+err.Error())
		return
	}

	limit := intFrom(input, "limit", 30)
	if limit < 1 {
		limit = 30
	}
	if limit > 500 {
		limit = 500
	}

	result := map[string]interface{}{"ok": true, "path": path, "remote": false}

	if head, err := repo.Head(); err == nil {
		result["branch"] = head.Name().Short()
		result["headHash"] = shortHash(head.Hash())
	}

	var commits []commitInfo
	if iter, err := repo.Log(&git.LogOptions{}); err == nil {
		_ = iter.ForEach(func(c *object.Commit) error {
			if len(commits) >= limit {
				return errStop
			}
			commits = append(commits, commitInfo{
				Hash:    shortHash(c.Hash),
				Author:  c.Author.Name,
				Time:    c.Author.When.Format("2006-01-02 15:04"),
				Message: firstLine(c.Message),
			})
			return nil
		})
	}
	result["commits"] = commits

	if wt, err := repo.Worktree(); err == nil {
		if st, err := wt.Status(); err == nil {
			files := make([]fileEntry, 0, len(st))
			for f, s := range st {
				code := s.Staging
				if code == git.Unmodified {
					code = s.Worktree
				}
				files = append(files, fileEntry{File: f, Status: statusLabel(code)})
			}
			result["status"] = files
			result["clean"] = st.IsClean()
			conflicts := 0
			for _, s := range st {
				if s.Staging == git.UpdatedButUnmerged || s.Worktree == git.UpdatedButUnmerged {
					conflicts++
				}
			}
			result["conflictCount"] = conflicts
		}
	}

	if bs, err := repo.Branches(); err == nil {
		var branches []string
		_ = bs.ForEach(func(ref *plumbing.Reference) error {
			branches = append(branches, ref.Name().Short())
			return nil
		})
		result["branches"] = branches
	}

	if tags, err := repo.Tags(); err == nil {
		var tagList []string
		_ = tags.ForEach(func(ref *plumbing.Reference) error {
			tagList = append(tagList, ref.Name().Short())
			return nil
		})
		if len(tagList) > 0 {
			result["tags"] = tagList
		}
	}

	// 合并状态：冲突页需要知道当前是否处于 merge/rebase/cherry-pick
	if mh, err := mergingHead(repo); err == nil && mh != nil {
		result["merging"] = true
		result["mergeHead"] = shortHash(mh.Hash)
		result["mergeSubject"] = firstLine(mh.Message)
	} else {
		result["merging"] = false
	}

	respond(id, result)
}

// ---- 远程仓库（GitHub）----

func systemProxyTransport() *url.URL { return nil }

func newClient(timeout time.Duration) *http.Client {
	sysURL := systemProxyURL()
	proxyFn := http.ProxyFromEnvironment
	if sysURL != nil {
		proxyFn = func(req *http.Request) (*url.URL, error) {
			if u, err := http.ProxyFromEnvironment(req); err == nil && u != nil {
				return u, nil
			}
			return sysURL, nil
		}
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = proxyFn
	return &http.Client{Timeout: timeout, Transport: tr}
}

func getJSON(client *http.Client, u string, out interface{}) error {
	req, err := http.NewRequestWithContext(context.Background(), "GET", u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "QuickDock")
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("http %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return json.Unmarshal(body, out)
}

func handleRepoRemote(id int64, input map[string]interface{}) {
	raw := strFrom(input, "repo")
	repoName := raw
	repoName = strings.TrimPrefix(repoName, "https://github.com/")
	repoName = strings.TrimPrefix(repoName, "http://github.com/")
	repoName = strings.TrimPrefix(repoName, "github.com/")
	repoName = strings.TrimSuffix(repoName, ".git")
	parts := strings.SplitN(repoName, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		respondError(id, -32602, "远程仓库需 owner/repo 形式")
		return
	}
	owner, name := parts[0], strings.TrimSuffix(parts[1], "/")
	client := newClient(15 * time.Second)
	base := "https://api.github.com/repos/" + owner + "/" + name

	result := map[string]interface{}{"ok": true, "repo": owner + "/" + name, "remote": true}

	var info map[string]interface{}
	if getJSON(client, base, &info) == nil {
		result["branch"] = strVal(info["default_branch"])
		result["desc"] = strVal(info["description"])
		if s, ok := info["stargazers_count"].(float64); ok {
			result["stars"] = int64(s)
		}
	}

	var rawCommits []map[string]interface{}
	if getJSON(client, base+"/commits?per_page=30", &rawCommits) == nil {
		var commits []commitInfo
		for _, c := range rawCommits {
			sha, _ := c["sha"].(string)
			commit, _ := c["commit"].(map[string]interface{})
			author, _ := commit["author"].(map[string]interface{})
			commits = append(commits, commitInfo{
				Hash:    shortStr(sha),
				Author:  strVal(author["name"]),
				Time:    strVal(author["date"])[:10],
				Message: firstLine(strVal(commit["message"])),
			})
		}
		result["commits"] = commits
	}

	var rawBranches []map[string]interface{}
	if getJSON(client, base+"/branches", &rawBranches) == nil {
		var branches []string
		for _, b := range rawBranches {
			branches = append(branches, strVal(b["name"]))
		}
		result["branches"] = branches
	}

	respond(id, result)
}

func strVal(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func shortStr(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}
