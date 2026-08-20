// Disk Analyzer - 磁盘空间分析工具
// JSON-RPC 2.0 over stdin/stdout

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
)

// RPC 消息结构
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      *int64      `json:"id,omitempty"`
	Result  interface{} `json:"result,omitempty"`
	Error   *rpcError   `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// 目录节点
type dirNode struct {
	Path     string    `json:"path"`
	Name     string    `json:"name"`
	Size     int64     `json:"size"`
	Children []dirNode `json:"children,omitempty"`
	IsFile   bool      `json:"isFile,omitempty"`
}

var (
	mu      sync.Mutex
	methods = map[string]func(json.RawMessage) (interface{}, error){}
)

func main() {
	methods["disk.scan"] = handleScan
	methods["disk.list"] = handleList
	methods["disk.info"] = handleInfo
	methods["ping"] = func(_ json.RawMessage) (interface{}, error) {
		return map[string]string{"status": "ok"}, nil
	}

	reader := bufio.NewReader(os.Stdin)
	writer := bufio.NewWriter(os.Stdout)

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		data := strings.TrimSpace(line)
		if data == "" {
			continue
		}

		var req rpcRequest
		if err := json.Unmarshal([]byte(data), &req); err != nil {
			sendError(writer, nil, -32700, "Parse error")
			continue
		}

		handler, ok := methods[req.Method]
		if !ok {
			sendError(writer, req.ID, -32601, "Method not found: "+req.Method)
			continue
		}

		result, err := handler(req.Params)
		if err != nil {
			sendError(writer, req.ID, -32603, err.Error())
			continue
		}

		sendResponse(writer, req.ID, result)
		writer.Flush()
	}
}

func sendResponse(w *bufio.Writer, id *int64, result interface{}) {
	resp := rpcResponse{JSONRPC: "2.0", Result: result}
	if id != nil {
		resp.ID = id
	}
	data, _ := json.Marshal(resp)
	fmt.Fprintln(w, string(data))
}

func sendError(w *bufio.Writer, id *int64, code int, message string) {
	resp := rpcResponse{
		JSONRPC: "2.0",
		Error:   &rpcError{Code: code, Message: message},
	}
	if id != nil {
		resp.ID = id
	}
	data, _ := json.Marshal(resp)
	fmt.Fprintln(w, string(data))
}

func handleScan(params json.RawMessage) (interface{}, error) {
	var p struct {
		Path  string `json:"path"`
		Depth int    `json:"depth"`
		Limit int    `json:"limit"`
	}
	json.Unmarshal(params, &p)
	if p.Path == "" {
		p.Path = getRootPath()
	}
	if p.Depth == 0 {
		p.Depth = 3
	}
	if p.Limit == 0 {
		p.Limit = 50
	}

	root := &dirNode{Path: p.Path, Name: filepath.Base(p.Path)}
	mu.Lock()
	err := scanDir(p.Path, root, 0, p.Depth, p.Limit)
	mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("扫描失败: %w", err)
	}
	return root, nil
}

func handleList(_ json.RawMessage) (interface{}, error) {
	root := getRootPath()
	entries, err := osReadDir(root)
	if err != nil {
		return nil, err
	}
	var items []map[string]interface{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		items = append(items, map[string]interface{}{
			"name": e.Name(),
			"path": filepath.Join(root, e.Name()),
			"size": formatSize(info.Size()),
		})
	}
	sort.Slice(items, func(i, j int) bool {
		si := items[i]["size"].(string)
		sj := items[j]["size"].(string)
		return si > sj
	})
	return items, nil
}

func handleInfo(_ json.RawMessage) (interface{}, error) {
	root := getRootPath()
	stat, err := getDiskStats(root)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"total":  formatSize(int64(stat.Total)),
		"used":   formatSize(int64(stat.Used)),
		"free":   formatSize(int64(stat.Free)),
		"usage":  fmt.Sprintf("%.1f%%", stat.UsagePct),
		"mount":  root,
		"fstype": runtime.GOOS,
	}, nil
}

func scanDir(path string, parent *dirNode, depth, maxDepth, limit int) error {
	if depth > maxDepth {
		return nil
	}
	entries, err := osReadDir(path)
	if err != nil {
		info, statErr := osStat(path)
		if statErr == nil {
			parent.Size = info.Size()
		}
		return nil
	}

	var children []dirNode
	totalSize := int64(0)

	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		childPath := filepath.Join(path, e.Name())
		child := dirNode{Path: childPath, Name: e.Name()}

		if e.IsDir() {
			if depth < maxDepth && len(children) < limit {
				if scanErr := scanDir(childPath, &child, depth+1, maxDepth, limit); scanErr == nil {
					children = append(children, child)
				}
			} else {
				child.Size = estimateDirSize(childPath)
				child.Children = []dirNode{{Name: "...", Path: childPath, Size: child.Size}}
			}
		} else {
			child.Size = info.Size()
			child.IsFile = true
		}
		totalSize += child.Size
	}

	sort.Slice(children, func(i, j int) bool {
		return children[i].Size > children[j].Size
	})

	parent.Size = totalSize
	parent.Children = children
	return nil
}

func estimateDirSize(path string) int64 {
	var size int64
	filepath.Walk(path, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		size += info.Size()
		return nil
	})
	return size
}

func formatSize(bytes int64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	size := float64(bytes)
	idx := 0
	for size >= 1024 && idx < len(units)-1 {
		size /= 1024
		idx++
	}
	return fmt.Sprintf("%.1f %s", size, units[idx])
}

func getRootPath() string {
	switch runtime.GOOS {
	case "windows":
		return "C:\\"
	default:
		return "/"
	}
}

// ---- 跨平台文件操作 ----

func osReadDir(path string) ([]os.DirEntry, error) {
	return os.ReadDir(path)
}

func osStat(path string) (os.FileInfo, error) {
	return os.Stat(path)
}
