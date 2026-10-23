#!/usr/bin/env python3
"""生成市场索引 site/index.json —— 仅在「本次成功打包」的插件上升级版本。

背景
----
原来 index.json 由 pages.yml 在每次 push 到 main 时直接按 plugin.json 重新生成并部署，
与 build-plugins.yml 的打包 / 发布完全解耦。一旦把 plugin.json 版本号提高但 Go 编译失败，
Pages 上的 index.json 已是新版本，而 latest release 里仍是旧 zip —— 应用内点「更新」就会
下载到「声称新版本、实为旧版」的 zip。

本脚本让 index.json 只在插件真正打包成功后才前进：
  - 本次成功打包的插件：用 plugin.json 当前内容刷新条目（新版本 + 新下载直链）。
  - 其余插件（本次未打包 / 打包失败 / 未变更）：保留上一版 index.json 的条目，
    其版本号因此与 latest release 中仍然存在的旧 zip 保持一致。
若无上一版（首次 / 过渡期），回退全量按 plugin.json 生成。

典型用法（由 build-plugins.yml 调用）：
    python3 tools/gen_market_index.py \
        --previous site/index.json \
        --built "disk-analyzer database" \
        --failed "some-broken-plugin"
"""

import argparse
import json
import sys
from datetime import datetime, timezone
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO_ROOT))

from gen_site import collect_plugins, build_plugin_entry  # noqa: E402


def main():
    ap = argparse.ArgumentParser(description="生成 build-gated 的市场索引 index.json")
    ap.add_argument("--previous", help="上一版 index.json 路径（合并基线；省略则全量生成）")
    ap.add_argument("--built", default="", help="本次成功打包的插件目录名，空格分隔")
    ap.add_argument("--failed", default="", help="本次打包失败的插件目录名，空格分隔（无历史时也跳过，避免死链）")
    ap.add_argument("-o", "--output", default="site/index.json", help="输出路径（默认 site/index.json）")
    args = ap.parse_args()

    plugins = collect_plugins(REPO_ROOT)
    if not plugins:
        print("❌ 未找到任何插件目录（缺少 plugin.json）")
        sys.exit(1)

    built = set(args.built.split()) if args.built.strip() else set()
    failed = set(args.failed.split()) if args.failed.strip() else set()

    # 上一版条目：id -> entry，作为未打包/失败插件的回退基线
    prev = {}
    if args.previous:
        p = Path(args.previous)
        if p.exists():
            try:
                data = json.loads(p.read_text(encoding="utf-8"))
                for it in data.get("plugins", []):
                    if it.get("id"):
                        prev[it["id"]] = it
            except Exception as e:
                print(f"⚠️  解析上一版 index.json 失败: {e}，回退全量生成")
        else:
            print(f"⚠️  未找到上一版 {p}，回退全量生成")

    items = []
    skipped = []
    has_baseline = bool(prev)
    for p in plugins:
        mf, pdir = p["manifest"], p["dir"]
        pid = mf.get("id", "")
        if pdir in built:
            items.append(build_plugin_entry(mf, pdir))
            print(f"✓ 刷新（已打包）: {mf.get('name', pid)} v{mf.get('version', '?')}")
        elif pid in prev:
            items.append(prev[pid])
            print(f"• 沿用上一版: {prev[pid].get('name', pid)} v{prev[pid].get('version', '?')}")
        elif pdir in failed:
            # 本次打包失败且无历史条目 → release 里没有它的 zip，纳入会产生死链
            skipped.append(pid)
            print(f"✗ 跳过（打包失败且无历史）: {mf.get('name', pid)}")
        elif has_baseline:
            # 已有发布基线、但该插件既不在基线里、本次也没成功打包
            # （典型：新增插件但编译失败）。此时 release 里没有它的 zip，
            # 若强行纳入索引会得到一个点开就 404 的下载链接，故跳过。
            skipped.append(pid)
            print(f"✗ 跳过（无历史且本次未成功打包）: {mf.get('name', pid)}")
        else:
            # 无任何基线（首次引入 build-gated 之前）——全量引导，按当前 plugin.json 生成
            items.append(build_plugin_entry(mf, pdir))
            print(f"• 兜底（无基线全量引导）: {mf.get('name', pid)} v{mf.get('version', '?')}")

    out = Path(args.output).resolve()
    out.parent.mkdir(parents=True, exist_ok=True)
    payload = {
        "name": "QuickDock 插件中心",
        "updated": datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
        "plugins": items,
    }
    out.write_text(json.dumps(payload, ensure_ascii=False, indent=2), encoding="utf-8")
    advanced = len(built & {p["dir"] for p in plugins})
    tail = f"，跳过 {len(skipped)} 个无产物的新插件" if skipped else ""
    print(f"✅ 市场索引已生成: {out}（{len(items)} 个插件，本次前进 {advanced} 个{tail}）")


if __name__ == "__main__":
    main()
