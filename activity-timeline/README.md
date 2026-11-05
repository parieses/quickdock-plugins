# 电脑活动痕迹 / Activity Timeline

> 电脑被别人借用之后，按日期段还原「这段时间这台机器大概发生了什么」。

一个**只读**的 Windows 取证聚合插件：把散落在事件日志、注册表、文件系统里的行为痕迹归一化成一条时间轴，并明确标注每条记录的证据来源与可信度。

## 能查到什么

| 类别 | 证据源 |
|---|---|
| 登录 / 解锁 / 开关机 | Security `4624`（`LogonType=7` 即输密码解锁）、`4625`（密码输错）、`4800/4801`、`4634/4647`、`4648`、`4672`、`4688`；System `6005/6006/6009/1074/42/107/109/1`；RDP 会话日志 `21/22/24/25/1149` |
| 跑过什么程序 | `Prefetch`、`UserAssist`（ROT13 + 启动次数）、`BAM/DAM`、PowerShell `4103/4104`、Sysmon `1` |
| 插过什么 U 盘 | `USBSTOR` 序列号 + 键 LastWriteTime、`DriverFrameworks-UserMode 2003/2100`、`setupapi.dev.log` 首次安装时间 |
| 打开过什么文件 | `Recent\*.lnk`（启发式解析目标路径）、Jump Lists、`RecentDocs`/`OpenSavePidlMRU`/`ShellBags` 体检、`4663` 文件访问审计 |
| 删除了什么 | 各盘符 `$Recycle.Bin` 的 `$I` 记录（原路径 + 删除时间 + 大小）、Sysmon `23/26` |
| 系统变更 | `4720/4726/4732` 账户与提权、`7045/4697` 服务、`4698` 计划任务、MsiInstaller 安装/卸载 |
| 网络 / 外传 | `5140` 共享访问、Sysmon `15` 下载流、RDP 来源 IP |
| 有没有清痕迹 | `1102` 安全日志被清、`104` System 日志被清、`4719` 审核策略被改 |

## 默认查不到的（报告里会明确写）

- **复制 / 移动了哪个文件**：资源管理器默认不记录，只能靠 `$UsnJrnl`、回收站、LNK 卷序列号间接推断。
- **剪贴板内容、输入了什么、屏幕画面**。
- **用的是密码还是 PIN / 指纹**：`4624` 只有认证包名称，不记凭据类型。
- **过去没开过的审计**：`4800/4801`、`4688`、`4663` 默认不记录，事后开启也补不回来。

## 使用

在插件窗口选择起止日期（默认近 7 天）→ 点「查询」。右侧「体检」面板会说明哪些证据源可用、哪些因权限/未启用审核而读不到、安全日志最早能追溯到哪天。可导出 CSV 或自包含 HTML 报告。

Ctrl+K 里输入 `2026-10-28~2026-11-05` 也可直接带日期段打开。

## 证据强度

- **确证**：事件日志直接记录（如 4624/1102/回收站 `$I`）。
- **交叉印证**：多个残留痕迹共同支持（如 Prefetch、USBSTOR 键时间）。
- **推断**：只证明「有可能」（如跳转列表更新、LNK 关联 U 盘）。**推断项不能当确证**，`插了 U 盘 ≠ 拷了文件`。

## 权限与边界

- 全程只读：不开启审计、不改注册表、不装驱动、不常驻、不联网。
- 调用 `wevtutil` / `reg` / `auditpol` / `fsutil` 做只读查询，已在 `plugin.json` 的 `permissions.process` 里声明。
- 读取 Security 日志、注册表 HKLM 部分键、`C:\Windows\Prefetch` 需要**管理员权限**；权限不足时对应来源会显示「不可用」而不是编造结果。
- 只用于查自己的设备；涉及他人或公司设备请先取得授权。

## 开发

```bash
go build ./...                        # 本模块编译（go.mod 零依赖）
python build.py activity-timeline     # 打包
python tools/check_plugins.py activity-timeline
python gen_icons.py --gen activity-timeline
```

字形源在仓库 `glyphs.py` 的 `GLYPH["activity-timeline"]`，语义色 `GROUP["activity-timeline"]="amber"`。
设计方案见 `.workbuddy/design/activity-timeline.md`（本地忽略目录，不入库）。
