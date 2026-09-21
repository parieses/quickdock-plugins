#!/usr/bin/env python3
"""QuickDock 插件文档表格生成器

扫描仓库根下所有插件目录的 plugin.json，重新生成：
  - README.md 的「插件列表」表格（位于 <!--PLUGINS_TABLE_START--> / END 之间）
  - plugin-dev-guide.md 的「插件参考」三张分类表（位于 <!--DEVGUIDE_PLUGINS_START--> / END 之间）

数量与行全部由 plugin.json 推导。新增/删除插件后只需跑本脚本，
无需再手动同步任何表格。

用法:
    python gen_tables.py
"""
import json
import re
import sys
from pathlib import Path

if hasattr(sys.stdout, "reconfigure"):
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")

REPO = Path(__file__).resolve().parent
README = REPO / "README.md"
GUIDE = REPO / "plugin-dev-guide.md"

# 分类展示顺序（其余 runtime 追加在后面）
RT_ORDER = ["goja", "none", "native"]
RT_LABEL = {
    "goja":   '**Goja 插件（`backend.runtime: "goja"`，内嵌 JS 引擎，有后端逻辑）**',
    "none":   '**Pure Frontend 插件（`runtime: none`，纯前端经宿主桥接调 Host API）**',
    "native": '**Native 插件（`runtime: native`，自带 Go 源码 + 编译产物）**',
}


def esc(s):
    """转义表格内的竖线，避免破坏 Markdown 表格。"""
    return (s or "").replace("|", "\\|")


def collect():
    out = []
    for entry in sorted(REPO.iterdir()):
        mf = entry / "plugin.json"
        if not entry.is_dir() or entry.name.startswith(".") or not mf.exists():
            continue
        try:
            m = json.load(open(mf, encoding="utf-8"))
        except Exception as e:
            print(f"⚠️  {entry.name}/plugin.json 解析失败: {e}")
            continue
        out.append({"dir": entry.name, "m": m})
    return out


def readme_table(plugs):
    rows = ["| 插件 | ID | 版本 | 说明 |", "|------|----|------|------|"]
    for p in sorted(plugs, key=lambda x: x["dir"]):
        m = p["m"]
        name = esc(m.get("name", p["dir"]))
        pid = m.get("id", "")
        ver = m.get("version", "")
        desc = esc(m.get("description", ""))
        rows.append(f"| [{name}](./{p['dir']}/) | `{pid}` | {ver} | {desc} |")
    return "\n".join(rows)


def devguide_block(plugs):
    groups = {}
    for p in plugs:
        rt = p["m"].get("backend", {}).get("runtime", "other")
        groups.setdefault(rt, []).append(p)
    parts = []
    total = len(plugs)
    for rt in RT_ORDER + [k for k in groups if k not in RT_ORDER]:
        items = sorted(groups.get(rt, []), key=lambda x: x["dir"])
        if not items:
            continue
        parts.append(RT_LABEL.get(rt, f"**{rt} 插件**") + f" — 共 {len(items)} 个")
        if rt == "none":
            parts.append("| 插件 ID | 功能 | 演示的宿主能力 |")
            parts.append("|---|---|---|")
            for p in items:
                m = p["m"]
                apis = m.get("host_apis") or []
                host = " → ".join(apis) if apis else "纯前端"
                parts.append(f"| {p['dir']} | {esc(m.get('description', ''))} | {esc(host)} |")
        else:
            parts.append("| 插件 ID | 功能 |")
            parts.append("|---|---|")
            for p in items:
                m = p["m"]
                parts.append(f"| {p['dir']} | {esc(m.get('description', ''))} |")
        parts.append("")
    note = (
        "> 以上 " + str(total) + " 个插件均已迁至 `plugins/external/`（ID 改为 `io.github.parieses.*`），"
        "代码可直接复用。goja/none 插件演示「零宿主依赖、纯 JS 自包含」的外部化样板；"
        "native 插件演示「Go 源码 vendor + 自编译 entry exe」模式（`build.py` 直接在插件目录 `go build`）。"
        "完整 goja 模板见上文「完整示例」。"
    )
    parts.append(note)
    return "\n".join(parts).rstrip() + "\n"


def replace_between(path, start, end, content):
    txt = path.read_text(encoding="utf-8")
    if start not in txt or end not in txt:
        print(f"⚠️  {path.name} 缺少标记 {start}/{end}，跳过")
        return False
    pat = re.compile(re.escape(start) + r".*?" + re.escape(end), re.DOTALL)
    new = pat.sub(start + "\n\n" + content + "\n" + end, txt, count=1)
    path.write_text(new, encoding="utf-8")
    return True


def main():
    plugs = collect()
    print(f"扫描到 {len(plugs)} 个插件")
    if replace_between(README, "<!--PLUGINS_TABLE_START-->", "<!--PLUGINS_TABLE_END-->", readme_table(plugs)):
        print("✅ README.md 插件列表已更新")
    if replace_between(GUIDE, "<!--DEVGUIDE_PLUGINS_START-->", "<!--DEVGUIDE_PLUGINS_END-->", devguide_block(plugs)):
        print("✅ plugin-dev-guide.md 分类表已更新")


if __name__ == "__main__":
    main()
