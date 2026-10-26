package main

import "regexp"

// Category 区分结果的语义分组，前端据此着色与筛选。
const (
	catSecret = "secret" // 密钥 / 凭证（高）
	catToken  = "token"  // 令牌 / 访问令牌（高）
	catKey    = "key"    // 私钥 / 加密材料（高）
	catPII    = "pii"    // 个人身份信息（中）
)

// severityRank 仅用于前端排序。
func severityRank(s string) int {
	switch s {
	case "high":
		return 3
	case "medium":
		return 2
	default:
		return 1
	}
}

// Rule 一条检测规则：正则 + 元数据 + 可选校验（身份证/银行卡的校验位）。
// ValueGroup > 0 时取该捕获组作为「命中值」，否则取整个匹配（避免把 password= 前缀也带出来）。
type Rule struct {
	ID          string
	Name        string
	Category    string
	Severity    string
	Desc        string
	Re          *regexp.Regexp
	ValueGroup  int  // 0 = 整段匹配
	MultiLine   bool // true 时对整个文件内容匹配（如私钥块），按 \n 推算行号
	DefaultOn   bool // 默认是否启用
	Validate    func(val string) bool
}

// rulesAll 是全部内置规则。新增检测器只改这里。
var rulesAll = []Rule{
	// ---------- 密钥 / 凭证 ----------
	{
		ID: "aws_access_key", Name: "AWS Access Key ID", Category: catSecret, Severity: "high",
		Desc: "AWS 访问密钥 ID（AKIA 开头），泄露可直接访问云资源",
		Re:       regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
		DefaultOn: true,
	},
	{
		ID: "aws_secret", Name: "AWS Secret / 通用 40 位密钥", Category: catSecret, Severity: "high",
		Desc: "形如 aws_secret_access_key = xxxx 的赋值，或单独出现的 40 位 base64 密钥",
		Re:        regexp.MustCompile(`(?i)(?:aws_?secret_?access_?key|secret_?access_?key)\s*[:=]+\s*['"]([A-Za-z0-9/+=]{40})['"]`),
		ValueGroup: 1, DefaultOn: true,
	},
	{
		ID: "private_key", Name: "私钥块", Category: catKey, Severity: "high",
		Desc: "PEM 格式私钥（RSA/EC/OPENSSH/PGP），可直接解密或登录",
		Re:        regexp.MustCompile(`(?s)-----BEGIN (?:[A-Z0-9 ]* )?PRIVATE KEY-----.*?-----END (?:[A-Z0-9 ]* )?PRIVATE KEY-----`),
		MultiLine: true, DefaultOn: true,
	},
	{
		ID: "google_api", Name: "Google API Key", Category: catSecret, Severity: "high",
		Desc: "Google API 密钥（AIza 开头，35 位）",
		Re:       regexp.MustCompile(`AIza[0-9A-Za-z_\-]{35}`),
		DefaultOn: true,
	},
	{
		ID: "stripe_key", Name: "Stripe 密钥", Category: catSecret, Severity: "high",
		Desc: "Stripe 支付密钥（sk_live_/rk_live_ 等）",
		Re:       regexp.MustCompile(`[rs]k_(?:live|test)_[A-Za-z0-9]{16,}`),
		DefaultOn: true,
	},
	{
		ID: "slack_token", Name: "Slack Token", Category: catToken, Severity: "high",
		Desc: "Slack 访问令牌（xoxb-/xoxp-/xoxa- 等）",
		Re:       regexp.MustCompile(`xox[baprs]-[A-Za-z0-9-]{10,}`),
		DefaultOn: true,
	},
	{
		ID: "slack_webhook", Name: "Slack Webhook", Category: catToken, Severity: "high",
		Desc: "Slack 入站 Webhook 地址，可用于向频道推送消息",
		Re:       regexp.MustCompile(`https://hooks\.slack\.com/services/T[A-Z0-9]{8,10}/B[A-Z0-9]{8,10}/[A-Za-z0-9]{24}`),
		DefaultOn: true,
	},
	{
		ID: "github_token", Name: "GitHub Token", Category: catToken, Severity: "high",
		Desc: "GitHub 个人访问令牌（ghp_/gho_/ghu_/ghs_/ghr_/github_pat_）",
		Re:       regexp.MustCompile(`(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{36}|github_pat_[A-Za-z0-9_]{82}`),
		DefaultOn: true,
	},
	{
		ID: "jwt", Name: "JWT", Category: catToken, Severity: "medium",
		Desc: "JSON Web Token（eyJ 开头的两段式 Base64），常被误当普通字符串硬编码",
		Re:       regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`),
		DefaultOn: true,
	},
	{
		ID: "generic_secret", Name: "疑似密钥赋值", Category: catSecret, Severity: "high",
		Desc: "api_key/secret/token/password 等赋值，值为 8 位以上字母数字串（排除明显占位符）",
		Re:        regexp.MustCompile(`(?i)(?:api[_-]?key|secret|access[_-]?token|auth[_-]?token|client[_-]?secret|private[_-]?key)\s*[:=]+\s*['"]([A-Za-z0-9_\-\.]{8,})['"]`),
		ValueGroup: 1, DefaultOn: true,
		Validate: notPlaceholder,
	},
	{
		ID: "env_secret", Name: ".env 敏感变量", Category: catSecret, Severity: "high",
		Desc: ".env 风格赋值：PASSWORD/SECRET_KEY/ACCESS_TOKEN/PRIVATE_KEY 等",
		Re:        regexp.MustCompile(`(?im)^\s*(?:DB_?PASSWORD|PASSWORD|SECRET_?KEY|ACCESS_?TOKEN|PRIVATE_?KEY|API_?KEY|ENCRYPTION_?KEY)\s*[:=]+\s*(.+?)\s*$`),
		ValueGroup: 1, DefaultOn: true,
		Validate: notPlaceholder,
	},

	// ---------- 个人身份信息（PII）----------
	{
		ID: "id_card", Name: "身份证号", Category: catPII, Severity: "medium",
		Desc: "中国居民身份证号（18 位，含校验位验证，降低误报）",
		Re:       regexp.MustCompile(`\b[1-9]\d{5}(?:18|19|20)\d{2}(?:0[1-9]|1[0-2])(?:0[1-9]|[12]\d|3[01])\d{3}[\dXx]\b`),
		DefaultOn: true, Validate: validChinaID,
	},
	{
		ID: "bank_card", Name: "银行卡号", Category: catPII, Severity: "medium",
		Desc: "15–19 位数字且通过 Luhn 校验的卡号",
		Re:       regexp.MustCompile(`\b[1-9]\d{14,18}\b`),
		DefaultOn: true, Validate: validLuhn,
	},
	{
		ID: "phone_cn", Name: "手机号", Category: catPII, Severity: "low",
		Desc: "中国大陆手机号（1[3-9] 开头 11 位），误报较高，默认关闭",
		Re:       regexp.MustCompile(`\b1[3-9]\d{9}\b`),
		DefaultOn: false,
	},
	{
		ID: "email", Name: "电子邮箱", Category: catPII, Severity: "low",
		Desc: "电子邮箱地址，常非机密，默认关闭",
		Re:       regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`),
		DefaultOn: false,
	},
}

// notPlaceholder 排除明显的占位符/示例值。注意：只认「明确的占位词或标记」，
// 不做 example/key/secret 这类宽泛子串匹配——真实密钥里可能恰好含这些词（如 AWS 文档示例
// 密钥 AKIAIOSFODNN7EXAMPLE），会误杀。
func notPlaceholder(v string) bool {
	if len(v) < 8 {
		return false
	}
	if allSame(v) {
		return false
	}
	low := toLowerASCII(v)
	// 整值等于这些词才算占位
	exact := map[string]bool{
		"your-key-here": true, "your_password_here": true, "your-secret-key": true,
		"changeme": true, "replaceme": true, "replace_me": true, "placeholder": true,
		"example": true, "dummy": true, "dummykey": true, "sample": true, "xxxx": true,
		"********": true, "insert-key-here": true, "fixme": true, "todo": true,
	}
	if exact[low] {
		return false
	}
	// 含这些明确占位标记才过滤
	for _, p := range []string{"xxxx", "****", "<", ">", "your-", "-here",
		"changeme", "replace_me", "replaceme", "dummy", "placeholder"} {
		if contains(low, p) {
			return false
		}
	}
	return true
}

func allSame(s string) bool {
	if s == "" {
		return true
	}
	first := s[0]
	for i := 1; i < len(s); i++ {
		if s[i] != first {
			return false
		}
	}
	return true
}

// validChinaID 校验身份证末位校验码（GB 11643-1999）。
func validChinaID(id string) bool {
	if len(id) != 18 {
		return false
	}
	weights := []int{7, 9, 10, 5, 8, 4, 2, 1, 6, 3, 7, 9, 10, 5, 8, 4, 2}
	checkCodes := "10X98765432"
	sum := 0
	for i := 0; i < 17; i++ {
		c := id[i]
		if c < '0' || c > '9' {
			return false
		}
		sum += int(c-'0') * weights[i]
	}
	return checkCodes[sum%11] == id[17] || (id[17] == 'x' && checkCodes[sum%11] == 'X')
}

// validLuhn 校验 Luhn 算法（银行卡/信用卡）。
func validLuhn(num string) bool {
	if len(num) < 13 {
		return false
	}
	sum, alt := 0, false
	for i := len(num) - 1; i >= 0; i-- {
		c := num[i]
		if c < '0' || c > '9' {
			return false
		}
		d := int(c - '0')
		if alt {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		alt = !alt
	}
	return sum%10 == 0
}

func toLowerASCII(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
