#!/usr/bin/env python3
"""QuickDock 插件一键打包工具（默认仅 Windows）

用法:
    cd <plugin_dir> && python build.py                    # 默认只打包 windows（主程序目前仅 Windows 版本）
    python build.py disk-analyzer                         # 指定插件目录，打包 windows
    python build.py disk-analyzer --platform windows      # 仅打指定平台
    python build.py disk-analyzer --platform all          # 全平台（windows/darwin/linux）
    python build.py disk-analyzer --skip-build            # 跳过编译仅打包

产物: <插件ID>-<platform>.zip（按平台区分，如 io-github-parieses-disk-analyzer-windows.zip）
native 运行时依赖 Go 交叉编译（GOOS 指定目标平台）。主程序目前仅发布 Windows 版本，默认只产出
Windows 安装包；需要其他平台时用 --platform 指定（如 --platform all）。
"""

import argparse
import json
import os
import subprocess
import sys
import zipfile
from pathlib import Path

PLATFORMS = ["windows", "darwin", "linux"]
# 主程序目前仅发布 Windows 版本，默认只打包 windows；需要其他平台时用 --platform 指定
DEFAULT_PLATFORMS = ["windows"]


def load_manifest(plugin_dir: Path) -> dict:
    manifest_path = plugin_dir / "plugin.json"
    if not manifest_path.exists():
        print(f"❌ 未找到 plugin.json: {manifest_path}")
        sys.exit(1)
    with open(manifest_path, "r", encoding="utf-8") as f:
        manifest = json.load(f)
    return manifest


def validate_manifest(manifest: dict) -> None:
    required = ["id", "name", "version", "backend"]
    for field in required:
        if field not in manifest:
            print(f"❌ plugin.json 缺少必填字段: {field}")
            sys.exit(1)
    runtime = manifest.get("backend", {}).get("runtime", "")
    if runtime not in ("native", "goja", "none"):
        print(f"❌ 不支持的 runtime: {runtime}，仅支持 native/goja/none")
        sys.exit(1)
    if runtime == "native" and not manifest["backend"].get("entry"):
        print("❌ native runtime 必须指定 backend.entry")
        sys.exit(1)


def platform_binary_name(entry: str, platform: str) -> str:
    """目标平台上 native 二进制的文件名。
    Windows 加 .exe（QuickDock 用 exec.Command(entry) 启动，CreateProcess 会自动补 .exe，
    因此 zip 内放 disk-analyzer.exe 才能命中）；darwin/linux 无后缀直接执行。"""
    if platform == "windows":
        return entry + ".exe"
    return entry


def build_native(plugin_dir: Path, manifest: dict, platform: str, build_root: Path) -> bool:
    """用 Go 交叉编译目标平台二进制，产物统一放 <plugin_dir>/.build/<platform>/（避免同名冲突）"""
    entry = manifest["backend"]["entry"]
    bin_name = platform_binary_name(entry, platform)
    out_dir = build_root / platform
    out_dir.mkdir(parents=True, exist_ok=True)
    bin_path = out_dir / bin_name

    if bin_path.exists():
        # 源码比产物新 → 缓存过期，强制重编译（否则改了 .go 会永远打出旧二进制）
        src_files = list(plugin_dir.glob("*.go")) + [plugin_dir / "go.mod"]
        newest_src = max((p.stat().st_mtime for p in src_files if p.exists()), default=0.0)
        if newest_src <= bin_path.stat().st_mtime:
            print(f"✓ 编译产物已存在: {bin_name}（{platform}），跳过编译")
            return True
        print(f"♻️  源码较缓存产物有更新: {bin_name}（{platform}），重新编译")

    main_go = None
    for p in plugin_dir.glob("*.go"):
        main_go = p
        break
    if not main_go:
        print("⚠️  未找到 Go 源码，跳过编译")
        return False

    print(f"🔨 交叉编译 → {bin_name} (GOOS={platform}, GOARCH=amd64)")
    env = os.environ.copy()
    env["GOOS"] = platform
    env["GOARCH"] = "amd64"
    env["CGO_ENABLED"] = "0"
    try:
        result = subprocess.run(
            ["go", "build", "-o", str(bin_path), "."],
            cwd=str(plugin_dir),
            env=env,
            capture_output=True,
            text=True,
            timeout=180,
        )
        if result.returncode != 0:
            print(f"❌ {platform} 编译失败:\n{result.stderr}")
            sys.exit(1)
        size_mb = bin_path.stat().st_size / 1024 / 1024
        print(f"✓ {platform} 编译成功: {bin_name} ({size_mb:.1f} MB)")
        return True
    except FileNotFoundError:
        print("❌ 未找到 go 命令，请确保 Go 已安装")
        sys.exit(1)
    except subprocess.TimeoutExpired:
        print(f"❌ {platform} 编译超时（180s）")
        sys.exit(1)


def should_exclude(name: str, path_parts: list) -> bool:
    """检查文件名是否需要排除"""
    # 排除源码文件
    if name.endswith(".go") or name in ("go.mod", "go.sum"):
        return True
    # 排除打包脚本本身
    if name == "build.py":
        return True
    # 排除 zip 产物：避免把上一次的打包结果递归打进新 zip
    if name.endswith(".zip"):
        return True
    return False


def create_zip(plugin_dir: Path, manifest: dict, output_path: Path, platform: str,
               build_root: Path, include_build: bool = True) -> Path:
    runtime = manifest.get("backend", {}).get("runtime", "")
    # 所有平台的入口二进制名：插件目录里出现的一律不收集（历史残留/异平台产物），
    # 统一由 .build/<platform> 按当前平台显式引入，避免同名重复或平台错配。
    entry_bins = {
        platform_binary_name(manifest["backend"]["entry"], p)
        for p in PLATFORMS
    } if runtime == "native" else set()

    # 当前平台二进制：编译（如需）后从 .build/<platform> 显式写入 zip 根
    bin_name = ""
    if runtime == "native":
        if include_build:
            build_native(plugin_dir, manifest, platform, build_root)
        bin_name = platform_binary_name(manifest["backend"]["entry"], platform)

    # 收集共享文件（源码/产物/缓存目录一律排除）
    files_to_pack = []
    for root, dirs, filenames in os.walk(str(plugin_dir)):
        # 跳过 .git 与 .build（平台二进制/中间产物目录）
        dirs[:] = [d for d in dirs if d not in (".git", ".build")]

        rel_root = os.path.relpath(root, str(plugin_dir))
        if rel_root == ".":
            rel_root = ""

        for fname in filenames:
            if should_exclude(fname, rel_root.split(os.sep)):
                continue
            if fname in entry_bins:
                continue

            full_path = os.path.join(root, fname)
            arcname = os.path.join(rel_root, fname) if rel_root else fname
            files_to_pack.append((full_path, arcname))

    # 写入 zip
    with zipfile.ZipFile(str(output_path), "w", zipfile.ZIP_DEFLATED) as zf:
        # plugin.json 必须位于 zip 根部（QuickDock 安装器要求）
        zf.write(str(plugin_dir / "plugin.json"), "plugin.json")
        for full_path, arcname in files_to_pack:
            if arcname != "plugin.json":
                zf.write(full_path, arcname)
        # native：把当前平台的二进制放入 zip 根（文件名与平台匹配）
        if bin_name:
            bin_path = build_root / platform / bin_name
            if bin_path.exists():
                zf.write(str(bin_path), bin_name)

    size_mb = output_path.stat().st_size / 1024 / 1024
    print(f"✓ 打包完成[{platform}]: {output_path} ({size_mb:.1f} MB)")
    return output_path


def main():
    parser = argparse.ArgumentParser(description="QuickDock 插件一键打包工具（多平台）")
    parser.add_argument("plugin_dir", nargs="?", default=None, help="插件目录（默认脚本所在目录）")
    parser.add_argument("-o", "--output", help="输出 zip 路径（仅单平台时可用）")
    parser.add_argument("-d", "--output-dir", help="输出目录（默认仓库根 dist/）")
    parser.add_argument("--platform", choices=PLATFORMS + ["all"], default=None,
                        help="目标平台（默认取 plugin.json platforms 字段，全部打）")
    parser.add_argument("--skip-build", action="store_true", help="跳过编译步骤")
    args = parser.parse_args()

    # 确定插件目录
    if args.plugin_dir:
        plugin_dir = Path(args.plugin_dir).resolve()
    else:
        cwd = Path.cwd().resolve()
        plugin_dir = cwd if (cwd / "plugin.json").exists() else Path(__file__).parent.resolve()

    if not plugin_dir.is_dir():
        print(f"❌ 目录不存在: {plugin_dir}")
        sys.exit(1)

    manifest = load_manifest(plugin_dir)
    validate_manifest(manifest)

    runtime = manifest["backend"]["runtime"]
    plugin_id = manifest["id"]
    safe_name = plugin_id.replace(".", "-").lower()

    # 确定目标平台集合
    if args.platform == "all":
        targets = PLATFORMS
    elif args.platform:
        targets = [args.platform]
    else:
        targets = DEFAULT_PLATFORMS

    print(f"\n📋 插件信息:")
    print(f"   ID:      {plugin_id}")
    print(f"   名称:    {manifest.get('name', '-')}")
    print(f"   版本:    {manifest.get('version', '-')}")
    print(f"   Runtime: {runtime}")
    print(f"   目标平台: {targets}\n")

    build_root = plugin_dir / ".build"

    # 输出目录：默认仓库根 dist/（zip 不在插件文件夹里散布）
    out_dir = Path(args.output_dir).resolve() if args.output_dir else (Path(__file__).parent / "dist")
    out_dir.mkdir(parents=True, exist_ok=True)

    for platform in targets:
        if args.output:
            if len(targets) > 1:
                print("❌ -o 自定义输出仅支持单平台（--platform 指定一个）")
                sys.exit(1)
            output_path = Path(args.output).resolve()
        else:
            output_path = out_dir / f"{safe_name}-{platform}.zip"

        create_zip(plugin_dir, manifest, output_path, platform, build_root,
                   include_build=not args.skip_build)

    print(f"\n✅ 完成！安装包（{out_dir}）:")
    for platform in targets:
        print(f"   • {out_dir / f'{safe_name}-{platform}.zip'}  [{platform}]")
    print(f"   在 QuickDock 中：插件管理 → 从文件安装 → 选择对应平台的 zip")


if __name__ == "__main__":
    main()