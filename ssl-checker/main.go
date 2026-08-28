// SSL Checker - 检查 TLS 证书信息（原生 JSON-RPC 子进程，标准库 crypto/tls）
// 命令：check  input {host, port}  （port 默认 443；host 可含 :port）
package main

import (
	"bufio"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"os"
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

func strFrom(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
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

func handleCheck(id int64, input map[string]interface{}) {
	host := strings.TrimSpace(strFrom(input, "host"))
	if host == "" {
		respondError(id, -32602, "请输入域名")
		return
	}
	port := strings.TrimSpace(strFrom(input, "port"))
	if port == "" {
		port = "443"
	}
	// 支持 host 直接带 :port
	serverName := host
	addr := host
	if !strings.Contains(host, ":") {
		addr = host + ":" + port
	} else {
		// 拆分 host:port
		h, p, has := strings.Cut(host, ":")
		if has {
			serverName = h
			addr = h + ":" + p
		}
	}

	cfg := &tls.Config{InsecureSkipVerify: true, ServerName: serverName}
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 8 * time.Second}, "tcp", addr, cfg)
	if err != nil {
		respondError(id, -1, "连接失败: "+err.Error())
		return
	}
	defer conn.Close()

	cs := conn.ConnectionState()
	if len(cs.PeerCertificates) == 0 {
		respondError(id, -1, "未获取到证书")
		return
	}
	cert := cs.PeerCertificates[0]

	verifyErr := ""
	if err := conn.VerifyHostname(serverName); err != nil {
		verifyErr = err.Error()
	}
	now := time.Now()
	daysLeft := int(time.Until(cert.NotAfter).Hours() / 24)

	respond(id, map[string]interface{}{
		"subjectCN":   cert.Subject.CommonName,
		"issuerCN":    cert.Issuer.CommonName,
		"subject":     cert.Subject.String(),
		"issuer":      cert.Issuer.String(),
		"san":         cert.DNSNames,
		"serial":      cert.SerialNumber.String(),
		"notBefore":   cert.NotBefore.Format(time.RFC3339),
		"notAfter":    cert.NotAfter.Format(time.RFC3339),
		"expired":     now.After(cert.NotAfter),
		"notYetValid": now.Before(cert.NotBefore),
		"sigAlgo":     cert.SignatureAlgorithm.String(),
		"pubAlgo":     cert.PublicKeyAlgorithm.String(),
		"version":     cert.Version,
		"verifyError": verifyErr,
		"daysLeft":    daysLeft,
	})
}

func dispatch(raw string) {
	var req rpcRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		respondError(0, -32700, "parse error: "+err.Error())
		return
	}
	switch req.Method {
	case "initialize":
		respond(req.ID, map[string]interface{}{"status": "ready", "name": "QuickDock SSL Checker"})
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
