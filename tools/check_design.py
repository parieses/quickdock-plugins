#!/usr/bin/env python3
"""设计系统门禁引擎：禁止插件在本地内联样式里影子重定义共享组件类。

背景
----
共享层 `templates/qd-theme.css`（令牌 + p-* 骨架）与 `templates/components.css`
（btn / input / tabs / card 等组件）是全库唯一的视觉来源，改一处应全库生效。
但实测有 20 个插件在 `index.html` 的内联 `<style>` 里顶层重定义了 `.btn`
（三种按钮规格并存），7 个把 `.p-btn` 改小、5 个把 `.p-head` 改成 36px——
于是同一个"执行"按钮在三个插件里三种尺寸，顶栏高度有 36/40/44 三种。
设计系统的价值在于"改一处、全库变"；影子重定义让这个承诺失效。

两级判定
--------
**一级 · 视觉组件类**（ERROR 级）——它们的尺寸/配色/圆角/字号直接被用户感知，
跨插件不一致就是"不像同一个软件"：

    .btn .btn-* .p-btn .p-btn-* .input .select .textarea
    .p-input .p-select .p-textarea .p-copy .p-range
    .tabs .tab .tab-* .p-tabs .p-tab .p-tab-*
    .badge .badge-* .p-tag .p-tag-*
    .card .card-* .p-empty .empty-state* .p-toast* .modal*
    .p-head .p-head-title .p-head-actions .spinner .status-dot .tree*

**二级 · 布局骨架类**（WARNING 级）——结构尺寸、滚动、gap 的局部适配是
合理定制，只在特定上下文生效，不构成"跨插件视觉分裂"：

    .p-app .p-body .p-body-col .p-content .p-section .p-section-label
    .p-toolbar .p-pane .p-pane-header .p-statusbar .p-list .p-item
    .p-item-label .p-item-desc .p-field .p-spacer .p-sep .p-label
    .p-muted .p-hero .table* .list* .field* .text-*

只抓**顶层裸类选择器**的重定义，形如：

    .btn { ... }              ← 违规（覆盖了共享 .btn）
    .btn:hover { ... }        ← 放行（只是补交互态）
    .app > .p-head { ... }    ← 放行（作用域限定，不影响其他上下文）
    .p-check .p-input { ... } ← 放行（同上）
    @media (max-width:700px){ .btn { ... } }  ← 放行（响应式覆盖）

带祖先限定的写法只在特定上下文生效，是合理的局部定制；真正要拦的是
"裸露地改写共享类的定义"。

豁免机制
--------
1) 注释豁免（写在被豁免规则的紧邻位置）：

       /* qd-allow-local: btn */
       .btn { height: 24px; }        /* 密度特例：数据网格内按钮 */

2) 台账豁免 `tools/design_baseline.json`——已知存量违规登记进去，
   门禁只拦**新增**违规。存量靠逐阶段迁移消除，迁完一个删一条。

用法
----
    python tools/check_design.py                # 全量检查（ERROR/WARN 混合）
    python tools/check_design.py --list         # 列出全部违规明细
    python tools/check_design.py --gen-baseline # 重新生成存量台账
退出码：0 = 无新增一级违规，1 = 有新增一级违规。
"""
from __future__ import annotations

import json
import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
TPL = os.path.join(ROOT, "templates")
BASELINE = os.path.join(ROOT, "tools", "design_baseline.json")

# ---------------------------------------------------------------
# 一级：视觉组件类（跨插件必须一致）
# ---------------------------------------------------------------
TIER1_EXACT = {
    # 按钮
    "btn", "btn-primary", "btn-danger", "btn-success", "btn-ghost",
    "btn-sm", "btn-lg", "btn-icon",
    "p-btn", "p-btn-primary", "p-btn-ghost", "p-btn-danger", "p-btn-sm",
    "p-btn-group", "p-copy",
    # 输入
    "input", "select", "textarea",
    "p-input", "p-input-mono", "p-select", "p-textarea", "p-range",
    "p-native-color",
    # 标签栏
    "tabs", "tab", "tab-icon", "p-tabs", "p-tab", "p-tab-active",
    # 徽章 / 标签
    "badge", "badge-default", "badge-accent", "badge-success",
    "badge-warning", "badge-danger",
    "p-tag", "p-tag-ok", "p-tag-warn", "p-tag-info", "p-tag-neutral",
    # 卡片 / 空态 / 提示
    "card", "card-header", "card-title", "card-body", "card-footer",
    "p-card", "p-empty", "p-error",
    "empty-state", "empty-state-icon", "empty-state-title", "empty-state-desc",
    "p-toast", "p-toast-success", "p-toast-error",
    "modal", "modal-overlay", "modal-header", "modal-title", "modal-close",
    "modal-body", "modal-footer",
    # 顶栏
    "p-head", "p-head-title", "p-head-actions",
    # 状态 / 加载
    "spinner", "spinner-sm", "spinner-lg", "status-dot",
    # 树
    "tree", "tree-node", "tree-node-caret", "tree-node-icon",
    "tree-node-label", "tree-children",
}

# 一级：按前缀匹配的类（p-btn-* 之类）
TIER1_PREFIX = ("p-btn", "p-tab", "p-tag", "p-head", "badge-", "p-toast")


# ---------------------------------------------------------------
# 二级：布局骨架类（局部适配放行，仅警告）
# ---------------------------------------------------------------
TIER2_EXACT = {
    "p-app", "p-body", "p-body-col", "p-content", "p-section",
    "p-section-label", "p-toolbar", "p-pane", "p-pane-header",
    "p-statusbar", "p-list", "p-item", "p-item-label", "p-item-desc",
    "p-field", "p-spacer", "p-sep", "p-label", "p-muted", "p-hero",
    "p-output",
    "table", "table-wrapper", "table-striped",
    "list", "list-item", "list-item-icon", "list-item-content",
    "list-item-title", "list-item-desc", "list-item-meta",
    "field", "field-label", "divider", "divider-vertical",
}

TIER2_PREFIX = ("p-item", "p-pane", "p-section", "p-body", "list-item", "table")


def tier_of(cls: str) -> int:
    """返回 1（一级·视觉组件）/ 2（二级·布局骨架）/ 0（非共享类）。"""
    if cls in TIER1_EXACT or cls.startswith(TIER1_PREFIX):
        return 1
    if cls in TIER2_EXACT or cls.startswith(TIER2_PREFIX):
        return 2
    return 0


# ---------------------------------------------------------------
# 共享类全集：从 templates/ 的权威文件提取（不手写，避免漂移）
# ---------------------------------------------------------------
def read(path: str) -> str:
    try:
        with open(path, "r", encoding="utf-8", errors="ignore") as f:
            return f.read()
    except OSError:
        return ""


def shared_classes() -> set[str]:
    out: set[str] = set()
    for name in ("qd-theme.css", "components.css"):
        for m in re.finditer(r"\.([A-Za-z_][A-Za-z0-9_-]*)", read(os.path.join(TPL, name))):
            out.add(m.group(1))
    return out


# ---------------------------------------------------------------
# CSS 解析：找出顶层规则（不在任何 @ 块内）
# ---------------------------------------------------------------
RULE_RE = re.compile(r"([^{}]+)\{([^{}]*)\}", re.S)


def _strip_comments(text: str) -> str:
    """把注释替换成等长空白，保留行列位置便于定位。"""
    return re.sub(r"/\*.*?\*/",
                  lambda m: re.sub(r"[^\n]", " ", m.group(0)),
                  text, flags=re.S)


def _at_depth(css: str, pos: int) -> int:
    """pos 之前 @ 块的嵌套深度：>0 表示该规则位于 @media/@supports 内部。"""
    depth = 0
    for m in re.finditer(r"@[\w-]+[^{]*\{", css[:pos]):
        depth += 1
    for m in re.finditer(r"\}", css[:pos]):
        depth -= 1
    return max(0, depth)


def find_local_redefinitions(html: str, shared: set[str]) -> list[dict]:
    """返回插件内联样式中对共享类的顶层重定义列表。"""
    hits: list[dict] = []
    for style_block in re.findall(r"<style[^>]*>(.*?)</style>", html, re.S):
        css = _strip_comments(style_block)
        for m in RULE_RE.finditer(css):
            selector_raw = m.group(1).strip()
            if not selector_raw or selector_raw.startswith("@"):
                continue
            # 在 @media / @supports 内部的规则不算顶层重定义
            if _at_depth(css, m.start()) > 0:
                continue
            for sel in selector_raw.split(","):
                sel = sel.strip()
                if not sel:
                    continue
                # 出现空格 / > / + / ~ 等组合符 → 作用域限定，放行
                if re.search(r"[\s>+~]", sel):
                    continue
                # 伪类/伪元素后缀（.btn:hover）属于补交互态，放行
                base = re.split(r"[:\[]", sel, maxsplit=1)[0]
                if not base.startswith("."):
                    continue
                cls = base[1:]
                if cls not in shared:
                    continue
                t = tier_of(cls)
                if t == 0:
                    continue
                hits.append({
                    "cls": cls,
                    "tier": t,
                    "selector": sel,
                    "body": m.group(2).strip()[:120],
                })
    return hits


def has_allow_marker(html: str, cls: str) -> bool:
    """检查是否存在 /* qd-allow-local: cls */ 豁免注释。"""
    pattern = re.compile(
        r"/\*[^*]*qd-allow-local\s*:[^*]*\b" + re.escape(cls) + r"\b[^*]*\*/",
        re.I,
    )
    return bool(pattern.search(html))


def load_baseline() -> dict:
    data = read(BASELINE)
    if not data:
        return {}
    try:
        return json.loads(data)
    except json.JSONDecodeError:
        return {}


def scan(only: list[str] | None = None):
    """扫描全库，返回 (一级新违规, 一级存量, 二级警告) 三个列表。"""
    shared = shared_classes()
    baseline = load_baseline()

    dirs = []
    for name in sorted(os.listdir(ROOT)):
        p = os.path.join(ROOT, name)
        if os.path.isdir(p) and os.path.isfile(os.path.join(p, "plugin.json")):
            if only and name not in only:
                continue
            dirs.append((name, p))

    tier1_new: list[str] = []
    tier1_known: list[str] = []
    tier2: list[str] = []
    for d, p in dirs:
        html = read(os.path.join(p, "frontend", "index.html"))
        if not html:
            continue
        seen = set()
        for h in find_local_redefinitions(html, shared):
            key = (d, h["cls"])
            if key in seen:
                continue
            seen.add(key)
            if has_allow_marker(html, h["cls"]):
                continue
            registered = h["cls"] in (baseline.get(d) or {})
            if h["tier"] == 1:
                if registered:
                    tier1_known.append(
                        f"[{d}] .{h['cls']} 顶层重定义（选择器 `{h['selector']}`，基线已登记）")
                else:
                    tier1_new.append(
                        f"[{d}] .{h['cls']} 顶层重定义（选择器 `{h['selector']}`，基线未登记）")
            else:
                tier2.append(
                    f"[{d}] .{h['cls']} 布局骨架类局部覆盖（选择器 `{h['selector']}`）")
    return tier1_new, tier1_known, tier2, len(dirs)


def gen_baseline() -> int:
    """重新生成存量台账：把当前所有一级违规 + emoji 图标存量登记进 design_baseline.json。

    保留既有的特殊段（__font_size__ / __emoji_icon__），其余插件条目重建。
    """
    shared = shared_classes()
    old = load_baseline()
    special = {k: v for k, v in old.items() if k.startswith("__")}

    data: dict[str, dict] = {}
    emoji_data: dict[str, dict] = {}
    for name in sorted(os.listdir(ROOT)):
        p = os.path.join(ROOT, name)
        if not (os.path.isdir(p) and os.path.isfile(os.path.join(p, "plugin.json"))):
            continue
        html = read(os.path.join(p, "frontend", "index.html"))
        if not html:
            continue
        seen = set()
        for h in find_local_redefinitions(html, shared):
            if h["tier"] != 1 or (name, h["cls"]) in seen:
                continue
            if has_allow_marker(html, h["cls"]):
                continue
            seen.add((name, h["cls"]))
            data.setdefault(name, {})[h["cls"]] = "存量待迁移"

        # emoji 图标存量：游戏类插件（emoji 是美术素材）登记进 __emoji_icon__
        if name.startswith("game-") or name == "minesweeper":
            for m in _EMOJI_PIC_RE.finditer(html):
                emoji_data.setdefault(f"{name}:{ord(m.group(0)):04X}", "游戏美术素材，保留")

    special["__emoji_icon__"] = emoji_data
    data.update(special)

    with open(BASELINE, "w", encoding="utf-8") as f:
        json.dump(data, f, ensure_ascii=False, indent=2, sort_keys=True)
        f.write("\n")
    total = sum(len(v) for k, v in data.items() if not k.startswith("__"))
    print(f"已生成存量台账：{total} 处一级违规 + {len(emoji_data)} 处 emoji 图标存量 → "
          f"{os.path.relpath(BASELINE, ROOT)}")
    return 0


def plugin_dirs(only: list[str] | None = None):
    """返回 (插件名, 插件目录) 列表。"""
    out = []
    for name in sorted(os.listdir(ROOT)):
        p = os.path.join(ROOT, name)
        if os.path.isdir(p) and os.path.isfile(os.path.join(p, "plugin.json")):
            if only and name not in only:
                continue
            out.append((name, p))
    return out


def check_font_sizes(dirs):
    """字号白名单：小字号只允许刻度内的六个 token 值。

    背景：审计发现全库 485 处硬编码字号，且出现 8/9/9.5/10.5/11.5/12.5px 等
    刻度外值，中间文本层（副标题、说明、caption）全部塌缩，squint test 失效。

    规则：font-size 写成 px 且值不在 {11,12,13,14,16,20} 之内即报告——
    这些多为语义尺寸（markdown 标题层级 / 图标 / 统计数值），不强制归并，
    但要登记进台账，避免继续扩散。
    """
    ALLOWED_PX = {"11", "12", "13", "14", "16", "20"}
    font_allow = load_baseline().get("__font_size__", {})

    new_hits, known_hits = [], []
    for d, p in dirs:
        html = read(os.path.join(p, "frontend", "index.html"))
        if not html:
            continue
        for m in re.finditer(r"font-size:\s*([0-9]+(?:\.[0-9])?)px", html):
            v = m.group(1)
            if v in ALLOWED_PX:
                continue
            line = html[:m.start()].count("\n") + 1
            key = f"{d}:{v}"
            if key in font_allow:
                known_hits.append(f"[{d}] font-size:{v}px（行 {line}，台账已登记）")
            else:
                new_hits.append(
                    f"[{d}] font-size:{v}px（行 {line}）—— 字号应走 var(--fs-*) 令牌；"
                    f"确属语义尺寸则登记进 tools/design_baseline.json 的 __font_size__ 段")
    return new_hits, known_hits


# ---------------------------------------------------------------
# emoji 图标门禁：插件不得用彩色 emoji 充当 UI 图标
# ---------------------------------------------------------------
# 背景：审计发现 37 个插件把彩色 emoji（📊📋🚀📁…）当按钮 / 树 / 标题图标，
# 跨平台字形漂移（Windows 的 Segoe UI Emoji 与 macOS 的 Apple Color Emoji 不同），
# 专业工具感打折。全库已建共享图标层 templates/icons.css（Lucide SVG，
# mask-image + data URI，opaque origin 安全），应统一迁移。
# 几何 / 箭头 / 状态符号（→ ✕ ✓ ⚠ ★）跨平台稳定，保留。
_EMOJI_PIC_RE = re.compile(
    "[\U0001F000-\U0001FAFF]"
)


def check_emoji_icons(dirs):
    """彩色 emoji 图标门禁：新增彩色 pictograph（U+1F000–U+1FAFF）即 ERROR。

    几何 / 箭头 / 状态符号（→ ✕ ✓ ⚠ ★ 等）不属于此范围，放行。
    游戏类插件的 emoji 是美术素材而非 UI 图标，登记进台账豁免。
    返回 (新增命中, 存量命中) 两个列表，与字号门禁同构。
    """
    emoji_allow = load_baseline().get("__emoji_icon__", {})

    new_hits, known_hits = [], []
    for d, p in dirs:
        html = read(os.path.join(p, "frontend", "index.html"))
        if not html:
            continue
        lines = html.split("\n")
        for ln, line in enumerate(lines, 1):
            for m in _EMOJI_PIC_RE.finditer(line):
                ch = m.group(0)
                key = f"{d}:{ord(ch):04X}"
                if key in emoji_allow:
                    known_hits.append(
                        f"[{d}] emoji 图标 {ch}（行 {ln}）← 台账已登记")
                else:
                    new_hits.append(
                        f"[{d}] 彩色 emoji {ch}（行 {ln}）充当 UI 图标——"
                        f"改用共享图标层 templates/icons.css 的 .qi-* 类；"
                        f"游戏美术素材则登记进 tools/design_baseline.json 的 __emoji_icon__ 段")
    return new_hits, known_hits


def check_focus_visible(dirs):
    """焦点环门禁：内联样式里出现 :focus 就必须同时有 :focus-visible。

    背景：审计发现内联样式层 0 处 :focus-visible，键盘用户 Tab 导航时
    焦点位置不可见。共享层的 .btn / .p-btn / .input 已带焦点环，
    但插件自建的控件体系没有。

    规则：插件内联 <style> 里若出现 :focus（且不是 :focus-visible / :focus-within），
    则必须能在同一文件里找到 :focus-visible——否则报告。
    """
    FOCUS_RE = re.compile(r":[ \t]*focus(?![-\w])")
    FOCUS_VISIBLE_RE = re.compile(r":focus-visible")
    FOCUS_WITHIN_RE = re.compile(r":focus-within")

    new_hits = []
    for d, p in dirs:
        html = read(os.path.join(p, "frontend", "index.html"))
        if not html:
            continue
        # 只看内联 <style> 块（共享层自带焦点环，不算缺口）
        css = "\n".join(re.findall(r"<style[^>]*>(.*?)</style>", html, re.S))
        if not css:
            continue
        if FOCUS_VISIBLE_RE.search(css):
            continue
        if not FOCUS_RE.search(css):
            continue
        # 排除只剩 :focus-within 的情况
        stripped = FOCUS_WITHIN_RE.sub("", css)
        if not FOCUS_RE.search(stripped):
            continue
        n = len(FOCUS_RE.findall(stripped))
        new_hits.append(
            f"[{d}] 内联样式有 {n} 处 :focus 但无 :focus-visible"
            f"（键盘用户 Tab 导航看不到焦点位置）——"
            f"改用 :focus-visible，或确认该控件不需焦点环后加注释说明")
    return new_hits


def main() -> int:
    args = sys.argv[1:]
    if "--gen-baseline" in args:
        return gen_baseline()
    list_only = "--list" in args
    only = [a for a in args if not a.startswith("--")]

    tier1_new, tier1_known, tier2, ndirs = scan(only or None)
    font_new, font_known = check_font_sizes(plugin_dirs(only or None))
    focus_new = check_focus_visible(plugin_dirs(only or None))
    emoji_new, emoji_known = check_emoji_icons(plugin_dirs(only or None))

    if list_only:
        for line in sorted(tier1_known + tier1_new + tier2 + font_new + font_known + focus_new + emoji_new + emoji_known):
            print(line)
        print(f"\n一级新增 {len(tier1_new)} 处 / 一级存量 {len(tier1_known)} 处 / "
              f"二级布局 {len(tier2)} 处 / 字号刻度外 {len(font_new) + len(font_known)} 处 / "
              f"焦点环 {len(focus_new)} 处 / emoji 图标 {len(emoji_new) + len(emoji_known)} 处")
        return 0

    for line in sorted(tier1_new):
        print(f"[ERROR] {line}")
    for line in sorted(font_new):
        print(f"[ERROR] {line}")
    for line in sorted(focus_new):
        print(f"[ERROR] {line}")
    for line in sorted(emoji_new):
        print(f"[ERROR] {line}")
    for line in sorted(tier1_known):
        print(f"[WARN ] {line}  ← 迁移后可从 tools/design_baseline.json 移除")
    for line in sorted(font_known):
        print(f"[WARN ] {line}  ← 确认是语义尺寸后可忽略")
    for line in sorted(emoji_known):
        print(f"[WARN ] {line}")
    for line in sorted(tier2):
        print(f"[INFO ] {line}")

    print(f"\n设计门禁：检查 {ndirs} 个插件，"
          f"{len(tier1_new)} 处新增一级违规 / {len(tier1_known)} 处一级存量 / "
          f"{len(tier2)} 处二级布局覆盖 / emoji 图标 {len(emoji_new)} 新增 + {len(emoji_known)} 存量")
    if tier1_new:
        print("  修法：①删除本地重定义，改用共享类；"
              "②确属密度特例则加 /* qd-allow-local: 类名 */ 注释；"
              "③确属存量则登记进 tools/design_baseline.json")
    return 1 if tier1_new else 0


if __name__ == "__main__":
    sys.exit(main())
