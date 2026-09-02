package main

import (
	"sort"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

type lineBlame struct {
	No     int    `json:"no"`
	Author string `json:"author"`
	Time   string `json:"time"`
	Hash   string `json:"hash"`
	Text   string `json:"text"`
}

type commitAgg struct {
	Hash    string `json:"hash"`
	Author  string `json:"author"`
	Time    string `json:"time"`
	Message string `json:"message"`
	Lines   int    `json:"lines"`
	First   int    `json:"firstLine"`
	Last    int    `json:"lastLine"`
	Percent float64 `json:"percent"`
}

type authorAgg struct {
	Author  string  `json:"author"`
	Lines   int     `json:"lines"`
	Percent float64 `json:"percent"`
	First   string  `json:"first"`
	Last    string  `json:"last"`
}

func handleTimeline(id int64, input map[string]interface{}) {
	path := strFrom(input, "path")
	file := strFrom(input, "file")
	ref := strFrom(input, "ref")
	if ref == "" {
		ref = "HEAD"
	}
	if path == "" || file == "" {
		respondError(id, -32602, "需要 path / file")
		return
	}
	repo, err := openRepo(path)
	if err != nil {
		respondError(id, -1, "无法打开仓库: "+err.Error())
		return
	}
	c, err := resolveCommit(repo, ref)
	if err != nil {
		respondError(id, -1, err.Error())
		return
	}

	t := startTaskID("tl")
	go runTimeline(t, repo, c, file, input)

	respond(id, map[string]interface{}{
		"ok":     true,
		"taskId": t.ID,
		"file":   file,
		"ref":    shortHash(c.Hash),
	})
}

func runTimeline(t *asyncTask, repo *git.Repository, c *object.Commit, file string, input map[string]interface{}) {
	t.setProgress(5, "正在解析文件历史")

	res, err := git.Blame(c, file)
	if err != nil {
		t.fail(err)
		return
	}
	if t.cancelled() {
		return
	}
	t.setProgress(60, "正在聚合作者与提交")

	start := intFrom(input, "startLine", 0)
	end := intFrom(input, "endLine", 0)
	if start < 1 {
		start = 1
	}
	if end < 1 || end > len(res.Lines) {
		end = len(res.Lines)
	}
	if start > end {
		start, end = end, start
	}

	lines := make([]lineBlame, 0, end-start+1)
	byCommit := make(map[string]*commitAgg)
	byAuthor := make(map[string]*authorAgg)
	total := 0

	for i := start - 1; i < end && i < len(res.Lines); i++ {
		l := res.Lines[i]
		if l == nil {
			continue
		}
		total++
		name := l.AuthorName
		if name == "" {
			name = l.Author
		}
		ts := l.Date.Format("2006-01-02 15:04")
		hash := shortHash(l.Hash)

		lines = append(lines, lineBlame{
			No:     i + 1,
			Author: name,
			Time:   ts,
			Hash:   hash,
			Text:   l.Text,
		})

		full := l.Hash.String()
		if ca, ok := byCommit[full]; ok {
			ca.Lines++
			if i+1 < ca.First {
				ca.First = i + 1
			}
			if i+1 > ca.Last {
				ca.Last = i + 1
			}
		} else {
			byCommit[full] = &commitAgg{
				Hash:  hash,
				Author: name,
				Time:  ts,
				Lines: 1,
				First: i + 1,
				Last:  i + 1,
			}
		}

		if aa, ok := byAuthor[name]; ok {
			aa.Lines++
			if ts < aa.First {
				aa.First = ts
			}
			if ts > aa.Last {
				aa.Last = ts
			}
		} else {
			byAuthor[name] = &authorAgg{Author: name, Lines: 1, First: ts, Last: ts}
		}
	}

	// 补充提交标题（批量查一次，避免重复解析）
	for full, ca := range byCommit {
		if cc, err := repo.CommitObject(plumbing.NewHash(full)); err == nil {
			ca.Message = firstLine(cc.Message)
			ca.Time = cc.Author.When.Format("2006-01-02 15:04")
		}
	}
	if t.cancelled() {
		return
	}
	t.setProgress(90, "整理结果")

	commits := make([]commitAgg, 0, len(byCommit))
	for _, ca := range byCommit {
		ca.Percent = pct(ca.Lines, total)
		commits = append(commits, *ca)
	}
	sort.Slice(commits, func(i, j int) bool {
		if commits[i].Time == commits[j].Time {
			return commits[i].Lines > commits[j].Lines
		}
		return commits[i].Time < commits[j].Time // 由旧到新，便于画时间轴
	})

	authors := make([]authorAgg, 0, len(byAuthor))
	for _, aa := range byAuthor {
		aa.Percent = pct(aa.Lines, total)
		authors = append(authors, *aa)
	}
	sort.Slice(authors, func(i, j int) bool { return authors[i].Lines > authors[j].Lines })

	t.done(map[string]interface{}{
		"file":    file,
		"total":   total,
		"scanned": len(res.Lines),
		"range":   []int{start, end},
		"lines":   lines,
		"commits": commits,
		"authors": authors,
		"ref":     shortHash(c.Hash),
		"refTime": c.Author.When.Format("2006-01-02 15:04"),
		"finishedAt": time.Now().Format("15:04:05"),
	})
}

func pct(part, total int) float64 {
	if total <= 0 {
		return 0
	}
	return float64(part) * 100 / float64(total)
}
