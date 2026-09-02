package main

import (
	"sort"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

type authorStat struct {
	Name     string  `json:"name"`
	Email    string  `json:"email"`
	Commits  int     `json:"commits"`
	Adds     int     `json:"adds"`
	Dels     int     `json:"dels"`
	Percent  float64 `json:"percent"`
	FirstDay string  `json:"firstDay"`
	LastDay  string  `json:"lastDay"`
}

type fileStat struct {
	Path     string   `json:"path"`
	Changes  int      `json:"changes"`
	Adds     int      `json:"adds"`
	Dels     int      `json:"dels"`
	Authors  []string `json:"authors"`
	LastDay  string   `json:"lastDay"`
	SoleRisk bool     `json:"soleRisk"` // 知识孤岛：仅一人改动过
}

type trendPoint struct {
	Day   string `json:"day"`
	Adds  int    `json:"adds"`
	Dels  int    `json:"dels"`
	Total int    `json:"total"` // 累计代码行数
}

func handleStatsStart(id int64, input map[string]interface{}) {
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
	limit := intFrom(input, "limit", 800)
	limit = clampInt(limit, 50, 5000)

	t := startTaskID("st")
	go runStats(t, repo, limit)

	respond(id, map[string]interface{}{"ok": true, "taskId": t.ID, "limit": limit})
}

func runStats(t *asyncTask, repo *git.Repository, limit int) {
	t.setProgress(3, "读取提交历史")

	iter, err := repo.Log(&git.LogOptions{Order: git.LogOrderCommitterTime})
	if err != nil {
		t.fail(err)
		return
	}

	type commitRec struct {
		c *object.Commit
	}
	var recs []commitRec
	_ = iter.ForEach(func(c *object.Commit) error {
		if len(recs) >= limit {
			return errStop
		}
		recs = append(recs, commitRec{c})
		return nil
	})
	if t.cancelled() {
		return
	}

	total := len(recs)
	if total == 0 {
		t.done(map[string]interface{}{"commits": 0, "message": "仓库没有提交"})
		return
	}
	// LogOrder 为「由新到旧」，翻转为「由旧到新」便于画趋势
	for i, j := 0, total-1; i < j; i, j = i+1, j-1 {
		recs[i], recs[j] = recs[j], recs[i]
	}

	byAuthor := make(map[string]*authorStat)
	byFile := make(map[string]*fileStat)
	heat := make([][]int, 7) // heat[weekday][hour]
	for i := range heat {
		heat[i] = make([]int, 24)
	}
	daily := make(map[string]int)
	var trends []trendPoint
	dayAdds := make(map[string]int)
	dayDels := make(map[string]int)
	var dayOrder []string
	seenDay := make(map[string]bool)

	cumulative := 0
	mergeCount := 0

	for i, r := range recs {
		if i%20 == 0 {
			if t.cancelled() {
				return
			}
			p := 5 + i*85/total
			t.setProgress(p, "分析提交 "+itoa(i+1)+"/"+itoa(total))
		}
		c := r.c
		day := c.Author.When.Format("2006-01-02")
		wd := int(c.Author.When.Weekday())
		heat[wd][c.Author.When.Hour()]++
		daily[day]++
		if !seenDay[day] {
			seenDay[day] = true
			dayOrder = append(dayOrder, day)
		}

		key := c.Author.Name + " <" + c.Author.Email + ">"
		as, ok := byAuthor[key]
		if !ok {
			as = &authorStat{
				Name: c.Author.Name, Email: c.Author.Email,
				FirstDay: day, LastDay: day,
			}
			byAuthor[key] = as
		}
		as.Commits++
		if day < as.FirstDay {
			as.FirstDay = day
		}
		if day > as.LastDay {
			as.LastDay = day
		}

		// 合并提交无法计算有效 diff，跳过（否则会把整棵树算成新增）
		if len(c.ParentHashes) > 1 {
			mergeCount++
			continue
		}
		st, err := c.Stats()
		if err != nil {
			continue
		}
		for _, fs := range st {
			as.Adds += fs.Addition
			as.Dels += fs.Deletion
			cumulative += fs.Addition - fs.Deletion
			dayAdds[day] += fs.Addition
			dayDels[day] += fs.Deletion

			f, ok := byFile[fs.Name]
			if !ok {
				f = &fileStat{Path: fs.Name}
				byFile[fs.Name] = f
			}
			f.Changes++
			f.Adds += fs.Addition
			f.Dels += fs.Deletion
			if !containsStr(f.Authors, c.Author.Name) {
				f.Authors = append(f.Authors, c.Author.Name)
			}
			if day > f.LastDay {
				f.LastDay = day
			}
		}
	}

	if t.cancelled() {
		return
	}
	t.setProgress(92, "汇总指标")

	authors := make([]authorStat, 0, len(byAuthor))
	for _, a := range byAuthor {
		a.Percent = pct(a.Commits, total)
		authors = append(authors, *a)
	}
	sort.Slice(authors, func(i, j int) bool { return authors[i].Commits > authors[j].Commits })

	files := make([]fileStat, 0, len(byFile))
	for _, f := range byFile {
		f.SoleRisk = len(f.Authors) == 1 && f.Changes >= 3
		files = append(files, *f)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Changes > files[j].Changes })
	if len(files) > 100 {
		files = files[:100]
	}

	// 知识孤岛：改动频繁但只有一个人动过
	var islands []fileStat
	for _, f := range files {
		if f.SoleRisk {
			islands = append(islands, f)
		}
	}
	sort.Slice(islands, func(i, j int) bool { return islands[i].Changes > islands[j].Changes })
	if len(islands) > 30 {
		islands = islands[:30]
	}

	// 按天重算累计行数，保证趋势线单调可解释
	running := 0
	for _, d := range dayOrder {
		running += dayAdds[d] - dayDels[d]
		trends = append(trends, trendPoint{Day: d, Adds: dayAdds[d], Dels: dayDels[d], Total: running})
	}

	// 最近 90 天的每日提交数
	sort.Strings(dayOrder)
	var recentDays []map[string]interface{}
	if len(dayOrder) > 90 {
		dayOrder = dayOrder[len(dayOrder)-90:]
	}
	for _, d := range dayOrder {
		recentDays = append(recentDays, map[string]interface{}{"day": d, "count": daily[d]})
	}

	maxHeat := 0
	for _, row := range heat {
		for _, v := range row {
			if v > maxHeat {
				maxHeat = v
			}
		}
	}

	spanDays := ""
	if len(dayOrder) > 0 {
		spanDays = dayOrder[0] + " ~ " + dayOrder[len(dayOrder)-1]
	}

	t.done(map[string]interface{}{
		"commits":    total,
		"mergeCount": mergeCount,
		"authors":    authors,
		"authorCount": len(authors),
		"files":      files,
		"fileCount":  len(byFile),
		"islands":    islands,
		"heatmap":    heat,
		"heatMax":    maxHeat,
		"daily":      recentDays,
		"trends":     trends,
		"span":       spanDays,
		"netLines":   running,
		"finishedAt": time.Now().Format("15:04:05"),
	})
}

func containsStr(arr []string, s string) bool {
	for _, v := range arr {
		if v == s {
			return true
		}
	}
	return false
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
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
