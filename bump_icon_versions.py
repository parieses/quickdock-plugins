#!/usr/bin/env python
"""批量 bump patch 版本号 + 在 changelog 顶部补图标条目。

用法：
    python bump_icon_versions.py --dry     # 只看将要改什么
    python bump_icon_versions.py           # 实际写入

两类插件：
  · 已发布（dist/ 里有对应 zip）-> 版本 patch +1
  · 未发布                        -> 版本不动，只在现有条目里补一句

幂等：changelog 里已经含本次图标文案的插件会被跳过。
"""
import argparse
import json
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent

ENTRY_TITLE = "图标按新规范重做"
ENTRY_BODY = (
    "- 图标改用统一 64 网格满幅圆角贴片 + 白色图形，线宽归一\n"
    "- 新增浅色主题图标 `icon.light.svg`，随应用主题自动切换"
)


def bump_patch(v: str) -> str:
    m = re.fullmatch(r"(\d+)\.(\d+)\.(\d+)(.*)", v.strip())
    if not m:
        raise ValueError(f"版本号格式不认识: {v}")
    return f"{m.group(1)}.{m.group(2)}.{int(m.group(3)) + 1}{m.group(4)}"


def released(plugin_id: str) -> bool:
    slug = plugin_id.replace(".", "-")
    return any(ROOT.glob(f"dist/{slug}-*.zip"))


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--dry", action="store_true")
    a = ap.parse_args()

    changed, skipped = [], []
    for pj in sorted(ROOT.glob("*/plugin.json")):
        name = pj.parent.name
        raw = pj.read_text(encoding="utf-8")
        if ENTRY_TITLE in raw:
            skipped.append(f"{name}: changelog 已有图标条目")
            continue
        data = json.loads(raw)
        old = data.get("version", "0.0.0")
        is_rel = released(data.get("id", ""))
        new = bump_patch(old) if is_rel else old
        if is_rel:
            head = f"## v{new}\n\n{ENTRY_BODY}\n\n"
        else:
            head = ""            # 未发布：不动版本，只在旧条目里追加说明
        old_log = data.get("changelog", "") or ""
        if head:
            data["changelog"] = head + old_log
        elif old_log:
            data["changelog"] = old_log + f"\n- {ENTRY_TITLE}：统一贴片 + 浅色档"
        else:
            data["changelog"] = f"## v{old}\n\n{ENTRY_BODY}"

        data["version"] = new
        tail = "\n" if raw.endswith("\n") else ""
        out = json.dumps(data, ensure_ascii=False, indent=2) + tail
        verb = f"{old} -> {new}" if is_rel else f"{old} (未发布，不动)"
        changed.append(f"{name:22s} {verb}")
        if not a.dry:
            pj.write_text(out, encoding="utf-8")

    for c in changed:
        print("  改 " + c)
    for s in skipped:
        print("  跳过 " + s)
    print(f"\n合计：改 {len(changed)} / 跳过 {len(skipped)}"
          + ("（dry-run，未写盘）" if a.dry else ""))
    return 0


if __name__ == "__main__":
    sys.exit(main())
