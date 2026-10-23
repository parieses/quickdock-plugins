#!/usr/bin/env python3
"""共享代码单一来源一致性检查（CI 门禁）。

背景
----
`qd-theme.css` / `components.css` / `qdlog.go` 是跨插件复用的“共享代码”，在 77 / 64 / 33
个插件里各自是一份逐字节相同的副本。此前没有单一来源：改一处要手动同步几十份，极易漏改，
导致“A 插件换主题色、B 插件还是旧的”这种难查的不一致。

本脚本把 `templates/` 下的三份作为**权威（single source of truth）**，断言：

  * 每个 `frontend/components.css`      == `templates/components.css`
  * 每个 `frontend/qd-theme.css`        == `templates/qd-theme.css`
        —— 除非该插件登记在 `QTHEME_FORKS`（已知 fork）；
           fork 不要求逐字节相等，但**必须包含权威 `:root` 里的全部主题令牌且取值一致**
           （fork 允许在 `:root` 里追加自己的令牌，如 photo-library 的 `--canvas-*`）。
  * 每个 `qdlog.go`（不含 templates 本身）== `templates/qdlog.go`
        —— qdlog.go 的内容由 `tools/apply_qdlog.py` 负责同步到各插件，
           本检查只负责“漂移报警”。

出口码：0 = 全部一致；1 = 存在漂移（需先跑 `tools/sync_shared.py` 或手工合并 fork）。

用法
----
  python3 tools/check_shared.py
  python3 tools/check_shared.py --stats     # 额外打印每族文件计数
"""
from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
TPL = ROOT / "templates"

# 已知 qd-theme.css fork：保留自己的布局/组件类与专有选择器，但主题令牌须与权威一致。
# 新增 fork 时在这里登记，并写明原因（见各插件目录或 PR 说明）。
QTHEME_FORKS = {"photo-library"}


def root_tokens(text: str) -> dict[str, str]:
    """抽取主 `:root { ... }` 块里的 `--name: value;` 令牌，返回 name->value。"""
    text = re.sub(r"/\*.*?\*/", "", text, flags=re.S)
    m = re.search(r":root\s*\{([^}]*)\}", text)
    if not m:
        return {}
    out: dict[str, str] = {}
    for line in m.group(1).splitlines():
        line = line.strip()
        mm = re.match(r"(--[\w-]+)\s*:\s*(.+?)\s*;?\s*$", line)
        if mm:
            out[mm.group(1)] = mm.group(2)
    return out


def family_components() -> list[Path]:
    return sorted(ROOT.glob("**/frontend/components.css"))


def family_icons() -> list[Path]:
    return sorted(ROOT.glob("**/frontend/icons.css"))


def family_qtheme() -> list[Path]:
    return sorted(ROOT.glob("**/frontend/qd-theme.css"))


def family_qdlog() -> list[Path]:
    return [p for p in sorted(ROOT.glob("**/qdlog.go")) if p.resolve() != (TPL / "qdlog.go").resolve()]


def plugin_name(p: Path) -> str:
    # frontend/.../qd-theme.css -> 插件目录名（qd-theme.css 在其 frontend/ 下）
    parts = p.relative_to(ROOT).parts
    if "frontend" in parts:
        return parts[parts.index("frontend") - 1]
    return parts[0]


def check_components(stats: bool) -> list[str]:
    tpl = (TPL / "components.css").read_text(encoding="utf-8")
    bad: list[str] = []
    for f in family_components():
        if f.read_text(encoding="utf-8") != tpl:
            bad.append(str(f.relative_to(ROOT)))
    if stats:
        print(f"[components.css] 检查 {len(family_components())} 份，漂移 {len(bad)} 份")
    return bad


def check_qtheme(stats: bool) -> list[str]:
    tpl_text = (TPL / "qd-theme.css").read_text(encoding="utf-8")
    tpl_tokens = root_tokens(tpl_text)
    bad: list[str] = []
    for f in family_qtheme():
        text = f.read_text(encoding="utf-8")
        name = plugin_name(f)
        if name in QTHEME_FORKS:
            tok = root_tokens(text)
            missing = {k: v for k, v in tpl_tokens.items() if tok.get(k) != v}
            if missing:
                detail = ", ".join(f"{k}={v!r}" for k, v in missing.items())
                bad.append(f"{f.relative_to(ROOT)} (fork 缺/改 :root 令牌: {detail})")
            continue
        if text != tpl_text:
            bad.append(str(f.relative_to(ROOT)))
    if stats:
        total = len(family_qtheme())
        print(f"[qd-theme.css] 检查 {total} 份（fork 豁免 {len(QTHEME_FORKS & {plugin_name(f) for f in family_qtheme()})} 份），漂移 {len(bad)} 份")
    return bad


def check_icons(stats: bool) -> list[str]:
    tpl = (TPL / "icons.css").read_text(encoding="utf-8")
    bad = []
    for f in family_icons():
        if f.read_text(encoding="utf-8") != tpl:
            bad.append(str(f.relative_to(ROOT)))
    if stats:
        print(f"[icons.css] 检查 {len(family_icons())} 份，漂移 {len(bad)} 份")
    return bad


def check_qdlog(stats: bool) -> list[str]:
    tpl = (TPL / "qdlog.go").read_text(encoding="utf-8")
    bad: list[str] = []
    for f in family_qdlog():
        if f.read_text(encoding="utf-8") != tpl:
            bad.append(str(f.relative_to(ROOT)))
    if stats:
        print(f"[qdlog.go] 检查 {len(family_qdlog())} 份，漂移 {len(bad)} 份")
    return bad


def main() -> int:
    ap = argparse.ArgumentParser(description="共享代码单一来源一致性检查")
    ap.add_argument("--stats", action="store_true", help="打印每族文件计数")
    args = ap.parse_args()

    if not (TPL / "qd-theme.css").exists() or not (TPL / "components.css").exists():
        print("错误：templates/qd-theme.css 或 templates/components.css 不存在")
        return 1

    bad = []
    bad += check_components(args.stats)
    bad += check_icons(args.stats)
    bad += check_qtheme(args.stats)
    bad += check_qdlog(args.stats)

    if not bad:
        print("OK：所有共享代码副本与 templates/ 权威一致（fork 已核对 :root 令牌）。")
        return 0

    print(f"\n发现 {len(bad)} 处漂移，需先同步：")
    for b in bad:
        print(f"  ✗ {b}")
    print("\n修复：python3 tools/sync_shared.py --all   （fork 请手工合并 :root 令牌后重跑本检查）")
    return 1


if __name__ == "__main__":
    sys.exit(main())
