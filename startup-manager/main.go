package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strings"

	"system-tools/sysutil"
)

type RPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}
type RPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// 注册表 Run 根键（HKLM / HKCU / Wow6432Node）
var runRoots = []struct {
	Root string
	Name string
}{
	{`HKEY_LOCAL_MACHINE\Software\Microsoft\Windows\CurrentVersion\Run`, "HKLM 启动项"},
	{`HKEY_CURRENT_USER\Software\Microsoft\Windows\CurrentVersion\Run`, "HKCU 启动项"},
	{`HKEY_LOCAL_MACHINE\Software\Wow6432Node\Microsoft\Windows\CurrentVersion\Run`, "HKLM(32位) 启动项"},
}

var regTypeRe = regexp.MustCompile(`REG_(SZ|EXPAND_SZ|MULTI_SZ|DWORD|BINARY|NONE)`)

func main() {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 256*1024), 256*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var req RPCRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			continue
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					fmt.Fprintf(os.Stderr, "startup-manager panic: %v\n%s\n", r, debug.Stack())
					respondError(req.ID, -32000, fmt.Sprintf("internal error: %v", r))
				}
			}()
			handleRequest(req)
		}()
	}
}

func handleRequest(req RPCRequest) {
	switch req.Method {
	case "initialize":
		respond(req.ID, map[string]interface{}{"status": "ready", "name": "QuickDock Startup Manager"})
	case "host.ping":
		respond(req.ID, map[string]interface{}{"pong": true})
	case "plugin.execute":
		handleExecute(req)
	default:
		respondError(req.ID, -32601, "unknown method: "+req.Method)
	}
}

func handleExecute(req RPCRequest) {
	var p struct {
		Command string                 `json:"command"`
		Input   map[string]interface{} `json:"input"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil {
		respondError(req.ID, -32602, "invalid params")
		return
	}
	input := p.Input
	if t, ok := input["text"].(string); ok && t != "" {
		if strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[") {
			var nested map[string]interface{}
			if err := json.Unmarshal([]byte(t), &nested); err == nil {
				for k, v := range nested {
					if _, e := input[k]; !e {
						input[k] = v
					}
				}
			}
		}
	}
	switch {
	case strings.HasPrefix(p.Command, "startup-"):
		handleStartup(req.ID, p.Command, input)
	default:
		respondError(req.ID, -32601, "unknown command: "+p.Command)
	}
}

func getStr(input map[string]interface{}, key string) string {
	if v, ok := input[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func handleStartup(id int64, cmd string, input map[string]interface{}) {
	switch cmd {
	case "startup-list":
		handleStartupList(id)
	case "startup-toggle":
		handleStartupToggle(id, input)
	default:
		respondError(id, -32601, "unknown command: "+cmd)
	}
}

type regVal struct {
	Name, Type, Data string
}

func regListValues(key string) []regVal {
	cmd := sysutil.Hide(exec.Command("reg", "query", key))
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var res []regVal
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, "REG_") {
			continue
		}
		m := regTypeRe.FindStringIndex(line)
		if m == nil {
			continue
		}
		name := strings.TrimSpace(line[:m[0]])
		typ := line[m[0]:m[1]]
		data := strings.TrimSpace(line[m[1]:])
		if name == "" || name == "(Default)" || data == "(value not set)" {
			continue
		}
		res = append(res, regVal{name, typ, data})
	}
	return res
}

func runReg(args []string) error {
	cmd := sysutil.Hide(exec.Command("reg", args...))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func handleStartupList(id int64) {
	var registry []map[string]interface{}
	for _, r := range runRoots {
		for _, sub := range []string{"Run", "RunDisabled"} {
			enabled := sub == "Run"
			for _, v := range regListValues(r.Root + `\` + sub) {
				registry = append(registry, map[string]interface{}{
					"id":       "reg::" + r.Root + "::" + v.Name,
					"location": r.Name,
					"name":     v.Name,
					"command":  v.Data,
					"enabled":  enabled,
				})
			}
		}
	}

	var folders []map[string]interface{}
	for _, fk := range []struct {
		Kind, Name string
	}{{"user", "用户启动文件夹"}, {"common", "公共启动文件夹"}} {
		for _, fi := range listFolder(fk.Kind) {
			folders = append(folders, map[string]interface{}{
				"id":       "folder::" + fk.Kind + "::" + fi.name,
				"location": fk.Name,
				"name":     fi.name,
				"enabled":  fi.enabled,
			})
		}
	}

	respond(id, map[string]interface{}{"registry": registry, "folders": folders})
}

type folderItem struct {
	name    string
	enabled bool
}

func startupFolder(kind string) string {
	base := os.Getenv("APPDATA")
	if kind == "common" {
		base = os.Getenv("PROGRAMDATA")
	}
	return filepath.Join(base, "Microsoft", "Windows", "Start Menu", "Programs", "Startup")
}

func readDir(dir string) []os.DirEntry {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	return entries
}

func listFolder(kind string) []folderItem {
	folder := startupFolder(kind)
	disabledDir := filepath.Join(folder, "Disabled")
	var items []folderItem
	for _, f := range readDir(folder) {
		if f.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(f.Name()))
		if ext == ".lnk" || ext == ".url" {
			items = append(items, folderItem{f.Name(), true})
		}
	}
	for _, f := range readDir(disabledDir) {
		if f.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(f.Name()))
		if ext == ".lnk" || ext == ".url" {
			items = append(items, folderItem{f.Name(), false})
		}
	}
	return items
}

func handleStartupToggle(id int64, input map[string]interface{}) {
	eid := getStr(input, "id")
	if eid == "" {
		respondError(id, -32602, "缺少 id")
		return
	}
	enable := false
	if v, ok := input["enabled"].(bool); ok {
		enable = v
	}
	parts := strings.SplitN(eid, "::", 3)
	if len(parts) != 3 {
		respondError(id, -32602, "非法的 id")
		return
	}
	switch parts[0] {
	case "reg":
		root, name := parts[1], parts[2]
		if err := toggleRegistry(root, name, enable); err != nil {
			respondError(id, -1, "切换失败: "+err.Error())
			return
		}
	case "folder":
		kind, name := parts[1], parts[2]
		if err := toggleFolder(kind, name, enable); err != nil {
			respondError(id, -1, "切换失败: "+err.Error())
			return
		}
	default:
		respondError(id, -32602, "非法的 id 类型")
		return
	}
	respond(id, map[string]interface{}{"ok": true})
}

func toggleRegistry(root, name string, enable bool) error {
	srcKey := root + `\Run`
	dstKey := root + `\RunDisabled`
	if enable {
		srcKey, dstKey = dstKey, srcKey
	}
	var found *regVal
	for _, v := range regListValues(srcKey) {
		if v.Name == name {
			found = &v
			break
		}
	}
	if found == nil {
		return fmt.Errorf("未找到启动项: %s", name)
	}
	args := []string{"add", dstKey, "/v", name, "/t", found.Type, "/f"}
	if found.Data != "" {
		args = append(args, "/d", found.Data)
	}
	if err := runReg(args); err != nil {
		return err
	}
	return runReg([]string{"delete", srcKey, "/v", name, "/f"})
}

func toggleFolder(kind, name string, enable bool) error {
	folder := startupFolder(kind)
	disabledDir := filepath.Join(folder, "Disabled")
	srcDir := folder
	dstDir := disabledDir
	if enable {
		srcDir, dstDir = disabledDir, folder
	}
	src := filepath.Join(srcDir, name)
	dst := filepath.Join(dstDir, name)
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		return err
	}
	return os.Rename(src, dst)
}

func respond(id int64, result interface{}) {
	data, _ := json.Marshal(RPCResponse{JSONRPC: "2.0", ID: id, Result: mustMarshal(result)})
	data = append(data, '\n')
	os.Stdout.Write(data)
}
func respondError(id int64, code int, msg string) {
	data, _ := json.Marshal(RPCResponse{JSONRPC: "2.0", ID: id, Error: &RPCError{Code: code, Message: msg}})
	data = append(data, '\n')
	os.Stdout.Write(data)
}
func mustMarshal(v interface{}) json.RawMessage {
	d, _ := json.Marshal(v)
	return d
}
