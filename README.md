# QuickDock 插件集合

存放我开发的所有 QuickDock 非内置插件。QuickDock 是 Windows 桌面效率工具（[主仓库](https://github.com/parieses/quickdock)），插件提供命令面板命令与独立前端页面两种形态。

## 插件列表

| 插件 | ID | 版本 | 说明 |
|------|----|------|------|
| [磁盘分析器](./disk-analyzer/) | `io.github.parieses.disk-analyzer` | 0.1.1 | 可视化磁盘空间占用分析，类似 SpaceSniffer，树图展示目录结构（Windows / macOS / Linux 三平台） |
| [Windows 垃圾清理](./junk-cleaner/) | `io.github.parieses.junk-cleaner` | 0.1.3 | 扫描并清理系统垃圾，18 类白名单（参考 ZyperWinOptimize）：更新/传递优化缓存、缩略图、网页缓存/Cookies、D3D/.NET/远程桌面缓存、崩溃转储与 WER、诊断/Defender/WinSxS 临时、系统临时/预读取、回收站（高危默认关）。安全只读扫描 + 确认后删除（仅 Windows）。确认弹窗改用 QuickDock 框架 modal 样式（替代原生 confirm/alert，高危项红色二次警告） |
| [接口压测](./api-loadtest/) | `io.github.parieses.api-loadtest` | 0.1.1 | 功能丰富的 HTTP 接口压测工具：并发/时长双模式、自定义 Header 与 Body、实时 QPS 与延迟分布(p50/p90/p95/p99)、状态码分布、错误率统计与结果一键导出 |
| [PDF 工具箱](./pdf-toolkit/) | `io.github.parieses.pdf-toolkit` | 0.1.7 | PDF 合并/拆分/压缩/加水印/提取图片，无需 Adobe Acrobat |
| [颜色工具](./color-converter/) | `io.github.parieses.color-converter` | 0.3.1 | 颜色格式互转（HEX/RGB/HSL + 常见英文色名识别）+ 屏幕取色（F8 取色 / ESC 取消） |
| [对比工具](./compare/) | `io.github.parieses.compare` | 0.2.2 | 文件/图片对比（元数据 + 预览 + 文本差异）与文本块逐行 Diff（点选改走原生对话框，拖拽保留） |
| [Crontab 解释器](./cron-explainer/) | `io.github.parieses.cron-explainer` | 0.2.1 | 解析 cron 表达式，显示可读描述、下次执行时间与可视化时间表 |
| [HTTP 状态码速查](./http-status/) | `io.github.parieses.http-status` | 0.2.1 | 查询 HTTP 状态码含义、描述和常见场景 |
| [cURL 转代码](./curl-converter/) | `io.github.parieses.curl-converter` | 0.1.0 | 把 curl 命令解析成 Python / Go / JavaScript / PHP 请求代码，也支持把 fetch、requests 代码反向转回 curl |
| [计算稿纸](./calcsheet/) | `io.github.parieses.calcsheet` | 0.2.0 | 兼具草稿纸自由度与电子表格智能的多行计算工具，支持行号引用、变量定义、函数计算（自内置插件迁移） |
| [Emoji 搜索](./emoji-search/) | `io.github.parieses.emoji-search` | 0.2.0 | 搜索 Emoji 并一键复制到剪贴板（自内置插件迁移） |
| [代码格式化](./formatter/) | `io.github.parieses.formatter` | 0.2.0 | 通用代码压缩/美化（JS / CSS / HTML 自动检测）与 SQL 格式化合二为一（自内置插件迁移） |
| [Markdown 渲染](./markdown-preview/) | `io.github.parieses.markdown-preview` | 0.3.0 | 真实 GFM 渲染（标题/列表/表格/任务列表/引用）+ 代码高亮（marked + highlight.js），复制 HTML（自内置插件迁移） |
| [二维码工具](./qrcode/) | `io.github.parieses.qrcode` | 0.2.1 | 文本/URL 生成二维码，支持保存 PNG；从图片识别二维码内容（自内置插件迁移，选图改走原生对话框） |
| [正则提取工具](./regex-extractor/) | `io.github.parieses.regex-extractor` | 0.2.1 | 正则提取与替换：匹配高亮、分组捕获、反向引用替换（$1/$2）、一键复制结果（自内置插件迁移） |
| [文本工具箱](./text-encoder/) | `io.github.parieses.text-encoder` | 0.3.0 | Base64 / URL / HTML 编解码，MD5 / SHA1 / SHA256 / SHA512 哈希与 HMAC 签名（纯 JS 自实现，无宿主依赖），Base64 图片预览（自内置插件迁移） |
| [时间转换](./time-converter/) | `io.github.parieses.time-converter` | 0.3.0 | Unix 时间戳 / ISO 8601 / 中文日期 / 相对时间互转，支持任意时区偏移输出（预设 + 自定义）（自内置插件迁移） |
| [JSON 工具箱](./json-toolbox/) | `io.github.parieses.json-toolbox` | 0.2.1 | JSON 编辑器（格式化/折叠/编辑）、JSON → TypeScript / Go、JSON ↔ YAML / TOML / XML 互转（自内置插件迁移，打开文件改走原生对话框） |
| [JWT 解码器](./jwt-decoder/) | `io.github.parieses.jwt-decoder` | 0.2.0 | 解码 JWT Token，查看 Header/Payload，验证过期时间（自内置插件迁移） |
| [Hosts 管理器](./hosts-manager/) | `io.github.parieses.hosts-manager` | 0.2.0 | 管理系统 hosts 文件条目，一键启用/禁用/新增（自内置插件迁移，自带 system-tools.exe） |
| [端口检查器](./port-scanner/) | `io.github.parieses.port-scanner` | 0.2.0 | 检查端口占用，显示进程名和 PID（自内置插件迁移，自带 system-tools.exe） |
| [WiFi 管理器](./wifi-manager/) | `io.github.parieses.wifi-manager` | 0.2.0 | 查看网络列表、WiFi 密码、连接状态（自内置插件迁移，自带 system-tools.exe） |
| [MD 表格转换器](./md-table-converter/) | `io.github.parieses.md-table` | 0.1.0 | Markdown 表格与 CSV / JSON / HTML 四种格式互转，自动识别输入格式 |

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
# 打包（Windows 安装包，输出到根目录 dist/）
python build.py disk-analyzer            # 默认只打包 windows（主程序目前仅 Windows）
python build.py disk-analyzer --platform windows   # 显式指定（默认即此）
python build.py disk-analyzer --platform all       # 需要其他平台时全平台打包
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
| `icon` | | 图标相对路径（plugin.json 同目录），主页与管理页展示 |
| `author` / `category` | | 展示信息 |
| `platforms` | | 支持平台数组 `windows/darwin/linux`；**未声明 = 全平台**。加载时按当前平台过滤 |
| `backend.runtime` | ✅ | `native`（独立子进程二进制）/ `goja`（纯 JS）/ `none`（仅前端） |
| `backend.entry` | native 必填 | 可执行入口文件名（Windows 自动补 `.exe`，unix 直接执行） |
| `backend.args` | | 启动附加参数 |
| `frontend.enabled` `entry` `width` `height` | | 声明前端页面：`entry` 为 `frontend/index.html`，`width/height` 为窗口初始尺寸 |
| `capabilities` | | 能力声明：`command`（命令面板）/ `frontend`（前端页面）等；有前端页面必须含 `frontend` |
| `permissions` | | 权限声明：`network` / `filesystem` / `clipboard`，安装时展示 |
| `commands[]` | | 命令面板条目：`id`（命令唯一标识）/ `title` / `keywords`（搜索关键词）/ `aliases`（别名） |

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

### 宿主桥接脚本（自动注入）

宿主会向每个前端页面注入一段桥接脚本，页面可直接调用以下全局 API（无需自己 postMessage）：

| API | 说明 |
|------|------|
| `window.qdConfirm(msg)` / `window.qdAlert(msg)` | 宿主 toast 确认 / 提示 |
| `window.qdPickFile(opts?)` → `Promise<string|null>` | 原生文件选择；`opts={title?, filter?, pattern?}`，取消/失败返回 `null` |
| `window.qdReadFile(path)` → `Promise<{type,content}|null>` | 读取 `qdPickFile` 选中的文件；文本→`{type:'text',content}`，图片/二进制→`{type:'dataurl',content}`；配合 `qdPickFile` 使用 |

> **选文件一律用 `qdPickFile`**：iframe 沙箱内的 `<input type=file>` 会触发宿主窗口失焦问题，且脚本无法精确控制。示例：`const p = await window.qdPickFile({filter:'JSON', pattern:'*.json'}); const res = p && await window.qdReadFile(p);`

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
   - `build-plugins.yml`：diff 出本次变更的插件目录 → `build.py --platform windows` 打包 → 发布到名为 `latest` 的 GitHub Release，资产即 `releases/latest/download/<id-小写横线>-windows.zip`。
   - `pages.yml`：运行 `gen_site.py` 重新生成 `site/index.html` + `site/index.json` → 部署到 GitHub Pages（即 `https://parieses.github.io/quickdock-plugins/`）。
4. 应用内「在线市场」与插件中心主页会随之更新（GitHub Pages 有缓存，通常几十秒到几分钟内生效）。

> 注：主程序 QuickDock 目前仅发布 Windows 版本，故插件默认只打包 windows（`gen_site.py` 的 `PLATFORMS` 也仅含 `windows`）。需要其他平台时：在 `build.py` 命令后加 `--platform all`（或 `darwin`/`linux`），并同步把目标平台加进 `gen_site.py` 的 `PLATFORMS` 与插件 `plugin.json` 的 `platforms`，否则在线市场不会列出该平台下载。

## 开发约定

- 插件 ID 域名反写 `io.github.<账号>.<插件名>`，一个插件一个子目录
- native 命令名用 **kebab-case**（manifest 声明与分发统一），兼容点号写法
- 长任务（>2s）务必后台执行 + 立即返回 + 轮询，避开宿主 30s RPC 超时
- 调试输出写 stderr，绝不污染 stdout 协议
- 新版插件包名保持 `<id转-小写>-<platform>.zip`，覆盖安装即升级

更多实现细节参考 [QuickDock 主仓库](https://github.com/parieses/quickdock)（`internal/plugin/` 目录为协议实现）。