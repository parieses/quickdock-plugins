package main

// Windows 事件日志采集：Security / System / TerminalServices / DriverFrameworks /
// PowerShell / Application(MsiInstaller) / Sysmon。
// wevtutil 输出 XML（可能没有单一根节点），用 xml.Decoder 流式逐个 Event 解码。

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

type evtxEvent struct {
	System struct {
		Provider struct {
			Name string `xml:"Name,attr"`
		} `xml:"Provider"`
		EventID     int    `xml:"EventID"`
		Level       int    `xml:"Level"`
		Channel     string `xml:"Channel"`
		Computer    string `xml:"Computer"`
		TimeCreated struct {
			SystemTime string `xml:"SystemTime,attr"`
		} `xml:"TimeCreated"`
	} `xml:"System"`
	EventData struct {
		Data []struct {
			Name  string `xml:"Name,attr"`
			Value string `xml:",chardata"`
		} `xml:"Data"`
	} `xml:"EventData"`
	UserData struct {
		Inner string `xml:",innerxml"`
	} `xml:"UserData"`
}

// RE2 不支持反向引用，闭合标签名不逐一校验；UserData 结构简单，够用。
var userDataTagRe = regexp.MustCompile(`<([A-Za-z_][A-Za-z0-9_]*)[^>]*>([^<]*)</[A-Za-z_][A-Za-z0-9_]*>`)

func (e evtxEvent) fields() map[string]string {
	m := make(map[string]string, 14)
	for i, d := range e.EventData.Data {
		name := d.Name
		if name == "" {
			name = fmt.Sprintf("param%d", i+1)
		}
		m[name] = strings.TrimSpace(d.Value)
	}
	if len(m) == 0 && e.UserData.Inner != "" {
		for _, mm := range userDataTagRe.FindAllStringSubmatch(e.UserData.Inner, -1) {
			if v := strings.TrimSpace(mm[2]); v != "" {
				m[mm[1]] = v
			}
		}
	}
	return m
}

func parseEventTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	layouts := []string{
		time.RFC3339Nano,
		"2006-01-02T15:04:05.0000000Z07:00",
		"2006-01-02T15:04:05Z07:00",
	}
	for _, l := range layouts {
		if t, err := time.Parse(l, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

func buildXPath(ids []int, from, to time.Time, withTime bool) string {
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, fmt.Sprintf("EventID=%d", id))
	}
	cond := "(" + strings.Join(parts, " or ") + ")"
	if withTime {
		cond += fmt.Sprintf(" and TimeCreated[@SystemTime>='%s' and @SystemTime<='%s']",
			from.UTC().Format("2006-01-02T15:04:05.000Z"),
			to.UTC().Format("2006-01-02T15:04:05.000Z"))
	}
	return "*[System[" + cond + "]]"
}

func queryEvents(channel string, ids []int, from, to time.Time, handle func(evtxEvent)) (int, error) {
	q := buildXPath(ids, from, to, true)
	out, err := execOut(14*time.Second, "wevtutil", "qe", channel, "/q:"+q,
		"/f:xml", "/rd:true", "/c:5000")
	if err != nil {
		// 某些系统的 XPath 对 TimeCreated 属性支持不一致：退化为只按 ID 查，由 Go 侧过滤时间
		q = buildXPath(ids, from, to, false)
		out, err = execOut(14*time.Second, "wevtutil", "qe", channel, "/q:"+q,
			"/f:xml", "/rd:true", "/c:5000")
		if err != nil {
			return 0, err
		}
	}
	dec := xml.NewDecoder(bytes.NewReader(out))
	n := 0
	for {
		tok, terr := dec.Token()
		if terr == io.EOF || terr != nil {
			break
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "Event" {
			continue
		}
		var e evtxEvent
		if derr := dec.DecodeElement(&e, &se); derr != nil {
			continue
		}
		n++
		handle(e)
	}
	return n, nil
}

type eventMapper func(e evtxEvent, d map[string]string, t time.Time, emit func(TimelineEvent))

func collectChannel(name, channel string, ids []int, c CollectContext, m eventMapper, emit func(TimelineEvent)) SourceStatus {
	produced := 0
	n, err := queryEvents(channel, ids, c.From, c.To, func(e evtxEvent) {
		t := parseEventTime(e.System.TimeCreated.SystemTime)
		if t.IsZero() {
			return
		}
		lt := t.Local()
		if lt.Before(c.From.Add(-time.Minute)) || lt.After(c.To.Add(time.Minute)) {
			return
		}
		m(e, e.fields(), lt, func(te TimelineEvent) {
			produced++
			emit(te)
		})
	})
	st := src(name, produced, err, "该时段无记录（可能未启用对应审核，或日志已滚动覆盖）")
	if err == nil && n > 0 && produced == 0 {
		st.Note = "有原始事件但都被过滤（多为系统噪音）"
	}
	return st
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v = strings.TrimSpace(v); v != "" && v != "-" {
			return v
		}
	}
	return ""
}

func logonFailureReason(s string) string {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "0XC000006A":
		return "密码错误"
	case "0XC000006D":
		return "用户名或密码错误"
	case "0XC0000234":
		return "账户已锁定"
	case "0XC000006E":
		return "账户受限"
	case "0XC0000071":
		return "密码已过期"
	case "0XC0000072":
		return "账户被禁用"
	case "0XC000015B":
		return "未授予该登录类型"
	case "0XC0000193":
		return "账户已过期"
	default:
		return s
	}
}

// ---------------------------------------------------------------- Security

var securityIDs = []int{
	4624, 4625, 4634, 4647, 4648, 4672, 4800, 4801, 4802, 4803,
	4778, 4779, 4720, 4726, 4732, 4719, 1102, 4697, 4698,
	4688, 4663, 5140, 4656,
}

func collectSecurityLog(c CollectContext, emit func(TimelineEvent)) SourceStatus {
	return collectChannel("安全日志 Security", "Security", securityIDs, c, mapSecurity, emit)
}

func mapSecurity(e evtxEvent, d map[string]string, t time.Time, emit func(TimelineEvent)) {
	id := e.System.EventID
	user := firstNonEmpty(d["TargetUserName"], d["SubjectUserName"], d["User"])
	raw := rawFrom(d, "TargetUserName", "TargetDomainName", "TargetLogonId", "LogonType",
		"IpAddress", "AuthenticationPackageName", "LogonProcessName", "Status", "SubStatus",
		"ProcessName", "CommandLine", "ObjectName", "ServiceName", "TaskName", "MemberName")
	srcName := fmt.Sprintf("Security.evtx / %d", id)

	switch id {
	case 4624:
		lt := d["LogonType"]
		if lt == "5" { // 服务登录是纯系统噪音
			return
		}
		title := "登录成功"
		switch lt {
		case "7":
			title = "解锁成功（锁屏后重新进入桌面）"
		case "2":
			title = "本地登录（在键盘前登录）"
		case "10":
			title = "远程桌面登录"
		case "11":
			title = "缓存交互登录"
		case "3":
			title = "网络登录（访问共享/服务）"
		case "4":
			title = "批处理登录"
		}
		detail := "账户 " + user + " · 登录类型 " + lt
		if p := d["AuthenticationPackageName"]; p != "" {
			detail += " · 认证包 " + p
		}
		if ip := d["IpAddress"]; ip != "" && ip != "-" && ip != "::1" && ip != "127.0.0.1" {
			detail += " · 来源 " + ip
		}
		if lt == "7" {
			detail += " · 默认就能查到的「输密码解锁」证据；不区分密码 / PIN / 指纹"
		}
		emit(ev(t, TypeSession, LevelConfirmed, title, detail, srcName, raw))
	case 4625:
		reason := logonFailureReason(firstNonEmpty(d["Status"], d["SubStatus"]))
		detail := "账户 " + user + " · " + reason
		if ip := d["IpAddress"]; ip != "" && ip != "-" {
			detail += " · 来源 " + ip
		}
		detail += " · 连续出现通常意味着有人在试密码"
		emit(ev(t, TypeSession, LevelConfirmed, "登录失败（密码输错等）", detail, srcName, raw))
	case 4634, 4647:
		label := "会话结束"
		if id == 4647 {
			label = "用户主动注销"
		}
		emit(ev(t, TypeSession, LevelConfirmed, label, "账户 " + user + " · 可用 TargetLogonId 与登录事件配对算会话时长", srcName, raw))
	case 4648:
		emit(ev(t, TypeChange, LevelConfirmed, "使用显式凭据（runas / 计划任务）",
			"目标账户 " + user + " · 发起进程 " + firstNonEmpty(d["ProcessName"], "-"), srcName, raw))
	case 4672:
		emit(ev(t, TypeSession, LevelConfirmed, "管理员权限会话建立", "账户 " + user + " 获得特殊权限", srcName, raw))
	case 4800:
		emit(ev(t, TypeSession, LevelConfirmed, "工作站被锁定",
			"账户 " + user + " · 该事件默认不记录，出现说明本机已开启 Other Logon/Logoff 审核", srcName, raw))
	case 4801:
		emit(ev(t, TypeSession, LevelConfirmed, "工作站被解锁",
			"账户 " + user + " · 与 4800 配对可得精确锁屏时长", srcName, raw))
	case 4802, 4803:
		label := "屏保启动"
		if id == 4803 {
			label = "屏保被关闭"
		}
		emit(ev(t, TypeSession, LevelConfirmed, label, "账户 " + user, srcName, raw))
	case 4778, 4779:
		label := "远程会话重连"
		if id == 4779 {
			label = "远程会话断开"
		}
		emit(ev(t, TypeSession, LevelConfirmed, label,
			"账户 " + user + " · 会话 " + firstNonEmpty(d["SessionName"], "-"), srcName, raw))
	case 4720:
		emit(ev(t, TypeChange, LevelConfirmed, "创建了新账户", "账户 " + user, srcName, raw))
	case 4726:
		emit(ev(t, TypeChange, LevelConfirmed, "删除了账户", "账户 " + user, srcName, raw))
	case 4732:
		emit(ev(t, TypeChange, LevelConfirmed, "账户被加入安全组（可能提权）",
			"成员 " + firstNonEmpty(d["MemberName"], user) + " · 组 " + firstNonEmpty(d["TargetUserName"], "-"), srcName, raw))
	case 4719:
		emit(ev(t, TypeTamper, LevelConfirmed, "系统审核策略被更改",
			"有人调整了审计开关——这会影响之后能查到什么", srcName, raw))
	case 1102:
		emit(ev(t, TypeTamper, LevelConfirmed, "安全日志被清空",
			"有人主动清除 Security 日志；清日志这个动作本身留下 1102，是重要的反取证线索", srcName, raw))
	case 4697:
		emit(ev(t, TypeChange, LevelConfirmed, "安装了系统服务",
			"服务 " + firstNonEmpty(d["ServiceName"], "-") + " · 路径 " + firstNonEmpty(d["ServiceFileName"], "-"), srcName, raw))
	case 4698:
		emit(ev(t, TypeChange, LevelConfirmed, "创建了计划任务",
			"任务 " + firstNonEmpty(d["TaskName"], "-"), srcName, raw))
	case 4688:
		emit(ev(t, TypeProgram, LevelConfirmed, "进程创建（进程审核）",
			firstNonEmpty(d["NewProcessName"], "-") + " · 命令行 " + truncStr(d["CommandLine"], 200),
			srcName, raw))
	case 4663:
		emit(ev(t, TypeFile, LevelConfirmed, "文件/对象被访问（对象访问审核）",
			firstNonEmpty(d["ObjectName"], "-") + " · 进程 " + firstNonEmpty(d["ProcessName"], "-"), srcName, raw))
	case 5140:
		emit(ev(t, TypeNetwork, LevelConfirmed, "网络共享被访问",
			"共享 " + firstNonEmpty(d["ShareName"], "-") + " · 来源 " + firstNonEmpty(d["IpAddress"], "-"), srcName, raw))
	}
}

// ---------------------------------------------------------------- System

var systemIDs = []int{6005, 6006, 6009, 1074, 42, 107, 109, 1, 7045, 104}

func collectSystemLog(c CollectContext, emit func(TimelineEvent)) SourceStatus {
	return collectChannel("系统日志 System", "System", systemIDs, c, mapSystem, emit)
}

func mapSystem(e evtxEvent, d map[string]string, t time.Time, emit func(TimelineEvent)) {
	id := e.System.EventID
	provider := e.System.Provider.Name
	raw := rawFrom(d, "param1", "param2", "param3", "param4", "param5",
		"ServiceName", "ImagePath", "WakeSourceText", "SleepTime", "WakeTime")
	srcName := fmt.Sprintf("System.evtx / %d", id)
	switch id {
	case 6005:
		emit(ev(t, TypeSession, LevelConfirmed, "开机（事件日志服务启动）", "常用作开机时间近似值", srcName, raw))
	case 6006:
		emit(ev(t, TypeSession, LevelConfirmed, "关机（事件日志服务停止）", "非正常断电离场可能没有这条", srcName, raw))
	case 6009:
		emit(ev(t, TypeSession, LevelConfirmed, "系统启动", "含系统版本信息", srcName, raw))
	case 1074:
		emit(ev(t, TypeSession, LevelConfirmed, "关机 / 重启被发起",
			"发起进程 " + firstNonEmpty(d["param1"], d["param4"], "-") + " · 原因 " + firstNonEmpty(d["param2"], "-"),
			srcName, raw))
	case 42:
		emit(ev(t, TypeSession, LevelConfirmed, "系统进入睡眠", "睡眠期间一般不会有人操作", srcName, raw))
	case 107:
		emit(ev(t, TypeSession, LevelConfirmed, "系统从睡眠恢复", "可用作「有人开始用电脑」的边界", srcName, raw))
	case 109:
		emit(ev(t, TypeSession, LevelConfirmed, "内核电源转换（关机/重启启动）", "", srcName, raw))
	case 1:
		if !strings.Contains(provider, "Power-Troubleshooter") {
			return
		}
		emit(ev(t, TypeSession, LevelConfirmed, "系统被唤醒",
			"唤醒源 " + firstNonEmpty(d["WakeSourceText"], "-"), srcName, raw))
	case 7045:
		emit(ev(t, TypeChange, LevelConfirmed, "安装了服务（服务控制管理器）",
			"服务 " + firstNonEmpty(d["ServiceName"], "-") + " · 映像 " + firstNonEmpty(d["ImagePath"], "-"), srcName, raw))
	case 104:
		emit(ev(t, TypeTamper, LevelConfirmed, "System 日志被清空",
			"有人清除了系统日志，属于反取证线索", srcName, raw))
	}
}

// ---------------------------------------------------------------- 远程会话

func collectRDSessionLog(c CollectContext, emit func(TimelineEvent)) SourceStatus {
	n := 0
	for _, ch := range []struct {
		name string
		ids  []int
	}{
		{"Microsoft-Windows-TerminalServices-LocalSessionManager/Operational", []int{21, 22, 24, 25}},
		{"Microsoft-Windows-TerminalServices-RemoteConnectionManager/Operational", []int{1149}},
	} {
		cnt, err := queryEvents(ch.name, ch.ids, c.From, c.To, func(e evtxEvent) {
			t := parseEventTime(e.System.TimeCreated.SystemTime)
			if t.IsZero() {
				return
			}
			lt := t.Local()
			if lt.Before(c.From.Add(-time.Minute)) || lt.After(c.To.Add(time.Minute)) {
				return
			}
			d := e.fields()
			label := "远程会话事件"
			switch e.System.EventID {
			case 21:
				label = "远程会话登录"
			case 22:
				label = "远程会话 Shell 启动"
			case 24:
				label = "远程会话断开"
			case 25:
				label = "远程会话重连"
			case 1149:
				label = "远程桌面认证成功"
			}
			n++
			emit(ev(lt, TypeSession, LevelConfirmed, label,
				"用户 " + firstNonEmpty(d["User"], d["param1"], "-") +
					" · 来源 " + firstNonEmpty(d["Address"], d["param2"], "-"),
				ch.name + " / " + fmt.Sprint(e.System.EventID), rawFrom(d, "User", "Address", "SessionID", "param1", "param2")))
		})
		if err != nil {
			return src("远程会话日志", n, err, "该通道不存在或未启用")
		}
		_ = cnt
	}
	return src("远程会话日志", n, nil, "该时段无 RDP / 远程会话记录")
}

// ---------------------------------------------------------------- USB 插拔事件

func collectUSBEventLog(c CollectContext, emit func(TimelineEvent)) SourceStatus {
	return collectChannel("USB 插拔事件（DriverFrameworks）",
		"Microsoft-Windows-DriverFrameworks-UserMode/Operational",
		[]int{2003, 2004, 2005, 2010, 2100, 2101, 2102}, c, mapUSBEvent, emit)
}

func mapUSBEvent(e evtxEvent, d map[string]string, t time.Time, emit func(TimelineEvent)) {
	label := ""
	switch e.System.EventID {
	case 2003:
		label = "USB 设备接入"
	case 2100, 2102:
		label = "USB 设备移除"
	case 2004, 2005, 2010, 2101:
		label = "USB 设备状态变化"
	default:
		return
	}
	dev := firstNonEmpty(d["DeviceInstanceId"], d["DeviceInstance"], d["param1"], d["DriverName"])
	emit(ev(t, TypeDevice, LevelConfirmed, label,
		"设备 " + truncStr(dev, 160) + " · 精确到秒的连接/移除记录（该日志通道默认未启用，出现说明已开）",
		fmt.Sprintf("DriverFrameworks-UserMode / %d", e.System.EventID),
		rawFrom(d, "DeviceInstanceId", "param1", "param2", "DriverName", "SessionId")))
}

// ---------------------------------------------------------------- PowerShell

func collectPowerShellLog(c CollectContext, emit func(TimelineEvent)) SourceStatus {
	return collectChannel("PowerShell 脚本日志", "Microsoft-Windows-PowerShell/Operational",
		[]int{4103, 4104}, c, mapPowerShell, emit)
}

func mapPowerShell(e evtxEvent, d map[string]string, t time.Time, emit func(TimelineEvent)) {
	label := "PowerShell 脚本块执行"
	if e.System.EventID == 4103 {
		label = "PowerShell 模块调用"
	}
	script := firstNonEmpty(d["ScriptBlockText"], d["param1"], d["Payload"])
	emit(ev(t, TypeProgram, LevelConfirmed, label,
		"脚本片段：" + truncStr(script, 160),
		fmt.Sprintf("PowerShell/Operational / %d", e.System.EventID),
		rawFrom(d, "ScriptBlockText", "Path", "param1")))
}

// ---------------------------------------------------------------- 软件安装

func collectAppInstallLog(c CollectContext, emit func(TimelineEvent)) SourceStatus {
	return collectChannel("软件安装日志（MsiInstaller）", "Application",
		[]int{1033, 1034, 11707, 11724}, c, mapMsi, emit)
}

func mapMsi(e evtxEvent, d map[string]string, t time.Time, emit func(TimelineEvent)) {
	if !strings.Contains(strings.ToLower(e.System.Provider.Name), "msiinstaller") {
		return
	}
	label := "软件安装 / 卸载事件"
	switch e.System.EventID {
	case 1033:
		label = "已安装软件（MSI）"
	case 1034:
		label = "已卸载软件（MSI）"
	case 11707:
		label = "安装完成（MSI）"
	case 11724:
		label = "卸载完成（MSI）"
	}
	name := firstNonEmpty(d["param1"], d["ProductName"], d["param2"])
	emit(ev(t, TypeChange, LevelConfirmed, label, truncStr(name, 180),
		fmt.Sprintf("Application / MsiInstaller %d", e.System.EventID),
		rawFrom(d, "param1", "param2", "param3", "param4")))
}

// ---------------------------------------------------------------- Sysmon（若已安装）

func collectSysmonLog(c CollectContext, emit func(TimelineEvent)) SourceStatus {
	return collectChannel("Sysmon 事件（若已安装）", "Microsoft-Windows-Sysmon/Operational",
		[]int{1, 11, 15, 23, 26}, c, mapSysmon, emit)
}

func mapSysmon(e evtxEvent, d map[string]string, t time.Time, emit func(TimelineEvent)) {
	label, typ := "Sysmon 事件", TypeProgram
	switch e.System.EventID {
	case 1:
		label, typ = "进程创建（Sysmon）", TypeProgram
	case 11:
		label, typ = "文件被创建（Sysmon）", TypeFile
	case 15:
		label, typ = "下载流写入（Zone.Identifier）", TypeNetwork
	case 23, 26:
		label, typ = "文件被删除（Sysmon）", TypeDelete
	}
	detail := firstNonEmpty(d["Image"], d["TargetFilename"], d["param1"])
	if cl := d["CommandLine"]; cl != "" {
		detail += " · " + truncStr(cl, 160)
	}
	emit(ev(t, typ, LevelConfirmed, label, detail,
		fmt.Sprintf("Sysmon/Operational / %d", e.System.EventID),
		rawFrom(d, "Image", "CommandLine", "TargetFilename", "User", "ProcessId")))
}
