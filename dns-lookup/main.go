// DNS Lookup - 查询域名 DNS 记录（原生 JSON-RPC 子进程，标准库 net）
// 命令：lookup  input {domain, types[]}  类型支持 A/AAAA/MX/TXT/CNAME/NS
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
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

func strFrom(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func strSliceFrom(m map[string]interface{}, key string) []string {
	if v, ok := m[key].([]interface{}); ok {
		out := []string{}
		for _, x := range v {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
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

func handleLookup(id int64, input map[string]interface{}) {
	domain := strings.TrimSpace(strFrom(input, "domain"))
	if domain == "" {
		respondError(id, -32602, "请输入域名")
		return
	}
	for len(domain) > 0 && domain[len(domain)-1] == '.' {
		domain = domain[:len(domain)-1]
	}
	types := strSliceFrom(input, "types")
	if len(types) == 0 {
		types = []string{"A", "AAAA", "MX", "TXT", "CNAME", "NS"}
	}
	recs := []map[string]interface{}{}
	add := func(rtype, value string) {
		recs = append(recs, map[string]interface{}{"type": rtype, "value": value})
	}
	for _, t := range types {
		rt := strings.ToUpper(strings.TrimSpace(t))
		switch rt {
		case "A", "AAAA":
			ips, err := net.LookupIP(domain)
			if err != nil {
				add(rt, "查询失败: "+err.Error())
				continue
			}
			for _, ip := range ips {
				if rt == "A" && ip.To4() != nil {
					add("A", ip.String())
				}
				if rt == "AAAA" && ip.To4() == nil {
					add("AAAA", ip.String())
				}
			}
		case "MX":
			mxs, err := net.LookupMX(domain)
			if err != nil {
				add("MX", "查询失败: "+err.Error())
				continue
			}
			for _, mx := range mxs {
				add("MX", fmt.Sprintf("%d %s", mx.Pref, mx.Host))
			}
		case "TXT":
			txts, err := net.LookupTXT(domain)
			if err != nil {
				add("TXT", "查询失败: "+err.Error())
				continue
			}
			for _, t := range txts {
				add("TXT", t)
			}
		case "CNAME":
			cname, err := net.LookupCNAME(domain)
			if err != nil {
				add("CNAME", "查询失败: "+err.Error())
				continue
			}
			add("CNAME", cname)
		case "NS":
			nss, err := net.LookupNS(domain)
			if err != nil {
				add("NS", "查询失败: "+err.Error())
				continue
			}
			for _, ns := range nss {
				add("NS", ns.Host)
			}
		default:
			add(rt, "不支持的类型")
		}
	}
	respond(id, map[string]interface{}{"domain": domain, "records": recs})
}

func dispatch(raw string) {
	var req rpcRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		respondError(0, -32700, "parse error: "+err.Error())
		return
	}
	switch req.Method {
	case "initialize":
		respond(req.ID, map[string]interface{}{"status": "ready", "name": "QuickDock DNS Lookup"})
	case "host.ping":
		respond(req.ID, map[string]interface{}{"pong": true})
	case "plugin.execute":
		var params executeParams
		if len(req.Params) > 0 {
			_ = json.Unmarshal(req.Params, &params)
		}
		switch strings.ToLower(strings.TrimSpace(params.Command)) {
		case "lookup":
			handleLookup(req.ID, params.Input)
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
