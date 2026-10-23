#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""QuickDock 插件脚手架 CLI（engine 的薄壳，同时被插件 UI 复用）。

用法：
  # 直接生成
  python tools/newplugin.py --dir my-tool --name-zh "我的工具" \
      --desc-zh "一句话说明" --runtime none --category 开发者日常 --repo . --integrate

  # 交互式
  python tools/newplugin.py --interactive --repo . --integrate

  # 供插件 UI 调用（spec 经 base64 传入，避免命令行转义）
  python tools/newplugin.py --spec-b64 <b64> --repo <仓库根> --integrate
"""
import argparse
import base64
import json
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from scaffold.engine import (  # noqa: E402
    CATEGORIES, generate_plugin, slugify,
)

PERMS = ["clipboard", "network", "filesystem", "shell", "process", "processKill"]


def _prompt(text, default=""):
    try:
        val = input("%s%s: " % (text, (" [%s]" % default) if default else "")).strip()
    except EOFError:
        val = ""
    return val or default


def interactive_spec() -> dict:
    print("== QuickDock 插件脚手架（交互模式）==")
    name_zh = _prompt("中文显示名")
    name_en = _prompt("英文显示名", slugify(name_zh))
    desc_zh = _prompt("中文简介")
    desc_en = _prompt("英文简介", desc_zh)
    print("可选 runtime: none / goja / native")
    runtime = _prompt("runtime", "none")
    while runtime not in ("none", "goja", "native"):
        runtime = _prompt("runtime（none/goja/native）", "none")
    print("可选分类: " + " / ".join(CATEGORIES))
    category = _prompt("分类", "开发者日常")
    while category not in CATEGORIES:
        category = _prompt("分类", "开发者日常")
    author = _prompt("作者", "QuickDock")
    cmd_id = _prompt("主命令 id（kebab）", slugify(name_zh))
    cmd_title_en = _prompt("主命令英文标题", name_en)
    spec = {
        "dir": slugify(name_zh), "name_zh": name_zh, "name_en": name_en,
        "desc_zh": desc_zh, "desc_en": desc_en, "runtime": runtime,
        "category": category, "author": author,
        "commands": [{"id": cmd_id, "title_zh": name_zh, "title_en": cmd_title_en, "keywords": []}],
    }
    print("可选权限（逗号分隔，留空=无）: " + ", ".join(PERMS))
    perms_raw = _prompt("权限", "")
    spec["permissions"] = {p: True for p in [x.strip() for x in perms_raw.split(",") if x.strip()]}
    return spec


def main():
    ap = argparse.ArgumentParser(description="QuickDock 插件脚手架")
    ap.add_argument("--dir")
    ap.add_argument("--name-zh", default="")
    ap.add_argument("--name-en", default="")
    ap.add_argument("--desc-zh", default="")
    ap.add_argument("--desc-en", default="")
    ap.add_argument("--runtime", default="none", choices=["none", "goja", "native"])
    ap.add_argument("--category", default="开发者日常")
    ap.add_argument("--author", default="QuickDock")
    ap.add_argument("--width", type=int, default=960)
    ap.add_argument("--height", type=int, default=600)
    ap.add_argument("--permissions", default="", help="逗号分隔权限键")
    ap.add_argument("--command", action="append", default=[], help="命令 id（可多次）")
    ap.add_argument("--repo", default=".")
    ap.add_argument("--integrate", action="store_true")
    ap.add_argument("--interactive", action="store_true")
    ap.add_argument("--spec-b64", help="base64(JSON spec)")
    a = ap.parse_args()

    if a.spec_b64:
        spec = json.loads(base64.b64decode(a.spec_b64).decode("utf-8"))
    elif a.interactive:
        spec = interactive_spec()
    else:
        perms = {p: True for p in [x.strip() for x in a.permissions.split(",") if x.strip()]}
        cmds = a.command or []
        spec = {
            "dir": a.dir, "name_zh": a.name_zh, "name_en": a.name_en,
            "desc_zh": a.desc_zh, "desc_en": a.desc_en, "runtime": a.runtime,
            "category": a.category, "author": a.author,
            "width": a.width, "height": a.height, "permissions": perms,
            "commands": [{"id": c, "title_zh": a.name_zh, "title_en": a.name_en, "keywords": []} for c in cmds]
            or None,
        }

    res = generate_plugin(spec, a.repo, integrate=a.integrate)
    print(json.dumps(res, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
