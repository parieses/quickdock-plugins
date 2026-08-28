// Package Check - 包版本速查（npm / PyPI / Composer / Go）
// JSON-RPC 2.0 over stdin/stdout (native 插件协议)
// 命令：query（同步）输入 {name}，并发查 4 个 registry，返回最新版本/描述/许可证/周下载量。

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows/registry"
)

// ---- 代理（同 mail-check：Go 默认不读 Windows 系统代理）----

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

// ---- 数据模型 ----

type pkgInfo struct {
	Found     bool   `json:"found"`
	Name      string `json:"name,omitempty"`
	Latest    string `json:"latest,omitempty"`
	Desc      string `json:"desc,omitempty"`
	License   string `json:"license,omitempty"`
	Downloads int64  `json:"downloads,omitempty"`
	Modified  string `json:"modified,omitempty"`
}

func getJSON(client *http.Client, url string, out interface{}) error {
	resp, err := client.Get(url)
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

func str(v interface{}) string {
	switch t := v.(type) {
	case string:
		return t
	case map[string]interface{}:
		if s, ok := t["type"].(string); ok {
			return s
		}
	case []interface{}:
		parts := make([]string, 0, len(t))
		for _, x := range t {
			if s, ok := x.(string); ok {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, ", ")
	}
	return ""
}

func queryNPM(name string) pkgInfo {
	client := newClient(8 * time.Second)
	var d map[string]interface{}
	if err := getJSON(client, "https://registry.npmjs.org/"+name, &d); err != nil {
		return pkgInfo{Found: false}
	}
	info := pkgInfo{Found: true, Name: name}
	if tags, ok := d["dist-tags"].(map[string]interface{}); ok {
		info.Latest = str(tags["latest"])
	}
	info.Desc = str(d["description"])
	info.License = str(d["license"])
	if t, ok := d["time"].(map[string]interface{}); ok {
		info.Modified = str(t["modified"])
	}
	// 周下载量
	var dl map[string]interface{}
	dlURL := "https://api.npmjs.org/downloads/point/last-week/" + strings.ReplaceAll(name, "/", "%2F")
	if getJSON(client, dlURL, &dl) == nil {
		if n, ok := dl["downloads"].(float64); ok {
			info.Downloads = int64(n)
		}
	}
	return info
}

func queryPyPI(name string) pkgInfo {
	client := newClient(8 * time.Second)
	var d map[string]interface{}
	if err := getJSON(client, "https://pypi.org/pypi/"+name+"/json", &d); err != nil {
		return pkgInfo{Found: false}
	}
	info := pkgInfo{Found: true, Name: name}
	if i, ok := d["info"].(map[string]interface{}); ok {
		info.Latest = str(i["version"])
		info.Desc = str(i["summary"])
		info.License = str(i["license"])
	}
	return info
}

func queryComposer(name string) pkgInfo {
	// 需要 vendor/package 形式
	if !strings.Contains(name, "/") {
		return pkgInfo{Found: false}
	}
	client := newClient(8 * time.Second)
	var d map[string]interface{}
	if err := getJSON(client, "https://repo.packagist.org/p2/"+name+".json", &d); err != nil {
		return pkgInfo{Found: false}
	}
	info := pkgInfo{Found: true, Name: name}
	if pkgs, ok := d["packages"].(map[string]interface{}); ok {
		if list, ok := pkgs[name].([]interface{}); ok && len(list) > 0 {
			if first, ok := list[0].(map[string]interface{}); ok {
				info.Latest = str(first["version"])
				info.Desc = str(first["description"])
				info.License = str(first["license"])
			}
		}
	}
	return info
}

func queryGo(name string) pkgInfo {
	client := newClient(8 * time.Second)
	var d map[string]interface{}
	if err := getJSON(client, "https://proxy.golang.org/"+name+"/@latest", &d); err != nil {
		return pkgInfo{Found: false}
	}
	info := pkgInfo{Found: true, Name: name}
	info.Latest = str(d["Version"])
	info.Modified = str(d["Time"])
	return info
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
	out, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0", "id": id, "result": result,
	})
	fmt.Println(string(out))
}

func respondError(id int64, code int, msg string) {
	out, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0", "id": id,
		"error": map[string]interface{}{"code": code, "message": msg},
	})
	fmt.Println(string(out))
}

func handleQuery(id int64, input map[string]interface{}) {
	name := strings.TrimSpace(strFrom(input, "name"))
	if name == "" {
		respondError(id, -32602, "缺少 name")
		return
	}
	var mu sync.Mutex
	result := map[string]pkgInfo{}
	var wg sync.WaitGroup
	run := func(kind string, fn func() pkgInfo) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			info := fn()
			mu.Lock()
			result[kind] = info
			mu.Unlock()
		}()
	}
	run("npm", func() pkgInfo { return queryNPM(name) })
	run("pypi", func() pkgInfo { return queryPyPI(name) })
	run("composer", func() pkgInfo { return queryComposer(name) })
	run("go", func() pkgInfo { return queryGo(name) })
	wg.Wait()
	respond(id, result)
}

func dispatch(raw string) {
	var req rpcRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		respondError(0, -32700, "parse error: "+err.Error())
		return
	}
	switch req.Method {
	case "initialize":
		respond(req.ID, map[string]interface{}{"status": "ready", "name": "QuickDock Package Check"})
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
		case "query":
			handleQuery(req.ID, params.Input)
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
		wg.Add(1)
		go func(raw string) {
			defer wg.Done()
			dispatch(raw)
		}(data)
	}
	wg.Wait()
}
