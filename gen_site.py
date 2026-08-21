#!/usr/bin/env python3
"""QuickDock 插件中心主页生成器

扫描仓库根下所有插件目录的 plugin.json，生成 site/index.html（GitHub Pages 主页）。
图标使用各插件 plugin.json 的 icon 字段（raw 链接到仓库 main 分支）。
用法:
    python gen_site.py
    python gen_site.py -o site/index.html
"""

import argparse
import json
import os
import sys
import urllib.parse
from pathlib import Path

REPO_OWNER = "parieses"
REPO_NAME = "quickdock-plugins"
RAW_BASE = f"https://github.com/{REPO_OWNER}/{REPO_NAME}/raw/HEAD"
REL_BASE = f"https://github.com/{REPO_OWNER}/{REPO_NAME}/releases/latest/download"

# 主程序目前仅发布 Windows 版本，主页只展示 Windows 下载
PLATFORMS = [("windows", "Windows")]

TEMPLATE = """<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>QuickDock 插件中心</title>
<style>
  :root {{ --bg:#121316; --card:#1a1c20; --border:#2a2d33; --text:#e8eaed; --muted:#8b919c; --accent:#4a9eff; --good:#4caf50; }}
  * {{ margin:0; padding:0; box-sizing:border-box; }}
  body {{ background:var(--bg); color:var(--text); font:14px/1.6 system-ui,-apple-system,"Segoe UI",sans-serif; padding:40px 20px; }}
  .wrap {{ max-width:860px; margin:0 auto; }}
  h1 {{ font-size:24px; margin-bottom:6px; }}
  .sub {{ color:var(--muted); margin-bottom:28px; }}
  .card {{ background:var(--card); border:1px solid var(--border); border-radius:12px; padding:20px; margin-bottom:16px; }}
  .card-head {{ display:flex; align-items:center; gap:14px; margin-bottom:10px; }}
  .card-head img {{ width:44px; height:44px; border-radius:8px; background:#26282e; padding:6px; }}
  .card-title {{ font-size:17px; font-weight:600; }}
  .card-id {{ color:var(--muted); font-size:12px; font-family:monospace; }}
  .card-desc {{ color:var(--muted); margin-bottom:14px; }}
  .badge {{ display:inline-block; padding:1px 10px; border-radius:10px; font-size:12px; background:rgba(76,175,80,.15); color:var(--good); margin-left:8px; }}
  .downloads {{ display:flex; flex-wrap:wrap; gap:8px; }}
  .dl-item {{ display:inline-flex; align-items:stretch; gap:0; border-radius:8px; overflow:hidden; border:1px solid var(--border); background:#23262c; }}
  .dl {{ display:inline-flex; align-items:center; gap:6px; padding:8px 14px; color:var(--text); text-decoration:none; font-size:13px; transition:border-color .15s,background .15s; }}
  .dl:hover {{ border-color:var(--accent); }}
  .copy-btn {{ border:0; border-left:1px solid var(--border); background:transparent; color:var(--muted); cursor:pointer; padding:0 10px; font-size:12px; transition:color .15s,background .15s; }}
  .copy-btn:hover {{ color:var(--accent); background:rgba(74,158,255,.08); }}
  .copy-btn.done {{ color:var(--good); }}
  .card-actions {{ margin-top:12px; }}
  .copy-all {{ display:inline-flex; align-items:center; gap:6px; padding:6px 12px; border-radius:8px; border:1px solid var(--border); background:transparent; color:var(--muted); cursor:pointer; font-size:12px; transition:color .15s,border-color .15s; }}
  .copy-all:hover {{ color:var(--accent); border-color:var(--accent); }}
  .copy-all.done {{ color:var(--good); border-color:var(--good); }}
  .hint {{ color:var(--muted); font-size:12px; margin-top:24px; }}
  .hint a {{ color:var(--accent); }}
  .toast {{ position:fixed; left:50%; bottom:32px; transform:translateX(-50%) translateY(20px); background:#23262c; border:1px solid var(--border); color:var(--text); padding:10px 18px; border-radius:10px; font-size:13px; opacity:0; transition:opacity .2s,transform .2s; pointer-events:none; z-index:99; }}
  .toast.show {{ opacity:1; transform:translateX(-50%) translateY(0); }}
</style>
</head>
<body>
<div class="wrap">
  <h1>🪛 QuickDock 插件中心</h1>
  <p class="sub">为 <a href="https://github.com/parieses/quickdock" style="color:var(--accent)">QuickDock</a> 开发的非内置插件，提供 Windows 安装包（主程序目前仅支持 Windows）。</p>
{cards}
  <p class="hint">安装方法：打开 QuickDock → 插件管理 → 从文件安装 → 选择下载的 zip（或直接把 zip 拖进插件管理页）。升级时覆盖安装，自动备份旧版本。<br>
  下载 404？对应平台还没有 Release 资产，或该插件没发布对应平台的安装包，见 <a href="{rel_base}">Releases</a>。更多信息：<a href="https://github.com/parieses/quickdock-plugins">仓库主页</a>。</p>
</div>
<div class="toast" id="toast"></div>
<script>
  function showToast(msg) {{
    var t = document.getElementById('toast');
    t.textContent = msg;
    t.classList.add('show');
    clearTimeout(t._timer);
    t._timer = setTimeout(function() {{ t.classList.remove('show'); }}, 1600);
  }}
  function copyText(text, btn) {{
    var done = function() {{
      showToast('链接已复制');
      if (btn) {{ btn.classList.add('done'); setTimeout(function() {{ btn.classList.remove('done'); }}, 1200); }}
    }};
    if (navigator.clipboard && navigator.clipboard.writeText) {{
      navigator.clipboard.writeText(text).then(done, function() {{ fallbackCopy(text); }});
    }} else {{ fallbackCopy(text); }}
  }}
  function fallbackCopy(text) {{
    var ta = document.createElement('textarea');
    ta.value = text; ta.style.position = 'fixed'; ta.style.opacity = '0';
    document.body.appendChild(ta); ta.select();
    try {{ document.execCommand('copy'); showToast('链接已复制'); }} catch (e) {{ showToast('复制失败，请手动复制'); }}
    document.body.removeChild(ta);
  }}
  function copyAll(card) {{
    var links = [];
    card.querySelectorAll('.dl').forEach(function(a) {{ links.push(a.href); }});
    if (!links.length) return;
    copyText(links.join('\\n'), card.querySelector('.copy-all'));
  }}
</script>
</body>
</html>
"""

CARD_TEMPLATE = """  <div class="card" id="card-{card_id}">
    <div class="card-head">
      <img src="{icon}" alt="icon">
      <div>
        <span class="card-title">{name}</span>
        <span class="badge">v{version}</span>
        <div class="card-id">{plugin_id}</div>
      </div>
    </div>
    <p class="card-desc">{desc}</p>
    <div class="downloads">
{downloads}
    </div>
    <div class="card-actions">
      <button class="copy-all" onclick="copyAll(document.getElementById('card-{card_id}'))">📋 复制全部下载链接</button>
    </div>
  </div>"""

DOWNLOAD_TEMPLATE = """      <span class="dl-item"><a class="dl" href="{href}" data-link="{href}">{label}</a><button class="copy-btn" data-link="{href}" onclick="copyText(this.dataset.link, this)">复制</button></span>"""


def collect_plugins(repo_root: Path) -> list:
    """扫描仓库根下所有含 plugin.json 的插件目录"""
    plugins = []
    for entry in sorted(repo_root.iterdir()):
        mf_path = entry / "plugin.json"
        if not entry.is_dir() or entry.name.startswith(".") or not mf_path.exists():
            continue
        try:
            mf = json.load(open(mf_path, encoding="utf-8"))
        except Exception as e:
            print(f"⚠️  {entry.name}/plugin.json 解析失败: {e}")
            continue
        plugins.append({"dir": entry.name, "manifest": mf})
    return plugins


def build_cards(plugins: list) -> str:
    cards = []
    for p in plugins:
        mf, pdir = p["manifest"], p["dir"]
        safe_id = mf["id"].replace(".", "-").lower()

        # 图标：plugin.json 的 icon 字段 → 仓库 raw 链接
        icon = mf.get("icon", "")
        if icon:
            icon_url = f"{RAW_BASE}/{urllib.parse.quote(pdir)}/{urllib.parse.quote(icon)}"
        else:
            icon_url = "data:image/svg+xml;utf8,<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24'><rect width='24' height='24' rx='6' fill='%2326282e'/><text x='12' y='16' font-size='12' text-anchor='middle' fill='%238b919c'>?</text></svg>"

        downloads = []
        for plat, label in PLATFORMS:
            href = f"{REL_BASE}/{safe_id}-{plat}.zip"
            downloads.append(DOWNLOAD_TEMPLATE.format(href=href, label=f"⬇ {label}"))
        downloads_html = "\n".join(downloads)

        cards.append(CARD_TEMPLATE.format(
            card_id=safe_id,
            icon=icon_url,
            name=mf.get("name", mf["id"]),
            version=mf.get("version", "0.0.0"),
            plugin_id=mf["id"],
            desc=mf.get("description", "—"),
            downloads=downloads_html,
        ))
    return "\n".join(cards)


def main():
    parser = argparse.ArgumentParser(description="QuickDock 插件中心主页生成器")
    parser.add_argument("-o", "--output", default="site/index.html", help="输出路径（默认 site/index.html）")
    args = parser.parse_args()

    repo_root = Path(__file__).parent.resolve()
    plugins = collect_plugins(repo_root)
    if not plugins:
        print("❌ 未找到任何插件目录（缺少 plugin.json）")
        sys.exit(1)

    print(f"📋 发现 {len(plugins)} 个插件:")
    for p in plugins:
        print(f"   • {p['manifest'].get('name', p['dir'])} ({p['manifest']['id']})")

    html = TEMPLATE.format(cards=build_cards(plugins), rel_base=REL_BASE)
    out = Path(args.output).resolve()
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(html, encoding="utf-8")
    print(f"✅ 主页已生成: {out}")


if __name__ == "__main__":
    main()