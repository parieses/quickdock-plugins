#!/usr/bin/env python3
"""给 native 插件接入统一的运行期日志（qdlog.go + 5 处埋点）。

背景：宿主把插件日志统一收在 <dataDir>/logs/plugin-YYYYMMDD.log（[plugin:<id>] 前缀），
但插件侧此前只有零散 stderr 输出（宿主一律记 WARN、且被按行拆碎），命令失败 / panic /
退出原因基本无迹可查。本脚本只做机械埋点，不改变任何业务语义：

  1. 复制 templates/qdlog.go -> <plugin>/qdlog.go
  2. main 开头：qdLogStart() + defer qdLogExit()
  3. main 读循环：if qdObserveLine(<var>) { continue }（只吞自己 log.* 的回包，其余原样交出）
  4. respond 内：defer qdFinishRequest(id, 0, "")
  5. respondError 内：defer qdFinishRequest(id, code, msg)
  6. panic 的 fmt.Fprintf(os.Stderr, ...) -> qdLogPanic(...)（带栈、记 ERROR，不再散成多行 WARN）

用法：
  python tools/apply_qdlog.py --dry-run port-scanner
  python tools/apply_qdlog.py port-scanner
  python tools/apply_qdlog.py --all
"""

from __future__ import annotations

import argparse
import re
import shutil
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
TEMPLATE = ROOT / "templates" / "qdlog.go"

MAIN_START = "\tqdLogStart()\n\tdefer qdLogExit()\n"

RESPOND_RE = re.compile(r"^func respond\((\w+) int64, (\w+) (?:interface\{\}|any)\) \{\n", re.M)
RESPOND_ERR_RE = re.compile(r"^func respondError\((\w+) int64, (\w+) int, (\w+) string\) \{\n", re.M)

LOOP_DATA_RE = re.compile(
    r"(?P<indent>\t+)data := strings\.TrimSpace\(line\)\n"
    r"(?P=indent)if data == \"\" \{\n"
    r"(?P=indent)\tcontinue\n"
    r"(?P=indent)\}\n"
)
LOOP_LINE_RE = re.compile(
    r"(?P<indent>\t+)line = strings\.TrimSpace\(line\)\n"
    r"(?P=indent)if line == \"\" \{\n"
    r"(?P=indent)\tcontinue\n"
    r"(?P=indent)\}\n"
)

PANIC_RE = re.compile(
    r"^(?P<indent>\t+)(?:_\s*,\s*_\s*=\s*)?fmt\.Fprintf\(os\.Stderr,\s*"
    r"(?P<fmt>\"(?:[^\"\\]|\\.)*\")\s*,(?P<args>[^\n]*)\)\s*$",
    re.M,
)


def patch_main_start(src: str) -> str:
    if "qdLogStart()" in src:
        return src
    m = re.search(r"^func main\(\) \{\n", src, re.M)
    if not m:
        return src
    return src[: m.end()] + MAIN_START + src[m.end() :]


def patch_loop(src: str) -> str:
    """只在 main 体内插一次（main 之后的第一个 stdin 读循环）。"""
    if "qdObserveLine(" in src:
        return src
    m = re.search(r"^func main\(\) \{\n", src, re.M)
    if not m:
        return src
    head, tail = src[: m.end()], src[m.end() :]

    def repl(match: re.Match) -> str:
        ind = match.group("indent")
        var = "data" if "data := strings.TrimSpace" in match.group(0) else "line"
        return match.group(0) + f"{ind}if qdObserveLine({var}) {{\n{ind}\tcontinue\n{ind}}}\n"

    for rx in (LOOP_DATA_RE, LOOP_LINE_RE):
        new, n = rx.subn(repl, tail, count=1)
        if n:
            return head + new
    return src


def patch_respond(src: str) -> str:
    if 'qdFinishRequest(id, 0, "")' in src:
        return src
    return RESPOND_RE.sub(lambda m: m.group(0) + '\tdefer qdFinishRequest(id, 0, "")\n', src, count=1)


def patch_respond_error(src: str) -> str:
    if "qdFinishRequest(id, code, msg)" in src:
        return src
    return RESPOND_ERR_RE.sub(
        lambda m: m.group(0) + "\tdefer qdFinishRequest(id, code, msg)\n", src, count=1
    )


def _clean_tag(fmt_lit: str) -> str:
    """从原 stderr 文案剥掉「%v + debug.Stack()」尾部占位符，剩下的当 panic 标签。

    fmt_lit 带引号（Go 字符串字面量），返回同样带引号的字面量，便于直接拼进调用。
    """
    body = fmt_lit[1:-1] if len(fmt_lit) >= 2 else fmt_lit
    body = re.sub(r"%v\\n%s\\n?$", "", body)  # 去掉尾部 "%v\n%s\n"
    body = re.sub(r"\s*err=?\s*$", "", body)
    body = re.sub(r"[\s:;=,(]+$", "", body)
    if not body:
        body = "panic"
    return f'"{body}"'


def patch_panics(src: str) -> str:
    def repl(m: re.Match) -> str | None:
        args = [a.strip() for a in m.group("args").split(",")]
        if not args or args[-1] != "debug.Stack()":
            return None  # 不是 panic 语句，别动
        rec = args[-2] if len(args) >= 2 else "r"
        extras = args[:-2]
        where = _clean_tag(m.group("fmt"))
        if extras:
            return f'{m.group("indent")}qdLogPanic(fmt.Sprintf({where}, {", ".join(extras)}), {rec})'
        return f'{m.group("indent")}qdLogPanic({where}, {rec})'

    return PANIC_RE.sub(lambda m: repl(m) or m.group(0), src)


def strip_unused_debug_import(src: str) -> str:
    if re.search(r"\bdebug\.", src):
        return src
    return src.replace('\t"runtime/debug"\n', "")


def process(plugin_dir: Path, dry: bool) -> bool:
    go_files = sorted(p for p in plugin_dir.glob("*.go") if not p.name.endswith("_test.go"))
    if not go_files:
        return False
    changed = False

    main_file = next((p for p in go_files if re.search(r"^func main\(\)", p.read_text(encoding="utf-8"), re.M)), None)

    for path in go_files:
        src = path.read_text(encoding="utf-8")
        orig = src
        if path == main_file:
            src = patch_main_start(src)
            src = patch_loop(src)
        src = patch_respond(src)
        src = patch_respond_error(src)
        src = patch_panics(src)
        src = strip_unused_debug_import(src)
        if src != orig:
            changed = True
            print(f"  ~ {path.relative_to(ROOT)}")
            if not dry:
                path.write_text(src, encoding="utf-8")

    target = plugin_dir / "qdlog.go"
    if not target.exists() or target.read_text(encoding="utf-8") != TEMPLATE.read_text(encoding="utf-8"):
        changed = True
        print(f"  + {target.relative_to(ROOT)}")
        if not dry:
            shutil.copyfile(TEMPLATE, target)
    return changed


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("plugins", nargs="*", help="插件目录名")
    ap.add_argument("--all", action="store_true", help="处理全部 native 插件")
    ap.add_argument("--dry-run", action="store_true")
    args = ap.parse_args()

    if args.all:
        dirs = sorted(p for p in ROOT.iterdir() if p.is_dir() and (p / "go.mod").exists())
    else:
        dirs = [ROOT / p for p in args.plugins]
    if not dirs:
        print("未指定插件（或没有 go.mod）")
        return 1

    touched = [d.name for d in dirs if process(d, args.dry_run)]
    print(f"\n改动插件 {len(touched)}/{len(dirs)}：{', '.join(touched) if touched else '（无）'}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
