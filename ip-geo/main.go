// IP Geolocation - 查询 IP 地理位置/运营商/经纬度（原生 JSON-RPC 子进程，标准库 net/http）
// 命令：lookup  input {ip}   （ip 留空则查询本机出口 IP）
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

type ipResponse struct {
	IP        string `json:"ip"`
	Success   bool   `json:"success"`
	Message   string `json:"message"`
	Type      string `json:"type"`
	Continent string `json:"continent"`
	Country   string `json:"country"`
	Region    string `json:"region"`
	City      string `json:"city"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Timezone  struct {
		Id   string `json:"id"`
		Abbr string `json:"abbr"`
	} `json:"timezone"`
	Connection struct {
		ASN    int    `json:"asn"`
		Org    string `json:"org"`
		ISP    string `json:"isp"`
		Domain string `json:"domain"`
	} `json:"connection"`
	Currency struct {
		Code string `json:"code"`
		Name string `json:"name"`
	} `json:"currency"`
}

func handleLookup(id int64, input map[string]interface{}) {
	ip := strings.TrimSpace(strFrom(input, "ip"))
	url := "https://ipwho.is"
	if ip != "" {
		url = "https://ipwho.is/" + ip
	}

	client := &http.Client{Timeout: 12 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		respondError(id, -1, "请求失败: "+err.Error())
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		respondError(id, -1, "读取响应失败: "+err.Error())
		return
	}

	var data ipResponse
	if err := json.Unmarshal(body, &data); err != nil {
		respondError(id, -1, "解析响应失败: "+err.Error())
		return
	}
	if !data.Success {
		msg := data.Message
		if msg == "" {
			msg = "查询失败"
		}
		respond(id, map[string]interface{}{"ok": false, "error": msg})
		return
	}

	fields := []map[string]string{
		{"label": "IP", "value": data.IP},
		{"label": "类型", "value": data.Type},
		{"label": "大洲", "value": data.Continent},
		{"label": "国家", "value": data.Country},
		{"label": "地区", "value": data.Region},
		{"label": "城市", "value": data.City},
		{"label": "时区", "value": data.Timezone.Id + " (" + data.Timezone.Abbr + ")"},
		{"label": "运营商", "value": data.Connection.ISP},
		{"label": "组织", "value": data.Connection.Org},
		{"label": "ASN", "value": fmt.Sprintf("AS%d", data.Connection.ASN)},
		{"label": "域名", "value": data.Connection.Domain},
		{"label": "货币", "value": data.Currency.Code + " - " + data.Currency.Name},
		{"label": "经纬度", "value": fmt.Sprintf("%.4f, %.4f", data.Latitude, data.Longitude)},
	}

	mapURL := ""
	if data.Latitude != 0 || data.Longitude != 0 {
		mapURL = fmt.Sprintf("https://www.openstreetmap.org/?mlat=%.4f&mlon=%.4f#map=10/%.4f/%.4f",
			data.Latitude, data.Longitude, data.Latitude, data.Longitude)
	}

	respond(id, map[string]interface{}{
		"ok":      true,
		"query":   ip,
		"fields":  fields,
		"lat":     data.Latitude,
		"lng":     data.Longitude,
		"mapUrl":  mapURL,
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
		respond(req.ID, map[string]interface{}{"status": "ready", "name": "QuickDock IP Geolocation"})
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
