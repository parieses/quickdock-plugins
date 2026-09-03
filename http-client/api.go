package main

import (
	"context"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)
const (
	httpClientMaxBody   = 16 << 20
	httpClientTimeout   = 30 * time.Second
	httpClientMaxRedirs = 10
	httpTimeout         = 30 * time.Second
)

// ApiRequestInput 前端传入的请求（新建/更新/发送共用）。
type ApiRequestInput struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	ProjectID     string `json:"projectId"`
	FolderID      string `json:"folderId"`
	EnvironmentID string `json:"environmentId"`
	Method        string `json:"method"`
	URL           string `json:"url"`
	Headers       string `json:"headers"`
	Body          string `json:"body"`
	BodyType      string `json:"bodyType"`
	AuthType      string `json:"authType"`
	AuthToken     string `json:"authToken"`
	AuthUser      string `json:"authUser"`
	AuthPass      string `json:"authPass"`
	Sort          int    `json:"sort"`
}

// ApiResponse 发送结果（不落库）。
type ApiResponse struct {
	Status     int               `json:"status"`
	OK         bool              `json:"ok"`
	Headers    map[string]string `json:"headers"`
	Body       string            `json:"body"`
	DurationMs int64             `json:"durationMs"`
	Size       int               `json:"size"`
	Truncated  bool              `json:"truncated"`
}

var userHTTPClient = &http.Client{
	Timeout: httpClientTimeout,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= httpClientMaxRedirs {
			return fmt.Errorf("重定向次数超过 %d 次", httpClientMaxRedirs)
		}
		return nil
	},
}

// collectEnvVars 收集指定环境的启用变量。
func (s *Server) collectEnvVars(environmentID string) map[string]string {
	vars := map[string]string{}
	if environmentID == "" {
		return vars
	}
	env, err := s.db.GetEnvironment(environmentID)
	if err != nil || env == nil {
		return vars
	}
	var list []struct {
		Key     string `json:"key"`
		Value   string `json:"value"`
		Enabled bool   `json:"enabled"`
	}
	if e := json.Unmarshal([]byte(env.Variables), &list); e == nil {
		for _, v := range list {
			if v.Enabled && v.Key != "" {
				vars[v.Key] = v.Value
			}
		}
	}
	return vars
}

// applyProjectAndEnv 把项目共享头与激活环境的变量作用到请求输入上（原地修改）。
func (s *Server) applyProjectAndEnv(input *ApiRequestInput) error {
	vars := s.collectEnvVars(input.EnvironmentID)

	input.URL = substituteVars(input.URL, vars)
	input.Body = substituteVars(input.Body, vars)
	input.AuthToken = substituteVars(input.AuthToken, vars)
	input.AuthUser = substituteVars(input.AuthUser, vars)
	input.AuthPass = substituteVars(input.AuthPass, vars)

	reqHeaders := map[string]string{}
	if input.Headers != "" {
		if err := json.Unmarshal([]byte(input.Headers), &reqHeaders); err != nil {
			reqHeaders = map[string]string{}
		}
	}
	merged := map[string]string{}
	if input.ProjectID != "" {
		if proj, err := s.db.GetProject(input.ProjectID); err == nil && proj != nil && proj.Headers != "" {
			var projHeaders map[string]string
			if e2 := json.Unmarshal([]byte(proj.Headers), &projHeaders); e2 == nil {
				for k, v := range projHeaders {
					merged[k] = substituteVars(v, vars)
				}
			}
		}
	}
	for k, v := range reqHeaders {
		merged[k] = substituteVars(v, vars)
	}
	b, _ := json.Marshal(merged)
	input.Headers = string(b)
	return nil
}

// substituteVars 将文本中的 {{key}} 替换为 vars[key]（未匹配保留原样）。
func substituteVars(s string, vars map[string]string) string {
	if s == "" || len(vars) == 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] == '{' && i+1 < len(s) && s[i+1] == '{' {
			end := strings.Index(s[i+2:], "}}")
			if end >= 0 {
				key := s[i+2 : i+2+end]
				if val, ok := vars[key]; ok {
					b.WriteString(val)
					i = i + 2 + end + 2
					continue
				}
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// doUserHTTP 执行一次 HTTP 请求：仅放行 http/https，应用 auth 与自定义 header。
func doUserHTTP(input ApiRequestInput) (*ApiResponse, error) {
	method := strings.ToUpper(strings.TrimSpace(input.Method))
	if method == "" {
		method = http.MethodGet
	}
	rawURL := strings.TrimSpace(input.URL)
	if rawURL == "" {
		return nil, fmt.Errorf("URL 不能为空")
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("URL 非法: %w", err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil, fmt.Errorf("仅支持 http/https 协议，收到: %s", u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("URL 缺少主机名")
	}

	var bodyReader io.Reader
	if input.Body != "" && method != http.MethodGet && method != http.MethodHead {
		bodyReader = bytes.NewReader([]byte(input.Body))
	}
	req, err := http.NewRequestWithContext(context.Background(), method, u.String(), bodyReader)
	if err != nil {
		return nil, err
	}

	if input.Headers != "" {
		var hdrs map[string]string
		if err := json.Unmarshal([]byte(input.Headers), &hdrs); err == nil {
			for k, v := range hdrs {
				if strings.EqualFold(k, "Host") {
					continue
				}
				req.Header.Set(k, v)
			}
		}
	}

	switch strings.ToLower(input.AuthType) {
	case "bearer":
		if input.AuthToken != "" {
			req.Header.Set("Authorization", "Bearer "+input.AuthToken)
		}
	case "basic":
		if input.AuthUser != "" || input.AuthPass != "" {
			req.SetBasicAuth(input.AuthUser, input.AuthPass)
		}
	}

	if bodyReader != nil && req.Header.Get("Content-Type") == "" {
		switch input.BodyType {
		case "form":
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		case "text":
			req.Header.Set("Content-Type", "text/plain; charset=utf-8")
		case "xml":
			req.Header.Set("Content-Type", "application/xml; charset=utf-8")
		default:
			req.Header.Set("Content-Type", "application/json")
		}
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "QuickDock-HttpClient")
	}

	start := time.Now()
	resp, err := userHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()
	elapsed := time.Since(start)

	data, err := io.ReadAll(io.LimitReader(resp.Body, int64(httpClientMaxBody)+1))
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}
	truncated := false
	if len(data) > httpClientMaxBody {
		data = data[:httpClientMaxBody]
		truncated = true
	}

	respHeaders := make(map[string]string, len(resp.Header))
	for k := range resp.Header {
		respHeaders[k] = resp.Header.Get(k)
	}

	return &ApiResponse{
		Status:     resp.StatusCode,
		OK:         resp.StatusCode >= 200 && resp.StatusCode < 300,
		Headers:    respHeaders,
		Body:       string(data),
		DurationMs: elapsed.Milliseconds(),
		Size:       len(data),
		Truncated:  truncated,
	}, nil
}

// buildCurlString 从请求结构生成 curl 命令。
func buildCurlString(input ApiRequestInput) string {
	method := strings.ToUpper(strings.TrimSpace(input.Method))
	if method == "" {
		method = "GET"
	}
	var b strings.Builder
	b.WriteString("curl -X ")
	b.WriteString(method)
	b.WriteString(" ")
	b.WriteString(shellQuote(input.URL))

	if input.Headers != "" {
		var hdrs map[string]string
		if json.Unmarshal([]byte(input.Headers), &hdrs) == nil {
			for k, v := range hdrs {
				if strings.EqualFold(k, "Host") {
					continue
				}
				b.WriteString(" -H ")
				b.WriteString(shellQuote(k + ": " + v))
			}
		}
	}
	switch strings.ToLower(input.AuthType) {
	case "bearer":
		if input.AuthToken != "" {
			b.WriteString(" -H ")
			b.WriteString(shellQuote("Authorization: Bearer " + input.AuthToken))
		}
	case "basic":
		if input.AuthUser != "" || input.AuthPass != "" {
			b.WriteString(" -u ")
			b.WriteString(shellQuote(input.AuthUser + ":" + input.AuthPass))
		}
	}
	if input.Body != "" && method != "GET" && method != "HEAD" {
		b.WriteString(" --data ")
		b.WriteString(shellQuote(input.Body))
	}
	return b.String()
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// ---------- Postman 导入 ----------

type postmanRequest struct {
	Name   string          `json:"name"`
	Method string          `json:"method"`
	URL    json.RawMessage `json:"url"`
	Header json.RawMessage `json:"header"`
	Body   json.RawMessage `json:"body"`
	Auth   json.RawMessage `json:"auth"`
}

// ImportPostman 导入 Postman Collection v2.1 JSON：在当前项目下创建请求。
func (s *Server) ImportPostman(jsonStr string) (map[string]interface{}, error) {
	var collection struct {
		Info struct {
			Name string `json:"name"`
		} `json:"info"`
		Item json.RawMessage `json:"item"`
	}
	if err := json.Unmarshal([]byte(jsonStr), &collection); err != nil {
		return nil, fmt.Errorf("不是合法的 Postman JSON: %v", err)
	}

	projectName := strings.TrimSpace(collection.Info.Name)
	if projectName == "" {
		projectName = "Postman 导入"
	}
	proj, err := s.ensureProject(projectName)
	if err != nil {
		return nil, err
	}

	var flatten func(json.RawMessage) []postmanRequest
	flatten = func(items json.RawMessage) []postmanRequest {
		var out []postmanRequest
		var arr []struct {
			Name   string          `json:"name"`
			Item   json.RawMessage `json:"item"`
			Method string          `json:"method"`
			URL    json.RawMessage `json:"url"`
			Header json.RawMessage `json:"header"`
			Body   json.RawMessage `json:"body"`
			Auth   json.RawMessage `json:"auth"`
		}
		_ = json.Unmarshal(items, &arr)
		for _, it := range arr {
			if len(it.Item) > 0 {
				out = append(out, flatten(it.Item)...)
				continue
			}
			out = append(out, postmanRequest{
				Name:   it.Name,
				Method: it.Method,
				URL:    it.URL,
				Header: it.Header,
				Body:   it.Body,
				Auth:   it.Auth,
			})
		}
		return out
	}

	var reqs []postmanRequest
	if len(collection.Item) > 0 {
		reqs = append(reqs, flatten(collection.Item)...)
	}

	count := 0
	var errs []string
	for _, req := range reqs {
		var headers, body, bodyType, urlStr string

		if len(req.URL) > 0 {
			var urlStrPlain string
			if json.Unmarshal(req.URL, &urlStrPlain) == nil && urlStrPlain != "" {
				urlStr = urlStrPlain
			} else {
				var uo struct {
					Raw string `json:"raw"`
				}
				if json.Unmarshal(req.URL, &uo) == nil {
					urlStr = uo.Raw
				}
			}
		}

		var headerList []struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		}
		if len(req.Header) > 0 {
			_ = json.Unmarshal(req.Header, &headerList)
			m := map[string]string{}
			for _, hd := range headerList {
				if hd.Key != "" {
					m[hd.Key] = hd.Value
				}
			}
			if mb, e := json.Marshal(m); e == nil {
				headers = string(mb)
			}
		}

		if len(req.Body) > 0 {
			var bodyObj struct {
				Mode string `json:"mode"`
				Raw  string `json:"raw"`
			}
			if json.Unmarshal(req.Body, &bodyObj) == nil {
				body = bodyObj.Raw
				bodyType = bodyObj.Mode
				if bodyType == "formdata" || bodyType == "urlencoded" {
					bodyType = "form"
				} else if bodyType == "file" {
					bodyType = "json"
				}
			}
		}
		if bodyType == "" {
			bodyType = "json"
		}
		if urlStr == "" {
			errs = append(errs, fmt.Sprintf("跳过无 URL 的请求: %s", req.Name))
			continue
		}
		name := req.Name
		if name == "" {
			name = urlStr
		}
		rec := &ApiRequest{
			ID:        newID(),
			ProjectID: proj.ID,
			Name:      name,
			Method:    req.Method,
			URL:       urlStr,
			Headers:   headers,
			Body:      body,
			BodyType:  bodyType,
			Sort:      count,
		}
		if err := s.db.CreateRequest(rec); err != nil {
			errs = append(errs, fmt.Sprintf("导入失败 %s: %v", name, err))
			continue
		}
		count++
	}
	return map[string]interface{}{"imported": count, "projectId": proj.ID, "errors": errs}, nil
}

// ensureProject 按名字查找项目，无则创建。
func (s *Server) ensureProject(name string) (*HttpProject, error) {
	if strings.TrimSpace(name) == "" {
		name = "未命名项目"
	}
	projects, err := s.db.ListProjects()
	if err != nil {
		return nil, err
	}
	for i := range projects {
		if strings.TrimSpace(projects[i].Name) == strings.TrimSpace(name) {
			p := projects[i]
			return &p, nil
		}
	}
	rec := &HttpProject{Name: name, Headers: "{}", Sort: len(projects)}
	if err := s.db.CreateProject(rec); err != nil {
		return nil, err
	}
	return rec, nil
}
