package main

// 导出：CSV（Excel 可直接打开）与自包含 HTML 时间轴报告。

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"html"
	"strings"
	"time"
)

func typeLabel(t string) string {
	switch t {
	case TypeSession:
		return "会话"
	case TypeProgram:
		return "程序"
	case TypeDevice:
		return "设备"
	case TypeFile:
		return "文件"
	case TypeDelete:
		return "删除"
	case TypeChange:
		return "变更"
	case TypeNetwork:
		return "网络"
	case TypeTamper:
		return "异常/反取证"
	default:
		return t
	}
}

func levelLabel(l string) string {
	switch l {
	case LevelConfirmed:
		return "确证"
	case LevelCorroborated:
		return "交叉印证"
	case LevelInferred:
		return "推断"
	default:
		return l
	}
}

func exportCSV(events []TimelineEvent) string {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"时间", "类型", "证据强度", "事件", "详情", "证据来源", "原始字段"})
	for _, e := range events {
		_ = w.Write([]string{e.Time, typeLabel(e.Type), levelLabel(e.Level),
			e.Title, e.Detail, e.Source, e.Raw})
	}
	w.Flush()
	return "\ufeff" + buf.String()
}

func exportHTML(events []TimelineEvent, from, to string, sources []SourceStatus) string {
	var b strings.Builder
	b.WriteString(`<!DOCTYPE html><html lang="zh-CN"><head><meta charset="UTF-8"><title>电脑活动痕迹报告</title><style>
body{background:#0f1116;color:#e6e9ef;font-family:-apple-system,'Segoe UI',system-ui,sans-serif;margin:0;padding:28px;font-size:13px;line-height:1.6}
h1{font-size:20px;font-weight:500;margin:0 0 6px}h2{font-size:15px;font-weight:500;margin:26px 0 10px}
.meta{color:#a1a8b4;margin-bottom:18px}.note{background:#1c2029;border:1px solid #2e3440;border-radius:10px;padding:12px 16px;color:#a1a8b4;margin-bottom:20px}
table{width:100%;border-collapse:collapse}th,td{text-align:left;padding:8px 10px;border-bottom:1px solid #22272f;vertical-align:top}
th{color:#6f7784;font-weight:500;font-size:12px}td.t{white-space:nowrap;color:#a1a8b4;font-variant-numeric:tabular-nums}
.tag{display:inline-block;padding:1px 8px;border-radius:999px;font-size:11px;border:1px solid #2e3440}
.lv-confirmed{color:#7ee2a8;border-color:#276b45}.lv-corroborated{color:#9fc0ff;border-color:#33558f}.lv-inferred{color:#f5c07a;border-color:#8a6220}
.tp-tamper{color:#ff9b9b}.src{color:#6f7784;font-size:12px}
ul{color:#a1a8b4;padding-left:20px}.footer{margin-top:30px;color:#6f7784;font-size:12px}
</style></head><body>`)
	b.WriteString("<h1>电脑活动痕迹报告</h1>")
	b.WriteString(fmt.Sprintf(`<div class="meta">区间 %s ~ %s · 共 %d 条 · 生成于 %s</div>`,
		html.EscapeString(from), html.EscapeString(to), len(events),
		html.EscapeString(time.Now().Format("2006-01-02 15:04:05"))))
	b.WriteString(`<div class="note">本报告由 Windows 自留痕迹聚合而成，<b>不是操作录像回放</b>。标注「推断」的条目不能当确证；插了 U 盘不等于拷了文件，打开过文件不等于读过内容或外传。默认未开启的审计无法补录过去。</div>`)
	b.WriteString(`<h2>时间轴</h2><table><thead><tr><th>时间</th><th>类型</th><th>强度</th><th>事件</th><th>详情 / 证据来源</th></tr></thead><tbody>`)
	for _, e := range events {
		b.WriteString("<tr>")
		b.WriteString(fmt.Sprintf(`<td class="t">%s</td><td>%s</td><td><span class="tag lv-%s">%s</span></td>`,
			html.EscapeString(e.Time), html.EscapeString(typeLabel(e.Type)),
			html.EscapeString(e.Level), html.EscapeString(levelLabel(e.Level))))
		b.WriteString(fmt.Sprintf(`<td class="tp-%s">%s</td>`, html.EscapeString(e.Type), html.EscapeString(e.Title)))
		b.WriteString(fmt.Sprintf(`<td>%s<div class="src">%s</div></td>`,
			html.EscapeString(e.Detail), html.EscapeString(e.Source)))
		b.WriteString("</tr>")
	}
	b.WriteString(`</tbody></table><h2>证据源状态</h2><table><thead><tr><th>来源</th><th>状态</th><th>条数</th><th>说明</th></tr></thead><tbody>`)
	for _, s := range sources {
		state := "可用"
		if !s.Available {
			state = "不可用"
		} else if s.Level == "partial" {
			state = "部分"
		}
		b.WriteString(fmt.Sprintf(`<tr><td>%s</td><td>%s</td><td>%d</td><td>%s</td></tr>`,
			html.EscapeString(s.Name), state, s.Count, html.EscapeString(s.Note)))
	}
	b.WriteString(`</tbody></table><div class="footer">由 QuickDock「电脑活动痕迹」插件生成 · 全程只读，不修改系统设置</div></body></html>`)
	return b.String()
}
