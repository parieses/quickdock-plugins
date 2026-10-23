#!/usr/bin/env python3
"""按插件"原子化"打包：全部声明平台成功，才把 zip 并入 dist/。

为什么需要它
------------
`build.py` 是按平台逐个打包的，某个平台编译失败会 `sys.exit(1)` —— 但此前
已经成功的平台 zip 早已写进输出目录。CI 若只看退出码把这个插件排除出
`index.json`，这批"半成品"zip 仍会随 release 发布，且文件名与旧 asset 相同 →
**覆盖旧包**。结果：index.json 声称旧版本，用户实际下载到的却是新版本的包，
版本号与索引不一致（这正是本仓库反复踩过的坑）。

本脚本给每个插件一个独立暂存目录，做到插件级原子：
  1. `build.py <plugin> -d <stage>` 打包到暂存目录
  2. 校验 plugin.json 声明的每个平台都产出了 `<safe_id>-<platform>.zip`
  3. 全部满足才原子落地到 dist/（同名覆盖；跨文件系统时先 copyfile 到 .part 再改名）
  4. 任一步失败 → 不动 dist/、返回非零，由 CI 记入 failed 列表

用法（CI 调用）：
    python3 tools/package_isolated.py <plugin_dir> <stage_dir> [dist_dir]
"""
from __future__ import annotations

import os
import shutil
import subprocess
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO))

from build import load_manifest, resolve_platforms  # noqa: E402


def main() -> int:
    if len(sys.argv) < 3:
        print("用法: package_isolated.py <plugin_dir> <stage_dir> [dist_dir]", file=sys.stderr)
        return 2

    plugin_dir = Path(sys.argv[1]).resolve()
    stage = Path(sys.argv[2]).resolve()
    dist = Path(sys.argv[3]).resolve() if len(sys.argv) > 3 else REPO / "dist"

    manifest = load_manifest(plugin_dir)
    platforms = resolve_platforms(manifest, None)
    safe_id = manifest["id"].replace(".", "-").lower()

    # 每次用干净暂存目录，避免上一轮同名残留被误判为"本次产物"
    if stage.exists():
        shutil.rmtree(stage, ignore_errors=True)
    stage.mkdir(parents=True, exist_ok=True)

    print(f"==> {plugin_dir.name}: 目标平台 {platforms} → 暂存 {stage}")
    proc = subprocess.run(
        [sys.executable, str(REPO / "build.py"), str(plugin_dir), "-d", str(stage)]
    )
    if proc.returncode != 0:
        print(f"❌ 打包失败: {plugin_dir.name}（build.py 退出码 {proc.returncode}）", file=sys.stderr)
        return 1

    expected = [stage / f"{safe_id}-{p}.zip" for p in platforms]
    missing = [p for p, f in zip(platforms, expected) if not f.is_file()]
    if missing:
        print(
            f"❌ 平台产物缺失: {plugin_dir.name} 缺少 {missing}（zip 不并入 dist/，index 不前进）",
            file=sys.stderr,
        )
        return 1

    dist.mkdir(parents=True, exist_ok=True)
    for f in expected:
        dst = dist / f.name
        # 暂存目录常由 CI 的 mktemp 放在 /tmp，与工作区不在同一文件系统，
        # 直接 os.replace 会抛 EXDEV（Invalid cross-device link）。
        # 先 copyfile 到 dist/ 内的 .part，再在同目录原子改名：
        # 既跨设备安全，又保证不会有"复制到一半"的 zip 被发布。
        part = dist / (f.name + ".part")
        try:
            shutil.copyfile(str(f), str(part))
            os.replace(str(part), str(dst))
        finally:
            if part.exists():
                part.unlink()
    print(f"✓ 已并入 dist/: {plugin_dir.name} [{', '.join(platforms)}]")
    return 0


if __name__ == "__main__":
    sys.exit(main())
