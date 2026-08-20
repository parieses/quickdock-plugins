#!/usr/bin/env python3
"""QuickDock 插件一键打包工具

用法:
    cd <plugin_dir> && python build.py
    python build.py -o dist/my.zip
    python build.py --skip-build
"""

import argparse
import json
import os
import subprocess
import sys
import zipfile
from pathlib import Path


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


def get_exe_name(manifest: dict) -> str:
    """确定 native 插件的 exe 文件名"""
    entry = manifest.get("backend", {}).get("entry", "")
    if entry.endswith(".exe"):
        return entry
    if sys.platform == "win32":
        return entry + ".exe"
    return entry


def build_native(plugin_dir: Path, manifest: dict) -> bool:
    """编译 native Go 插件"""
    exe_name = get_exe_name(manifest)
    exe_path = plugin_dir / exe_name

    if exe_path.exists():
        print(f"✓ 编译产物已存在: {exe_name}，跳过编译")
        return True

    # 查找 main.go
    main_go = None
    for p in plugin_dir.glob("*.go"):
        main_go = p
        break

    if not main_go:
        print("⚠️  未找到 Go 源码，跳过编译")
        return False

    print(f"🔨 编译 native 插件 → {exe_name}")
    try:
        result = subprocess.run(
            ["go", "build", "-o", str(exe_path), "."],
            cwd=str(plugin_dir),
            capture_output=True,
            text=True,
            timeout=120,
        )
        if result.returncode != 0:
            print(f"❌ 编译失败:\n{result.stderr}")
            sys.exit(1)
        size_mb = exe_path.stat().st_size / 1024 / 1024
        print(f"✓ 编译成功: {exe_name} ({size_mb:.1f} MB)")
        return True
    except FileNotFoundError:
        print("❌ 未找到 go 命令，请确保 Go 已安装")
        sys.exit(1)
    except subprocess.TimeoutExpired:
        print("❌ 编译超时（120s）")
        sys.exit(1)


def should_exclude(name: str, path_parts: list) -> bool:
    """检查文件名是否需要排除"""
    # 排除源码文件
    if name.endswith(".go") or name in ("go.mod", "go.sum"):
        return True
    # 排除打包脚本本身
    if name == "build.py":
        return True
    return False


def create_zip(plugin_dir: Path, manifest: dict, output_path: Path, include_build: bool = True) -> Path:
    if include_build and manifest.get("backend", {}).get("runtime") == "native":
        build_native(plugin_dir, manifest)

    exe_name = get_exe_name(manifest) if manifest.get("backend", {}).get("runtime") == "native" else ""
    
    # 收集文件
    files_to_pack = []
    for root, dirs, filenames in os.walk(str(plugin_dir)):
        # 跳过 .git 目录
        dirs[:] = [d for d in dirs if d != ".git"]
        
        rel_root = os.path.relpath(root, str(plugin_dir))
        if rel_root == ".":
            rel_root = ""
        
        for fname in filenames:
            if should_exclude(fname, rel_root.split(os.sep)):
                continue
            
            # native 运行时：排除非 entry 的 exe
            if exe_name and fname.endswith(".exe") and fname != exe_name:
                continue
            
            full_path = os.path.join(root, fname)
            if rel_root:
                arcname = os.path.join(rel_root, fname)
            else:
                arcname = fname
            
            files_to_pack.append((full_path, arcname))
    
    # 写入 zip
    print(f"📦 打包 {len(files_to_pack)} 个文件 → {output_path}")
    with zipfile.ZipFile(str(output_path), "w", zipfile.ZIP_DEFLATED) as zf:
        # 写入 plugin.json（确保在根目录）
        zf.write(str(plugin_dir / "plugin.json"), "plugin.json")
        # 写入其他文件
        for full_path, arcname in files_to_pack:
            if arcname != "plugin.json":
                zf.write(full_path, arcname)

    size_mb = output_path.stat().st_size / 1024 / 1024
    print(f"✓ 打包完成: {output_path} ({size_mb:.1f} MB)")
    return output_path


def main():
    parser = argparse.ArgumentParser(description="QuickDock 插件一键打包工具")
    parser.add_argument("plugin_dir", nargs="?", default=None, help="插件目录（默认脚本所在目录）")
    parser.add_argument("-o", "--output", help="输出 zip 路径（默认 <plugin_id>.zip）")
    parser.add_argument("--skip-build", action="store_true", help="跳过编译步骤")
    args = parser.parse_args()

    # 确定插件目录
    if args.plugin_dir:
        plugin_dir = Path(args.plugin_dir).resolve()
    else:
        # 优先使用当前工作目录（如果里面有 plugin.json）
        cwd = Path.cwd().resolve()
        if (cwd / "plugin.json").exists():
            plugin_dir = cwd
        else:
            # 回退到脚本所在目录
            plugin_dir = Path(__file__).parent.resolve()

    if not plugin_dir.is_dir():
        print(f"❌ 目录不存在: {plugin_dir}")
        sys.exit(1)

    manifest = load_manifest(plugin_dir)
    validate_manifest(manifest)

    runtime = manifest["backend"]["runtime"]
    plugin_id = manifest["id"]

    print(f"\n📋 插件信息:")
    print(f"   ID:      {plugin_id}")
    print(f"   名称:    {manifest.get('name', '-')}")
    print(f"   版本:    {manifest.get('version', '-')}")
    print(f"   Runtime: {runtime}")
    print(f"   平台:    {manifest.get('platforms', ['windows'])}\n")

    # 确定输出路径
    if args.output:
        output_path = Path(args.output).resolve()
    else:
        safe_name = plugin_id.replace(".", "-").lower()
        output_path = plugin_dir / f"{safe_name}.zip"

    create_zip(plugin_dir, manifest, output_path, include_build=not args.skip_build)

    print(f"\n✅ 完成！安装包: {output_path}")
    print(f"   在 QuickDock 中：插件管理 → 从文件安装 → 选择此 zip")


if __name__ == "__main__":
    main()
