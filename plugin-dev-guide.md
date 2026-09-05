# QuickDock v3 插件开发指南

## 概述

QuickDock v3 支持三种插件运行时，**均无需外部依赖**：

| 运行时 | 说明 | 适用场景 |
|--------|------|---------|
| `none` | 纯前端插件，无后端进程 | 计算稿纸、翻译面板、JSON 格式化等 UI 型插件 |
| `goja` | 内嵌 JS 引擎（Goja），进程中执行 | 需要后端逻辑但不需要子进程的插件 |
| `native` | 独立可执行文件（.exe） | 需要独立进程、系统 API 或高计算量的插件 |

> ❌ **Python / Node.js / PowerShell 运行时已不再支持**。所有插件运行时均嵌入 QuickDock 内部，用户无需安装任何外部环境。

---

## 目录结构

一个插件是一个文件夹，放在 `~/.quickdock/plugins/<plugin-id>/` 下：

```
my-plugin/
├── plugin.json            # 插件清单（必须）
├── main.js                # Goja 后端脚本（goja runtime）
├── main.exe               # 可执行文件（native runtime）
├── frontend/              # 前端资源（none/goja/native 均可选）
│   ├── index.html
│   ├── style.css
│   └── app.js
```

---

## plugin.json 清单格式

```json
{
  "id": "com.quickdock.my-plugin",
  "name": "我的插件",
  "name_i18n": { "zh-CN": "我的插件", "en-US": "My Plugin" },
  "version": "0.1.0",
  "description": "插件功能描述",
  "description_i18n": { "zh-CN": "插件功能描述", "en-US": "Plugin description" },
  "author": "Your Name",

  "backend": {
    "runtime": "goja",
    "entry": "main.js"
  },

  "frontend": {
    "enabled": false,
    "entry": "frontend/index.html",
    "width": 400,
    "height": 300
  },

  "capabilities": ["command"],

  "permissions": {
    "network": false,
    "filesystem": false,
    "clipboard": true
  },

  "commands": [
    {
      "id": "hello",
      "title": "Hello World",
      "keywords": ["hw", "greet"]
    }
  ]
}
```

### 字段说明

| 字段 | 说明 |
|---|---|
| `id` | 唯一标识，格式 `com.quickdock.xxx`（至少一个点号）|
| `name` | 插件显示名称 |
| `version` | 语义化版本号 |
| `backend.runtime` | 运行环境：`none` / `goja` / `native` |
| `backend.entry` | 入口文件名（`none` runtime 不需要）|
| `permissions` | 权限声明，影响插件能调用的 Host API |
| `commands` | 注册到命令面板的命令列表 |
| `commands[].keywords` | 搜索别名数组，用户输入这些词也能匹配到该命令 |

### commands 字段

每个命令对象支持的字段：

| 字段 | 说明 |
|---|---|
| `id` | 命令唯一 ID（插件内唯一） |
| `title` | 命令显示名称 |
| `hotkey` | 全局热键（如 `Ctrl+Shift+T`），可选 |
| `keywords` | 搜索别名数组，用户输入这些词也能匹配到该命令 |
| `aliases` | 中文别名数组（如 `["计算器","jsq"]`），扩展中文搜索覆盖 |
| `prefix` | Slash 前缀（如 `/tr`），命令面板输入 `/tr` 时仅该命令激活 |
| `matchPattern` | 正则匹配模式，命令面板输入文本命中该正则时该命令会被推荐 |
| `acceptsInput` | **声明该命令接收命令面板传入的参数**，详见「从命令面板接收输入」 |

> ⚠️ **`matchPattern` / `prefix` 只负责「让命令被推荐/激活」，并不代表参数会自动传入插件。** 若要让命令面板输入框中的文本（如 `500`、`192.168.1.1`、`*/5 * * * *`）真正带进插件并执行，必须在命令上声明 `"acceptsInput": true`。

### Runtime 说明

| runtime | entry 示例 | 说明 |
|---------|-----------|------|
| `none` | 无 | 纯前端插件，没有后端进程。所有逻辑在 iframe 的 JS 中执行 |
| `goja` | `main.js` | 内嵌 JS 引擎。插件 JS 在 QuickDock 进程内执行，无需安装 Node.js |
| `native` | `main.exe` | 独立可执行文件。QuickDock 会启动为子进程，通过 stdin/stdout JSON-RPC 通信 |

---

## 三种运行时详解

### none runtime（纯前端）

适用于不需要后端逻辑的 UI 插件。插件只是一个 HTML 页面，在独立窗口中通过 iframe 加载。

**plugin.json 示例：**
```json
{
  "backend": { "runtime": "none" },
  "frontend": { "enabled": true, "entry": "frontend/index.html" }
}
```

**特点：**
- 不启动子进程，零资源开销
- 所有逻辑在浏览器 JS 中执行
- 通过 `parent.postMessage` 与主程序通信（经由 PluginPage 中转）
- 数据持久化使用 `localStorage`

---

### goja runtime（内嵌 JS 引擎）

适用于需要后端逻辑但不需要子进程的插件。JS 代码在 QuickDock 进程内直接执行。

**plugin.json 示例：**
```json
{
  "backend": { "runtime": "goja", "entry": "main.js" }
}
```

**main.js 模板：**
```javascript
function handleInitialize(params) {
    api.log('插件初始化完成')
    return { status: 'ready', version: '0.1.0' }
}

function handleExecute(params) {
    var command = params.command || ''
    var input = params.input || {}

    if (command === 'hello') {
        var name = input.text || 'World'
        return { result: 'Hello, ' + name + '!' }
    }

    throw new Error('未知命令: ' + command)
}
```

**特点：**
- 无需安装 Node.js，内嵌 Goja 引擎（纯 Go，无 CGO）
- 支持 ES5.1 + 大部分 ES6 特性
- 通过 `api.log()`（INFO）/ `api.warn()`（WARN）/ `api.error()`（ERROR）输出日志
- 无宿主 `api.crypto`：md5/sha/base64/url/html 等需在插件内纯 JS 自实现（参考 text-encoder 的 main.js 自带 crypto 库）
- 导出 `handleInitialize()` 和 `handleExecute()` 函数供主程序调用

---

### native runtime（独立可执行文件）

适用于需要独立进程、系统 API 或高计算量的插件。

**通信方式：stdin/stdout JSON-RPC 2.0**

插件通过 stdin 接收请求，通过 stdout 发送响应。每行一个完整的 JSON 对象。

#### 生命周期

**1. initialize（主程序 → 插件）**

```json
// 主程序发送
{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"hostVersion":"3.0.0","pluginDir":"..."}}

// 插件响应（15 秒内）
{"jsonrpc":"2.0","id":1,"result":{"status":"ready","pluginId":"com.quickdock.my-plugin"}}
```

**2. plugin.execute（主程序 → 插件）**

用户在命令面板执行插件命令时触发。

```json
// 主程序发送
{"jsonrpc":"2.0","id":2,"method":"plugin.execute","params":{"command":"hello","input":{"name":"World"}}}

// 插件响应（10 秒内）
{"jsonrpc":"2.0","id":2,"result":{"message":"Hello, World!"}}
```

**3. shutdown（主程序 → 插件）**

插件被卸载/禁用/主程序退出时触发。

```json
// 主程序发送（通知，无需响应）
{"jsonrpc":"2.0","method":"shutdown","params":null}
```

#### Host Methods（插件可调用的主程序 API）

native 插件在收到请求后，可以通过 stdout 向主程序发起回调请求：

| 方法 | 说明 | 所需权限 |
|---|---|---|
| `log.info` | 记录日志 | 无需权限 |
| `log.warn` | 记录告警日志 | 无需权限 |
| `log.error` | 记录错误日志 | 无需权限 |
| `host.notify` | 弹出系统通知 | 无需权限 |
| `host.clipboard.read` | 读取剪贴板文本 | `clipboard: true` |
| `host.clipboard.write` | 写入剪贴板文本 | `clipboard: true` |
| `host.dialog.open` / `host.dialog.save` | 文件/保存对话框（**前端桥接层拦截**，见「文件与目录选择」） | 无需权限（前端拦截） |

```json
// 插件 → 主程序（回调请求）
{"jsonrpc":"2.0","id":101,"method":"host.clipboard.write","params":{"text":"剪贴板内容"}}

// 主程序 → 插件（响应）
{"jsonrpc":"2.0","id":101,"result":{"success":true}}
```

#### 通过 RPC 写日志（native 插件推荐，精确控制级别）

写日志与上面的 `host.clipboard.write` 是同一条通道、同一个格式，方法名换成 `log.info` / `log.warn` / `log.error` 即可。宿主收到后落盘到 `<dataDir>/logs/plugin-YYYYMMDD.log`，行首自动带 `[plugin:<id>]` 前缀，与应用主日志分离：

```json
// 插件 → 主程序：写一条 INFO 日志（级别换 log.warn / log.error 即告警 / 错误）
{"jsonrpc":"2.0","id":102,"method":"log.info","params":{"message":"任务完成"}}
```

- 参数只有 `{"message": "..."}` 一个字段，三种级别均**免权限**（plugin.json 无需声明）。
- 宿主**先落盘、后回响应**，插件可以不等响应直接继续；等到的响应为 `{"result": null}`。
- ⚠️ **必须带 `id`**：宿主对不带 `id` 的"通知"（notification）会**静默丢弃**——不执行 handler、不落盘（与 JSON-RPC 2.0 规范"通知应执行但不响应"不一致，属宿主当前已知行为，勿依赖）。统一按"回调请求"带自增 id 发送即可。

封装参考（复用模板里现成的 `sendJSON` / `respond` / `hostCall` 写 stdout 函数）：

```go
var hostReqID int64 // 进程内自增 id

func hostLog(level, format string, args ...interface{}) {
	id := atomic.AddInt64(&hostReqID, 1)
	p, _ := json.Marshal(map[string]string{"message": fmt.Sprintf(format, args...)})
	sendJSON(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  "log." + level,
		"params":  json.RawMessage(p), // 必须是 RawMessage，否则 []byte 会被 base64 编码
	})
}

hostLog("info", "开始处理 %s", file) // → plugin-*.log 的 I 级
hostLog("warn", "重试第 %d 次", n)    // → W 级
hostLog("error", "处理失败: %v", err) // → E 级
```

> 与 stderr / stdout 散行的区别：直接写 `os.Stderr` 也会被宿主收进插件日志，但**固定记为 WARN** 且无法分级；协议外的 stdout 散行虽已被宿主以 `[stdout]` 前缀捕获（不破坏通信），但超大单行会被截断。**正式日志请一律走 `log.*`**，stderr 仅用于宿主侧无法收口时的兜底。

### 超时与通信约束（native 插件必看）

主程序对每条 JSON-RPC 都有超时，插件必须在时限内回写响应，否则请求会被判失败：

| 场景 | 超时 |
|---|---|
| 默认 Call | 30s |
| `plugin.execute`（命令执行） | 20s |
| `initialize`（插件启动握手） | 15s |
| `health.ping`（健康检查） | 5s |

- **耗时操作（大文件、外部 CLI 调用）务必预估时长**；若可能超过 20s，应在后端拆成「先返回受理、后台异步处理」或提示用户。
- 响应单行 JSON，主程序用 **1MB buffer** 读取，超过会截断；不要通过 stdout 返回超大文本。
- **stdout 只能写 JSON-RPC**：任何 `fmt.Println` / `console.log` 到 stdout 都会破坏协议（见「注意事项」）。

### 文件与目录选择（native 插件）

> 🚫 **强制约束（2026-08-27 起）：文件/目录选择一律走宿主暴露的原生方法，禁止插件后端自行 spawn 子进程弹系统对话框。**
>
> - ✅ **允许**：`host.dialog.open` / `host.dialog.save`（文件/保存，宿主 Host API）、`qdPickFolder`（目录，宿主注入桥接）、`qdPickFile`（文件，宿主注入桥接）。这四类都是宿主抛出的原生方法，由插件前端直接调用。
> - ❌ **禁止**：在插件后端用 `exec.Command("powershell"/"osascript"/"zenity", ...)` 弹 `FolderBrowserDialog` / ` NSOpenPanel` / ` gtk 文件选择器` 等。原因：慢（每次起子进程）、依赖运行环境（WinForms/AppleScript 可用性）、关闭后焦点不归还宿主（需 `user32` 兜底）、且子进程 stdout 易污染路径（PowerShell 会回吐 `True`）。旧插件 git-workbench / pdf-toolkit 已统一迁移，新插件不得再写这套。

native 插件的前端（iframe）通过宿主桥接（`usePluginHost.ts`）完成文件/目录选择。**关键点：文件选择完全在前端桥接层完成，不进入后端子进程；目录选择也只在前端调用 `qdPickFolder`，不再经过插件后端命令。**

#### 选择文件（宿主 Host API，强制）

> 🚫 文件选择**必须**走 `host.dialog.open` / `host.dialog.save`，由宿主拦截后调用 Wails `Dialogs.OpenFile()`。插件后端**不得**自行 spawn 文件对话框。

前端直接把命令设为 `host.dialog.open` 即可，宿主拦截后调用 Wails `Dialogs.OpenFile()`：

```javascript
// 单选
const r = await this.send('host.dialog.open', {
  title: '选择 PDF 文件',
  filters: [{ name: 'PDF 文件', pattern: '*.pdf' }]
})
// 成功: { canceled:false, path:'D:/a.pdf' }   取消: { canceled:true, path:'' }

// 多选（一次框选多个）
const r = await this.send('host.dialog.open', {
  title: '选择要合并的 PDF',
  multiple: true,
  filters: [{ name: 'PDF 文件', pattern: '*.pdf' }]
})
// { canceled:false, multiple:true, paths:['D:/a.pdf','D:/b.pdf'] }
```

| 调用 | input | 返回 |
|---|---|---|
| 单选 | `{ title, filters:[{name,pattern}] }` 命令 `host.dialog.open` | `{ canceled, path }` |
| 多选 | `{ multiple:true, title, filters }` 命令 `host.dialog.open` | `{ canceled, multiple:true, paths:[] }` |
| 保存 | `{ title, defaultName, filters }` 命令 `host.dialog.save` | `{ canceled, path }` |

> ⚠️ **输出文件尚不存在时必须用 `host.dialog.save`**（保存对话框可命名新文件）。用 `host.dialog.open`（打开对话框）无法选中不存在的文件。

#### 选择目录（宿主原生对话框，强制）

> 🚫 目录选择**必须**走宿主注入的 `qdPickFolder`，**禁止**在插件后端用 `exec.Command` 弹 `FolderBrowserDialog` 等系统目录框。

Wails v3 的 `Dialogs.OpenFile` **支持目录选择**：`CanChooseDirectories(true)` + `CanChooseFiles(false)` 即可弹出系统「选文件夹」对话框（Windows 走 `IFileDialog` 的 `FOS_PICKFOLDERS`，不是旧版树形 FolderBrowserDialog）。宿主已封装好桥接，插件**无需自己 spawn 子进程**，直接调注入的 `qdPickFolder` 即可，跨平台、主题一致、无焦点丢失。

**前端**（直接调注入的 `qdPickFolder`，取消/失败返回 `null`）：

```javascript
const path = await qdPickFolder({ title: '选择输出目录' })
if (!path) return            // 用户取消
// path: 'D:/out'（绝对路径）
```

**宿主侧（已就绪，插件不必实现）**：
- 桥接脚本注入 `window.qdPickFolder(opts)` → `postMessage('plugin:pickfolder')`
- `frontend/src/composables/usePluginHost.ts` 监听后调 Wails 绑定 `PickFolderPath(title)`
- `services/plugin_install.go` 的 `PickFolderPath`：`a.app.Dialog.OpenFile().CanChooseDirectories(true).CanChooseFiles(false).SetTitle(title).PromptForSingleSelection()`

> ✅ 与 `qdPickFile`（`PickFilePath`）同源，复用同一套原生对话框链路。
> ⚠️ **不要**在插件后端用 `exec.Command("powershell", ...)` 弹 `FolderBrowserDialog` 选目录——慢、依赖 WinForms、关闭后焦点不归还宿主（需 `user32` 兜底），且 PowerShell 会把 `user32` 返回的 `True` 混进 stdout 污染路径。旧插件 git-workbench / pdf-toolkit 已统一迁移到 `qdPickFolder`。

---

## 权限声明

插件在 `plugin.json` 中声明所需权限：

```json
"permissions": {
  "network": false,    // 能否发起 HTTP 请求
  "filesystem": false, // 能否访问文件对话框
  "clipboard": true    // 能否读写剪贴板
}
```

### 安全边界

插件运行在安全沙箱中，了解边界有助于合理设计：

| 层级 | 防护措施 |
|---|---|
| **进程隔离** | native 插件运行在独立子进程，崩溃不影响主程序 |
| **JS 沙箱** | goja 引擎纯 Go 实现，无文件系统/网络能力，仅暴露受限 `api.*` |
| **权限声明** | `plugin.json` 声明所需权限，Host Method 层运行时校验 |
| **Nonce 握手** | iframe postMessage 携带随机 nonce，防止跨源消息伪造 |
| **存储隔离** | 每个插件只能读写 `plugin_data` 中自己 `plugin_id` 的数据 |
| **ZIP 安全** | Zip Slip 路径穿越防护、100MB 解压上限、50MB 单文件上限、回滚机制 |
| **前端沙箱** | iframe `sandbox="allow-scripts allow-same-origin allow-modals"` |
| **崩溃恢复** | 子进程崩溃后自动重启，最多 3 次 + 指数退避 |

> 插件**不能**越权访问其他插件数据、不能绕过权限调用 Host API；需要某能力时，先确认 `plugin.json` 已声明对应 `permissions`。

---

## 前端开发

插件前端是一个标准的 HTML 页面，在独立窗口中通过 iframe 加载。

### 与主程序通信

通过 `window.parent.postMessage` 与主程序通信（`javascript:void(0)`）：

```javascript
// 插件前端 → 主程序
window.parent.postMessage(
  { type: 'plugin:execute', id: 1, command: 'hello', input: { name: 'World' } },
  '*'
)
```

### 从命令面板接收输入（acceptsInput）

当用户在命令面板选中某个插件命令，且输入框里有文本时，这些文本**默认不会**传给插件。只有命令在 `plugin.json` 中声明了 `"acceptsInput": true`，宿主才会把文本注入插件。

典型场景：端口检查（输入 `8080`）、HTTP 状态码（输入 `500`）、时间戳转换（输入 `1700000000`）、Cron 解释（输入 `*/5 * * * *`）等「单一数据主体」类命令。

#### 投递路径

宿主按插件是否带前端分两条路径投递：

**路径 A：插件带前端（none / goja / native 且配了 frontend）**

宿主调用 `SetPendingPluginInit(text, commandID)` 暂存参数并打开插件窗口 / 内联 iframe，加载完成后向 iframe 发送 `plugin:init` 消息：

```javascript
// 宿主发送（plugin:init）:
// { type:'plugin:init', data: { text: '<用户输入>', command: '<命令ID>', theme:'dark', locale:'zh' } }

window.addEventListener('message', (e) => {
  if (e.data?.type === 'plugin:init') {
    const { text, command } = e.data.data || {}
    if (text) {
      // 1. 把 text 填入插件输入框
      // 2. 调用插件自身的转换/执行函数（如 showDetail(code) / convert()）
    }
  }
})
```

> 内置插件使用 Nonce 握手安全机制，`plugin:init` 由 PluginPage.vue 在 iframe `onload` 后自动发送，插件只需监听 `message` 事件即可。

**路径 B：插件无前端（纯后端命令）**

宿主直接调用 `ExecutePluginCommand(pluginID, commandID, { text })`，把文本作为 `input.text` 传给后端：

```javascript
// goja 后端 main.js
function handleExecute(params) {
    var command = params.command || ''
    var input = params.input || {}
    var text = input.text || ''   // ← 命令面板传入的文本
    // ...
}

// native 后端（JSON-RPC plugin.execute）
// params: { "command":"hello", "input": { "text": "用户输入" } }
```

#### 声明示例

```json
{
  "commands": [
    {
      "id": "lookup-status",
      "title": "HTTP 状态码查询",
      "prefix": "/http",
      "matchPattern": "^[1-5][0-9]{2}$",
      "acceptsInput": true
    }
  ]
}
```

---

## 多语言（i18n）

宿主已打通插件 i18n 通路：打开插件页面时按主应用当前语言注入 `<html lang="locale">`
（`zh-CN` / `en-US`），切换语言时宿主通过 `plugin:theme{theme,locale}` 消息热更新 `lang`。
插件侧只需「提供翻译」即可，无需感知宿主实现。

### 1. 元数据多语言（插件列表 / 命令面板 / 市场显示）

`plugin.json` 可选字段（未声明时回退 `name` / `description` / `title`）：

```json
{
  "name": "HTTP 状态码速查",
  "name_i18n": { "zh-CN": "HTTP 状态码速查", "en-US": "HTTP Status Codes" },
  "description": "查询 HTTP 状态码含义",
  "description_i18n": { "zh-CN": "查询 HTTP 状态码含义", "en-US": "Look up HTTP status code meanings" },
  "commands": [
    {
      "id": "lookup-status",
      "title": "HTTP 状态码查询",
      "title_i18n": { "zh-CN": "HTTP 状态码查询", "en-US": "Lookup HTTP Status" }
    }
  ]
}
```

语言键约定：`{locale}` 精确匹配（`zh-CN`/`en-US`），未命中回退主语言（`zh`/`en`），再回退默认字段。
`title_i18n` 的各语言值会进入命令面板搜索索引——英文界面输入英文标题也能搜到该命令。

### 2. 页面文案多语言（插件前端页面）

页面自包含轻量 i18n（不依赖宿主版本）：

```html
<script>
// 语言包：key 用中文原文，en-US 提供翻译；zh-CN 省略即回退原文
const L10N = { 'en-US': { '合并 PDF': 'Merge PDFs', '选择文件': 'Select File' } };
function L(key, params) {
  const loc = document.documentElement.getAttribute('lang') || 'zh-CN';
  const map = L10N[loc] || L10N[loc.split('-')[0]] || {};
  let s = (map && map[key] !== undefined) ? map[key] : key;
  if (params) for (const k in params) s = s.replace('{' + k + '}', String(params[k]));
  return s;
}
window.addEventListener('message', (e) => {
  if (e.data && e.data.type === 'plugin:theme' && e.data.data && e.data.data.locale) {
    document.documentElement.setAttribute('lang', e.data.data.locale);
    renderAll(); // 重渲染页面文案
  }
});
</script>
```

- **静态文本**：按元素选择器遍历取原始文本查翻译（注意缓存 `data-orig` 原文，避免重复翻译）。
- **动态消息**：`showResult(el, 'success', L('成功合并 {n} 个 PDF', { n: count }))`，占位用 `{name}`。
- 完整样板见 `plugins/external/pdf-toolkit/frontend/index.html` 顶部 i18n 段。宿主 `common.js`
  （主程序重建后自动注入到所有插件页面）也提供标准版 `window.QD.i18n(langPack)`，结构与上文一致，
  届时可直接替换。

---

## 安装与调试

### 安装插件

1. 将插件打包为 `.zip` 文件（`plugin.json` 必须在根目录）
2. 打开 QuickDock → 插件管理页面
3. 拖入 zip 文件或点击「安装插件」选择文件
4. 安装成功后插件自动启动

> ⚠️ **更新已安装插件前必须先彻底退出 QuickDock**：仅关闭窗口时主进程仍在、插件子进程会被自动拉起，`~/.quickdock/plugins/<id>/` 仍被锁定，重装的 zip 不会真正覆盖旧 exe（表现为一直报老错误）。正确做法是**托盘图标右键 → 退出**让主进程连同子进程一起死，再「从文件安装」重装。

### 查看日志

插件日志统一写入 **`<dataDir>/logs/plugin-YYYYMMDD.log`**（按日滚动；与应用主日志 `<dataDir>/logs/quickdock-YYYYMMDD.log` 分离；行首带 `[plugin:<id>]` 前缀，可按插件过滤）：

- `goja` 插件：`api.log(msg)`（INFO）/ `api.warn(msg)`（WARN）/ `api.error(msg)`（ERROR）
- `native` 插件：通过 `log.info` / `log.warn` / `log.error` Host Method 主动输出（`params: {"message": "..."}`）
- 插件进程 **stderr** 输出自动记为该插件 WARN；未被 JSON-RPC 解析的 **stdout 散行**（如 console.log）不再静默丢失，自动以 `[stdout]` 前缀记 INFO

### 调试建议

1. 先用模板创建项目，确认基础通信正常
2. 新插件建议使用 `goja` runtime（无需编译，修改 JS 后重启插件即可）
3. 用 `api.log` / `log.info` 输出调试信息，重现场景后查 `plugin-*.log`

---

## 注意事项

1. **native 插件 stdout 主通道专用于 JSON-RPC**: 协议外的散行（`fmt.Println` / `console.log` 落到 stdout）虽已被宿主捕获进插件日志（`[stdout]` 前缀，不破坏通信），但超大单行会被截断、多行 JSON 无法解析——调试输出请统一走 `log.info` / `log.warn` / `log.error`（或 stderr）
2. **错误处理**: 始终用 JSON-RPC 错误响应返回错误
3. **热键冲突**: 如果多个插件声明相同热键，后安装的插件注册会失败
4. **存储隔离**: 插件的 `db.*` Host Method 只能读写自己 `plugin_id` 的数据

---

## 调试与部署陷阱（native 插件必读）

1. **命令必须全小写（致命）**
   后端 `handleExecute` 在分发前会 `strings.ToLower(command)`，`switch` 的 `case` 标签若大小写混合（如 `"pickFolder"`），小写化后 `"pickfolder"` 匹配不上，直接落入 `default` 报 `unknown command: pickFolder`。
   ✅ 所有命令的 case 标签一律小写（`"pickfolder"`），前端发送时也用小写。

2. **build.py 编译缓存（致命，已根治）**
   `build.py` 曾检测到 `<plugin>/.build/windows/<entry>.exe` 已存在就**跳过编译**，导致「改了 Go 代码、zip 里永远是旧 exe」——症状为前端新功能调用新命令时报 `unknown command: <新命令>`。
   ✅ 现已加 **mtime 防护**：`*.go`/`go.mod` 比缓存产物新会自动重新编译。若仍遇诡异旧行为，可手动 `rm -rf <plugin>/.build` 兜底；装完用 zip 内 exe 的字节数/md5 与安装位对账，别只信「打包完成」输出。

3. **运行时目录被锁定，旧进程不释放（致命）**
   `~/.quickdock/plugins/<id>/` 被 QuickDock 主进程及其插件子进程占用。仅「关闭窗口」时 QuickDock 主进程仍在、子进程被它自动拉起，旧 exe 不会被覆盖，重装仍是旧版——且 zip 解压可能**部分成功**（文本文件换新、exe 覆盖失败静默保留），造成前后端版本错位。
   ✅ 更新插件前**托盘图标右键 → 退出**，让主进程连同子进程一起死，再「插件管理 → 从文件安装」重装。
   💡 运行中紧急替换：Windows 允许 rename 运行中的映像——`mv <entry>.exe <entry>.exe.old` 后写入新 exe，重启主程序生效。

4. **外部二进制不要依赖 PATH**
   插件调用的第三方 CLI（如 pdfcpu）若依赖用户 PATH 里飘忽的版本，极易因版本/参数 API 不一致整片功能崩溃。
   ✅ 把所需二进制随插件打包，运行时优先用插件目录下的 sibling 文件（`filepath.Dir(os.Args[0]) + 二进制名`），找不到再回退 PATH。外部插件 **PDF Toolkit** 即采用此模式，自带 `pdfcpu.exe` 锁定版本，避免升级/重装 pdfcpu 引发连锁失败。

---

## 插件参考（避免重复造轮子）

以下能力均已有现成外部插件（位于 `plugins/external/`，ID 为 `io.github.parieses.*`），开发新插件前先确认是否已覆盖。2026-08-24 起**内置插件已全部外置**，`plugins/builtin/` 仅保留 `common.css` / `common.js` 骨架（宿主向后兼容注入用）：

**Goja 插件（有后端逻辑）**

| 插件 ID | 功能 |
|---|---|
| calcsheet | 计算表格 |
| formatter | 代码格式化 |
| json-toolbox | JSON 处理（格式化/校验/转换） |
| regex-extractor | 正则提取 |
| text-encoder | 文本编码/哈希（Base64/URL/HTML/MD5/SHA1/SHA256） |
| time-converter | 时间戳/时区转换 |

**Pure Frontend 插件（runtime: none）**

| 插件 ID | 功能 |
|---|---|
| emoji-search | Emoji 搜索 |
| jwt-decoder | JWT 解码 |
| markdown-preview | Markdown 预览 |
| qrcode | 二维码生成/识别 |

**Native 插件（自带 Go 源码 + system-tools.exe）**

| 插件 ID | 功能 |
|---|---|
| hosts-manager | hosts 文件管理（system-tools.exe，源码已 vendor 进插件目录） |
| port-scanner | 端口扫描（同上） |
| wifi-manager | WiFi 管理（同上） |

> 原内置插件已全部迁至 `plugins/external/`（ID 改为 `io.github.parieses.*`），代码可直接复用——goja/none 插件演示「零宿主依赖、纯 JS 自包含」的外部化样板；native 三件套演示「Go 源码 vendor + 自编译 entry exe」模式（`build.py` 直接在插件目录 `go build`）。完整 goja 模板见上文「完整示例」。

> 样式自包含（2026-08-24 约定）：外部插件的 `frontend/` 下必须自带 `qd-theme.css`（即 `common.css` 的副本，改名以绕开宿主对 `common.css` 后缀的拦截改写），页面用 `<link rel="stylesheet" href="qd-theme.css">` 引用——zip 解压到任何环境都有完整样式，不依赖宿主注入。宿主仍会向页面注入 `PluginsDir/builtin/common.css/js` 以兼容历史已安装的旧版插件，但新插件不得依赖该注入。

## 完整示例

参见 `plugins/templates/goja/` 目录下的 Goja 模板项目。
以及 `plugins/external/calcsheet/` 目录下的计算稿纸插件（`none` runtime）。
**外部 native 插件完整范例**：`plugins/external/pdf-toolkit/` —— 含合并/拆分/压缩/水印/提取图片/PDF 信息，演示了「多选文件选择 + 目录输出 + 自带 pdfcpu.exe + pickFolder 后端命令」全套实战模式；`plugins/external/hosts-manager/` —— 演示 native 插件「vendor Go 源码 + build.py 自动编译 entry exe」模式。
