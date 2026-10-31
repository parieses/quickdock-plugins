#!/usr/bin/env python3
"""把 templates/ 下的共享权威文件同步到各插件副本（check_shared.py 的修复工具）。

与 tools/apply_qdlog.py 同理：权威在 templates/，本脚本负责把权威复制到各插件对应位置。
改了 templates/qd-theme.css（或 components.css）之后，跑一次 `--all` 即可让所有副本更新，
随后 `tools/check_shared.py` 会确认零漂移。

约定
----
  * `qd-theme.css` 的已知 fork（photo-library）**不会被覆盖**——它保留了自己的布局/组件类与
    专有选择器；改了基础主题令牌后，请手工把 templates/qd-theme.css 的 `:root` 令牌合并进
    photo-library 的 `:root`，再重跑 check_shared.py。
  * `qdlog.go` 的同步由 tools/apply_qdlog.py 负责（它还会补 main.go 日志埋点），本脚本不碰。

用法
----
  python3 tools/sync_shared.py --all
  python3 tools/sync_shared.py --dry-run api-mock database photo-library
  python3 tools/sync_shared.py api-mock          # 只同步指定插件
"""
from __future__ import annotations

import argparse
import shutil
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
TPL = ROOT / "templates"

QTHEME_FORKS = {"photo-library"}  # 与 check_shared.py 保持一致


def sync_file(target: Path, source: Path, dry: bool) -> bool:
    if not source.exists():
        print(f"  ! 跳过（模板缺失）：{source.relative_to(ROOT)}")
        return False
    if target.exists() and target.read_text(encoding="utf-8") == source.read_text(encoding="utf-8"):
        return False
    print(f"  + {target.relative_to(ROOT)}")
    if not dry:
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(source, target)
    return True


def plugin_dirs(names: list[str] | None) -> list[Path]:
    if names is None:  # --all
        return sorted(p for p in ROOT.iterdir() if p.is_dir() and (p / "frontend").is_dir())
    return [ROOT / n for n in names]


def process(plugin: Path, dry: bool) -> bool:
    changed = False
    fe = plugin / "frontend"
    # components.css：只同步“已存在”的副本，不为不引用它的插件（如各 game-*）凭空创建
    comp = fe / "components.css"
    if comp.exists():
        changed |= sync_file(comp, TPL / "components.css", dry)
    # icons.css：同 components.css 规则——只有已存在副本的插件才同步
    icons = fe / "icons.css"
    if icons.exists():
        changed |= sync_file(icons, TPL / "icons.css", dry)
    # qd-theme.css：只同步已存在且非 fork 的副本
    theme = fe / "qd-theme.css"
    if plugin.name in QTHEME_FORKS:
        if theme.exists():
            print(f"  ~ {theme.relative_to(ROOT)}  (fork，跳过；如需更新请手工合并 :root)")
    else:
        if theme.exists():
            changed |= sync_file(theme, TPL / "qd-theme.css", dry)
    return changed


def main() -> int:
    ap = argparse.ArgumentParser(description="同步共享 CSS 到各插件副本")
    ap.add_argument("plugins", nargs="*", help="插件目录名（不填需配合 --all）")
    ap.add_argument("--all", action="store_true", help="同步全部插件")
    ap.add_argument("--dry-run", action="store_true", help="只报告不写入")
    args = ap.parse_args()

    if not args.all and not args.plugins:
        print("需指定插件名或 --all")
        return 1
    dirs = plugin_dirs(None if args.all else args.plugins)
    if not dirs:
        print("未匹配到插件目录")
        return 1

    touched = [d.name for d in dirs if process(d, args.dry_run)]
    print(f"\n{'[dry-run] ' if args.dry_run else ''}改动插件 {len(touched)}/{len(dirs)}：{', '.join(touched) if touched else '（无）'}")
    print("随后请跑：python3 tools/check_shared.py  确认零漂移")
    return 0


if __name__ == "__main__":
    sys.exit(main())
