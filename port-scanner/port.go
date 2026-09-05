package main

import (
	"os"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

type PortInfo struct {
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
	State    string `json:"state"`
	PID      int    `json:"pid,omitempty"`
	Process  string `json:"process,omitempty"`
	Path     string `json:"path,omitempty"`  // 进程 exe 绝对路径，wmic 全量缓存，空表示未取到
	AddrV4   string `json:"addrV4,omitempty"` // 本地 IPv4 绑定地址，如 0.0.0.0:8080
	AddrV6   string `json:"addrV6,omitempty"` // 本地 IPv6 绑定地址，如 [::]:8080
}

func handlePortCommand(id int64, cmd string, input map[string]interface{}) {
	switch cmd {
	case "port-list":
		portList(id)
	case "port-check":
		portCheck(id, input)
	case "port-kill":
		portKill(id, input)
	default:
		respondError(id, -32601, "unknown port command: "+cmd)
	}
}

func portList(id int64) {
	// Use `netstat -ano` to list all listening ports
	out, err := hiddenCmd("netstat", "-ano").Output()
	if err != nil {
		respondError(id, -1, "执行 netstat 失败: "+err.Error())
		return
	}

	// 预热进程名缓存（一次 tasklist 全量查，后续每行 O(1) 取）；
	// exe 路径用 getProcessPath 单值懒查（wmic 全量失败时按 PID syscall 补）。
	names := getAllProcessNames()

	// 先解析出所有原始条目（含本地地址，用于区分 IPv4/IPv6）
	type rawPort struct {
		port      int
		pid       int
		proto     string
		state     string
		localAddr string
		isV6      bool
	}
	var raws []rawPort

	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}

		state := fields[3]
		if state != "LISTENING" && !strings.Contains(line, "LISTEN") {
			continue
		}

		// Parse port from local address (e.g., "0.0.0.0:8080" or "[::]:8080")
		localAddr := fields[1]
		portStr := ""
		if idx := strings.LastIndex(localAddr, ":"); idx >= 0 {
			portStr = localAddr[idx+1:]
		}

		port := 0
		if p, err := strconv.Atoi(portStr); err == nil {
			port = p
		}
		if port <= 0 {
			continue
		}

		pid := 0
		if len(fields) >= 5 {
			pidStr := fields[len(fields)-1]
			if p, err := strconv.Atoi(pidStr); err == nil {
				pid = p
			}
		}

		proto := "tcp"
		if state == "" || strings.Contains(line, "UDP") {
			proto = "udp"
		}

		raws = append(raws, rawPort{
			port:      port,
			pid:       pid,
			proto:     proto,
			state:     state,
			localAddr: localAddr,
			isV6:      strings.Contains(localAddr, "::"),
		})
	}

	// 合并：同一进程 (端口,PID) 在 IPv4/IPv6 双栈各绑定一次时，
	// netstat 会产生两条本地地址不同、但 (端口,PID) 相同的记录。
	// 合并为一行，IPv4 地址落入 AddrV4、IPv6 地址落入 AddrV6 分列展示。
	// 不同 PID 占用同一端口（少见的多进程 SO_REUSEADDR）则保留多条。
	merged := make(map[string]*PortInfo)
	var order []string
	for _, r := range raws {
		key := strconv.Itoa(r.port) + ":" + strconv.Itoa(r.pid)
		if m, ok := merged[key]; ok {
			if r.isV6 && m.AddrV6 == "" {
				m.AddrV6 = r.localAddr
			} else if !r.isV6 && m.AddrV4 == "" {
				m.AddrV4 = r.localAddr
			}
			continue
		}
		pi := &PortInfo{
			Port:     r.port,
			Protocol: r.proto,
			State:    r.state,
			PID:      r.pid,
			Process:  names[r.pid],
			Path:     getProcessPath(r.pid),
		}
		if r.isV6 {
			pi.AddrV6 = r.localAddr
		} else {
			pi.AddrV4 = r.localAddr
		}
		merged[key] = pi
		order = append(order, key)
	}

	ports := make([]PortInfo, 0, len(order))
	for _, k := range order {
		ports = append(ports, *merged[k])
	}

	respond(id, map[string]interface{}{
		"ports": ports,
		"count": len(ports),
	})
}


// resolvePositiveInt 从输入中解析正整数参数。
// 依次尝试传入的 key（如 "port"/"pid"），并兼容命令面板内联匹配时
// 前端把原始文本放在 input["text"]（例如输入 "1" 命中端口检查）。
func resolvePositiveInt(input map[string]interface{}, keys ...string) (int, bool) {
	for _, key := range keys {
		if raw, ok := input[key].(float64); ok {
			if p := int(raw); p > 0 {
				return p, true
			}
		}
		if s, ok := input[key].(string); ok {
			if p, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && p > 0 {
				return p, true
			}
		}
	}
	if s, ok := input["text"].(string); ok {
		if p, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && p > 0 {
			return p, true
		}
	}
	return 0, false
}

func portCheck(id int64, input map[string]interface{}) {
	targetPort, ok := resolvePositiveInt(input, "port")
	if !ok {
		respondError(id, -1, "需要有效的 port 参数")
		return
	}

	matched, found := findByPort(targetPort)
	if found {
		respond(id, map[string]interface{}{
			"inUse":  true,
			"port":   matched.Port,
			"pid":    matched.PID,
			"state":  matched.State,
			"name":   matched.Process,
			"path":   matched.Path,
			"detail": matched,
		})
	} else {
		respond(id, map[string]interface{}{
			"inUse": false,
			"port":  targetPort,
		})
	}
}


func findByPort(port int) (PortInfo, bool) {
	out, err := hiddenCmd("netstat", "-ano").Output()
	if err != nil {
		return PortInfo{}, false
	}
	names := getAllProcessNames()

	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		// 仅匹配 LISTENING 状态；UDP 用 state==""
		if state := fields[3]; state != "LISTENING" && state != "" {
			continue
		}
		localAddr := fields[1]
		if idx := strings.LastIndex(localAddr, ":"); idx >= 0 {
			if p, err := strconv.Atoi(localAddr[idx+1:]); err == nil && p == port {
				pid := 0
				if len(fields) >= 5 {
					if p2, err2 := strconv.Atoi(fields[len(fields)-1]); err2 == nil {
						pid = p2
					}
				}
				proto := "tcp"
				if fields[3] == "" || strings.Contains(line, "UDP") {
					proto = "udp"
				}
				info := PortInfo{
					Port:     port,
					Protocol: proto,
					State:    fields[3],
					PID:      pid,
					Process:  names[pid],
					Path:     getProcessPath(pid),
				}
				if strings.Contains(localAddr, "::") {
					info.AddrV6 = localAddr
				} else {
					info.AddrV4 = localAddr
				}
				return info, true
			}
		}
	}
	return PortInfo{}, false
}

func getProcessName(pid int) string {
	names := getAllProcessNames()
	if name, ok := names[pid]; ok {
		return name
	}
	return ""
}

var processNameCache map[int]string
var processNameCacheDone bool

func getAllProcessNames() map[int]string {
	if processNameCacheDone {
		return processNameCache
	}
	out, err := hiddenCmd("tasklist", "/NH", "/FO", "CSV").Output()
	if err != nil {
		processNameCacheDone = true
		processNameCache = make(map[int]string)
		return processNameCache
	}
	processNameCache = make(map[int]string)
	lines := strings.Split(string(out), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, ",")
		if len(parts) < 2 {
			continue
		}
		name := strings.Trim(parts[0], "\"")
		pidStr := strings.Trim(parts[1], "\"")
		pid, err := strconv.Atoi(pidStr)
		if err == nil && name != "" && !strings.Contains(name, "INFO") {
			processNameCache[pid] = name
		}
	}
	processNameCacheDone = true
	return processNameCache
}

// processPathCache 全量缓存 (PID → exe 绝对路径)，避免每行单独 spawn wmic。
// 优先 wmic 全量拿（更快），wmic 不存在/不可用时回退到 Win32 syscall 按 PID 补。
var processPathCache map[int]string
var processPathCacheDone bool

func getProcessPaths() map[int]string {
	if processPathCacheDone {
		return processPathCache
	}
	processPathCache = make(map[int]string)
	out, err := hiddenCmd("wmic", "process", "where", "ProcessId>0", "get", "ProcessId,ExecutablePath", "/format:list").Output()
	if err == nil {
		var pid int
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			eq := strings.Index(line, "=")
			if eq <= 0 {
				continue
			}
			k, v := strings.TrimSpace(line[:eq]), strings.TrimSpace(line[eq+1:])
			switch k {
			case "ProcessId":
				if n, err := strconv.Atoi(v); err == nil {
					pid = n
				}
			case "ExecutablePath":
				if pid > 0 && v != "" {
					processPathCache[pid] = v
				}
			}
		}
	}
	processPathCacheDone = true
	return processPathCache
}

// getProcessPath 查询单个 PID 的 exe 绝对路径。
// 优先用缓存；wmic 全量失败（processPathCacheDone=true 但 processPathCache 为空
// 或某个 PID 没拿到）时，回退到 Win32 QueryFullProcessImageNameW syscall。
func getProcessPath(pid int) string {
	if pid <= 0 {
		return ""
	}
	if path, ok := getProcessPaths()[pid]; ok && path != "" {
		return path
	}
	path := queryFullProcessImageName(pid)
	if path != "" {
		if processPathCache == nil {
			processPathCache = make(map[int]string)
		}
		processPathCache[pid] = path
	}
	return path
}

// ---- Win32 syscall fallback ----
// 用 kernel32!QueryFullProcessImageNameW 拿单个进程 exe 路径。
// 适用于现代 Windows（Win10+/Server 2016+）wmic 默认不再预装的场景。

var (
	modkernel32                   = syscall.NewLazyDLL("kernel32.dll")
	procOpenProcess               = modkernel32.NewProc("OpenProcess")
	procQueryFullProcessImageName = modkernel32.NewProc("QueryFullProcessImageNameW")
	procCloseHandle               = modkernel32.NewProc("CloseHandle")
)

const processQueryLimitedInformation = 0x1000

func queryFullProcessImageName(pid int) string {
	h, _, _ := procOpenProcess.Call(processQueryLimitedInformation, 0, uintptr(pid))
	if h == 0 {
		return ""
	}
	defer procCloseHandle.Call(h)
	var sz uint32 = 32768
	buf := make([]uint16, sz)
	r, _, _ := procQueryFullProcessImageName.Call(h, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&sz)))
	if r == 0 {
		return ""
	}
	return syscall.UTF16ToString(buf[:sz])
}

// clearProcessCaches 让进程名/路径缓存下次访问时重建。
// 在 portKill 成功后调用，避免后续列表仍展示已结束进程的 stale 名称。
func clearProcessCaches() {
	processNameCache = nil
	processNameCacheDone = false
	processPathCache = nil
	processPathCacheDone = false
}

// portKill 支持两种语义：按端口号（推荐，命令面板和 UI 都用）或按 PID。
// 优先取 port，反查监听该端口的进程；PID 用于向后兼容外部直接调用。
func portKill(id int64, input map[string]interface{}) {
	if port, ok := resolvePositiveInt(input, "port"); ok {
		info, found := findByPort(port)
		if !found {
			respondError(id, -1, "端口 "+strconv.Itoa(port)+" 未被监听，无需结束")
			return
		}
		if info.PID <= 0 {
			respondError(id, -1, "端口 "+strconv.Itoa(port)+" 监听者不是用户态进程")
			return
		}
		killPIDAndRespond(id, info.PID, info)
		return
	}
	if pid, ok := resolvePositiveInt(input, "pid"); ok {
		killPIDAndRespond(id, pid, PortInfo{PID: pid})
		return
	}
	respondError(id, -1, "需要 port 或 pid 参数")
}

func killPIDAndRespond(id int64, pid int, info PortInfo) {
	// 安全检查：拒绝系统关键进程
	if pid <= 4 {
		respondError(id, -1, "拒绝操作：系统关键进程 (PID ≤ 4)")
		return
	}
	// 拒绝杀死自身
	if pid == os.Getpid() {
		respondError(id, -1, "拒绝操作：不能结束自身进程")
		return
	}

	// 获取进程名（缓存里取不到再回退一次 tasklist），检查是否属于已知系统进程
	procName := info.Process
	if procName == "" {
		procName = getProcessName(pid)
	}
	if isSystemProcess(procName) {
		respondError(id, -1, "拒绝操作：系统关键进程: "+procName)
		return
	}

	err := hiddenCmd("taskkill", "/F", "/PID", strconv.Itoa(pid)).Run()
	if err != nil {
		respondError(id, -1, "结束进程失败: "+err.Error())
		return
	}

	clearProcessCaches()

	respond(id, map[string]interface{}{
		"success": true,
		"pid":     pid,
		"name":    procName,
		"port":    info.Port,
	})
}

// isSystemProcess 检查是否是 Windows 系统关键进程
func isSystemProcess(name string) bool {
	switch strings.ToLower(strings.TrimSuffix(name, ".exe")) {
	case "system", "smss", "csrss", "wininit", "winlogon",
		"services", "lsass", "svchost", "lsm",
		"explorer", "taskhost", "taskhostw",
		"runtimebroker", "sihost", "ctfmon",
		"dwm", "conhost", "fontdrvhost",
		"spoolsv", "securityhealthservice",
		"trustedinstaller", "ntoskrnl":
		return true
	}
	return false
}
