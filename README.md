# QuickDock 插件集合

> 我开发的所有 QuickDock **非内置插件**的集中仓库。QuickDock 是 Windows 桌面效率工具（[主仓库](https://github.com/parieses/quickdock)），插件提供「命令面板命令」与「独立前端页面」两种形态，安装即生效、无需重启。

[![插件数](https://img.shields.io/badge/插件-79-blue)](#插件分类总览)
[![分类](https://img.shields.io/badge/分类-11-9cf)](#插件分类总览)
[![平台](https://img.shields.io/badge/平台-Windows-0078D6)](#安装方法)
[![许可证](https://img.shields.io/badge/license-MIT-green)](#许可证)
[![在线市场](https://img.shields.io/badge/市场-QuickDock%20Online-ff69b4)](#在线市场)

---

## 项目简介

这个仓库收集了我为 QuickDock 编写的 **79 个插件**，覆盖开发调试、办公效率、网络/安全、系统工具、内容创作与休闲游戏等场景。所有插件：

- **本地优先**：数据处理默认在本机完成，需要联网的插件会在说明里标出（如 `breach-check` 查泄露库、`image-uploader` 传图床）。
- **权限透明**：每个插件在 `plugin.json` 静态声明所需权限（`network` / `filesystem` / `clipboard` / `shell` / `process` / `processKill`），安装时展示、fail-closed。
- **一键发布**：推送代码后 CI 自动打包 zip 并生成在线市场索引，应用内「在线市场」随即可见。
- **仅支持 Windows**（当前发布策略，见下方「发布流程」说明）。

---

## 最近更新

> 以下日期取自真实提交记录。更早的变更见 [Releases](https://github.com/parieses/quickdock-plugins/releases) 与各插件 `plugin.json` 的 `changelog`。

- **2026-10-21 · database 标签页右键菜单（参考 VS Code）**：顶部标签栏任意标签上右键即可「关闭 / 关闭其他 / 关闭左侧 / 关闭右侧 / 全部关闭」，一次开很多表和查询标签时不用再挨个点 ×；无可关闭的标签时对应项置灰，菜单靠近窗口边缘自动翻转，Esc 或点击空白处关闭。
- **2026-10-21 · 修复 http-client 打开即报错白屏（`self.boot is not a function`）**：首屏加载入口 `boot` 误写在 `createApp` 顶层 option 上，不会挂到组件实例，`mounted` 的 1.5s 兜底计时器和宿主 `plugin:init` 回调都取不到它；已将 `boot` 挪入 `methods` 并给 `plugin:init` 回调补 `typeof` 防御。
- **2026-10-21 · Ctrl+K 命令直达与死命令清理（database / git-workbench / http-client 等 12 个插件）**：命令面板选中的插件命令此前只会打开插件首页、跳不到对应页面。现在多标签插件的前端接管 `plugin:init` 下发的 `command` 做页面直达（database 连接列表/库表树/执行查询/保存连接、git-workbench 六个标签页、http-client 发送/历史/项目、cron 解释页、密码生成页等）；同时全仓核对 137 条命令声明，移除 10 条前后端均无监听、从面板点了没反应的真死命令（如「测试连接」「解析变量」「查看 WiFi 密码」），其余按「开门命令 / 首页即目标页」分类保留。
- **2026-10-21 · 新增 `net-traffic`（实时流量监控）与 `app-manager`（应用管理）**：流量监控走 `iphlpapi` 每秒采样、自动合并同一网卡的多个 NDIS 绑定行避免重复计数，连接表按进程归集；应用管理枚举三处注册表卸载项（中文名经 Base64 回传避免代码页乱码），MSI 程序白名单内直接唤起、其余给复制命令/打开目录/跳转系统设置。
- **2026-10-20 · 数据回显与持久化修复（http-client / calcsheet / database / control-receiver）**：修复 http-client「数据已在 plugin-data 里却不显示」（首屏目录列表传空项目 ID 只返回空集；以及库文件名字认错导致静默新建空库）、calcsheet「稿纸保存后重新打开是空白」（后端数据返回前就渲染、返回后又不再重渲染）；并为 database、control-receiver 补上「同目录异名库复用」与「配置损坏时回退历史有效配置」，四处均新增启动诊断日志便于定位。
- **2026-10-20 · 统一前端桥接注入（全仓库 77 个插件）**：移除各插件内联桥接脚本副本（共 126 份），改由宿主统一注入（`qdHostCall` / 主题 / 异常上报 / `qdCrypto`）；插件体积减小、消除多份实现漂移，并即时获得主题跟随与 `host.crypto.*` 能力。
- **2026-10-20 · 共享代码单一来源治理**：`qd-theme.css` / `components.css` 收敛为 `templates/` 单一来源，由 `tools/sync_shared.py` 同步、`tools/check_shared.py` 做 CI 漂移门禁。
- **2026-10-19 · 全仓库统一运行期日志**；插件数据统一迁到 `plugin-data`，`database` / `http-client` 连接与超时优化。
- **2026-10-15 · 全仓库主题跟随修复**，并修复启动项管理致命 bug。
- **2026-10-13 · 新增 `control-receiver`**（手机屏幕镜像接收控制端，配套发送端见 [QuestPhoneMirror](https://github.com/parieses/QuestPhoneMirror)）。

---

## 插件分类总览

本仓库共 **79** 个插件，分为 **11** 类。点击分类跳转：

- [开发者日常](#开发者日常)（24）— 数据库 / HTTP / JSON / JWT / Git / 正则 / 时间 ……
- [开发工具](#开发工具)（2）— 接口压测 / cURL 转代码
- [办公效率](#办公效率)（6）— PDF / 思维导图 / 表格转换 / 单位换算 / 字数 / 人民币大写
- [内容创作](#内容创作)（3）— ASCII 艺术 / 图文卡片 / 视频转 GIF
- [文件工具](#文件工具)（5）— 批量重命名 / EXIF / 图片工坊 / 图床 / 照片库
- [网络工具](#网络工具)（9）— IP 归属地 / 站点审计 / 网络诊断 / 实时流量 / WebSocket / 子域名枚举 ……
- [安全工具](#安全工具)（5）— 泄露查询 / 加密工具箱 / 登录爆破自检 / OTP / 密码库
- [系统工具](#系统工具)（6）— 磁盘分析 / 应用管理 / 垃圾清理 / 服务 / 启动项 / 系统信息
- [效率工具](#效率工具)（4）— Hosts / OCR / 端口 / WiFi
- [教育学习](#教育学习)（2）— 背单词卡 / 字帖打印
- [游戏娱乐](#游戏娱乐)（13）— 2048 / 扫雷 / 数独 / 俄罗斯方块 / 五子棋 / 中国象棋 ……

<!--PLUGINS_TABLE_START-->

## 开发者日常（24）

| 插件 | 版本 | 说明 |
|------|------|------|
| [API Mock 服务](./api-mock/) | 1.0.5 | 本地 HTTP 接口 Mock 服务：可视化配置路由规则（方法/路径/状态码/响应体/延迟），一键启动本地服务，实时查看请求日志，联调前端与第三方对接无需真实后端 |
| [Crontab 解释器](./cron-explainer/) | 1.0.4 | 解析 cron 表达式（含义/下次执行/小时分布），并可可视化生成表达式、实时预览下次执行时间 |
| [Emoji 搜索](./emoji-search/) | 1.0.4 | 搜索 Emoji 并一键复制到剪贴板 |
| [Git 工作台](./git-workbench/) | 1.0.5 | Git 仓库一体化工作台：仓库浏览、二分定位 bug 引入提交、三方合并冲突可视化解决、代码演化时间轴、历史改写（改作者/删敏感文件）、仓库体检与知识孤岛识别。 |
| [HTTP 客户端](./http-client/) | 1.1.0 | 轻量 HTTP 请求调试客户端：项目管理请求、目录与文档树、环境变量与 {{var}} 替换、请求历史重放、Postman 集合导入。数据独立存储。 |
| [JSON 工具箱](./json-toolbox/) | 1.0.4 | JSON 编辑器（格式化/折叠/编辑）、JSON → TypeScript / Go、JSON ↔ YAML / TOML / XML 互转 |
| [JWT 解码器](./jwt-decoder/) | 1.0.3 | 解码 JWT Token，查看 Header/Payload，验证过期时间 |
| [Markdown 渲染](./markdown-preview/) | 1.0.3 | 实时渲染 Markdown（GFM：标题/列表/表格/任务列表/引用）+ 代码高亮，一键复制为 HTML |
| [UUID & 假数据生成器](./data-generator/) | 1.0.2 | UUID v4 / v7 生成（批量、可大写）＋ 随机字符串 / 整数 / 字节 ＋ 测试假数据（姓名 / 手机 / 邮箱 / 公司 / 地址 / 身份证(测试) / 日期 / 网址 / 用户名 / 人员），全部在本机生成，不联网、不上传 |
| [二维码工具](./qrcode/) | 1.0.3 | 文本/URL 生成二维码，支持保存 PNG；从图片识别二维码内容 |
| [代码卡片](./code-card/) | 1.0.2 | 把代码渲染成高颜值分享卡片：语法高亮、主题背景、窗口装饰，一键导出 PNG |
| [代码格式化](./formatter/) | 1.0.3 | 通用代码压缩/美化（JS / CSS / HTML 自动检测）与 SQL 格式化合二为一 |
| [代码运行沙箱](./code-runner/) | 1.0.4 | 在浏览器内运行 JS / HTML 片段并实时预览，可选加载 Pyodide 运行 Python。无需安装任何环境。 |
| [包版本速查](./package-check/) | 1.1.3 | 输入包名，并发查询 npm / PyPI / Composer / Go 四个仓库的版本信息；并基于 OSV.dev 查询包版本的已知漏洞（CVE / 安全公告） |
| [对比工具](./compare/) | 1.0.3 | 文件/图片对比（元数据 + 图片预览 + 文本内容差异）与文本块逐行 Diff 合二为一 |
| [打字练习](./type-trainer/) | 1.0.4 | 开发者向打字训练器：英文 / 中文 / 代码 / 导入四种模式，支持导入 TXT 字库，实时统计 WPM 与准确率，本地记录历史与最佳成绩 |
| [数据库客户端](./database/) | 1.7.1 | 轻量数据库连接与查询工具：MySQL / SQLite / Redis 连接管理、SQL 与 Redis 命令执行、库表浏览器（库→表/视图→字段、Redis 键树）、Redis 键类型感知详情与增删改（string/hash/list/set/zset）、TTL 与 DB 管理、结果网格内联行编辑、勾选批量删除（支持全选匹配筛选的行）。数据独立存储。 |
| [文件哈希](./hash-calc/) | 1.0.7 | 计算文件的 MD5/SHA1/SHA256/SHA512 摘要，结果一键复制 |
| [文本工具箱](./text-encoder/) | 1.0.5 | Base64 / URL / HTML 编解码，MD5 / SHA1 / SHA256 / SHA512 哈希与 HMAC 签名，Base64 图片识别预览，2/8/10/16 进制互转与字节单位换算 |
| [时间转换](./time-converter/) | 1.0.3 | Unix 时间戳 / ISO 8601 / 中文日期 / 相对时间互转，支持任意时区偏移输出 |
| [正则提取工具](./regex-extractor/) | 1.0.4 | 正则提取与替换：匹配高亮、分组捕获、反向引用替换（$1/$2）、一键复制结果 |
| [计算稿纸](./calcsheet/) | 1.0.6 | 兼具草稿纸自由度与电子表格智能的多行计算工具，支持行号引用、变量定义、函数计算 |
| [邮箱足迹查询](./mail-check/) | 1.0.4 | 邮箱足迹与有效性检查：全量 123 站探测（参考 holehe 适配，Gravatar/GitHub/ProtonMail/Spotify 等已校准，其余逐步补），语法 / MX / SMTP 有效性验证 |
| [颜色工具](./color-converter/) | 1.0.4 | 颜色格式互转（HEX / RGB / HSL）+ 屏幕取色，支持常见英文色名识别 |

## 开发工具（2）

| 插件 | 版本 | 说明 |
|------|------|------|
| [cURL 转代码](./curl-converter/) | 1.0.2 | 把 curl 命令解析成 Python / Go / JavaScript / PHP 请求代码，也支持把 fetch、requests 代码反向转回 curl |
| [接口压测](./api-loadtest/) | 1.0.2 | 功能丰富的 HTTP 接口压测工具：支持并发/时长双模式、自定义 Header 与 Body、实时 QPS 与延迟分布(p50/p90/p95/p99)、状态码分布、错误率统计与结果一键导出 |

## 办公效率（6）

| 插件 | 版本 | 说明 |
|------|------|------|
| [MD 表格转换器](./md-table-converter/) | 1.0.3 | Markdown 表格与 CSV / JSON / HTML 四种格式互转，自动识别输入格式，写文档、导数据的顺手小工具 |
| [PDF 工具箱](./pdf-toolkit/) | 1.0.3 | PDF 处理工具箱：合并/拆分/压缩/加水印/提取图片，无需安装 Adobe Acrobat |
| [人民币大写](./rmb-upper/) | 1.0.2 | 数字金额转中文大写（壹贰叁…），财务报销、开票、合同的刚需小工具 |
| [单位换算器](./unit-converter/) | 1.0.2 | 长度 / 面积 / 体积 / 重量 / 温度 / 速度 / 数据存储 / 时间 / 压力 / 能量 / 功率 / 角度 共 12 类单位实时互转，输入一个值即列出该类别全部换算结果 |
| [字数统计](./word-count/) | 1.0.1 | 实时统计文本的字数、词数、行数、段落数、字节数与阅读时长，支持中英文混合。 |
| [思维导图](./mindmap/) | 1.0.2 | 把 Markdown 大纲 / 缩进列表实时渲染成思维导图，自动分层配色、可点击折叠分支、支持缩放与导出 PNG / SVG |

## 内容创作（3）

| 插件 | 版本 | 说明 |
|------|------|------|
| [ASCII 艺术](./ascii-art/) | 1.0.4 | 图片转字符画（亮度映射到字符集），或输入文字生成 ASCII 艺术字（figlet 风格）。 |
| [图文卡片](./md2card/) | 1.4.5 | Markdown 与纯文字一键变身高颜值图文卡片：双模式、9 种文字模板、28 套主题、自定义背景图、尺寸预设、字体/圆角/阴影/留白可调、水印、二维码、自定义主题，自动分页、实时预览，导出长图 / PNG / HTML，复制图片，支持宿主打印通道。 |
| [视频转 GIF](./video-to-gif/) | 1.0.3 | 浏览器内把视频片段转换为 GIF 动图，基于 ffmpeg.wasm，可调起止时间、尺寸与帧率，数据不出本机。 |

## 文件工具（5）

| 插件 | 版本 | 说明 |
|------|------|------|
| [EXIF 查看](./exif-viewer/) | 1.0.4 | 选择图片（JPEG/PNG）查看拍摄时间、相机/镜头、参数与 GPS 经纬度等 EXIF 信息 |
| [图床上传](./image-uploader/) | 1.0.2 | 选择本地图片，读取后通过宿主网络能力上传到图床（freeimage.host / imgbb），返回可访问的图片链接，链接可一键复制。不上传任何其它文件。 |
| [图片工坊](./image-studio/) | 1.0.3 | 仿 Squoosh 实时对比：左右预览对比，质量滑块、缩放(锁定比例/百分比/预设)、旋转/翻转、亮度/对比度/饱和度调整，实时预览文件大小与压缩率 |
| [批量重命名](./batch-rename/) | 1.0.1 | 选择一个文件夹，按前缀/后缀/查找替换/正则/序号/扩展名/大小写规则预览并重命名文件，全部通过宿主文件系统能力执行，不离开本机 |
| [照片库浏览](./photo-library/) | 1.4.1 | 选择本地文件夹，网格缩略图浏览照片，按日期/文件夹筛选，点击查看 EXIF 拍摄信息与参数 |

## 网络工具（9）

| 插件 | 版本 | 说明 |
|------|------|------|
| [IP 归属地](./geoip/) | 1.0.3 | 查询 IP 的地理位置、运营商、时区与坐标，支持指定 IP 或查询本机公网 IP。 |
| [WebSocket 测试](./ws-tester/) | 1.0.4 | 连接 ws/wss 服务，发送消息并实时查看返回的帧，支持多连接与历史 |
| [qpm接收端](./control-receiver/) | 1.0.14 | 手机屏幕镜像接收端 + 控制端：连接 QuestPhoneMirror 发送端实时显示画面，鼠标点击/拖拽反向操控真机（配套手机端发送端见 https://github.com/parieses/QuestPhoneMirror） |
| [子域名枚举](./subdomain-enum/) | 1.0.2 | 被动收集域名子域名（证书透明日志 CertSpotter / crt.sh + HackerTarget + urlscan + rapiddns + AlienVault OTX），可选并发解析 A 记录筛选存活 |
| [实时流量监控](./net-traffic/) | 1.0.0 | 每秒采样的本机流量监控：实时上下行速率与 5 分钟曲线、按网卡拆分（自动合并同一网卡的多个 NDIS 绑定，不重复计数）、本次会话累计与峰值，并列出 IPv4 连接对应的进程、本地/远端地址与状态。 |
| [目录探测](./dir-buster/) | 1.0.2 | 对指定目标 URL 用内置常见路径字典进行轻量探测（自用）：并发受限、可配扩展名，采用异步会话模型实时返回命中的非 404 路径。仅探测你授权的目标，内置字典、不递归 |
| [站点审计箱](./site-audit/) | 1.0.5 | 将 WHOIS 查询、SSL 证书检查、DNS 查询、DNS 传播检查、HTTP 状态码速查、HTTP 安全头审计六个站点工具合并为单一插件，按需切换标签页，共享一个原生子进程 |
| [网络诊断箱](./netdiag/) | 1.0.2 | 将 Ping 监视、路由追踪、局域网扫描、IP 归属地、端口指纹五个网络工具合并为单一插件，按需切换标签页，共享一个原生子进程 |
| [网速测试](./speed-test/) | 1.0.2 | 测量网络下载速率与延迟：流式下载测速（支持自定义测速节点 URL），实时显示速率与进度，采用异步会话模型规避宿主执行超时 |

## 安全工具（5）

| 插件 | 版本 | 说明 |
|------|------|------|
| [OTP 验证器](./otp/) | 1.0.2 | 本地 TOTP 两步验证码生成器（兼容 Google Authenticator），密钥仅存本机，离线可用。 |
| [密码与加密工具箱](./crypto-toolbox/) | 1.0.3 | 随机密码 / 口令生成（字符集可配、熵值与强度评估）＋ AES-GCM / AES-CBC 加解密 ＋ RSA-OAEP 密钥对与加解密 ＋ PBKDF2 密钥派生，全部在本机完成，不联网、不上传 |
| [密码库](./vault/) | 1.0.4 | 本地加密密码库：主密码解锁，AES-GCM 加密存储，支持导出/导入加密备份文件。数据不出本机。 |
| [泄露查询](./breach-check/) | 1.0.3 | 基于 Have I Been Pwned 检查密码是否出现在泄露库中（k-匿名，密码不出本机）；邮箱泄露需自备 API Key。 |
| [登录爆破测试器](./login-tester/) | 1.0.3 | 对自身网站登录接口进行密码库撞库/爆破安全自检，支持并发、限速与锁定检测。仅用于你拥有或已授权的站点。 |

## 系统工具（6）

| 插件 | 版本 | 说明 |
|------|------|------|
| [Windows 垃圾清理](./junk-cleaner/) | 1.0.4 | 扫描并清理系统垃圾文件：临时文件/更新缓存/缩略图缓存/预读取/崩溃转储等，安全只读扫描+确认后删除 |
| [启动项管理](./startup-manager/) | 1.0.2 | 查看并启停本机开机启动项，覆盖注册表 Run 键与「启动」文件夹中的快捷方式。 |
| [应用管理](./app-manager/) | 1.0.0 | 本机已安装应用一览：名称 / 版本 / 发布者 / 占用体积 / 安装日期 / 安装范围（所有用户 / 当前用户 / 32 位），支持搜索与按体积、安装日期排序。MSI 程序可直接唤起卸载，自带卸载器的程序提供命令复制、安装目录直达与系统卸载页入口。 |
| [服务管理](./service-manager/) | 1.0.2 | 查看本机 Windows 服务列表，按名称筛选，并启动 / 停止 / 重启服务。 |
| [磁盘分析器](./disk-analyzer/) | 1.0.5 | 可视化磁盘空间占用分析工具，类似 SpaceSniffer，支持树图展示目录结构 |
| [系统信息](./system-info/) | 1.0.2 | neofetch 式一键采集本机操作系统、CPU、内存、显卡、磁盘与运行时长等信息。 |

## 效率工具（4）

| 插件 | 版本 | 说明 |
|------|------|------|
| [Hosts 管理器](./hosts-manager/) | 1.0.5 | 管理系统 hosts 文件条目，一键启用/禁用/新增 |
| [OCR 文字识别](./ocr-tool/) | 2.0.5 | 基于 PaddleOCR (ONNX) 的离线文字识别，支持中英文，首次使用自动下载约 178MB 模型（ModelScope 镜像），之后完全离线运行，跨 Windows / macOS / Linux。 |
| [WiFi 管理器](./wifi-manager/) | 1.0.4 | 查看网络列表、WiFi 密码、连接状态 |
| [端口检查器](./port-scanner/) | 1.0.7 | 检查端口占用，显示进程名和 PID |

## 教育学习（2）

| 插件 | 版本 | 说明 |
|------|------|------|
| [字帖打印](./hanzi-copybook/) | 1.0.8 | 汉字描红字帖生成器：支持任意汉字定制、拼音与笔顺显示、田字格/米字格排版，面向小学生规范书写与笔顺习惯养成，一键导出/打印 |
| [背单词卡](./flashcards/) | 1.0.4 | 本地单词闪卡与间隔重复记忆（SM-2 算法），内置常用词库，支持自定义词单导入。 |

## 游戏娱乐（13）

| 插件 | 版本 | 说明 |
|------|------|------|
| [2048](./game-2048/) | 1.0.3 | 经典数字合并：方向键 / WASD 滑动，相同数字相撞合成翻倍，冲击 2048。本地记录最佳分数。 |
| [中国象棋](./xiangqi/) | 1.0.3 | 单机中国象棋，完整走子与吃子规则，含基于局面评估的简易 AI 对手。 |
| [五子棋](./game-gomoku/) | 1.0.2 | 经典五子棋（Gomoku）：15×15 棋盘，五子连珠即胜。内置模式评分人机（攻防合一 + 威胁优先），先手为先。 |
| [人生重开模拟器](./game-liferestart/) | 1.0.1 | 选定天赋、分配初始属性，重开一段随机人生，看遍悲欢离合。不满意就再来一次。 |
| [代码贪吃蛇](./game-code-snake/) | 1.1.3 | 代码主题贪吃蛇：在「编辑器」里操控蛇吞掉散落的代码片段，每吃一个解锁一条开发小贴士（JS / PHP / Go / SQL 等）。本地记录分数与收集数。 |
| [俄罗斯方块](./game-tetris/) | 1.0.3 | 经典俄罗斯方块：7 种方块、旋转、消行、等级加速。方向键 / X 旋转，空格硬降。本地记录最高分。 |
| [小黑屋](./game-adarkroom/) | 1.1.2 | 极简文字冒险：添柴升温、建造设施、外出探索，还会偶遇流浪商人做交易，在黑暗中一步步壮大村落。点击即可推进。 |
| [扫清黑洞](./game-proxx/) | 1.0.3 | 真·六边形扫雷：每格最多 6 个邻居，数字提示周围黑洞数量，别踩中。 |
| [扫雷](./minesweeper/) | 1.0.3 | 复刻 Windows 原生扫雷：经典灰色立体棋盘、笑脸重置、LED 雷数/计时器，支持初级/中级/高级与自定义 |
| [数独](./game-sudoku/) | 1.0.4 | 数独：随机生成题目 + 唯一解校验，简单/中等/困难/专家四档难度。笔记、提示、冲突高亮，计时与最佳成绩。 |
| [终端 Roguelike](./game-roguelike/) | 1.0.2 | 纯键盘 ASCII 地牢探险：撞向怪物即攻击，拾取药水武器护甲，逐层下探，斩杀第 6 层的巨龙。永久死亡，比拼最深层数。 |
| [贪吃蛇](./game-snake/) | 1.1.3 | 经典贪吃蛇：方向键 / WASD 控制，吃食物变长，撞墙或咬到自己即结束。本地记录最高分与最长长度。 |
| [黑白棋](./game-reversi/) | 1.0.2 | 经典黑白棋（Othello / Reversi）：8×8 棋盘，夹住对方棋子即翻转。内置 minimax + α-β 剪枝人机，难度可调。 |

<!--PLUGINS_TABLE_END-->

---

## 安装方法

QuickDock「插件管理」页：

1. **在线市场一键安装（推荐）**：切到「在线市场」标签，自动拉取本仓库发布的插件列表，点「安装」即可，无需手动下载；已装插件显示「升级」按钮（仅允许升级或同版本重装，拒绝降级）。
2. **从文件安装**：点「从文件安装」选择 **Windows 安装包 zip**（Release 资产）；
3. **拖拽安装**：直接把 zip 拖进插件管理页。

安装即生效、无需重启；升级覆盖安装自动备份旧版本，失败自动回滚。
在线市场的插件数据来自本仓库 GitHub Pages 的 [`index.json`](https://parieses.github.io/quickdock-plugins/index.json)；zip 下载见 [Releases](https://github.com/parieses/quickdock-plugins/releases) 或[插件中心主页](https://parieses.github.io/quickdock-plugins/)。

## 在线市场（应用内一键安装）

QuickDock 内置「在线市场」标签页，让用户免下载直接安装/升级本仓库的插件。工作原理：

1. 主程序访问 `https://parieses.github.io/quickdock-plugins/index.json`（本仓库 GitHub Pages 自动部署的机器可读索引）。
2. 索引由 `gen_site.py` 在每次 push 到 `main` 时自动生成，`pages.yml` 负责部署 `site/` 目录到 GitHub Pages。
3. 每个插件的 `downloads.windows` 指向 `https://github.com/parieses/quickdock-plugins/releases/latest/download/<id-小写横线>-windows.zip`，即 `build-plugins.yml` 自动打包并发布到 `latest` Release 的 zip 资产（如 `io-github-parieses-disk-analyzer-windows.zip`）。
4. **安装校验**：仅允许**新安装**与**升级**；**同版本重装**放行；**降级**拒绝（防止回退到更旧版本）。

> 因此发布一个新插件或新版本，只需推送代码：CI 会自动打包 zip 到 `latest` Release、并重新生成部署 `index.json`，应用内市场随即可见。

## 快速开始

```bash
# 打包（输出到根目录 dist/，按平台区分 zip）
python build.py disk-analyzer            # 默认按 plugin.json 的 platforms 字段逐一打包
python build.py disk-analyzer --platform windows   # 仅打指定平台（须在插件 platforms 内）
python build.py disk-analyzer --platform all       # 全平台（windows/darwin/linux，取与 platforms 交集）
python build.py disk-analyzer --skip-build         # 跳过编译仅打包

# 生成插件中心主页 + 市场索引（扫描各插件 plugin.json）
#   site/index.html -> GitHub Pages 主页
#   site/index.json -> 机器可读市场索引，QuickDock 应用内"在线市场"拉取
python gen_site.py

# 重新生成 README / 开发文档里的插件表格（扫描各插件 plugin.json）
python gen_tables.py
```

## 目录结构

```
quickdock-plugins/
├── build.py            # 插件打包工具（默认 Windows，支持交叉编译）
├── gen_site.py         # 插件中心主页生成器
├── gen_tables.py       # README / 开发文档插件表格生成器
├── dist/               # 打包产物（windows zip，gitignore 不入库）
├── site/index.html     # GitHub Pages 主页（自动生成）
├── site/index.json     # 机器可读市场索引（自动生成，应用内在线市场拉取）
├── disk-analyzer/      # 示例插件（native Go + 前端页面）
├── tools/              # 共享代码同步 / 检查 / 插件静态审计脚本
├── templates/          # 共享前端样式单一来源（qd-theme.css / components.css）
└── .github/workflows/
    ├── build-plugins.yml   # push 自动打包（diff 变更插件 → windows）
    └── pages.yml           # push 自动部署插件主页
```

---

# 插件开发规范

> 插件开发的完整格式说明与开发文档（含 Host Methods、文件/目录选择、调试部署陷阱、桥接协议细节等）见 [plugin-dev-guide.md](./plugin-dev-guide.md)。

## 单个插件的目录结构

一个插件一个子目录，约定：

```
<插件目录>/
├── plugin.json          # 插件清单（必填，文件名固定）
├── frontend/            # 前端页面（frontend.enabled=true 时必填）
│   └── index.html       # 单页应用（entry 指定）
├── icon.svg             # 图标（manifest icon 字段引用，主页/插件管理用）
├── *.go + go.mod        # native 插件 Go 源码（runtime=native）
└── README.md            # 插件说明（可选）
```

## 三种 runtime

| runtime | 说明 | 适用 |
|---------|------|------|
| `native` | 独立子进程二进制（Go 等），经 **stdin/stdout JSON-RPC** 通信 | 需要系统能力/性能（文件系统、网络、进程） |
| `goja` | 内嵌 JS 沙箱执行 `entry` 指定的 js 文件 | 纯逻辑工具（格式化/转换/计算） |
| `none` | 无后端，仅前端页面 | 纯 UI 类插件 |

## 前端桥接（要点）

页面运行在 sandbox iframe 中（无 `allow-same-origin`），与宿主全部通过 `postMessage`。宿主会**自动注入桥接脚本**，页面可直接调用全局 API：

- `window.qdHostCall(method, params?)` → 调用任意宿主 Host 方法（受 `permissions` 管控）
- `window.qdHttp(opts)` → HTTP 便捷封装，走宿主代发绕过 iframe CORS
- `window.qdPickFile / qdPickFolder / qdReadFile` → 原生文件选择/读取（**不要用 `<input type=file>`**）
- `window.qdPrint({html})` → 系统打印（**不要直接 `window.print()`**，沙箱里只会打印出整个应用外壳）

## 打包与校验规则

`build.py` 打出的 zip 必须满足安装器校验（`internal/plugin/installer.go`）：

- **`plugin.json` 必须位于 zip 根部**（build.py 强制写入根）
- 解压总大小 ≤ 100 MB、单文件 ≤ 50 MB
- 文件名不允许 `..` 路径穿越（自动防护）
- `id/name/version/backend.runtime` 为必填；native 必须给 `entry`；runtime 仅支持 `native/goja/none`
- native 二进制按平台命名：Windows `<entry>.exe`，darwin/linux `<entry>`（宿主 CreateProcess 自动补 `.exe`，unix 直接执行）
- 打包自动排除：`.go`/`go.mod`/`go.sum`/`build.py`/`*.zip`（防自递归）/`git/`/`.build/`；只保留当前平台的入口二进制

## 发布流程

1. 改 `plugin.json` 的 `version`（并相应更新代码）。
2. `git add . && git commit && git push`
3. 推送后 **CI 自动完成发布**，无需手工操作：
   - `build-plugins.yml`：diff 出本次变更的插件目录 → `build.py` 按各插件 `plugin.json` 的 `platforms` 字段逐一打包 → 发布到名为 `latest` 的 GitHub Release。
   - `pages.yml`：运行 `gen_site.py` 重新生成 `site/index.html` + `site/index.json` → 部署到 GitHub Pages。
4. 应用内「在线市场」与插件中心主页会随之更新（GitHub Pages 有缓存，通常几十秒到几分钟内生效）。

> **当前发布策略（2026-09-23 起）：仅发布 Windows 安装包**。仓库内全部插件的 `platforms` 均已收敛为 `["windows"]`，CI 不再产出 darwin/linux 资产。`--platform darwin` 会被 `build.py` 拒绝（不在声明内）。恢复某插件多平台：把该插件 `platforms` 改回并推送即可。

## 开发约定

- 插件 ID 域名反写 `io.github.<账号>.<插件名>`，一个插件一个子目录
- native 命令名用 **kebab-case**（manifest 声明与分发统一），兼容点号写法
- 长任务（>2s）务必后台执行 + 立即返回 + 轮询，避开宿主 30s RPC 超时
- 调试输出写 stderr，绝不污染 stdout 协议
- 新版插件包名保持 `<id转-小写>-<platform>.zip`，覆盖安装即升级

更多实现细节参考 [QuickDock 主仓库](https://github.com/parieses/quickdock)（`internal/plugin/` 目录为协议实现）。

---

## 许可证

本项目插件以 **MIT 许可证** 开源，详见 [LICENSE](./LICENSE)。
