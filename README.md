# QuickDock 插件集合

存放我开发的所有 QuickDock 非内置插件。QuickDock 是 Windows 桌面效率工具（[主仓库](https://github.com/parieses/quickdock)），插件提供命令面板命令与独立前端页面两种形态。

## 插件列表

<!--PLUGINS_TABLE_START-->

| 插件 | ID | 版本 | 说明 |
|------|----|------|------|
| [接口压测](./api-loadtest/) | `io.github.parieses.api-loadtest` | 1.0.0 | 功能丰富的 HTTP 接口压测工具：支持并发/时长双模式、自定义 Header 与 Body、实时 QPS 与延迟分布(p50/p90/p95/p99)、状态码分布、错误率统计与结果一键导出 |
| [API Mock 服务](./api-mock/) | `io.github.parieses.api-mock` | 1.0.0 | 本地 HTTP 接口 Mock 服务：可视化配置路由规则（方法/路径/状态码/响应体/延迟），一键启动本地服务，实时查看请求日志，联调前端与第三方对接无需真实后端 |
| [批量重命名](./batch-rename/) | `io.github.parieses.batch-rename` | 1.0.0 | 选择一个文件夹，按前缀/后缀/查找替换/正则/序号/扩展名/大小写规则预览并重命名文件，全部通过宿主文件系统能力执行，不离开本机 |
| [计算稿纸](./calcsheet/) | `io.github.parieses.calcsheet` | 1.0.0 | 兼具草稿纸自由度与电子表格智能的多行计算工具，支持行号引用、变量定义、函数计算 |
| [代码卡片](./code-card/) | `io.github.parieses.code-card` | 1.0.0 | 把代码渲染成高颜值分享卡片：语法高亮、主题背景、窗口装饰，一键导出 PNG |
| [颜色工具](./color-converter/) | `io.github.parieses.color-converter` | 1.0.1 | 颜色格式互转（HEX / RGB / HSL）+ 屏幕取色，支持常见英文色名识别 |
| [对比工具](./compare/) | `io.github.parieses.compare` | 1.0.1 | 文件/图片对比（元数据 + 图片预览 + 文本内容差异）与文本块逐行 Diff 合二为一 |
| [Crontab 解释器](./cron-explainer/) | `io.github.parieses.cron-explainer` | 1.0.1 | 解析 cron 表达式（含义/下次执行/小时分布），并可可视化生成表达式、实时预览下次执行时间 |
| [密码与加密工具箱](./crypto-toolbox/) | `io.github.parieses.crypto-toolbox` | 1.0.0 | 随机密码 / 口令生成（字符集可配、熵值与强度评估）＋ AES-GCM / AES-CBC 加解密 ＋ RSA-OAEP 密钥对与加解密 ＋ PBKDF2 密钥派生，全部在本机完成，不联网、不上传 |
| [cURL 转代码](./curl-converter/) | `io.github.parieses.curl-converter` | 1.0.0 | 把 curl 命令解析成 Python / Go / JavaScript / PHP 请求代码，也支持把 fetch、requests 代码反向转回 curl |
| [UUID & 假数据生成器](./data-generator/) | `io.github.parieses.data-generator` | 1.0.0 | UUID v4 / v7 生成（批量、可大写）＋ 随机字符串 / 整数 / 字节 ＋ 测试假数据（姓名 / 手机 / 邮箱 / 公司 / 地址 / 身份证(测试) / 日期 / 网址 / 用户名 / 人员），全部在本机生成，不联网、不上传 |
| [数据库客户端](./database/) | `io.github.parieses.database` | 1.1.0 | 轻量数据库连接与查询工具：MySQL / SQLite / Redis 连接管理、SQL 与 Redis 命令执行、库表浏览器（库→表/视图→字段、Redis 键树）、Redis 键类型感知详情与增删改（string/hash/list/set/zset）、TTL 与 DB 管理、结果网格内联行编辑。数据独立存储。 |
| [目录探测](./dir-buster/) | `io.github.parieses.dir-buster` | 1.0.0 | 对指定目标 URL 用内置常见路径字典进行轻量探测（自用）：并发受限、可配扩展名，采用异步会话模型实时返回命中的非 404 路径。仅探测你授权的目标，内置字典、不递归 |
| [磁盘分析器](./disk-analyzer/) | `io.github.parieses.disk-analyzer` | 1.0.0 | 可视化磁盘空间占用分析工具，类似 SpaceSniffer，支持树图展示目录结构 |
| [Emoji 搜索](./emoji-search/) | `io.github.parieses.emoji-search` | 1.0.1 | 搜索 Emoji 并一键复制到剪贴板 |
| [EXIF 查看](./exif-viewer/) | `io.github.parieses.exif-viewer` | 1.0.1 | 选择图片（JPEG/PNG）查看拍摄时间、相机/镜头、参数与 GPS 经纬度等 EXIF 信息 |
| [代码格式化](./formatter/) | `io.github.parieses.formatter` | 1.0.0 | 通用代码压缩/美化（JS / CSS / HTML 自动检测）与 SQL 格式化合二为一 |
| [Git 工作台](./git-workbench/) | `io.github.parieses.git-workbench` | 1.0.1 | Git 仓库一体化工作台：仓库浏览、二分定位 bug 引入提交、三方合并冲突可视化解决、代码演化时间轴、历史改写（改作者/删敏感文件）、仓库体检与知识孤岛识别。 |
| [字帖打印](./hanzi-copybook/) | `io.github.parieses.hanzi-copybook` | 1.0.4 | 汉字描红字帖生成器：支持任意汉字定制、拼音与笔顺显示、田字格/米字格排版，面向小学生规范书写与笔顺习惯养成，一键导出/打印 |
| [文件哈希](./hash-calc/) | `io.github.parieses.hash-calc` | 1.0.1 | 计算文件的 MD5/SHA1/SHA256/SHA512 摘要，结果一键复制 |
| [Hosts 管理器](./hosts-manager/) | `io.github.parieses.hosts-manager` | 1.0.0 | 管理系统 hosts 文件条目，一键启用/禁用/新增 |
| [HTTP 客户端](./http-client/) | `io.github.parieses.http-client` | 1.0.1 | 轻量 HTTP 请求调试客户端：项目管理请求、目录与文档树、环境变量与 {{var}} 替换、请求历史重放、Postman 集合导入。数据独立存储。 |
| [图片工坊](./image-studio/) | `io.github.parieses.image-studio` | 1.0.0 | 仿 Squoosh 实时对比：左右预览对比，质量滑块、缩放(锁定比例/百分比/预设)、旋转/翻转、亮度/对比度/饱和度调整，实时预览文件大小与压缩率 |
| [图床上传](./image-uploader/) | `io.github.parieses.image-uploader` | 1.0.1 | 选择本地图片，读取后通过宿主网络能力上传到图床（freeimage.host / imgbb），返回可访问的图片链接，链接可一键复制。不上传任何其它文件。 |
| [JSON 工具箱](./json-toolbox/) | `io.github.parieses.json-toolbox` | 1.0.0 | JSON 编辑器（格式化/折叠/编辑）、JSON → TypeScript / Go、JSON ↔ YAML / TOML / XML 互转 |
| [Windows 垃圾清理](./junk-cleaner/) | `io.github.parieses.junk-cleaner` | 1.0.0 | 扫描并清理系统垃圾文件：临时文件/更新缓存/缩略图缓存/预读取/崩溃转储等，安全只读扫描+确认后删除 |
| [JWT 解码器](./jwt-decoder/) | `io.github.parieses.jwt-decoder` | 1.0.0 | 解码 JWT Token，查看 Header/Payload，验证过期时间 |
| [登录爆破测试器](./login-tester/) | `io.github.parieses.login-tester` | 1.0.0 | 对自身网站登录接口进行密码库撞库/爆破安全自检，支持并发、限速与锁定检测。仅用于你拥有或已授权的站点。 |
| [邮箱足迹查询](./mail-check/) | `io.github.parieses.mail-check` | 1.0.1 | 邮箱足迹与有效性检查：全量 123 站探测（参考 holehe 适配，Gravatar/GitHub/ProtonMail/Spotify 等已校准，其余逐步补），语法 / MX / SMTP 有效性验证 |
| [Markdown 渲染](./markdown-preview/) | `io.github.parieses.markdown-preview` | 1.0.0 | 实时渲染 Markdown（GFM：标题/列表/表格/任务列表/引用）+ 代码高亮，一键复制为 HTML |
| [MD 表格转换器](./md-table-converter/) | `io.github.parieses.md-table` | 1.0.0 | Markdown 表格与 CSV / JSON / HTML 四种格式互转，自动识别输入格式，写文档、导数据的顺手小工具 |
| [思维导图](./mindmap/) | `io.github.parieses.mindmap` | 1.0.0 | 把 Markdown 大纲 / 缩进列表实时渲染成思维导图，自动分层配色、可点击折叠分支、支持缩放与导出 PNG / SVG |
| [网络诊断箱](./netdiag/) | `io.github.parieses.netdiag` | 1.0.0 | 将 Ping 监视、路由追踪、局域网扫描、IP 归属地、端口指纹五个网络工具合并为单一插件，按需切换标签页，共享一个原生子进程 |
| [OCR 文字识别](./ocr-tool/) | `io.github.parieses.ocr-tool` | 2.0.0 | 基于 PaddleOCR (ONNX) 的离线文字识别，支持中英文，首次使用自动下载约 178MB 模型（ModelScope 镜像），之后完全离线运行，跨 Windows / macOS / Linux。 |
| [包版本速查](./package-check/) | `io.github.parieses.package-check` | 1.1.0 | 输入包名，并发查询 npm / PyPI / Composer / Go 四个仓库的版本信息；并基于 OSV.dev 查询包版本的已知漏洞（CVE / 安全公告） |
| [PDF 工具箱](./pdf-toolkit/) | `io.github.parieses.pdf-toolkit` | 1.0.0 | PDF 处理工具箱：合并/拆分/压缩/加水印/提取图片，无需安装 Adobe Acrobat |
| [端口检查器](./port-scanner/) | `io.github.parieses.port-scanner` | 1.0.2 | 检查端口占用，显示进程名和 PID |
| [二维码工具](./qrcode/) | `io.github.parieses.qrcode` | 1.0.0 | 文本/URL 生成二维码，支持保存 PNG；从图片识别二维码内容 |
| [正则提取工具](./regex-extractor/) | `io.github.parieses.regex-extractor` | 1.0.0 | 正则提取与替换：匹配高亮、分组捕获、反向引用替换（$1/$2）、一键复制结果 |
| [人民币大写](./rmb-upper/) | `io.github.parieses.rmb-upper` | 1.0.0 | 数字金额转中文大写（壹贰叁…），财务报销、开票、合同的刚需小工具 |
| [站点审计箱](./site-audit/) | `io.github.parieses.site-audit` | 1.0.1 | 将 WHOIS 查询、SSL 证书检查、DNS 查询、DNS 传播检查、HTTP 状态码速查、HTTP 安全头审计六个站点工具合并为单一插件，按需切换标签页，共享一个原生子进程 |
| [网速测试](./speed-test/) | `io.github.parieses.speed-test` | 1.0.0 | 测量网络下载速率与延迟：流式下载测速（支持自定义测速节点 URL），实时显示速率与进度，采用异步会话模型规避宿主执行超时 |
| [子域名枚举](./subdomain-enum/) | `io.github.parieses.subdomain-enum` | 1.0.0 | 被动收集域名子域名（证书透明日志 CertSpotter / crt.sh + HackerTarget + urlscan + rapiddns + AlienVault OTX），可选并发解析 A 记录筛选存活 |
| [文本工具箱](./text-encoder/) | `io.github.parieses.text-encoder` | 1.0.2 | Base64 / URL / HTML 编解码，MD5 / SHA1 / SHA256 / SHA512 哈希与 HMAC 签名，Base64 图片识别预览，2/8/10/16 进制互转与字节单位换算 |
| [时间转换](./time-converter/) | `io.github.parieses.time-converter` | 1.0.0 | Unix 时间戳 / ISO 8601 / 中文日期 / 相对时间互转，支持任意时区偏移输出 |
| [打字练习](./type-trainer/) | `io.github.parieses.type-trainer` | 1.0.1 | 开发者向打字训练器：英文 / 中文 / 代码 / 导入四种模式，支持导入 TXT 字库，实时统计 WPM 与准确率，本地记录历史与最佳成绩 |
| [单位换算器](./unit-converter/) | `io.github.parieses.unit-converter` | 1.0.0 | 长度 / 面积 / 体积 / 重量 / 温度 / 速度 / 数据存储 / 时间 / 压力 / 能量 / 功率 / 角度 共 12 类单位实时互转，输入一个值即列出该类别全部换算结果 |
| [WiFi 管理器](./wifi-manager/) | `io.github.parieses.wifi-manager` | 1.0.0 | 查看网络列表、WiFi 密码、连接状态 |
| [WebSocket 测试](./ws-tester/) | `io.github.parieses.ws-tester` | 1.0.1 | 连接 ws/wss 服务，发送消息并实时查看返回的帧，支持多连接与历史 |
<!--PLUGINS_TABLE_END-->

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
```

## 目录结构

```
quickdock-plugins/
├── build.py            # 插件打包工具（默认 Windows，支持交叉编译）
├── gen_site.py         # 插件中心主页生成器
├── dist/               # 打包产物（windows zip，gitignore 不入库）
├── site/index.html     # GitHub Pages 主页（自动生成）
├── site/index.json     # 机器可读市场索引（自动生成，应用内在线市场拉取）
├── disk-analyzer/      # 示例插件（native Go + 前端页面）
└── .github/workflows/
    ├── build-plugins.yml   # push 自动打包（diff 变更插件 → windows）
    └── pages.yml           # push 自动部署插件主页
```

---

# 插件开发规范

> 插件开发的完整格式说明与开发文档（含 Host Methods、文件/目录选择、调试部署陷阱等）见 [plugin-dev-guide.md](./plugin-dev-guide.md)。

## 目录结构

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

## plugin.json 字段说明

以 `disk-analyzer/plugin.json` 为例：

```json
{
  "id": "io.github.parieses.disk-analyzer",
  "name": "磁盘分析器",
  "version": "0.1.0",
  "description": "可视化磁盘空间占用分析工具…",
  "author": "QuickDock",
  "icon": "icon.svg",
  "category": "系统工具",
  "platforms": ["windows", "darwin", "linux"],
  "backend": { "runtime": "native", "entry": "disk-analyzer", "args": [] },
  "frontend": { "enabled": true, "entry": "frontend/index.html", "width": 900, "height": 640 },
  "capabilities": ["command", "frontend"],
  "permissions": { "filesystem": true },
  "commands": [
    { "id": "disk-scan", "title": "扫描磁盘空间",
      "keywords": ["disk", "磁盘", "空间", "分析"], "aliases": ["磁盘分析"] }
  ]
}
```

| 字段 | 必填 | 说明 |
|------|------|------|
| `id` | ✅ | 全局唯一，**域名反写** `io.github.<你的账号>.<插件名>`；必须至少含一个点号 |
| `name` / `version` / `description` | ✅ | 展示信息 |
| `icon` | - | 图标相对路径（plugin.json 同目录），主页与管理页展示 |
| `author` / `category` | - | 展示信息 |
| `platforms` | - | 支持平台数组 `windows/darwin/linux`；**未声明 = 全平台**。加载时按当前平台过滤 |
| `backend.runtime` | ✅ | `native`（独立子进程二进制）/ `goja`（纯 JS）/ `none`（仅前端） |
| `backend.entry` | native 必填 | 可执行入口文件名（Windows 自动补 `.exe`，unix 直接执行） |
| `backend.args` | - | 启动附加参数 |
| `frontend.enabled` `entry` `width` `height` | - | 声明前端页面：`entry` 为 `frontend/index.html`，`width/height` 为窗口初始尺寸 |
| `capabilities` | - | 能力声明：`command`（命令面板）/ `frontend`（前端页面）等；有前端页面必须含 `frontend` |
| `permissions` | - | 权限声明（**静态白名单，安装时展示、fail-closed**）：`network`（HTTP 域名白名单）/ `filesystem`（布尔 `true`=仅对话框；对象 `{read:[...], write:[...]}`=按目录 scope 读写）/ `clipboard` / `shell`（可执行程序前缀白名单）/ `processKill`（布尔，允许结束进程）。详见 [plugin-dev-guide.md](./plugin-dev-guide.md) |
| `commands[]` | - | 命令面板条目：`id`（命令唯一标识）/ `title` / `keywords`（搜索关键词）/ `aliases`（别名） |
| `changelog` / `changelog_i18n` | - | 版本更新说明（Markdown 文本），展示在插件市场详情页与官网（`gen_site.py` 读取）；建议每次发版随改随写，避免全量重传 |

## 三种 runtime

| runtime | 说明 | 适用 |
|---------|------|------|
| `native` | 独立子进程二进制（Go 等），经 **stdin/stdout JSON-RPC** 通信 | 需要系统能力/性能（文件系统、网络、进程） |
| `goja` | 内嵌 JS 沙箱执行 `entry` 指定的 js 文件 | 纯逻辑工具（格式化/转换/计算） |
| `none` | 无后端，仅前端页面 | 纯 UI 类插件 |

## native 插件协议（最重要）

**JSON-RPC 2.0 over stdin/stdout，每行一个 JSON 对象**。宿主把请求写到插件 stdin（末尾 `\n`），插件把响应写到 stdout 并**必须 Flush**。

### 必须实现的方法

| 方法 | 说明 |
|------|------|
| `initialize` | 启动握手，**必须尽快响应** `{"status":"ready",...}`，否则超时判定加载失败 |
| `host.ping` | 健康检查，响应 `{"pong":true}` |
| `plugin.execute` | **唯一业务入口**，`params = {command, input}` |

### plugin.execute 参数约定

```json
{ "jsonrpc": "2.0", "id": 1, "method": "plugin.execute",
  "params": { "command": "disk-scan", "input": { "path": "C:\\", "limit": 60 } } }
```

- `command`：`plugin.json` `commands[].id`；前端还可能传点号写法（如 `disk.scan`），建议**内部把 `.` 替换为 `-`** 再分发
- `input`：前端透传的参数对象。**兼容格式**：命令面板会把字符串参数打包为 `input.text = JSON.stringify(实际参数)`，插件收到后应尝试解析 `input.text`（若以 `{` 开头则反序列化后合并进 input）——参考 disk-analyzer 的兼容解包

### 响应与错误

```json
{ "jsonrpc": "2.0", "id": 1, "result": { ... } }
{ "jsonrpc": "2.0", "id": 1, "error": { "code": -32603, "message": "..." } }
```

- 成功/失败都必须写 stdout 并 **`Flush`**（否则滞留在缓冲区，宿主等超时）
- 错误码沿用 JSON-RPC：`-32700` 解析错误 / `-32601` 未知方法 / `-32602` 参数错误 / `-32603` 内部错误
- **stdout 只准输出 JSON-RPC**；调试日志写 **stderr**（宿主会采集）。stdout 出现无法解析的行会被宿主静默忽略，但会打乱协议，务必不要混入

### 超时与限制

- 宿主调用默认超时 30s（`initialize` 更短，约 15s）——**长任务必须丢后台 goroutine，立即返回**，前端轮询进度（见 disk-analyzer 的 `scan-full` + `scan-status` 模式）
- stdout 单行上限 64 MiB，超出截断（大结果请分页/增量返回）
- 插件可主动向宿主发"通知"（无 id 请求）和"请求"（回调）

### 最小 Go 骨架

```go
package main

import ("bufio"; "encoding/json"; "os"; "strings")

func main() {
    r := bufio.NewReader(os.Stdin)
    for {
        line, err := r.ReadString('\n')
        if err != nil { break }
        var req struct {
            JSONRPC string          `json:"jsonrpc"`
            ID      int64           `json:"id"`
            Method  string          `json:"method"`
            Params  json.RawMessage `json:"params"`
        }
        if json.Unmarshal([]byte(strings.TrimSpace(line)), &req) != nil { continue }
        switch req.Method {
        case "initialize":   respond(req.ID, map[string]any{"status": "ready"})
        case "host.ping":    respond(req.ID, map[string]any{"pong": true})
        case "plugin.execute": /* 解析 {command, input} 并分发 */
        }
    }
}

func respond(id int64, result any) {
    b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
    os.Stdout.Write(append(b, '\n'))
    os.Stdout.Sync() // 或 bufio Writer Flush
}
```

## goja 插件

`backend.entry` 指向一个 JS 文件，宿主内嵌 JS 引擎加载。适合轻逻辑工具：

```json
"backend": { "runtime": "goja", "entry": "index.js" }
```

JS 侧注册方法（参考主仓库内置插件 json-toolbox 等的实现方式）；需系统能力请用 native。

## 前端页面（frontend）

`capabilities` 含 `frontend` 且 `frontend.enabled=true` 时，命令面板命中该插件命令就会打开独立窗口加载 `entry` 页面。

### 桥接协议（iframe + postMessage）

页面运行在 sandbox iframe 中（无 allow-same-origin），与宿主全部通过 `postMessage`：

**宿主 → 页面**：
| type | 说明 |
|------|------|
| `plugin:init` | 页面打开时携带 `{command, input}` 初始数据 |
| `plugin:result` | 执行结果 `{id, data \| error}`——成功数据在 `data` 字段（⚠️ 不是 result），失败为 error |
| `plugin:theme` | 深浅色主题 `{data.theme}` |

**页面 → 宿主**：
| type | 说明 |
|------|------|
| `plugin:execute` | 调用后端 `{id, command, input}`（native 走 plugin.execute） |
| `plugin:copy` | 复制文本 `{id, text}` → 宿主回 `plugin:copy-result {id, ok}` |
| `plugin:pickfile` | 原生文件选择 `{id, title?, filter?, pattern?}` → 宿主回 `plugin:pickfile-result {id, path|null}` |
| `plugin:readfile` | 读取选中文件 `{id, path}` → 宿主回 `plugin:readfile-result {id, payload|null}`，`payload = {type:'text'|'dataurl', content}` |
| `plugin:print` | 系统打印 `{id, html, page?}` → 宿主回 `plugin:print-result {id, ok, error?}`。`html` 为**完整 HTML 文档字符串**，宿主在顶层文档渲染后调系统打印（**不要自己 `window.print()`**，见下） |

### 宿主桥接脚本（自动注入）

宿主会向每个前端页面注入一段桥接脚本，页面可直接调用以下全局 API（无需自己 postMessage）：

| API | 说明 |
|------|------|
| `window.qdConfirm(msg)` / `window.qdAlert(msg)` | 宿主 toast 确认 / 提示 |
| `window.qdPickFile(opts?)` → `Promise<string|null>` | 原生文件选择；`opts={title?, filter?, pattern?}`，取消/失败返回 `null` |
| `window.qdPickFolder(opts?)` → `Promise<string|null>` | 原生目录选择；`opts={title?}`，取消/失败返回 `null` |
| `window.qdReadFile(path)` → `Promise<{type,content}|null>` | 读取 `qdPickFile`/`qdPickFolder` 选中的文件；文本→`{type:'text',content}`，图片/二进制→`{type:'dataurl',content}` |
| `window.qdHostCall(method, params?)` → `Promise<any>` | **通用宿主代发**：调用任意宿主 Host 方法（如 `host.fs.read` / `host.fs.list` / `http.post` / `host.mcp.call`），受插件 `permissions` 管控，返回解析后的结果 |
| `window.qdHttp(opts)` → `Promise<{status,ok,headers,body,truncated}>` | `qdHostCall` 的 HTTP 便捷封装：`opts={url, method?, headers?, body?}`；自动 JSON 序列化 body 并补 `Content-Type`，走宿主代发绕过 iframe CORS |
| `window.qdPrint(opts)` → `Promise<true>` | 系统打印：`opts={html, page?}`；`html` 是一份**完整 HTML 文档字符串**（自带全部 CSS），`page` 可选（如 `'A4'`）覆盖 `@page`。打印对话框关闭后 resolve |

> **选文件一律用 `qdPickFile`**：iframe 沙箱内的 `<input type=file>` 会触发宿主窗口失焦问题，且脚本无法精确控制。示例：`const p = await window.qdPickFile({filter:'JSON', pattern:'*.json'}); const res = p && await window.qdReadFile(p);`

> **打印一律用 `qdPrint`，不要在插件里直接 `window.print()`**：WebView2 / Chromium 的 `window.print()` 只作用于**顶层文档**，插件跑在沙箱 iframe 里，直接调打印出来的是**整个 QuickDock 应用**（暗色外壳）—— 用户拿到的是**一张白纸**。`qdPrint` 把 HTML 交给宿主，由宿主在顶层文档渲染后打印；打印期间宿主界面被隐藏、插件 CSS 被包进 `@media print` 隔离（不污染宿主），打完自动清理。完整说明见 [plugin-dev-guide.md](./plugin-dev-guide.md) 的「打印：`qdPrint`」小节。

### 样式约定

- 统一使用公共样式 `plugins/builtin/common.css` 的 `.p-*` 类，**不要每页重写**
- 深浅色由宿主桥接脚本统一处理（`data-theme`），页面无需自加监听

> 完整示例见 [disk-analyzer/frontend/index.html](./disk-analyzer/frontend/index.html) 与主仓库 `plugins/builtin/json-toolbox/frontend/index.html`。

## 打包与校验规则

`build.py` 打出的 zip 必须满足安装器校验（`internal/plugin/installer.go`）：

- **`plugin.json` 必须位于 zip 根部**（build.py 强制写入根）
- 解压总大小 ≤ 100 MB、单文件 ≤ 50 MB
- 文件名不允许 `..` 路径穿越（自动防护）
- `id/name/version/backend.runtime` 为必填；native 必须给 `entry`；runtime 仅支持 `native/goja/none`
- native 二进制按平台命名：Windows `<entry>.exe`，darwin/linux `<entry>`（宿主 CreateProcess 自动补 `.exe`，unix 直接执行）
- 打包自动排除：`.go`/`go.mod`/`go.sum`/`build.py`/`*.zip`（防自递归）/`.git/`/`.build/`；只保留当前平台的入口二进制

## 发布流程

1. 改 `plugin.json` 的 `version`（并相应更新代码）。
2. `git add . && git commit && git push`
3. 推送后 **CI 自动完成发布**，无需手工操作：
   - `build-plugins.yml`：diff 出本次变更的插件目录 → `build.py` 按各插件 `plugin.json` 的 `platforms` 字段逐一打包 → 发布到名为 `latest` 的 GitHub Release，资产即 `releases/latest/download/<id-小写横线>-<platform>.zip`（如 `io-github-parieses-disk-analyzer-windows.zip` / `-darwin.zip` / `-linux.zip`）。
   - `pages.yml`：运行 `gen_site.py` 重新生成 `site/index.html` + `site/index.json` → 部署到 GitHub Pages（即 `https://parieses.github.io/quickdock-plugins/`）。
4. 应用内「在线市场」与插件中心主页会随之更新（GitHub Pages 有缓存，通常几十秒到几分钟内生效）。

> 平台说明：`build.py` 默认按各插件 `plugin.json` 的 `platforms` 字段打包，`gen_site.py` 据此生成对应平台的下载项（`downloads` 与下载按钮）。`platforms` 声明了 darwin/linux 的插件必须有对应的可交叉编译源码（否则 `go build` 会在 CI 失败）——因此**多平台声明的 native 插件必须真正能跨平台编译**；纯 Windows 功能（如依赖 Windows 注册表/专属二进制的插件）请把 `platforms` 收敛为 `["windows"]`。临时只打某平台用 `python build.py <dir> --platform <plat>`。
>
> **当前发布策略（2026-09-23 起）：仅发布 Windows 安装包**。仓库内全部插件的 `platforms` 均已收敛为 `["windows"]`，故 CI 不再产出 darwin/linux 资产，主页与市场索引也只显示 Windows 下载项（`--platform darwin` 会被 `build.py` 拒绝：不在声明内）。恢复某插件多平台：把该插件 `platforms` 改回并推送即可，无需改动 `build.py` / `gen_site.py`。

## 开发约定

- 插件 ID 域名反写 `io.github.<账号>.<插件名>`，一个插件一个子目录
- native 命令名用 **kebab-case**（manifest 声明与分发统一），兼容点号写法
- 长任务（>2s）务必后台执行 + 立即返回 + 轮询，避开宿主 30s RPC 超时
- 调试输出写 stderr，绝不污染 stdout 协议
- 新版插件包名保持 `<id转-小写>-<platform>.zip`，覆盖安装即升级

更多实现细节参考 [QuickDock 主仓库](https://github.com/parieses/quickdock)（`internal/plugin/` 目录为协议实现）。