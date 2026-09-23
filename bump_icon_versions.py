#!/usr/bin/env python
"""批量 bump patch 版本号 + 在 changelog 顶部补一条改写说明。

用法：
    python bump_icon_versions.py --title "标题" --body "正文" --dry   # 只看将要改什么
    python bump_icon_versions.py --title "标题" --body "正文"          # 实际写入

两类插件：
  · 已发布（dist/ 里有对应 zip）-> 版本 patch +1
  · 未发布                        -> 版本不动，只在现有条目里补一句

幂等：changelog 里已经含本次标题的插件会被跳过，所以重复跑不会叠加。
"""
import argparse
import json
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent

DEFAULT_TITLE = "图标色彩升级"
DEFAULT_BODY = (
    "- 贴片改为同色系渐变（沿色环微调色相 + 加深明度），打破原来「一个纯色 + 白」的单调\n"
    "- 图形内需要透底的元素（锁孔 / 角标描边环 / 书脊折痕）恢复用贴片锚点色反衬\n"
    "- 带状态语义的图标恢复强调色：红 = 失败/未读、绿 = 可用/生效、黄 = 告警/待定\n"
    "- 网格 / 安全区 / 线宽 / 深浅两档主题均保持不变"
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
    ap.add_argument("--title", default=DEFAULT_TITLE)
    ap.add_argument("--body", default=DEFAULT_BODY)
    a = ap.parse_args()

    changed, skipped = [], []
    for pj in sorted(ROOT.glob("*/plugin.json")):
        name = pj.parent.name
        raw = pj.read_text(encoding="utf-8")
        if a.title in raw:
            skipped.append(f"{name}: changelog 已有「{a.title}」条目")
            continue
        data = json.loads(raw)
        old = data.get("version", "0.0.0")
        is_rel = released(data.get("id", ""))
        new = bump_patch(old) if is_rel else old
        old_log = data.get("changelog", "") or ""
        if is_rel:
            data["changelog"] = f"## v{new}\n\n{a.body}\n\n" + old_log
        elif old_log:
            data["changelog"] = old_log + f"\n- {a.title}"
        else:
            data["changelog"] = f"## v{old}\n\n{a.body}"

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
