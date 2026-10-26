# 插件 UI 样式规范

适用范围：本仓库 `*/frontend/**`（80+ 个插件页面）。
开发流程与清单格式见 `plugin-dev-guide.md`，本文只管**长什么样、改哪里**。

一句话原则：**共享层只有一份**。改 `templates/` → 跑同步 → 全库一起变；
不要改某个插件目录下的副本，也不要在他 `index.html` 的 `<style>` 里重定义共享类。

---

## 1. 三份共享文件与它们的权威源

| 文件（插件目录下） | 权威源 | 提供什么 |
|---|---|---|
| `frontend/qd-theme.css` | `templates/qd-theme.css` | 设计令牌 + 布局骨架（`p-app/p-head/p-content`）+ 基础控件（`p-btn/p-input/p-tab` 等） |
| `frontend/components.css` | `templates/components.css` | 历史组件库（`btn/input/card/tabs/badge/modal`）。**尺寸已与 `p-*` 对齐，可以继续用；新界面仍优先 `p-*`** |
| `frontend/icons.css` | `templates/icons.css` | 图标尺寸与对齐 |

**同步与校验**

```bash
python tools/sync_shared.py --all     # 改完 templates/ 后把权威复制到各插件副本
python tools/check_shared.py          # 校验零漂移（应输出 OK）
python tools/check_design.py          # 拦截"影子重定义共享类"（新代码 ERROR 级）
python tools/check_plugins.py         # 资源引用缺失、未定义 CSS 变量、emoji 图标
```

- **fork 只有 `photo-library` 一个**：它不参与自动覆盖，`templates/qd-theme.css` 的令牌变了要手工合并进它的 `:root`，再跑 `check_shared.py`。新增 fork 必须同时在 `sync_shared.py` 和 `check_shared.py` 的 `QTHEME_FORKS` 里登记并写明原因。
- `sync_shared.py` 只同步**已存在**的副本，不会给不引用 `components.css` 的插件（如 `game-*`）凭空造一个。
- **打包自包含**：`frontend/` 下必须自带 `qd-theme.css`，页面用 `<link rel="stylesheet" href="qd-theme.css">`。zip 解压到任何环境都要能独立打开，不依赖宿主注入。

---

## 2. 为什么插件不能直接用宿主令牌

插件页跑在 iframe 里。宿主注入的桥脚本（quickdock 仓库 `frontend/src/utils/pluginBridge.ts`）只做两件事：
把 `data-theme` 写到 iframe 的 `<html>` 上、转发 postMessage。**它不注入任何 CSS 变量。**

所以在插件里套用宿主令牌（`color-bg-primary` 那一族）会拿到空值，整条声明静默失效，**没有任何报错** —— 颜色直接落回浏览器默认，表现为"这块背景是透明的/字是黑的"。
`tools/check_plugins.py` 的「CSS 变量未定义且无兜底值」专门拦这一类。

插件必须用 `qd-theme.css` 提供的 `p-*` 体系（下面这张表的变量名）。

---

## 3. 令牌（`qd-theme.css` 的 `:root`）

改宿主色阶时**必须同步这里**：宿主改 `frontend/src/style.css` → 把新值搬进
`templates/qd-theme.css` → `sync_shared.py --all`。两张表的对应关系以本文为准
（宿主那份 `ui.md` 不再收录插件令牌）。

| 语义 | 变量 | 深色 | 浅色（`html[data-theme="light"]`） |
|---|---|---|---|
| 应用底 | `--bg-primary` | `#0f1116` | `#f5f6f8` |
| 卡片/页头 | `--bg-secondary` | `#151920` | `#ebedf1` |
| 控件底 | `--bg-tertiary` | `#1c2029` | `#e3e6eb` |
| hover | `--bg-hover` | `#1f242e` | `#e1e4e9` |
| 选中/按下 | `--bg-active` | `#262d39` | `#d7dbe2` |
| 正文 | `--text-primary` | `#e6e9ef` | `#1f242c` |
| 次要 | `--text-secondary` | `#a1a8b4` | `#565e6b` |
| 弱 | `--text-muted` | `#6f7784` | `#737b88` |
| 禁用/占位 | `--text-disabled` / `--text-placeholder` | `#565d68` | `#a2a8b0` |
| 分隔线 | `--border` | `#22272f` | `#d8dce2` |
| hover 描边 | `--border-strong` | `#2e3440` | `#c6cbd3` |
| 强调 | `--accent` / `--accent-hover` | `#5b8cff` / `#4a7af0` | 同 |
| 强调淡底/描边 | `--accent-soft` / `--accent-line` | `.10` / `.45` | `.08` / `.5` |
| 成功 | `--success` / `--success-soft` | `#22c55e` / `.12` | 同 |
| 危险 | `--danger` / `--danger-soft` | `#ef4444` / `.12` | `/ .10` |
| 警告 | `--warning` / `--warning-soft` | `#f59e0b` / `.12` | `/ .14` |

**尺寸阶梯**

| 类别 | 值 |
|---|---|
| 圆角 | `--radius-xs` 4 / `--radius-sm` 6 / `--radius` 6 / `--radius-md` 10 / `--radius-lg` 10 / `--radius-xl` 14 / `--radius-pill` |
| 间距 | `--sp-1..6` = 4/8/12/16/20/24 |
| 字号 | `--fs-xs` 11 / `--fs-sm` 12 / `--fs-md` 13 / `--fs-lg` 15（`--fs-xl` 16、`--fs-2xl` 20 仅用于数值展示） |
| 字重 | `--fw-normal` 400 / `--fw-medium` 500 / `--fw-semibold` 600 |
| 动效 | `--transition` 150ms（只给 color/background/border） |

**高度基准（与宿主一致，不得自造）**

| 元素 | 高度 |
|---|---|
| 页头 `.p-head` | 44 |
| 按钮 `.p-btn` / 小号 `.p-btn-sm` | 28 / 24 |
| 输入 `.p-input` / 下拉 `.p-select` / 页签 `.p-tab` | 28 |
| 列表行 `.p-item` | `min-height` 34 |
| 复制按钮 `.p-copy` | 24 |
| 历史按钮 `.btn` / `.btn-sm` / `.btn-xs` / `.btn-xxs` | 28 / 24 / 22 / 20 |
| 历史输入 `.input` / `.select` / `.input-sm` / `.input-xxs` | 28 / 28 / 24 / 22 |

历史类（`components.css`）的高度**用固定 `height`**，不用上下 padding 撑 —— 插件常把字号局部改小，
padding 撑出来的高度会跟着缩，同一排按钮就不齐了。多行控件（`.textarea`，以及误用 `.input` 的
`<textarea>`）走 `height: auto`，这条在 `components.css` 里有元素选择器兜底。

---

## 4. 组件（用这些，别自己写）

### 4.1 布局骨架

```html
<div class="p-app">                       <!-- 整页列，100vh -->
  <div class="p-head">                    <!-- 44px 页头：标题 + 右侧动作 -->
    <span class="p-head-title">工具名</span>
    <div class="p-head-actions">
      <button class="p-btn">重置</button>
      <button class="p-btn p-btn-primary">执行</button>
    </div>
  </div>
  <div class="p-toolbar"><!-- 可选：筛选条 --></div>
  <div class="p-content">                 <!-- 滚动区，内边距 16 -->
    <div class="p-section">
      <div class="p-section-label">参数 <span class="p-spacer"></span></div>
      <div class="p-field">
        <span class="p-label">路径</span>
        <input class="p-input" />
      </div>
    </div>
  </div>
  <div class="p-statusbar">就绪</div>       <!-- 可选：底部状态 -->
</div>
```

滚动只发生在 `.p-content` / `.p-list` / `.p-body-col`；`body` 固定 `100vh` + `overflow:hidden`。

### 4.2 按钮

| 类 | 用途 |
|---|---|
| `.p-btn` | 默认次级：1px 边、透明底，hover 浮出底色 |
| `.p-btn-primary` | 主行动，实底 accent。**一屏只放一个** |
| `.p-btn-ghost` | 页头成组的次要动作，无边框 |
| `.p-btn-danger` | 破坏性动作：淡红底红字，hover 转实红 |
| `.p-btn.is-on` | 开关型已选中（视图切换/筛选） |
| `.p-btn-sm` | 表格行尾、卡片内部 |

图标按钮用 `.p-copy`（24×24）。图标用 `icons.css` 里的内联 SVG，**不要用 emoji**（`check_design.py` 会拦，游戏美术素材除外）。

### 4.3 与其他控件

| 用途 | 类 |
|---|---|
| 输入 / 多行 / 下拉 | `.p-input` / `.p-textarea` / `.p-select`（等宽输入加 `.p-input-mono`） |
| 输出区（日志/结果） | `.p-output`（等宽、自动换行、可滚动） |
| 页签 | `.p-tabs` > `.p-tab`（选中加 `.active`），不要用实底强调色做选中 |
| 列表 | `.p-list` > `.p-item`（选中加 `.selected`），内部 `.p-item-label` / `.p-item-desc` |
| 卡片 | `.p-card`（可点）/ `.p-section`（容器） |
| 状态标签 | `.p-tag` + `.p-tag-ok` / `-warn` / `-info` / `-neutral` |
| 提示 | `.p-error`（页内错误条）/ `.p-toast`（浮层） |
| 空状态 | `.p-empty` |
| 分隔 | `.p-sep` / `.p-spacer` |

### 4.4 历史组件类（`components.css`）—— 老插件怎么改

老插件里的 `.btn` / `.input` 不再是"另一套尺寸"，它们已经收到与 `p-*` 相同的 28px 基准。
写新界面仍旧用 `p-*`；维护老界面时，**把本地的语义修饰词换成共享修饰类，而不是在本地重定义外观**：

| 老写法 | 改成 | 说明 |
|---|---|---|
| `class="btn primary"`（本地 `.btn.primary`） | `class="btn btn-primary"` | 本地没有 `.primary` 就只是普通按钮，改完才真的有强调色 |
| `class="btn sm"`（本地 `.btn.sm`） | `class="btn btn-sm"` | |
| `class="btn stop"` / `.btn.danger` | `class="btn btn-danger"` | |
| `class="btn-ghost"`（本地把 ghost 写成带底带边的次级按钮） | `class="btn"` | 共享 `.btn-ghost` 是**无边框**的页头动作，不是"次级按钮" |
| 本地 `.input { flex:1; height:32px; padding…背景…边框… }` | `.input { flex:1 }` + `/* qd-allow-local: input */` | 只留宽度/换行策略，外观全部交给共享层 |

JS 里改类名同理：`b.className = 'btn stop'` 要写成 `'btn btn-danger'`，
按钮运行态切换最容易漏（`check_design.py` 只看样式，看不到 JS 字符串）。

---

## 5. 禁止事项

| 做法 | 为什么不行 | 正确做法 |
|---|---|---|
| 在 `index.html` 的 `<style>` 里写顶层 `.p-btn { height: 24px }` | 影子重定义共享类，同一个"执行"按钮在不同插件三种身高 —— 这正是 `check_design.py` 要消灭的 | 需要局部差异就加祖先限定（`.my-panel .p-btn`）或加自己的类；确属特例写 `/* qd-allow-local: btn */` 并登记台账 |
| 用宿主令牌（`color-bg-primary` 一族） | iframe 里没有这些变量，声明静默失效 | 用第 3 节的 `--bg-*` / `--text-*` |
| 写死颜色（`#1e1f23` / `white` / `rgba(0,0,0,.6)`） | 浅色主题直接失效 | 用令牌；半透明遮罩用 `--bg-primary` + `opacity` 或 `color-mix` |
| 用 `var(--x, #333)` 兜底 | 掩盖变量不存在 | 只写 `var(--x)` |
| emoji 当图标 | 跨平台字形不一致，与线性图标风格冲突 | 内联 SVG（`icons.css`） |
| 自己写 `confirm()` / `alert()` | 原生弹窗阻塞并脱离宿主风格 | `await qdConfirm(msg)` / `qdAlert(msg)` |
| 用 `input[type=file]` 选文件 | 沙箱限制 + 宿主失焦 | `qdPickFile()` / `qdPickFolder()` |
| `:has()` / `@container` | WebView2 旧版整条规则失效且无报错 | 显式加 class |
| 让 `.btn` / `.input`（components.css）承担新界面 | 尺寸虽已对齐，但两套命名长期并存必然再分叉 | 新界面一律 `p-*` |
| 在老插件的 `<style>` 里重定义 `.btn` / `.input` 的外观（含 `height`/`padding`/`background`） | 共享层改一处就全库变的前提被本地点覆盖掉，2026-10 之前有 20 个插件这么干，按钮从 28 到 34px 五种身高 | 删掉本地外观规则；语义修饰词按 4.4 换成 `btn-primary` / `btn-sm` / `btn-danger`；只留宽度策略并加 `qd-allow-local` 注释 |

弹窗里的 Esc：插件自己消费了要 `preventDefault()`，否则宿主会顺带把整个插件页关掉。

---

## 6. 提交前必跑

```bash
python tools/sync_shared.py --all     # 若改过 templates/
python tools/check_shared.py          # → OK
python tools/check_design.py          # → 0 处新增一级违规
python tools/check_plugins.py         # → 0 ERROR
```

三条都要绿。`check_design.py` 退出码非 0 = 有新增一级违规；存量违规登记在 `tools/design_baseline.json`，迁完一个删一条，**不要往台账里加新条目来"通过"检查**。

**注意**：`check_plugins.py` 扫的是**所有文本**，包括 CSS 注释。注释里写出完整的 `var(--xxx)` 字面量会被当成真实引用报「变量未定义」—— 讲这类例子时写成 `color-bg-primary 这一族` 或加空格，别写成可解析的完整调用。

---

## 7. 改样式的顺序（避免"改一处漏两处"）

1. 先改 `templates/qd-theme.css`（令牌 → 骨架 → 控件，一次只动一类）。
2. `sync_shared.py --all`，然后 `check_shared.py` 确认零漂移；`photo-library` 手工合并令牌。
3. 挑 2~3 个代表性插件（表单型 `json-toolbox`、多页签 `qrcode`、复杂布局 `database`）渲染截图，**深色 + 浅色各一张**，检查：页头 44、按钮 28、页签选中是否为淡蓝底、浅色下描边是否还看得见、长文本是否溢出。
4. 三条门禁跑绿再提交。

改宿主配色时反过来也要做：宿主 `style.css` 改完 → 把新值搬进 `templates/qd-theme.css` → 同步。
两边不一致时**以本文第 3 节的表为准**（它是插件侧的唯一权威源，宿主文档不收录插件令牌）。
