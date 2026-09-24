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
├── icon.svg               # 图标：深色主题档（必须，同时是所有消费方的默认回退）
├── icon.light.svg         # 图标：浅色主题档（可选，缺失则浅色主题沿用 icon.svg）
├── main.js                # Goja 后端脚本（goja runtime）
├── main.exe               # 可执行文件（native runtime）
├── frontend/              # 前端资源（none/goja/native 均可选）
│   ├── index.html
│   ├── style.css
│   └── app.js
```

---

## 图标规范

图标是插件在「插件管理」和「命令面板」里唯一的视觉标识，**必须**符合下面的规范 ——
否则几十个插件并排时大小、线重、配色会互相打架。规范由脚本强制校验：

```bash
python glyphs.py                     # 把手绘字形写进各插件 icon.svg（图形源，见下节）
python gen_icons.py --check          # 静态校验全部插件，返回非 0 表示有不合规项
python gen_icons.py --check-render   # 渲染级校验（需 Chrome）：白块占比 / 贴片可见度 / 安全区
python gen_icons.py --gen <插件目录>   # 按规范重新生成该插件的两套图标
python gen_icons.py --gen --all      # 全部重新生成（幂等，可反复跑）
```

`--check` 是纯文本规则，跑得快但**看不出「白块盖住贴片」** —— 贴片和图形都是合法白色，
规则判不出来。所以改完图标必须再跑一次 `--check-render`。

### 图形源：`glyphs.py`（唯一权威）

**不要直接改 `icon.svg`。** 每个插件目录下的 `icon.svg` 是生成产物，重新生成会被覆盖。
图形本体统一写在仓库根的 `glyphs.py` 里 —— 一个字典 `GLYPH = {插件目录名: 图形 markup}`，
用 **24 网格**手绘（生成器负责换算到 64 网格）：

```python
GLYPH["mail-check"] = """
  <rect x="2.5" y="4.4" width="19" height="12.6" rx="2.5"/>
  <path d="M3.3 6.2l8.7 5.8 8.7-5.8"/>
  <path d="M14.4 19.4l2 2 3.6-4" stroke="#008000"/>
"""
```

改图形的完整流程：

```bash
python glyphs.py && python gen_icons.py --gen --all
python gen_icons.py --check --check-render
```

`glyphs.py` 里的写法约束：

| 项 | 规定 |
|---|---|
| 网格 | 24×24，线条式；内容大致落在 `2..22`，生成器会等比缩放填满 64 网格安全区 |
| 线宽 | 统一写 `2` 即可 —— 生成器会把「最粗一笔」归一到 64 网格上的 `3` |
| 层次 | 只靠透明度：主档（不写）/ 次档 `opacity="0.55"` / 衬档 `opacity="0.3"` |
| 颜色 | 只能用哨兵值（见下），**未声明的颜色：深色档被涂白 / 浅色档被涂品牌色** |
| 禁止 | `<text>`、`currentColor`、`fill` 渐变 —— 在 `<img>` / 沙箱里都会失效 |

哨兵色约定（**键写进 `COLOR_SPEC` 才生效**，见「色彩层次」一节）：

| 哨兵 | 含义 |
|---|---|
| `#e6e6e6` | 主笔（深色档 → 白，浅色档 → 品牌色），最常用 |
| `#008000` | `accent:green` 成功 / 可用 / 已生效 |
| `#ff0000` | `accent:red` 失败 / 删除 / 未读 |
| `#cc8800` | `accent:amber` 告警 / 待定 / 过滤 |
| `#0000ff` | `anchor` 贴片锚点色 —— **慎用，见下节** |

### 形态（只有一种，没有第二种画法）

| 项 | 规定 |
|---|---|
| `viewBox` | 固定 `0 0 64 64` |
| 根元素 `width`/`height` | **禁止声明**，尺寸由宿主 CSS 决定 |
| 背景贴片 | 深色档：`<rect ... fill="url(#pg)"/>` + `<defs>` 里一条**同色系** `linearGradient`，满幅、不内缩。<br>浅色档：`<rect ... fill="#ffffff" stroke="品牌色" stroke-width="2.4"/>`，无渐变 |
| 图形主色 | 深色档白 `#fff` / 浅色档品牌色（深锚点），落在安全区 `12..52` 内 |
| 图形例外色 | 只有两种（见下节）：**强调色**（状态语义）与**锚点色**（反衬，慎用） |
| 线宽 | 图形内**最粗的一笔**在 64 网格上恒为 `3`（细部按比例，如 2.4） |
| 透明度 | 只允许三档：主 `1`（省略属性）/ 次 `0.55` / 衬 `0.3` |
| 线帽 | `stroke-linecap="round"` `stroke-linejoin="round"` |

必须避免的写法（都会被 `--check` 拦下）：

- `stroke="currentColor"`：`<img>` 里没有可继承的颜色上下文，会解析成**黑色** —— 深色主题下等于看不见。
- 颜色值写成 `##2a2a2a` 这类双 `#`：非法值会被静默忽略，贴片回退成纯黑。
- 用 `width="48"` / `viewBox="0 0 24 24"` 等旧网格：不同网格换算到同一渲染尺寸后线重会差一倍。
- 贴片写成纯色 `fill="#3381ff"`：观感太平，`--check` 会判错。

### 两套主题

插件图标分深浅两档，宿主按当前主题取用：

| 文件 | 用途 | 形态 |
|---|---|---|
| `icon.svg` | **深色主题**图标；也是所有消费方的默认回退 | 饱和同色系**渐变贴片** + **白色**字形 |
| `icon.light.svg` | **浅色主题**图标 | **白底贴片 + 品牌色描边圈** + **品牌色**字形 |

解析顺序：浅色主题先找 `icon.light.svg`，找不到就用 `icon.svg`；深色主题始终用 `icon.svg`。
所以**只带一个图标的老插件不用改也能正常工作**，但两档齐备观感最好。

**两档必须肉眼可辨，不能只是换个色号。** 设计原理（同 iOS/Android 明暗双形态惯例）：

- 深色档：饱和渐变贴片 + 白字 —— 在暗色外壳上最跳。
- 浅色档：图标显示在**近白背景**上，数学上「白字形 + 明亮贴片」过不了 3:1 对比度
  （白字要求贴片足够暗，贴片足够暗又和浅色背景分不开）。所以浅色档反过来：
  **白底贴片**（`fill="#ffffff" stroke="品牌色" stroke-width="2.4"` 描边圈负责在亮背景上勾出边界），
  字形主色 = 该组**深锚点**（在白底上对比度 ≈ 3.7:1），强调色用浅档（白底上 3.4~4.4:1）。
- **几何 / 网格 / 安全区 / 线宽两档 100% 一致** —— 只差颜色与贴片处理。摆在一起是
  「同一个图标的两种皮肤」，不是两个图标。

### 语义色板

插件图标颜色**只能**取自下表（6 个语义色，按插件功能归类，不按个人喜好）。

- **深色档**：锚点即贴片渐变起点，第二端（渐变端）由 OKLCH 推出（见下节）。
  硬约束：「白色图形对贴片**两端** ≥ 3:1」且「贴片两端对深色背景 ≥ 3:1」。
- **浅色档**：贴片是白底，**没有渐变端**；品牌色（字形与描边圈）直接用**深色档锚点**
  （在白底上 ≈ 3.7:1）。

| 语义 | 用途 | 深色档（锚点 → 渐变端） | 浅色档品牌色 |
|---|---|---|---|
| `blue` | 通用工具 / 转换 / 文档 | `#3381ff` → `#5957e0` | `#3381ff` |
| `violet` | 开发与代码 | `#916aff` → `#9740cd` | `#916aff` |
| `teal` | 网络与连通 | `#1c958a` → `#0f777f` | `#1c958a` |
| `amber` | 安全与探测 | `#d46615` → `#9b6215` | `#d46615` |
| `green` | 数据与系统 | `#24993e` → `#1d7959` | `#24993e` |
| `pink` | 媒体与图像 | `#ea4683` → `#d1243e` | `#ea4683` |

（`PALETTE` 里的 `light` 锚点值保留，仅作为色板推导的历史参考，不再参与浅色档生成。）

新增插件时在 `gen_icons.py` 的 `GROUP` 字典里登记插件目录 → 语义色，再跑一次 `--gen`。

### 色彩层次：渐变 / 挖空 / 强调色

「一个纯色贴片 + 全白图形」是**两种颜色**，几十个并排会显得单调。v3 在不改骨架的前提下
开了三个色彩来源（深色档其余一律白；浅色档其余一律品牌色）：

**1. 贴片 = 同色系渐变。** 锚点仍是上表的语义色（所以对比度约束天然不变），第二端沿色环
只漂 `HUE_DRIFT` 度、明度朝**更深**方向走 `LUM_DRIFT`：

```python
HUE_DRIFT = 18.0     # 同色系内，肉眼读作「同一个颜色」
LUM_DRIFT = 0.085    # 固定「更深」方向：深色档锚点已贴近 3:1 下限，提亮会当场击穿白字对比度
```

两个值都可调，但**改完必须重跑 `--check`** —— 它包含色板级对比度体检
（`palette_contrast_errors()`），漂得太狠会直接报错。

**2. 反衬 = 锚点色，但它画的是「贴片同色」，压在贴片上会隐形。**
它只在一个场景成立：**叠在白色实心图形之上**做分隔（白块里嵌一条「露底」的缝）。
想画「洞 / 锁孔 / 镂空」这类元素，**用白色实心**（`fill="#e6e6e6" stroke="none"`），
别用锚点色 —— 实测 `crypto-toolbox` 的钥匙孔用锚点色画，在贴片中间那一段渐变上直接消失。

**3. 强调色 = 3 个固定语义色，只给真语义元素。**

| 名 | 深色档 | 浅色档 | 语义 |
|---|---|---|---|
| `green` | `#35c268` | `#1a9a4a` | 成功 / 可用 / 已生效 |
| `red` | `#ff5a6e` | `#e0324a` | 失败 / 删除 / 未读 |
| `amber` | `#f5b025` | `#c07d00` | 告警 / 待定 / 过滤 |

**不做装饰性着色。** 判据是「这个元素本来就表达状态」——状态点、diff 增删条、端口状态。
节点、连线、外框这类**结构性元素**一律回主色（深色档白 / 浅色档品牌色），硬塞彩色只会显得杂。

声明写在 `gen_icons.py` 的 `COLOR_SPEC` 里，**键是 `glyphs.py` 里的哨兵色**，值是目标：

```python
COLOR_SPEC = {
    "mail-check":     {"#008000": "accent:green"},                          # 邮箱有效
    "compare":        {"#008000": "accent:green", "#ff0000": "accent:red"},  # diff 增 / 删
    "disk-analyzer":  {"#cc8800": "accent:amber"},                          # 空间告警区
    "color-converter": {"#ff00ff": "#ff5f6d", "#00ffff": "#4f8bff"},        # 直指定（例外）
}
```

映射是**逐属性**做的，所以同一元素上「强调色 + 白色描边」能各自保留。
`color-converter` 是唯一走「直接指定 hex」的例外 —— 它的内容本来就是颜色。

`SOURCE_PATCH`（源级字符串改写）保留但当前为空，只在颜色映射表表达不了时用
（比如图形内部自带渐变）。


### 从旧图标迁移（接入外来图标时用）

这套流水线原本是为「把仓库里的旧图标就地规格化」写的；现在本仓库 50 个插件已全部改由
`glyphs.py` 手绘，但**引入外部/第三方图标时仍走这条路** —— `--gen` 会就地规格化它，
**不改美术，只做四件事**：

1. **剥卡底** —— 删掉旧图标自带的整块底色矩形，统一换成规范贴片。
   判据是「实心填充 + 两轴 ≥ 70% viewBox + 基本居中」，`fill="none"` 的画框天然不会被误伤。
2. **着色** —— 未声明的颜色转白；`COLOR_SPEC` 里声明的转锚点色 / 强调色。
3. **归一线宽** —— 让最粗的一笔在 64 网格上恰为 `3`，细部按同一系数缩放。
4. **缩放进安全区** —— 图形包围盒（含描边外扩）贴合 `12..52`，居中。

必须知道的坑（前两类会让图标变成一块白方，图形彻底看不见）：

| 写法 / 操作 | 后果 |
|---|---|
| 卡底是**内缩**的（如 `<rect x="6" y="6" width="52" height="52">`，只占 81%） | 剥不掉 → 被转白 → 整片白块 |
| 卡底用**渐变**（`fill="url(#g)"`）或**带描边** | 同上；渐变还会让 `<stop>` 一起被转白 |
| 根元素上写表现属性（`<svg fill="none" stroke="#4a9eff" stroke-width="2">` + 裸几何） | 属性随 `parse_svg` 丢失 → 几何 `fill` 回退**默认黑** → 整块黑方 |
| 改了生成逻辑后**直接从已生成的图标再跑 `--gen`** | 输入已是 v3 → 走「还原再着色」路径，修复不生效。**必须先还原 v1** 再跑 |

第一类由 `--check-render` 兜住：白像素 > 45% 或贴片像素 < 25% 直接判错。

**幂等**由两条保证：几何吸附（`GEOM_TOL`，已在容差内就只换色不重拟合）+ 从 v3 源重生成时
先把已解析的颜色还原回源色（`uncolor`）。所以从 v1 生成一次后，连跑 N 次输出逐字节一致 ——
**这是必测项**，改完生成器要实际跑三次比对 md5。

**已知例外**：`<text>` 里的 emoji 不受 `fill` 控制（彩色字形由字体决定）。
本仓库已无此类图标，外部图标遇到时保留原样即可。


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
| `category` | 分类，展示在插件市场（如 `开发者日常` / `网络工具` / `办公效率`）|
| `icon` | 图标文件名，通常 `icon.svg`。规范见上文「图标规范」；浅色主题档为同目录的 `icon.light.svg`，无需在此声明 |
| `changelog` | 更新日志（纯文本/Markdown），展示在「在线市场」插件详情页；可选 `changelog_i18n` 提供多语言版本（`{"zh-CN": "...", "en-US": "..."}`）。不写则详情页显示「暂无更新日志」 |
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

宿主会向每个插件前端页面注入桥接脚本（见「前端开发 → 调用宿主能力」），因此 none 插件的 `frontend/index.html` 可以直接调用 `qdHostCall` / `qdHttp` / `qdPickFile` / `qdPickFolder` / `qdPrint` 等全局，无需自己 `postMessage`：

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
    // qdPickFile / qdPickFolder / qdHostCall / qdHttp / qdPrint 均由宿主注入，直接可用
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
| `host.window.hide` / `host.window.show` | 临时隐藏 / 恢复宿主窗口：同时管主窗口、命令面板窗口与**本插件的独立窗口**，隐藏前各自记录原可见性，恢复时只还原原本可见的（供屏幕取色等场景避免遮挡取样区域） | 无需权限 |
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

已注册工具（`services/mcp/service.go`；完整清单与等级以客户端 `tools/list` 返回为准）：

| 类别 | 工具 |
|---|---|
| 应用与系统 | `app_info` · `port_list` |
| 内容检索 | `item_search` / `item_open` · `recent_items` · `workspace_list` |
| 笔记 | `note_search` / `note_create` / `note_update` / `note_quick` |
| 待办 | `todo_list` / `todo_create` / `todo_done` |
| 剪贴板 | `clipboard_recent` / `clipboard_copy` |
| 环境编排 | `env_list` / `env_status` / `env_log` / `env_versions` / `env_start` / `env_stop` / `env_restart` |
| 日志与崩溃 | `log_list` / `log_read` / `crash_list` / `crash_read` / `plugin_list` / `plugin_execute` |
| ⚠️ 高危（需在环境管理页开启，默认拒绝） | `process_kill` / `system_command` |

**无需在 `permissions` 里声明任何东西**，安全边界由宿主的 MCP 等级门统一把关：
默认最高等级是「低危写」（`LevelRead` 17 个 + `LevelWrite` 11 个，共 28 个），上表最后一行的那两个
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

#### 打印：`qdPrint`（**必须走它，别自己调 `window.print()`**）

```javascript
await qdPrint({ html: buildPrintDocument(), page: 'A4' })
```

- `html`：**一份完整的 HTML 文档字符串**（自带全部 CSS），宿主会在顶层文档里渲染后调系统打印
- `page`：可选，`@page` 尺寸（如 `'A4'`）；省略则不覆盖
- 打印对话框关闭后 resolve；内容为空 / 宿主打印失败会 reject

> 🚫 **不要在插件里直接 `window.print()`** —— WebView2 / Chromium 的 `window.print()` 只作用于
> **顶层文档**。插件跑在沙箱 iframe 里，打印出来的是**整个 QuickDock 应用**（暗色外壳），
> 你辛苦排版的纸面内容根本不在打印上下文里，用户拿到的就是**一张白纸**。
> `qdPrint` 把 HTML 交给宿主，由宿主在顶层文档渲染后打印；打印期间宿主界面被隐藏、
> 插件 CSS 被包进 `@media print` 隔离（不会污染宿主），打完自动清理。

**两个配套建议**：

1. **打印与导出共用同一份 HTML**。例如都调 `buildPrintDocument()`，一份交给 `qdPrint`，
   一份另存为 `.html`。这样「打印件」与「导出件」永远一致，也不会出现只修好一边的情况。
2. **导出前先确认样式已内联**。宿主会把插件页里的 `<link rel="stylesheet">` 内联成
   **无 id 的 `<style>`**；一旦内联失败（只剩一条 `<!-- quickdock: css inline failed -->` 注释），
   你从 DOM 里采集到的 CSS 就是不完整的 —— 纸面尺寸、白底、边框全丢，只剩行内样式里
   那些浅灰描红字，**白纸+浅灰 = 用户眼中的「导出是一片空白」**。导出/打印前断言关键
   选择器（如 `.your-page`）确实出现在采集到的 CSS 里，缺失就明确报错，别把白纸交出去。

另外，导出的 HTML 建议显式声明 `color-scheme: light only` 并给纸面元素加
`forced-color-adjust: none`：前者避免浏览器把「跟随系统深色」的自动反转作用到固定浅色的
纸面上，后者避免 Windows 高对比度模式把 `background-image`（很多格子线就是用内联 SVG
背景画的）与自定颜色一并抹掉。

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

<!--DEVGUIDE_PLUGINS_START-->

**Goja 插件（`backend.runtime: "goja"`，内嵌 JS 引擎，有后端逻辑）** — 共 9 个
| 插件 ID | 功能 |
|---|---|
| calcsheet | 兼具草稿纸自由度与电子表格智能的多行计算工具，支持行号引用、变量定义、函数计算 |
| compare | 文件/图片对比（元数据 + 图片预览 + 文本内容差异）与文本块逐行 Diff 合二为一 |
| cron-explainer | 解析 cron 表达式（含义/下次执行/小时分布），并可可视化生成表达式、实时预览下次执行时间 |
| formatter | 通用代码压缩/美化（JS / CSS / HTML 自动检测）与 SQL 格式化合二为一 |
| json-toolbox | JSON 编辑器（格式化/折叠/编辑）、JSON → TypeScript / Go、JSON ↔ YAML / TOML / XML 互转 |
| jwt-decoder | 解码 JWT Token，查看 Header/Payload，验证过期时间 |
| regex-extractor | 正则提取与替换：匹配高亮、分组捕获、反向引用替换（$1/$2）、一键复制结果 |
| text-encoder | Base64 / URL / HTML 编解码，MD5 / SHA1 / SHA256 / SHA512 哈希与 HMAC 签名，Base64 图片识别预览，2/8/10/16 进制互转与字节单位换算 |
| time-converter | Unix 时间戳 / ISO 8601 / 中文日期 / 相对时间互转，支持任意时区偏移输出 |

**Pure Frontend 插件（`runtime: none`，纯前端经宿主桥接调 Host API）** — 共 15 个
| 插件 ID | 功能 | 演示的宿主能力 |
|---|---|---|
| batch-rename | 选择一个文件夹，按前缀/后缀/查找替换/正则/序号/扩展名/大小写规则预览并重命名文件，全部通过宿主文件系统能力执行，不离开本机 | qdPickFolder → host.fs.list → host.fs.move |
| code-card | 把代码渲染成高颜值分享卡片：语法高亮、主题背景、窗口装饰，一键导出 PNG | 纯前端 |
| crypto-toolbox | 随机密码 / 口令生成（字符集可配、熵值与强度评估）＋ AES-GCM / AES-CBC 加解密 ＋ RSA-OAEP 密钥对与加解密 ＋ PBKDF2 密钥派生，全部在本机完成，不联网、不上传 | 纯前端 |
| curl-converter | 把 curl 命令解析成 Python / Go / JavaScript / PHP 请求代码，也支持把 fetch、requests 代码反向转回 curl | 纯前端 |
| data-generator | UUID v4 / v7 生成（批量、可大写）＋ 随机字符串 / 整数 / 字节 ＋ 测试假数据（姓名 / 手机 / 邮箱 / 公司 / 地址 / 身份证(测试) / 日期 / 网址 / 用户名 / 人员），全部在本机生成，不联网、不上传 | 纯前端 |
| emoji-search | 搜索 Emoji 并一键复制到剪贴板 | 纯前端 |
| hanzi-copybook | 汉字描红字帖生成器：支持任意汉字定制、拼音与笔顺显示、田字格/米字格排版，面向小学生规范书写与笔顺习惯养成，一键导出/打印 | 纯前端 |
| image-uploader | 选择本地图片，读取后通过宿主网络能力上传到图床（freeimage.host / imgbb），返回可访问的图片链接，链接可一键复制。不上传任何其它文件。 | qdPickFile → host.fs.read → http.post |
| markdown-preview | 实时渲染 Markdown（GFM：标题/列表/表格/任务列表/引用）+ 代码高亮，一键复制为 HTML | 纯前端 |
| md-table-converter | Markdown 表格与 CSV / JSON / HTML 四种格式互转，自动识别输入格式，写文档、导数据的顺手小工具 | 纯前端 |
| mindmap | 把 Markdown 大纲 / 缩进列表实时渲染成思维导图，自动分层配色、可点击折叠分支、支持缩放与导出 PNG / SVG | 纯前端 |
| qrcode | 文本/URL 生成二维码，支持保存 PNG；从图片识别二维码内容 | 纯前端 |
| rmb-upper | 数字金额转中文大写（壹贰叁…），财务报销、开票、合同的刚需小工具 | 纯前端 |
| type-trainer | 开发者向打字训练器：英文 / 中文 / 代码 / 导入四种模式，支持导入 TXT 字库，实时统计 WPM 与准确率，本地记录历史与最佳成绩 | 纯前端 |
| unit-converter | 长度 / 面积 / 体积 / 重量 / 温度 / 速度 / 数据存储 / 时间 / 压力 / 能量 / 功率 / 角度 共 12 类单位实时互转，输入一个值即列出该类别全部换算结果 | 纯前端 |

**Native 插件（`runtime: native`，自带 Go 源码 + 编译产物）** — 共 25 个
| 插件 ID | 功能 |
|---|---|
| api-loadtest | 功能丰富的 HTTP 接口压测工具：支持并发/时长双模式、自定义 Header 与 Body、实时 QPS 与延迟分布(p50/p90/p95/p99)、状态码分布、错误率统计与结果一键导出 |
| api-mock | 本地 HTTP 接口 Mock 服务：可视化配置路由规则（方法/路径/状态码/响应体/延迟），一键启动本地服务，实时查看请求日志，联调前端与第三方对接无需真实后端 |
| color-converter | 颜色格式互转（HEX / RGB / HSL）+ 屏幕取色，支持常见英文色名识别 |
| database | 轻量数据库连接与查询工具：MySQL / SQLite / Redis 连接管理、SQL 与 Redis 命令执行、库表浏览器（库→表/视图→字段、Redis 键树）、Redis 键类型感知详情与增删改（string/hash/list/set/zset）、TTL 与 DB 管理、结果网格内联行编辑。数据独立存储。 |
| dir-buster | 对指定目标 URL 用内置常见路径字典进行轻量探测（自用）：并发受限、可配扩展名，采用异步会话模型实时返回命中的非 404 路径。仅探测你授权的目标，内置字典、不递归 |
| disk-analyzer | 可视化磁盘空间占用分析工具，类似 SpaceSniffer，支持树图展示目录结构 |
| exif-viewer | 选择图片（JPEG/PNG）查看拍摄时间、相机/镜头、参数与 GPS 经纬度等 EXIF 信息 |
| git-workbench | Git 仓库一体化工作台：仓库浏览、二分定位 bug 引入提交、三方合并冲突可视化解决、代码演化时间轴、历史改写（改作者/删敏感文件）、仓库体检与知识孤岛识别。 |
| hash-calc | 计算文件的 MD5/SHA1/SHA256/SHA512 摘要，结果一键复制 |
| hosts-manager | 管理系统 hosts 文件条目，一键启用/禁用/新增 |
| http-client | 轻量 HTTP 请求调试客户端：项目管理请求、目录与文档树、环境变量与 {{var}} 替换、请求历史重放、Postman 集合导入。数据独立存储。 |
| image-studio | 仿 Squoosh 实时对比：左右预览对比，质量滑块、缩放(锁定比例/百分比/预设)、旋转/翻转、亮度/对比度/饱和度调整，实时预览文件大小与压缩率 |
| junk-cleaner | 扫描并清理系统垃圾文件：临时文件/更新缓存/缩略图缓存/预读取/崩溃转储等，安全只读扫描+确认后删除 |
| login-tester | 对自身网站登录接口进行密码库撞库/爆破安全自检，支持并发、限速与锁定检测。仅用于你拥有或已授权的站点。 |
| mail-check | 邮箱足迹与有效性检查：全量 123 站探测（参考 holehe 适配，Gravatar/GitHub/ProtonMail/Spotify 等已校准，其余逐步补），语法 / MX / SMTP 有效性验证 |
| netdiag | 将 Ping 监视、路由追踪、局域网扫描、IP 归属地、端口指纹五个网络工具合并为单一插件，按需切换标签页，共享一个原生子进程 |
| ocr-tool | 基于 PaddleOCR (ONNX) 的离线文字识别，支持中英文，首次使用自动下载约 178MB 模型（ModelScope 镜像），之后完全离线运行，跨 Windows / macOS / Linux。 |
| package-check | 输入包名，并发查询 npm / PyPI / Composer / Go 四个仓库的版本信息；并基于 OSV.dev 查询包版本的已知漏洞（CVE / 安全公告） |
| pdf-toolkit | PDF 处理工具箱：合并/拆分/压缩/加水印/提取图片，无需安装 Adobe Acrobat |
| port-scanner | 检查端口占用，显示进程名和 PID |
| site-audit | 将 WHOIS 查询、SSL 证书检查、DNS 查询、DNS 传播检查、HTTP 状态码速查、HTTP 安全头审计六个站点工具合并为单一插件，按需切换标签页，共享一个原生子进程 |
| speed-test | 测量网络下载速率与延迟：流式下载测速（支持自定义测速节点 URL），实时显示速率与进度，采用异步会话模型规避宿主执行超时 |
| subdomain-enum | 被动收集域名子域名（证书透明日志 CertSpotter / crt.sh + HackerTarget + urlscan + rapiddns + AlienVault OTX），可选并发解析 A 记录筛选存活 |
| wifi-manager | 查看网络列表、WiFi 密码、连接状态 |
| ws-tester | 连接 ws/wss 服务，发送消息并实时查看返回的帧，支持多连接与历史 |

> 以上 49 个插件均已迁至 `plugins/external/`（ID 改为 `io.github.parieses.*`），代码可直接复用。goja/none 插件演示「零宿主依赖、纯 JS 自包含」的外部化样板；native 插件演示「Go 源码 vendor + 自编译 entry exe」模式（`build.py` 直接在插件目录 `go build`）。完整 goja 模板见上文「完整示例」。

<!--DEVGUIDE_PLUGINS_END-->

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
