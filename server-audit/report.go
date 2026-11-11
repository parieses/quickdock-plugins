// 风险汇总：把端口暴露、证书问题、敏感路径、未授权访问统一折算成分级结论 + 处置建议。
//
// 分级口径：
//
//	critical 已可直接利用（未授权访问、备份/环境变量文件可下载）
//	high     强攻击面（数据库/管理端口对公网、Docker API、证书过期或自签名、源码泄露）
//	medium   配置不当（过时 TLS、弱密钥、HSTS 缺失、状态页暴露）
//	low      信息性暴露（接口文档、明文 HTTP、目录结构）
//	info     中性观察项，附带说明
package main

import (
	"fmt"
	"sort"
	"strings"
)

type Finding struct {
	ID       string `json:"id"`
	Severity string `json:"severity"`
	Title    string `json:"title"`
	Detail   string `json:"detail"`
	Fix      string `json:"fix,omitempty"`
	Port     int    `json:"port,omitempty"`
}

func buildFindings(s *auditSession) []Finding {
	s.mu.Lock()
	services := append([]ServiceInfo(nil), s.services...)
	paths := append([]PathHit(nil), s.paths...)
	unauth := append([]UnauthCheck(nil), s.unauth...)
	stats := s.stats
	s.mu.Unlock()

	var out []Finding
	add := func(f Finding) { out = append(out, f) }

	unauthPorts := map[int]bool{}
	for _, u := range unauth {
		if u.Vulnerable {
			unauthPorts[u.Port] = true
			add(Finding{
				ID: fmt.Sprintf("unauth-%s-%d", u.Service, u.Port), Severity: u.Severity,
				Title:  "未授权访问：" + u.Label,
				Detail: fmt.Sprintf("端口 %d · %s", u.Port, u.Detail),
				Fix:    u.Fix, Port: u.Port,
			})
		}
	}

	for _, svc := range services {
		if unauthPorts[svc.Port] {
			continue // 已被未授权检查覆盖，不重复报
		}
		if f, ok := serviceFinding(svc); ok {
			add(f)
		}
		if svc.TLS != nil {
			out = append(out, tlsFindings(svc.Port, *svc.TLS)...)
		}
		if f, ok := headerFinding(svc); ok {
			add(f)
		}
	}

	for _, p := range paths {
		sev := p.Severity
		if sev == "info" && p.Status != 200 {
			// 受保护/跳转的路径不单独占一条风险，只在路径表里展示
			continue
		}
		title := "敏感路径暴露：" + p.Path
		detail := fmt.Sprintf("端口 %d · 状态 %d · %d 字节", p.Port, p.Status, p.Size)
		if p.Note != "" {
			detail += " · " + p.Note
		}
		if p.Evidence != "" {
			detail += "\n证据：" + p.Evidence
		}
		add(Finding{
			ID: fmt.Sprintf("path-%d-%s", p.Port, p.Path), Severity: sev,
			Title: title, Detail: detail, Port: p.Port,
			Fix: pathFix(p.Path),
		})
	}

	if stats.Open > 0 {
		add(Finding{
			ID: "exposure-summary", Severity: "info",
			Title:  fmt.Sprintf("对外暴露 %d 个开放端口", stats.Open),
			Detail: fmt.Sprintf("本次共探测 %d 个端口：开放 %d / 关闭 %d / 过滤 %d", stats.Total, stats.Open, stats.Closed, stats.Filtered),
			Fix:    "只保留业务必需端口，其余用安全组/防火墙（ufw、firewalld、云安全组）从公网侧拒绝",
		})
	}

	sort.SliceStable(out, func(i, j int) bool {
		if sevRank(out[i].Severity) != sevRank(out[j].Severity) {
			return sevRank(out[i].Severity) < sevRank(out[j].Severity)
		}
		return out[i].Port < out[j].Port
	})
	if len(out) > 60 {
		out = out[:60]
	}
	return out
}

func serviceFinding(svc ServiceInfo) (Finding, bool) {
	sev, title, fix := "", "", ""
	switch svc.Service {
	case "docker":
		sev, title = "critical", "Docker API 对外暴露"
		fix = "关闭 2375，改用 2376 + 客户端证书或只监听 unix socket"
	case "redis", "memcached", "mongodb", "es", "couchdb", "etcd":
		sev, title = "high", svc.Label+" 对公网开放"
		fix = "绑定 127.0.0.1 或内网地址，配认证，公网侧用安全组拒绝该端口"
	case "mysql", "postgres", "mssql", "oracle", "db2", "cassandra":
		sev, title = "high", svc.Label+" 数据库端口对公网开放"
		fix = "数据库只监听内网/本机，公网改走跳板机或 SSH 隧道；确认账号口令强度与来源 IP 限制"
	case "telnet", "vnc", "rdp", "smb", "msrpc", "netbios":
		sev, title = "high", svc.Label+" 远程管理端口对公网开放"
		fix = "远程管理走 VPN / 跳板机；RDP 开 NLA、限制来源 IP；停用 Telnet 换 SSH"
	case "ftp":
		sev, title = "medium", "FTP 明文服务对外暴露"
		fix = "改用 SFTP/FTPS，禁止匿名访问"
	case "smtp":
		sev, title = "low", "SMTP 端口对外暴露"
		fix = "确认未开启开放中继（open relay），限制来源 IP"
	case "telnet-smtp":
		sev = ""
	case "smb-open":
		sev = ""
	case "http":
		sev, title = "low", "明文 HTTP 对外提供内容"
		fix = "上 HTTPS 并把 HTTP 301 到 HTTPS，同时开 HSTS"
	case "k8s":
		sev, title = "high", "Kubernetes API 对公网开放"
		fix = "API Server 只在内网可达，公网走堡垒机；开启 RBAC 与审计日志"
	case "unknown", "other", "":
		sev = ""
	}
	if sev == "" {
		// SSH/HTTPS/DNS 等属于正常暴露，不单独记风险
		return Finding{}, false
	}
	ver := ""
	if svc.Version != "" {
		ver = "（版本 " + svc.Version + "）"
	}
	return Finding{
		ID: fmt.Sprintf("svc-%s-%d", svc.Service, svc.Port), Severity: sev,
		Title:  title + ver,
		Detail: fmt.Sprintf("端口 %d · %s · 判定依据：%s", svc.Port, svc.Label, svc.Evidence),
		Fix:    fix, Port: svc.Port,
	}, true
}

func tlsFindings(port int, ti TLSInfo) []Finding {
	var out []Finding
	add := func(id, sev, title, detail, fix string) {
		out = append(out, Finding{ID: id, Severity: sev, Title: title, Detail: detail, Fix: fix, Port: port})
	}
	switch {
	case ti.DaysLeft < 0:
		add("tls-expired", "critical", "TLS 证书已过期",
			fmt.Sprintf("端口 %d · 有效期至 %s（已过期 %d 天）", port, ti.NotAfter, -ti.DaysLeft),
			"立即续签证书并配置自动续期（certbot / acme.sh），加到期监控")
	case ti.DaysLeft <= 15:
		add("tls-expiring", "high", "TLS 证书即将过期",
			fmt.Sprintf("端口 %d · 有效期至 %s（剩 %d 天）", port, ti.NotAfter, ti.DaysLeft),
			"尽快续签并配置自动续期与到期告警")
	}
	if ti.SelfSigned {
		add("tls-selfsigned", "high", "使用自签名证书",
			fmt.Sprintf("端口 %d · 颁发者 %s（浏览器与客户端会告警，也易被中间人替换）", port, ti.Issuer),
			"换用受信任 CA 证书（Let's Encrypt 即可免费）")
	}
	if ti.HostMismatch {
		add("tls-mismatch", "high", "证书与访问域名不匹配",
			fmt.Sprintf("端口 %d · 证书 CN/SAN 未覆盖当前域名", port),
			"重新签发包含该域名的证书，或修正 SNI/默认站点")
	}
	if strings.Contains(ti.Version, "1.0") || strings.Contains(ti.Version, "1.1") {
		add("tls-oldproto", "medium", "启用了过时的 TLS 协议",
			fmt.Sprintf("端口 %d · 协商到 %s", port, ti.Version),
			"服务端禁用 TLS 1.0/1.1，只留 TLS 1.2 与 1.3")
	}
	if ti.KeyAlg == "RSA" && ti.KeyBits > 0 && ti.KeyBits < 2048 {
		add("tls-weakkey", "medium", "证书密钥长度不足",
			fmt.Sprintf("端口 %d · RSA %d 位", port, ti.KeyBits),
			"换成 2048 位以上 RSA 或 ECDSA P-256 证书")
	}
	if strings.Contains(strings.ToUpper(ti.SigAlg), "SHA1") {
		add("tls-sha1", "medium", "证书签名算法过时",
			fmt.Sprintf("端口 %d · %s", port, ti.SigAlg),
			"改用 SHA-256 及以上签名算法重新签发")
	}
	return out
}

func headerFinding(svc ServiceInfo) (Finding, bool) {
	if svc.Scheme != "https" {
		return Finding{}, false
	}
	hasHSTS := false
	for _, h := range svc.Headers {
		if strings.EqualFold(h.K, "strict-transport-security") {
			hasHSTS = true
		}
	}
	if hasHSTS {
		return Finding{}, false
	}
	return Finding{
		ID: fmt.Sprintf("hsts-%d", svc.Port), Severity: "medium",
		Title:  "缺少 HSTS 响应头",
		Detail: fmt.Sprintf("端口 %d · HTTPS 未返回 Strict-Transport-Security，仍可能被降级劫持", svc.Port),
		Fix:    "在 Web 服务器加 Strict-Transport-Security: max-age=31536000; includeSubDomains",
		Port:   svc.Port,
	}, true
}

func pathFix(path string) string {
	switch {
	case strings.Contains(path, ".git"), strings.Contains(path, ".svn"):
		return "在 Web 服务器拒绝访问 .git/.svn 目录（nginx: location ~ /\\.(git|svn) { deny all; }）"
	case strings.Contains(path, ".env"):
		return "把 .env 移出 Web 根目录，禁止点文件被直接访问，并轮换其中已泄露的口令"
	case strings.Contains(path, "sql"), strings.Contains(path, "zip"), strings.Contains(path, ".bak"):
		return "删除 Web 根目录下的备份文件，备份改存到站点目录之外并加密"
	case strings.Contains(path, "actuator"):
		return "关闭多余 actuator 端点，只保留 health/info 并加鉴权"
	case strings.Contains(path, "phpinfo"):
		return "删除 phpinfo/info 探针文件"
	case strings.Contains(path, "status"), strings.Contains(path, "metrics"):
		return "状态/指标端点只允许内网或加 Basic 认证"
	default:
		return "确认该路径是否需要对外，不需要就删除或加访问控制"
	}
}

func summarize(findings []Finding) Summary {
	var sm Summary
	for _, f := range findings {
		switch f.Severity {
		case "critical":
			sm.Critical++
		case "high":
			sm.High++
		case "medium":
			sm.Medium++
		case "low":
			sm.Low++
		default:
			sm.Info++
		}
	}
	switch {
	case sm.Critical > 0:
		sm.Verdict = fmt.Sprintf("发现 %d 个可直接利用的暴露，建议今天就处理（优先关端口 / 加认证）", sm.Critical)
	case sm.High > 0:
		sm.Verdict = fmt.Sprintf("发现 %d 个高危暴露面，建议本周收敛（限来源 IP、上 TLS、删备份文件）", sm.High)
	case sm.Medium > 0:
		sm.Verdict = fmt.Sprintf("没有可直接利用的漏洞，但有 %d 处配置加固项", sm.Medium)
	default:
		sm.Verdict = "未发现明显暴露，保持现状并定期复查"
	}
	return sm
}

func (s *auditSession) textReport() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	var b strings.Builder
	b.WriteString("服务器自查报告\n")
	b.WriteString(strings.Repeat("=", 46) + "\n")
	b.WriteString("目标：" + s.target.Host + " → " + s.target.IP + "\n")
	b.WriteString("开始：" + s.startedAt.Format("2006-01-02 15:04:05") + "\n")
	b.WriteString(fmt.Sprintf("端口：探测 %d / 开放 %d / 关闭 %d / 过滤 %d\n",
		s.stats.Total, s.stats.Open, s.stats.Closed, s.stats.Filtered))
	sm := summarize(s.findings)
	b.WriteString(fmt.Sprintf("结论：%s\n", sm.Verdict))
	if s.errMsg != "" {
		b.WriteString("异常：" + s.errMsg + "\n")
	}

	b.WriteString("\n[开放端口]\n")
	for _, p := range s.ports {
		svc := ""
		for _, x := range s.services {
			if x.Port == p.Port {
				svc = x.Label
				if x.Version != "" {
					svc += " " + x.Version
				}
				break
			}
		}
		b.WriteString(fmt.Sprintf("  %-6d %-8s %s\n", p.Port, p.State, svc))
	}

	b.WriteString("\n[风险清单]\n")
	if len(s.findings) == 0 {
		b.WriteString("  无\n")
	}
	for _, f := range s.findings {
		b.WriteString(fmt.Sprintf("  [%s] %s\n", strings.ToUpper(f.Severity), f.Title))
		for _, line := range strings.Split(f.Detail, "\n") {
			b.WriteString("        " + line + "\n")
		}
		if f.Fix != "" {
			b.WriteString("        处置：" + f.Fix + "\n")
		}
	}

	if len(s.paths) > 0 {
		b.WriteString("\n[敏感路径命中]\n")
		for _, p := range s.paths {
			b.WriteString(fmt.Sprintf("  %-28s %d  %s\n", p.Path, p.Status, p.Note))
		}
	}
	if len(s.unauth) > 0 {
		b.WriteString("\n[认证检查]\n")
		for _, u := range s.unauth {
			state := "需要认证"
			if u.Vulnerable {
				state = "未授权可访问"
			}
			b.WriteString(fmt.Sprintf("  %-6d %-16s %s · %s\n", u.Port, u.Label, state, u.Detail))
		}
	}
	b.WriteString("\n说明：本报告由只读探测生成，未做漏洞利用、未写入目标、未使用口令字典。\n")
	return b.String()
}
