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
- 调宿主能力用 `qdHostCall` / `qdHttp`（见「前端开发 → 与主程序通信」）：
  网络请求、剪贴板、系统通知、文件对话框、宿主 MCP 工具、插件私有 KV 都能直接用，
  **不需要后端进程**。`localStorage` 仍可用，但按 `plugin_id` 隔离的 `db.*` 更稳。

#### none 插件最小骨架

宿主会向每个插件前端页面注入桥接脚本（见「前端开发 → 调用宿主能力」），因此 none 插件的 `frontend/index.html` 可以直接调用 `qdHostCall` / `qdHttp` / `qdPickFile` / `qdPickFolder` 等全局，无需自己 `postMessage`：

```html
<!DOCTYPE html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <!-- 样式自包含：随插件带 qd-theme.css（common.css 副本），不依赖宿主注入 -->
  <link rel="stylesheet" href="qd-theme.css">
  <title>我的 none 插件</title>
</head>
<body>
  <button id="pick">选择文件</button>
  <pre id="out"></pre>
  <script>
    // qdPickFile / qdPickFolder / qdHostCall / qdHttp 均由宿主注入，直接可用
    document.getElementById('pick').onclick = async () => {
      const p = await qdPickFile({ filter: '文本', pattern: '*.txt' })
      if (!p) return                                  // 用户取消
      // host.fs.read 在 permissions.filesystem.read scope 内返回文件内容
      const r = await qdHostCall('host.fs.read', { path: p })
      document.getElementById('out').textContent =
        r.encoding === 'base64' ? atob(r.content) : r.content
    }
  </script>
</body>
</html>
```

> ⚠️ **none 插件没有后端进程**，`host.fs.*` / `http.*` 的权限校验**完全依赖 `plugin.json` 的 `permissions` 白名单**——选了 scope 外的路径会直接拿到 `-32001`。开发时把需要读写的目录预先写进 `permissions.filesystem` 再重装。

#### 三个官方 none 插件：Host API 端到端范式

这三个插件是 `runtime:none` 调用宿主能力的标准样板，源码可直接复用：

| 插件 | 调用链路 | 演示点 |
|---|---|---|
| `batch-rename` | `qdPickFolder()` → `host.fs.list` → 规则链 → `host.fs.move` | 目录选择 + 批量改名（冲突跳过） |
| `file-search` | `host.fs.list`（递归）→ `host.fs.read` → SHA-256 去重 → `host.fs.remove`（回收站） | 递归遍历 + 内容哈希 + 安全删除 |
| `image-uploader` | `qdPickFile()` → `host.fs.read`（base64）→ `http.post`（图床） | 文件读取 + 绕过 iframe CORS 的上传 |

完整源码见 `plugins/external/{batch-rename,file-search,image-uploader}/frontend/index.html`。

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

**调用宿主能力（`api.host`）：**
```javascript
function handleExecute(params) {
    // 需在 plugin.json 声明 "permissions": { "network": true }，否则返回权限错误
    var r = api.host('http.get', { url: 'https://example.com' })
    return { status: r.status, body: r.body }
}
```
- `api.host(method, params)` 转发到**与 native 完全相同的 Host Method 注册表**，权限校验一致，因此 `http.get` / `http.post`、`host.notify`、`host.dialog.*`、`host.clipboard.*`、`db.*`、`host.mcp.call` 都可以直接调用；方法不存在会抛错，权限不足会抛错，调用方按需 try/catch
- 想复用宿主的检索/待办/环境等能力，用 `api.host('host.mcp.call', { tool: 'todo_list', args: {} })`，无需自己实现（工具清单见「Host Methods」）
- `api.db.exec(sql, args)` / `api.db.query(sql, args)` 是插件私有 SQLite（`<dataDir>/data.db`）的**裸 SQL**。注意它与 native 的 `db.*` **不是同一套存储**——后者是宿主库中按 plugin_id 隔离的 KV
- ⚠️ `api.host` 是**同步阻塞**调用，受 `handleExecute` 的 20s 超时约束。`host.dialog.*` 这类需要用户操作的方法，在用户完成选择前就可能被打断，不宜依赖；长耗时任务请走 taskId + 轮询的异步范式

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

Host Method 是宿主注册的能力表。**三种运行时共用同一张表、同一套权限校验**，
只是调用通道不同（native 走 JSON-RPC、goja 走 `api.host`、none 走 `qdHostCall`）。

| 方法 | 说明 | 所需权限 |
|---|---|---|
| `log.info` / `log.warn` / `log.error` | 写插件日志（落 `<dataDir>/logs/plugin-YYYYMMDD.log`） | 无需权限 |
| `host.ping` | 存活探测，返回 `{pong, time}` | 无需权限 |
| `host.notify` | 弹出系统通知 | 无需权限 |
| `host.clipboard.read` | 读取剪贴板文本 | `clipboard: true` |
| `host.clipboard.write` | 写入剪贴板文本 | `clipboard: true` |
| `host.dialog.open` / `host.dialog.save` | 打开 / 保存文件对话框 → `{canceled, path}` | `filesystem: true` 或 scope 对象 ⚠️见下 |
| `host.fs.read` | 读文件 → `{path, size, encoding, content}`；合法 UTF-8 用 `utf8` 直出，否则 `base64`；>8 MiB **报错**（不截断） | `filesystem.read` scope |
| `host.fs.write` | 原子写（同目录临时文件 + rename），`content` + 可选 `encoding: utf8\|base64`；>8 MiB 报错。**不自动建父目录** | `filesystem.write` scope |
| `host.fs.list` | 列目录（不递归）→ `{path, entries:[{name,path,isDir,size,mtime}], truncated}`；上限 2000 条 | `filesystem.read` scope |
| `host.fs.stat` | 元信息 → `{path, exists, isDir, size, mtime, mode}`；不存在时返回 `exists:false` 而非报错 | `filesystem.read` scope |
| `host.fs.exists` | 存在性 → `{path, exists}` | `filesystem.read` scope |
| `host.fs.mkdir` | 建目录；`parents: true` 时递归创建 | `filesystem.write` scope |
| `host.fs.remove` | 删文件或目录 → 进入**系统回收站**（不是永久删除）；`{path, removed, trash}` | `filesystem.write` scope |
| `host.fs.move` | 改名 / 移动 → `{from, to, moved}`；**目标已存在即报错（不覆盖）**，不支持跨盘符 | `from` 与 `to` **都要**在 `filesystem.write` scope 内 |
| `http.get` | GET，超时 15s、响应上限 2 MiB → `{status, ok, headers, body, truncated}` | `network` 域名白名单（如 `["https://api.github.com"]`）；`true` 全放行 |
| `http.post` | POST，参数同上，另支持 `body` / `contentType`；重定向目标仍受白名单二次校验 | `network` 域名白名单 |
| `host.shell.open` | 打开 URL / 文件 / 目录（走 `sysutil.OpenDetached`，禁裸 exec）→ `{"success":true}` | `shell` 目标前缀白名单（如 `["file:///C:/Users/me","https://docs.example.com"]`）；`true` 全放行 |
| `host.process.list` | 列出全部进程 → `{processes:[{pid,name,memBytes}], count}`（CPU 单样本无意义，留 0） | 无需权限 |
| `host.process.kill` | 结束进程 → `{success, pid}` | `processKill: true`（显式开关，默认拒绝） |
| `host.mcp.call` | 调用宿主内置 MCP 工具 → `{tool, result}` | 无需权限（等级门见下） |
| `db.get` / `db.set` / `db.delete` / `db.list` | 插件私有 KV，按 `plugin_id` 强隔离，单值 ≤256 KiB | 无需权限 |

> ⚠️ **`host.dialog.*` 有两条路径，权限要求不同——别搞混：**
>
> | 调用方式 | 是否要 `filesystem: true` | 说明 |
> |---|---|---|
> | 经 Host Method：native JSON-RPC / goja `api.host` / none `qdHostCall('host.dialog.open')` | **要** | 落到后端 `checkPermission`，未声明即被拒（-32001） |
> | none 插件走 `plugin:execute`，命令名设为 `host.dialog.open` / `host.dialog.save` | **不要** | 由**宿主前端直接拦截**并调 Wails `Dialogs.OpenFile()`，**不进后端、不做权限校验**。闸门是「用户自己选了哪个文件」 |
>
> 后者是 none 插件的历史推荐用法（见「文件与目录选择」），也是唯一不需要 `filesystem` 的取文件路径。

```json
// 插件 → 主程序（回调请求）
{"jsonrpc":"2.0","id":101,"method":"host.clipboard.write","params":{"text":"剪贴板内容"}}

// 主程序 → 插件（响应）
{"jsonrpc":"2.0","id":101,"result":{"success":true}}
```

#### host.mcp.call：直接复用宿主的 MCP 工具（免重复实现）

宿主内置了一个 MCP Server，把一批业务能力注册成了工具。插件通过 `host.mcp.call`
可以直接复用，**不必自己再实现一遍检索/查询**，而且与 AI 客户端拿到的是同一能力面
（返回结构、错误信息、等级判定完全一致）。

```json
// 插件 → 主程序
{"jsonrpc":"2.0","id":102,"method":"host.mcp.call","params":{"tool":"item_search","args":{"q":"周报"}}}
```

已注册工具（共 29 个，`services/mcp/service.go`）：

| 类别 | 工具 |
|---|---|
| 内容检索 | `item_search` / `item_open` · `recent_items` · `workspace_list` |
| 笔记 | `note_search` / `note_create` / `note_update` / `note_quick` |
| 待办 | `todo_list` / `todo_create` / `todo_done` |
| 剪贴板 | `clipboard_recent` / `clipboard_copy` |
| 环境编排 | `env_list` / `env_status` / `env_log` / `env_versions` / `env_start` / `env_stop` / `env_restart` |
| 日志与崩溃 | `log_list` / `log_read` / `crash_list` / `crash_read` / `plugin_list` |
| 系统信息 | `port_list` / `app_info` |
| ⚠️ 高危（默认拒绝） | `process_kill` / `system_command` |

**无需在 `permissions` 里声明任何东西**，安全边界由宿主的 MCP 等级门统一把关：
默认最高等级是「低危写」（`LevelRead` + `LevelWrite` 共 27 个），上表最后两个
`LevelRisk` 工具**在用户于「环境管理页」手动开启高危等级之前一律被拒绝**
（返回错误，不会静默放行）。调用方按需 `try/catch` 即可。

#### 文件系统访问（`host.fs.*`）：必须声明路径 scope

文件读写**不是**靠 `filesystem: true` 打开的——那只是「能弹文件对话框」。要读写文件，
必须把 `permissions.filesystem` 写成带白名单的对象：

```json
"permissions": {
  "filesystem": {
    "read":  ["~/Documents/**", "D:/data/*.csv"],
    "write": ["~/Downloads/**"]
  }
}
```

| 写法 | 含义 |
|---|---|
| `"filesystem": true` | **仅**文件对话框（老插件的写法，升级后权限不变） |
| `"filesystem": { "read": [...] }` | 对话框 + 白名单内可读（`host.fs.read/list/stat/exists`） |
| `"filesystem": { "read": [...], "write": [...] }` | 再加白名单内可写（`host.fs.write/mkdir/remove/move`） |

**scope 条目规则**

| 写法 | 匹配范围 |
|---|---|
| `~/Documents` | 该目录**及其整个子树**（等价于 `~/Documents/**`） |
| `~/Documents/**` | 同上，`**` 跨任意多级 |
| `~/Downloads/*` | 只匹配 `~/Downloads` 的**直接**子项，`*` 不跨目录分隔符 |
| `D:/data/*.csv` | 段内通配：只匹配 `D:/data` 下的 csv |
| `../etc/**` / `Documents/**` | ❌ 清单校验直接失败——**必须绝对路径或以 `~` 开头** |

**三条必须知道的约束**

1. **先解析链接再判定。** 宿主会把请求路径解析为绝对真实路径（展开 `~`、消除 `..`、
   展开符号链接**与 Windows 目录联接 junction**）后才与白名单比对。所以
   `~/Documents/link`（软链到 `/etc`）不会被放行，写穿悬空链接同样落在正确的判定下。
2. **读写分离。** `read` 权限不蕴含 `write`，反之亦然。跨目录搬迁这类操作需要
   两侧都在对应白名单内。
3. **`host.fs.*` 是白名单制，表外方法一律拒绝。** 新增文件能力会先在宿主侧登记，
   插件没见过的 `host.fs.xxx` 会拿到 `-32601`（未知方法），不会静默放行。

```javascript
// none 插件示例：读一个 scope 内文件、改完写回
const st = await window.qdHostCall('host.fs.stat', { path: '~/Documents/a.txt' })
if (st.exists) {
  const r = await window.qdHostCall('host.fs.read', { path: '~/Documents/a.txt' })
  await window.qdHostCall('host.fs.write', {
    path: '~/Documents/a.txt',
    content: r.content.replace(/foo/g, 'bar'),
    encoding: r.encoding, // 原样带回，二进制文件才不会写坏
  })
}
```

> ⚠️ `host.fs.write` **不会自动创建父目录**，目录不存在会明确报错——
> 先调 `host.fs.mkdir`（需要 `write` scope），少一条隐式建目录的路径就少一处越权面。

**删除与移动**

```javascript
// 删除：进系统回收站（Windows 回收站 / macOS 废纸篓 / Linux XDG Trash），可恢复
await window.qdHostCall('host.fs.remove', { path: '~/Downloads/old.zip' })

// 改名 / 移动：两侧都要在 write scope 内
await window.qdHostCall('host.fs.move', {
  from: '~/Downloads/a.txt',
  to:   '~/Downloads/b.txt',
})
```

三条会直接报错（**不会**退化成永久删除或静默覆盖，插件可据此判断操作真的没发生）：

| 情况 | 结果 |
|---|---|
| 目标已存在 | 报错，`host.fs.move` 不覆盖——先 `remove` 再 `move` |
| 跨盘符 / 跨文件系统移动 | 报错（`os.Rename` 的限制）；跨卷请自行 read + write + remove |
| 删除网络驱动器 / U 盘 / 光驱上的文件，或路径超过 MAX_PATH | 报错。这些位置上的删除不可恢复，宿主直接拒绝而不是「假装进了回收站」 |

`host.fs.remove` 也不会接受卷根（`C:\` / `/`）这类路径。

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
  "network": false,                              // false=无网络；true=全放行；数组=域名白名单
  "filesystem": false,                           // 文件对话框；要读写文件需写成 {"read": [...], "write": [...]}
  "clipboard": true,                             // 能否读写剪贴板
  "shell": false,                               // false=无；true=全放行；数组=目标前缀白名单
  "processKill": false                          // 是否允许结束进程（高危，默认关）
}
```

`network` / `shell` 支持 **bool 或数组**（`true` = 全放行，数组 = 白名单）；`processKill` 是独立 bool 开关；`filesystem` 支持 **bool 或对象**（语义不同，别混用）：

```json
"network":   ["https://api.github.com", "*.example.com"]   // 仅这两个域名放行；重定向目标也受校验
"shell":     ["file:///C:/Users/me", "https://docs.example.com"]  // 仅这些前缀目标可打开
"filesystem": true                                    // 只能弹文件/保存对话框
"filesystem": { "read": ["~/Documents/**"], "write": ["~/Downloads/**"] }  // 对话框 + 按 scope 读写
```

> 老插件里普遍存在的 `"filesystem": true` 一律按「仅对话框」解释，**升级后不会凭空
> 获得任何读写能力**。路径 scope 的匹配规则见「文件系统访问（`host.fs.*`）」。
>
> ⚠️ **`network` / `shell` / `processKill` 是「清单静态声明」，不是「运行时按对话框授权」**：宿主没有「用户选了某路径/点了某链接就临时放行」的机制。插件要访问的路径/域名必须**预先写进 `plugin.json` 的白名单**后重新安装——这是有意的 fail-closed，保证最小权限可审计。

### 安全边界

插件运行在安全沙箱中，了解边界有助于合理设计：

| 层级 | 防护措施 |
|---|---|
| **进程隔离** | native 插件运行在独立子进程，崩溃不影响主程序 |
| **JS 沙箱** | goja 引擎纯 Go 实现，不直接提供文件系统/网络 API；需要时经 `api.host('http.get' / 'host.dialog.open' / ...)` 转发到宿主注册表，受 `permissions` 管控 |
| **权限声明** | `plugin.json` 声明所需权限，Host Method 层运行时校验 |
| **路径 scope** | 文件读写按 `filesystem.read/write` 白名单判定；判定前先把路径解析为绝对真实路径（展开 `~`、消除 `..`、解析符号链接**与 Windows 目录联接**），阻断借链接/相对路径逃出白名单 |
| **Nonce 握手** | iframe postMessage 携带随机 nonce，防止跨源消息伪造 |
| **存储隔离** | 每个插件只能读写 `plugin_data` 中自己 `plugin_id` 的数据 |
| **ZIP 安全** | Zip Slip 路径穿越防护、100MB 解压上限、50MB 单文件上限、回滚机制 |
| **前端沙箱** | iframe `sandbox="allow-scripts allow-modals allow-downloads"`——**刻意不含 `allow-same-origin`**，插件页因此处于不透明源：拿不到父窗口与 Wails 运行时对象，只能 `postMessage`（宿主能力一律经桥转发） |
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

#### 调用宿主能力：`qdHostCall` / `qdHttp`（推荐）

iframe 内**没有 `fetch` 的出路**——插件页跑在 `sandbox="allow-scripts allow-modals allow-downloads"`
的 iframe 里（无 `allow-same-origin`），是独立不透明源，任何跨域请求都会被拦死；同时它也
拿不到 Wails 运行时，无法直接调宿主绑定。

宿主为此注入了两个全局函数（**三种运行时里只有 `none` 能直接用**，因为它们就住在插件页里）：

```javascript
// 通用转发：method 即 Host Method 名，params 是参数对象，返回 Promise
const r = await qdHostCall('host.mcp.call', { tool: 'todo_list', args: {} })

// HTTP 便捷封装（需 permissions.network）
const r = await qdHttp({ url: 'https://api.example.com/list', method: 'GET', headers: { Authorization: 'Bearer x' } })
// 传对象 body 会自动 JSON.stringify 并补 Content-Type
await qdHttp({ url: 'https://api.example.com/create', method: 'POST', body: { title: 'hi' } })

// 复用宿主 MCP 工具，拿到待办/笔记/剪贴板/环境等能力
const todos = await qdHostCall('host.mcp.call', { tool: 'todo_list', args: {} })

// 插件私有 KV（按 plugin_id 隔离，比 localStorage 更稳）
await qdHostCall('db.set', { key: 'lastQuery', value: 'abc' })
```

- `qdHttp` 返回 `{status, ok, headers, body, truncated}`，`ok` 表示 2xx；响应体上限 2 MiB，超限 `truncated=true`
- 权限不足 / 插件未声明对应能力 / 宿主执行失败都会 **reject**，务必 `try/catch`
- **新增宿主能力时前端桥不需要改**——直接用 `qdHostCall('新方法名', {...})` 即可

> ⚠️ 插件身份（`pluginID`）由**宿主侧状态**决定，不是插件自报的。你用 `qdHostCall`
> 只能以「当前这个插件」的身份调用宿主能力，无法冒充其它插件读写其数据。

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

**Goja 插件（`backend.runtime: "goja"`，内嵌 JS 引擎，有后端逻辑）** — 共 9 个

| 插件 ID | 功能 |
|---|---|
| calcsheet | 计算稿纸（行号引用/变量/函数） |
| compare | 文件/图片对比 + 文本 Diff |
| cron-explainer | cron 表达式解析与可视化生成 |
| formatter | 代码压缩/美化 + SQL 格式化 |
| json-toolbox | JSON 编辑/转换 |
| jwt-decoder | JWT 解码 |
| regex-extractor | 正则提取与替换 |
| text-encoder | Base64/URL/HTML 编解码 + 哈希/HMAC |
| time-converter | 时间戳/时区转换 |

**Pure Frontend 插件（`runtime: none`，纯前端经宿主桥接调 Host API）** — 共 13 个

| 插件 ID | 功能 | 演示的宿主能力 |
|---|---|---|
| batch-rename | 批量重命名 | `qdPickFolder` → `host.fs.list` → `host.fs.move` |
| code-card | 代码分享卡片 | 纯前端 + 导出 PNG |
| crypto-toolbox | 密码/加密工具箱 | 纯前端（AES/RSA/PBKDF2 自实现） |
| curl-converter | curl ↔ 代码转换 | 纯前端 |
| emoji-search | Emoji 搜索 | 纯前端 |
| file-search | 本地文件搜索 + 去重 | `host.fs.list` 递归 + `host.fs.read` + SHA-256 + `host.fs.remove` |
| image-uploader | 图床上传 | `qdPickFile` → `host.fs.read` → `http.post` |
| markdown-preview | Markdown 预览 | 纯前端 |
| md-table-converter | Markdown 表格互转 | 纯前端 |
| mindmap | 思维导图 | 纯前端 |
| qrcode | 二维码生成/识别 | 纯前端 |
| rmb-upper | 金额大写 | 纯前端 |
| unit-converter | 单位换算 | 纯前端 |

**Native 插件（`runtime: native`，自带 Go 源码 + 编译产物）** — 共 25 个

| 插件 ID | 功能 |
|---|---|
| api-loadtest | HTTP 接口压测 |
| color-converter | 颜色格式互转 + 屏幕取色 |
| database | 数据库连接与查询 |
| dir-buster | 路径字典探测 |
| disk-analyzer | 磁盘空间分析 |
| dup-finder | 重复文件查找 |
| exif-viewer | EXIF 信息查看 |
| git-workbench | Git 工作台 |
| hash-calc | 文件哈希计算 |
| hosts-manager | hosts 管理（vendor Go 源码） |
| http-client | HTTP 调试客户端 |
| image-studio | 图片压缩对比 |
| junk-cleaner | 系统垃圾清理 |
| login-tester | 登录接口自检 |
| mail-check | 邮箱足迹检查 |
| netdiag | 网络诊断五合一 |
| ocr-tool | PaddleOCR 离线识别 |
| package-check | 包仓库查询 |
| pdf-toolkit | PDF 工具箱（自带 pdfcpu.exe） |
| port-scanner | 端口扫描（vendor Go 源码） |
| site-audit | 站点审计六合一 |
| speed-test | 网络测速 |
| subdomain-enum | 子域名被动收集 |
| wifi-manager | WiFi 管理（vendor Go 源码） |
| ws-tester | WebSocket 测试 |

> 以上 47 个插件均已迁至 `plugins/external/`（ID 改为 `io.github.parieses.*`），代码可直接复用。goja/none 插件演示「零宿主依赖、纯 JS 自包含」的外部化样板；native 插件演示「Go 源码 vendor + 自编译 entry exe」模式（`build.py` 直接在插件目录 `go build`）。完整 goja 模板见上文「完整示例」。

> 样式自包含（2026-08-24 约定）：外部插件的 `frontend/` 下必须自带 `qd-theme.css`（即 `common.css` 的副本，改名以绕开宿主对 `common.css` 后缀的拦截改写），页面用 `<link rel="stylesheet" href="qd-theme.css">` 引用——zip 解压到任何环境都有完整样式，不依赖宿主注入。宿主仍会向页面注入 `PluginsDir/builtin/common.css/js` 以兼容历史已安装的旧版插件，但新插件不得依赖该注入。

## 完整示例

- **Goja 模板**：`plugins/templates/goja/` 目录下的 Goja 模板项目（含 `main.js` 骨架）。
- **none 插件范式**（纯前端经宿主桥接调 Host API）：
  - `plugins/external/batch-rename/` —— `qdPickFolder` → `host.fs.list` → 规则链 → `host.fs.move` 批量改名（冲突跳过）。
  - `plugins/external/file-search/` —— `host.fs.list` 递归遍历 + `host.fs.read` 取内容 + SHA-256 去重 + `host.fs.remove` 安全删除。
  - `plugins/external/image-uploader/` —— `qdPickFile` → `host.fs.read`（base64）→ `http.post` 图床上传，演示绕过 iframe CORS 的上传。
  - `plugins/external/calcsheet/` —— 纯前端计算稿纸（`none` runtime）最小样板。
- **native 插件完整范例**：
  - `plugins/external/pdf-toolkit/` —— 含合并/拆分/压缩/水印/提取图片/PDF 信息，演示了「多选文件选择 + 目录输出 + 自带 pdfcpu.exe + pickFolder 后端命令」全套实战模式。
  - `plugins/external/hosts-manager/` —— 演示 native 插件「vendor Go 源码 + build.py 自动编译 entry exe」模式。
  - `plugins/external/ocr-tool/` —— 演示 native 插件调用大模型/外部推理后端（`PaddleOCR` ONNX）的离线识别模式。
