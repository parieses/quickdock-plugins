// DNS 传播检查 — 向多个公共 DNS 解析器并行查询指定记录，对比结果一致性
//
// 同步执行：各解析器独立 goroutine 并发查询（单解析器超时 4s），整体远小于宿主 20s 限制。
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

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

func strFrom(m map[string]interface{}, k string) string {
	if v, ok := m[k].(string); ok {
		return strings.TrimSpace(v)
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

type resolver struct {
	Name   string
	Server string
}

var defaultResolvers = []resolver{
	{"Cloudflare", "1.1.1.1"},
	{"Google", "8.8.8.8"},
	{"Quad9", "9.9.9.9"},
	{"阿里 DNS", "223.5.5.5"},
	{"DNSPod", "119.29.29.29"},
	{"OpenDNS", "208.67.222.222"},
}

func normalizeServer(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimPrefix(s, "https://")
	// 保留用户显式给出的 host:port；Dial 时若无端口再补默认 :53
	return s
}

func queryResolver(r resolver, domain, qtype string) map[string]interface{} {
	d := &net.Resolver{
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			dialer := net.Dialer{Timeout: 4 * time.Second}
			addr := r.Server
			if !strings.Contains(addr, ":") {
				addr = net.JoinHostPort(addr, "53")
			}
			return dialer.DialContext(ctx, "udp", addr)
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	start := time.Now()
	answers := []string{}
	var errStr string

	switch qtype {
	case "A", "AAAA":
		netw := "ip"
		if qtype == "A" {
			netw = "ip4"
		} else {
			netw = "ip6"
		}
		ips, err := d.LookupIP(ctx, netw, domain)
		if err != nil {
			errStr = err.Error()
		} else {
			seen := map[string]bool{}
			for _, ip := range ips {
				s := ip.String()
				if !seen[s] {
					seen[s] = true
					answers = append(answers, s)
				}
			}
		}
	case "CNAME":
		c, err := d.LookupCNAME(ctx, domain)
		if err != nil {
			errStr = err.Error()
		} else {
			answers = append(answers, c)
		}
	case "MX":
		mxs, err := d.LookupMX(ctx, domain)
		if err != nil {
			errStr = err.Error()
		} else {
			for _, m := range mxs {
				answers = append(answers, fmt.Sprintf("%d %s", m.Pref, m.Host))
			}
			sort.Strings(answers)
		}
	case "NS":
		nss, err := d.LookupNS(ctx, domain)
		if err != nil {
			errStr = err.Error()
		} else {
			for _, n := range nss {
				answers = append(answers, n.Host)
			}
			sort.Strings(answers)
		}
	case "TXT":
		txts, err := d.LookupTXT(ctx, domain)
		if err != nil {
			errStr = err.Error()
		} else {
			for _, t := range txts {
				answers = append(answers, t)
			}
			sort.Strings(answers)
		}
	default:
		errStr = "不支持的记录类型: " + qtype
	}

	el := int64(time.Since(start).Milliseconds())
	return map[string]interface{}{
		"name":      r.Name,
		"server":    r.Server,
		"answers":   answers,
		"error":     errStr,
		"elapsedMs": el,
		"ok":        errStr == "",
	}
}

func handleCheck(id int64, input map[string]interface{}) {
	domain := strFrom(input, "domain")
	if domain == "" {
		respondError(id, -32602, "请输入域名")
		return
	}
	domain = strings.TrimSuffix(domain, ".")
	qtype := strings.ToUpper(strFrom(input, "type"))
	if qtype == "" {
		qtype = "A"
	}
	switch qtype {
	case "A", "AAAA", "CNAME", "MX", "TXT", "NS":
	default:
		respondError(id, -32602, "不支持的记录类型: "+qtype)
		return
	}

	resolvers := defaultResolvers
	if custom := strFrom(input, "resolver"); custom != "" {
		resolvers = []resolver{{Name: "自定义", Server: normalizeServer(custom)}}
	}

	results := make([]map[string]interface{}, len(resolvers))
	var wg sync.WaitGroup
	for i, r := range resolvers {
		wg.Add(1)
		go func(i int, r resolver) {
			defer wg.Done()
			results[i] = queryResolver(r, domain, qtype)
		}(i, r)
	}
	wg.Wait()

	// 一致性分析
	var successful [][]string
	consistent := true
	var firstSet string
	for _, res := range results {
		if res["ok"].(bool) {
			ans := toStringSlice(res["answers"])
			sort.Strings(ans)
			key := strings.Join(ans, "|")
			if firstSet == "" {
				firstSet = key
			} else if key != firstSet {
				consistent = false
			}
			successful = append(successful, ans)
		}
	}
	if len(successful) == 0 {
		consistent = false
	}

	// 共识结果：所有成功结果的并集（去重排序）
	uniq := map[string]bool{}
	for _, set := range successful {
		for _, a := range set {
			uniq[a] = true
		}
	}
	consensus := make([]string, 0, len(uniq))
	for a := range uniq {
		consensus = append(consensus, a)
	}
	sort.Strings(consensus)

	respond(id, map[string]interface{}{
		"domain":    domain,
		"type":      qtype,
		"resolvers": results,
		"consistent": consistent,
		"consensus": consensus,
		"okCount":   len(successful),
	})
}

func toStringSlice(v interface{}) []string {
	if arr, ok := v.([]string); ok {
		return arr
	}
	return []string{}
}

/* ==================== RPC ==================== */

func dispatch(raw string) {
	var req rpcRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		respondError(0, -32700, "parse error: "+err.Error())
		return
	}
	switch req.Method {
	case "initialize":
		respond(req.ID, map[string]interface{}{"status": "ready", "name": "QuickDock DNS Propagation"})
	case "host.ping":
		respond(req.ID, map[string]interface{}{"pong": true})
	case "plugin.execute":
		var params executeParams
		if len(req.Params) > 0 {
			_ = json.Unmarshal(req.Params, &params)
		}
		switch strings.ToLower(strings.TrimSpace(params.Command)) {
		case "check":
			handleCheck(req.ID, params.Input)
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
