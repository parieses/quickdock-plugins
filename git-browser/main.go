// Git Browser - 只读浏览 Git 仓库（本地 + 远程公开仓库）
// JSON-RPC 2.0 over stdin/stdout (native 插件协议)
// 命令：
//   open        本地仓库：HEAD 分支、最近提交、工作区状态、本地分支
//   remote      owner/repo：GitHub API 查默认分支、提交、分支、描述、star
//   task-status 异步任务轮询
//   选目录：前端走宿主原生对话框 qdPickFolder（Wails Dialog 目录模式），不再在插件内 spawn PowerShell

package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"golang.org/x/sys/windows/registry"
)

var errStop = errors.New("stop")

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
	default:
		return "修改"
	}
}

// ---- 代理（GitHub API 国内需走系统代理）----

func systemProxyURL() *url.URL {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Internet Settings`, registry.QUERY_VALUE)
	if err != nil {
		return nil
	}
	defer k.Close()
	enable, _, err := k.GetIntegerValue("ProxyEnable")
	if err != nil || enable == 0 {
		return nil
	}
	srv, _, err := k.GetStringValue("ProxyServer")
	if err != nil || strings.TrimSpace(srv) == "" {
		return nil
	}
	for _, part := range strings.Split(srv, ";") {
		p := strings.TrimSpace(part)
		if strings.HasPrefix(p, "http=") || strings.HasPrefix(p, "https=") {
			p = p[strings.Index(p, "=")+1:]
		}
		if p == "" {
			continue
		}
		if !strings.Contains(p, "://") {
			p = "http://" + p
		}
		if u, err := url.Parse(p); err == nil && u.Host != "" {
			return u
		}
	}
	return nil
}

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

func getJSON(client *http.Client, url string, out interface{}) error {
	req, err := http.NewRequest("GET", url, nil)
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

// ---- 异步任务 ----

type asyncTask struct {
	ID       string                 `json:"id"`
	Status   string                 `json:"status"`
	Result   map[string]interface{} `json:"result,omitempty"`
	Error    string                 `json:"error,omitempty"`
	finished time.Time
}

var (
	tasksMu sync.Mutex
	tasks   = make(map[string]*asyncTask)
	taskSeq int64
)

func startTask() *asyncTask {
	tasksMu.Lock()
	defer tasksMu.Unlock()
	taskSeq++
	t := &asyncTask{ID: fmt.Sprintf("gb-%d", taskSeq), Status: "running"}
	tasks[t.ID] = t
	return t
}

func getTask(id string) (*asyncTask, bool) {
	tasksMu.Lock()
	defer tasksMu.Unlock()
	t, ok := tasks[id]
	return t, ok
}

func finishTask(t *asyncTask, result map[string]interface{}, err error) {
	tasksMu.Lock()
	defer tasksMu.Unlock()
	t.finished = time.Now()
	if err != nil {
		t.Status = "error"
		t.Error = err.Error()
	} else {
		t.Status = "done"
		t.Result = result
	}
}

// ---- JSON-RPC ----

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type executeParams struct {
	Command string                 `json:"command"`
	Input   map[string]interface{} `json:"input"`
}

func strFrom(input map[string]interface{}, key string) string {
	if v, ok := input[key].(string); ok {
		return v
	}
	return ""
}

func respond(id int64, result interface{}) {
	out, _ := json.Marshal(map[string]interface{}{"jsonrpc": "2.0", "id": id, "result": result})
	fmt.Println(string(out))
}

func respondError(id int64, code int, msg string) {
	out, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0", "id": id,
		"error": map[string]interface{}{"code": code, "message": msg},
	})
	fmt.Println(string(out))
}

// ---- 命令处理 ----

func handleOpen(id int64, input map[string]interface{}) {
	path := strFrom(input, "path")
	repo, err := git.PlainOpen(path)
	if err != nil {
		respondError(id, -1, "无法打开仓库: "+err.Error())
		return
	}
	result := map[string]interface{}{"ok": true, "path": path, "remote": false}

	if head, err := repo.Head(); err == nil {
		result["branch"] = head.Name().Short()
		result["headHash"] = head.Hash().String()[:8]
	}

	var commits []commitInfo
	if iter, err := repo.Log(&git.LogOptions{}); err == nil {
		_ = iter.ForEach(func(c *object.Commit) error {
			if len(commits) >= 30 {
				return errStop
			}
			msg := strings.TrimSpace(c.Message)
			if idx := strings.IndexByte(msg, '\n'); idx >= 0 {
				msg = msg[:idx]
			}
			commits = append(commits, commitInfo{
				Hash:    c.Hash.String()[:8],
				Author:  c.Author.Name,
				Time:    c.Author.When.Format("2006-01-02 15:04"),
				Message: msg,
			})
			return nil
		})
	}
	result["commits"] = commits

	if wt, err := repo.Worktree(); err == nil {
		if st, err := wt.Status(); err == nil {
			var files []fileEntry
			for f, s := range st {
				st := s.Staging
				if st == git.Unmodified {
					st = s.Worktree
				}
				files = append(files, fileEntry{File: f, Status: statusLabel(st)})
			}
			result["status"] = files
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

	respond(id, result)
}

func handleRemote(id int64, input map[string]interface{}) {
	repo := strings.TrimPrefix(strings.TrimSpace(strFrom(input, "repo")), "github.com/")
	parts := strings.SplitN(repo, "/", 2)
	if len(parts) != 2 {
		respondError(id, -32602, "远程仓库需 owner/repo 形式")
		return
	}
	owner, name := parts[0], parts[1]
	client := newClient(10 * time.Second)
	base := "https://api.github.com/repos/" + owner + "/" + name

	result := map[string]interface{}{"ok": true, "repo": owner + "/" + name, "remote": true}

	// 仓库信息
	var info map[string]interface{}
	if getJSON(client, base, &info) == nil {
		result["branch"] = str(info["default_branch"])
		result["desc"] = str(info["description"])
		if s, ok := info["stargazers_count"].(float64); ok {
			result["stars"] = int64(s)
		}
	}

	// 提交
	var rawCommits []map[string]interface{}
	if getJSON(client, base+"/commits?per_page=30", &rawCommits) == nil {
		var commits []commitInfo
		for _, c := range rawCommits {
			sha, _ := c["sha"].(string)
			commit, _ := c["commit"].(map[string]interface{})
			author, _ := commit["author"].(map[string]interface{})
			msg := str(commit["message"])
			if idx := strings.IndexByte(msg, '\n'); idx >= 0 {
				msg = msg[:idx]
			}
			commits = append(commits, commitInfo{
				Hash:    short(sha),
				Author:  str(author["name"]),
				Time:    str(author["date"])[:10],
				Message: msg,
			})
		}
		result["commits"] = commits
	}

	// 分支
	var rawBranches []map[string]interface{}
	if getJSON(client, base+"/branches", &rawBranches) == nil {
		var branches []string
		for _, b := range rawBranches {
			branches = append(branches, str(b["name"]))
		}
		result["branches"] = branches
	}

	respond(id, result)
}

func handleTaskStatus(id int64, input map[string]interface{}) {
	taskID := strFrom(input, "taskId")
	t, ok := getTask(taskID)
	if !ok {
		respond(id, map[string]interface{}{"status": "missing", "taskId": taskID})
		return
	}
	resp := map[string]interface{}{"status": t.Status, "taskId": t.ID}
	if t.Error != "" {
		resp["error"] = t.Error
	}
	if t.Result != nil {
		resp["result"] = t.Result
	}
	respond(id, resp)
}


func short(s string) string {
	if len(s) >= 8 {
		return s[:8]
	}
	return s
}

func str(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// ---- dispatch ----

func dispatch(raw string) {
	var req rpcRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		respondError(0, -32700, "parse error: "+err.Error())
		return
	}
	switch req.Method {
	case "initialize":
		respond(req.ID, map[string]interface{}{"status": "ready", "name": "QuickDock Git Browser"})
	case "host.ping":
		respond(req.ID, map[string]interface{}{"pong": true})
	case "plugin.execute":
		var params executeParams
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &params); err != nil {
				respondError(req.ID, -32602, "invalid params: "+err.Error())
				return
			}
		}
		switch strings.ToLower(strings.TrimSpace(params.Command)) {
		case "open":
			handleOpen(req.ID, params.Input)
		case "remote":
			handleRemote(req.ID, params.Input)
		case "task-status":
			handleTaskStatus(req.ID, params.Input)
		default:
			respondError(req.ID, -32601, "unknown command: "+params.Command)
		}
	default:
		respondError(req.ID, -32601, "unknown method: "+req.Method)
	}
}

func main() {
	reader := bufio.NewReader(os.Stdin)
	var wg sync.WaitGroup
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		data := strings.TrimSpace(line)
		if data == "" {
			continue
		}
		// 每个请求独立 goroutine：任何 handler 都不阻塞 stdin 读循环，
		// host.ping 永远秒回，杜绝"同步 handler 卡住 → 健康检查超时 → 被误杀"。
		wg.Add(1)
		go func(raw string) {
			defer wg.Done()
			dispatch(raw)
		}(data)
	}
	wg.Wait()
}
