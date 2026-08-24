#!/usr/bin/env python3
"""QuickDock 插件中心主页生成器

扫描仓库根下所有插件目录的 plugin.json，生成 site/index.html（GitHub Pages 主页）
与 site/index.json（应用内"在线安装"市场索引）。
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
from datetime import datetime, timezone
from html import escape as h
from pathlib import Path

# Windows GBK 控制台打不出 emoji/部分中文，统一切 UTF-8（其他平台无副作用）
if hasattr(sys.stdout, "reconfigure"):
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")

REPO_OWNER = "parieses"
REPO_NAME = "quickdock-plugins"
REPO_URL = f"https://github.com/{REPO_OWNER}/{REPO_NAME}"
RAW_BASE = f"{REPO_URL}/raw/HEAD"
REL_BASE = f"{REPO_URL}/releases/latest/download"
# Releases 页面链接：/releases/latest/download 只是"最新资产"下载直链前缀，单独打开会 404
REL_PAGE = f"{REPO_URL}/releases/tag/latest"

# 主程序目前仅发布 Windows 版本，主页只展示 Windows 下载
PLATFORMS = [("windows", "Windows")]

# 分类 emoji（按子串匹配，未命中回退 🧩）
CATEGORY_ICONS = [("网络", "🌐"), ("系统", "🗂️"), ("办公", "📄"), ("效率", "⚡"), ("开发", "🛠️")]


def cat_icon(cat):
    for key, icon in CATEGORY_ICONS:
        if key in (cat or ""):
            return icon
    return "🧩"


ICON_FALLBACK = ("data:image/svg+xml;utf8,<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 48 48'>"
                 "<rect width='48' height='48' rx='12' fill='%23202839'/>"
                 "<text x='24' y='30' font-size='20' text-anchor='middle' fill='%237c8db5'>?</text></svg>")

FAVICON = ("data:image/svg+xml,<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 100 100'>"
           "<text y='.9em' font-size='90'>🧩</text></svg>")

# 模板用 @@TOKEN@@ / __TOKEN__ 占位 + 字符串替换（避免 CSS/JS 花括号与 str.format 冲突）
TEMPLATE = r"""<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="dark light">
<meta name="theme-color" content="#0a0d13">
<meta name="description" content="QuickDock 插件中心：浏览、下载并一键安装 QuickDock 非内置插件。">
<link rel="icon" href="__FAVICON__">
<title>QuickDock 插件中心</title>
<style>
  :root {
    --bg:#0a0d13; --card:rgba(18,24,38,.66); --card-hi:#161e30;
    --border:rgba(148,163,190,.15); --border-hi:rgba(148,163,190,.34);
    --text:#eef2fa; --muted:#94a0b8; --faint:#5f6c88;
    --accent:#5aa2ff; --accent2:#9b8cff; --good:#34d399;
    --grad:linear-gradient(120deg,#4a9eff,#8b7bff 55%,#c084fc);
    --ring:rgba(90,162,255,.30);
    --shadow:0 18px 44px -14px rgba(0,0,0,.55);
    --radius:20px;
  }
  @media (prefers-color-scheme: light) {
    :root {
      --bg:#f6f7fb; --card:rgba(255,255,255,.72); --card-hi:#f2f6ff;
      --border:rgba(24,34,58,.11); --border-hi:rgba(24,34,58,.24);
      --text:#141d33; --muted:#57678a; --faint:#8b98b3;
      --shadow:0 18px 38px -18px rgba(30,50,90,.22);
    }
  }
  * { margin:0; padding:0; box-sizing:border-box; }
  html { scroll-behavior:smooth; }
  body {
    background:var(--bg); color:var(--text);
    font:15px/1.65 "Segoe UI", system-ui, -apple-system, "PingFang SC", "Microsoft YaHei", sans-serif;
    -webkit-font-smoothing:antialiased; min-height:100vh; overflow-x:hidden;
  }
  ::selection { background:rgba(90,162,255,.30); }

  /* ── 背景特效：极光光斑 + 点阵 ───────── */
  .bg-fx { position:fixed; inset:0; z-index:-2; overflow:hidden; pointer-events:none; }
  .orb { position:absolute; border-radius:50%; filter:blur(110px); will-change:transform; }
  .orb.a { width:620px; height:620px; left:-180px; top:-220px; background:rgba(59,130,246,.20); animation:drift 30s ease-in-out infinite alternate; }
  .orb.b { width:520px; height:520px; right:-160px; top:60px; background:rgba(139,92,246,.17); animation:drift 26s ease-in-out -8s infinite alternate-reverse; }
  .orb.c { width:560px; height:560px; left:32%; bottom:-300px; background:rgba(192,132,252,.11); animation:drift 34s ease-in-out -15s infinite alternate; }
  @keyframes drift { to { transform:translate(70px,46px) scale(1.12); } }
  body::before { content:""; position:fixed; inset:0; z-index:-1; pointer-events:none; opacity:.5;
    background-image:radial-gradient(rgba(148,163,190,.16) 1px, transparent 1.4px); background-size:30px 30px;
    mask-image:linear-gradient(#000, transparent 44%); -webkit-mask-image:linear-gradient(#000, transparent 44%); }
  @media (prefers-color-scheme: light) {
    .orb.a { background:rgba(59,130,246,.14); } .orb.b { background:rgba(139,92,246,.12); } .orb.c { background:rgba(192,132,252,.09); }
    body::before { background-image:radial-gradient(rgba(24,34,58,.10) 1px, transparent 1.4px); }
  }
  @media (prefers-reduced-motion: reduce) { .orb { animation:none; } }

  .wrap { max-width:1080px; margin:0 auto; padding:0 24px; }

  /* ── 顶栏 ─────────────────────────── */
  .topbar { position:sticky; top:0; z-index:10;
    background:color-mix(in srgb, var(--bg) 68%, transparent);
    backdrop-filter:blur(14px); -webkit-backdrop-filter:blur(14px);
    border-bottom:1px solid var(--border); }
  .bar-inner { display:flex; align-items:center; justify-content:space-between; height:60px; }
  .brand { display:flex; align-items:center; gap:10px; font-weight:750; font-size:15.5px; letter-spacing:-.01em; color:var(--text); text-decoration:none; }
  .logo { display:grid; place-items:center; width:31px; height:31px; border-radius:9px; font-size:16px;
    background:var(--grad); box-shadow:0 4px 16px rgba(99,102,241,.4); }
  .brand em { font-style:normal; color:var(--muted); font-weight:500; }
  .bar-links { display:flex; gap:4px; }
  .bar-links a { color:var(--muted); text-decoration:none; font-size:13.5px; font-weight:500; padding:7px 13px; border-radius:9px; transition:.15s; }
  .bar-links a:hover { color:var(--text); background:rgba(148,163,190,.12); }

  /* ── Hero ─────────────────────────── */
  .hero { padding:98px 0 48px; }
  .eyebrow { display:inline-flex; align-items:center; gap:9px; font-size:11px; font-weight:700; letter-spacing:.16em;
    text-transform:uppercase; color:var(--muted); border:1px solid var(--border); border-radius:999px;
    padding:7px 15px; margin-bottom:24px; background:var(--card); backdrop-filter:blur(8px); }
  .pulse { width:7px; height:7px; flex:none; border-radius:50%; background:var(--good); animation:pulse 2.2s cubic-bezier(.4,0,.6,1) infinite; }
  @keyframes pulse { 0%{box-shadow:0 0 0 0 rgba(52,211,153,.45)} 70%{box-shadow:0 0 0 8px rgba(52,211,153,0)} 100%{box-shadow:0 0 0 0 rgba(52,211,153,0)} }
  .hero h1 { font-size:clamp(34px,5.4vw,54px); line-height:1.12; letter-spacing:-.035em; font-weight:800; margin-bottom:18px; }
  .hero .grad { background:var(--grad); -webkit-background-clip:text; background-clip:text; color:transparent; }
  .hero .lead { color:var(--muted); font-size:16.5px; max-width:600px; margin-bottom:28px; }
  .hero .lead a { color:var(--accent); text-decoration:none; }
  .hero .lead a:hover { text-decoration:underline; }
  .stats { display:flex; align-items:center; flex-wrap:wrap; gap:14px; font-size:13.5px; color:var(--muted); }
  .stats b { color:var(--text); font-weight:700; }
  .stats .sep { width:3px; height:3px; border-radius:50%; background:var(--faint); }
  .stats .live { display:inline-flex; align-items:center; gap:8px; }

  /* ── 工具栏：搜索 + 分类 ───────────── */
  .toolbar { position:sticky; top:60px; z-index:9; padding:18px 0 8px; }
  .search-row { display:flex; align-items:center; gap:14px; flex-wrap:wrap; }
  .search { position:relative; flex:1; min-width:240px; max-width:450px; }
  .search-box { display:flex; align-items:center; gap:10px; height:47px; padding:0 14px; border-radius:14px;
    border:1px solid var(--border); background:var(--card); backdrop-filter:blur(14px); -webkit-backdrop-filter:blur(14px);
    transition:border-color .18s, box-shadow .18s; }
  .search:focus-within .search-box { border-color:var(--accent); box-shadow:0 0 0 4px var(--ring); }
  .search svg { width:16px; height:16px; flex:none; stroke:var(--faint); }
  .search input { flex:1; min-width:0; border:0; background:transparent; outline:none;
    font:inherit; font-size:14px; color:var(--text); }
  .search input::placeholder { color:var(--faint); }
  .search kbd { font:600 10.5px/1 ui-monospace, Consolas, monospace; color:var(--faint);
    border:1px solid var(--border); border-radius:6px; padding:4px 7px; user-select:none; }
  @media (max-width:560px) { .search kbd { display:none; } }
  .count { margin-left:auto; font-size:12.5px; color:var(--faint); white-space:nowrap; }
  .chips { display:flex; flex-wrap:wrap; gap:9px; padding:16px 0 22px; }
  .chip-f { appearance:none; cursor:pointer; padding:6px 14px; border-radius:999px; border:1px solid var(--border);
    background:var(--card); color:var(--muted); font-size:13px; font-weight:500; backdrop-filter:blur(8px); transition:.16s; }
  .chip-f:hover { color:var(--text); border-color:var(--border-hi); }
  .chip-f.on { background:var(--text); color:var(--bg); border-color:transparent; font-weight:600; }

  /* ── 卡片网格 ─────────────────────── */
  .grid { display:grid; grid-template-columns:repeat(auto-fill, minmax(min(100%, 300px), 1fr)); gap:26px 22px; padding:20px 0 52px; }
  .card { position:relative; display:flex; flex-direction:column; gap:14px; padding:24px 26px; overflow:hidden;
    border-radius:var(--radius); border:1px solid var(--border);
    background:linear-gradient(var(--card), var(--card)) padding-box,
               linear-gradient(165deg, rgba(148,163,190,.30), rgba(148,163,190,.05) 42%) border-box;
    backdrop-filter:blur(14px); -webkit-backdrop-filter:blur(14px);
    transition:transform .22s cubic-bezier(.22,1,.36,1), border-color .22s, box-shadow .22s; }
  .card::after { content:""; position:absolute; inset:0; pointer-events:none; opacity:0; transition:opacity .25s;
    background:radial-gradient(480px circle at var(--mx,50%) var(--my,50%), rgba(90,162,255,.11), transparent 42%); }
  .card:hover { transform:translateY(-4px); border-color:var(--border-hi); box-shadow:var(--shadow); }
  .card:hover::after { opacity:1; }
  @media (prefers-reduced-motion: no-preference) {
    .card { animation:rise .6s cubic-bezier(.22,1,.36,1) both; animation-delay:var(--d,0s); }
  }
  @keyframes rise { from { opacity:0; transform:translateY(16px); } }
  .card-top { display:flex; gap:14px; align-items:flex-start; }
  .icon { width:48px; height:48px; flex:none; border-radius:13px; padding:8px;
    background:var(--card-hi); border:1px solid var(--border); object-fit:contain; }
  .tt h3 { font-size:16.5px; font-weight:680; letter-spacing:-.01em; line-height:1.35; display:flex; align-items:center; gap:8px; flex-wrap:wrap; }
  .ver { font:600 10.5px/1 ui-monospace, Consolas, monospace; color:var(--good); background:rgba(52,211,153,.13);
    padding:4px 8px; border-radius:999px; letter-spacing:.03em; }
  .pid { display:block; margin-top:3px; font:11px/1.4 ui-monospace, Consolas, monospace; color:var(--faint);
    word-break:break-all; user-select:all; }
  .desc { color:var(--muted); font-size:14px; display:-webkit-box; -webkit-line-clamp:3; -webkit-box-orient:vertical; overflow:hidden; }
  .card-foot { margin-top:auto; display:flex; align-items:center; justify-content:space-between; gap:10px; }
  .cat-chip { font-size:12px; font-weight:500; color:var(--muted); background:rgba(148,163,190,.11);
    padding:4px 11px; border-radius:999px; white-space:nowrap; }
  .acts { display:flex; gap:8px; }
  .copy { display:inline-flex; align-items:center; justify-content:center; width:37px; height:37px; border-radius:11px;
    border:1px solid var(--border); background:transparent; color:var(--muted); cursor:pointer; transition:.15s; }
  .copy:hover { color:var(--accent); border-color:var(--accent); }
  .copy.done { color:var(--good); border-color:var(--good); }
  .copy svg { width:15px; height:15px; }
  .dl { display:inline-flex; align-items:center; gap:7px; padding:9px 18px; border-radius:12px; text-decoration:none;
    background:var(--grad); color:#fff; font-size:13.5px; font-weight:600; letter-spacing:.01em;
    box-shadow:0 6px 18px -4px rgba(99,102,241,.45); transition:.16s; }
  .dl:hover { filter:brightness(1.12); box-shadow:0 8px 24px -4px rgba(99,102,241,.55); }
  .dl svg { width:14px; height:14px; transition:transform .18s; }
  .dl:hover svg { transform:translateY(2px); }

  .empty { display:none; text-align:center; color:var(--faint); padding:56px 0 72px; }
  .empty .big { font-size:40px; margin-bottom:10px; }

  /* ── 底部说明 ─────────────────────── */
  .howto { margin-bottom:56px; border:1px solid var(--border); border-radius:var(--radius);
    background:var(--card); backdrop-filter:blur(14px); padding:28px 32px; }
  .howto h2 { font-size:15px; font-weight:700; margin-bottom:12px; }
  .howto ol { margin:0 0 12px 20px; color:var(--muted); font-size:13.5px; }
  .howto ol li + li { margin-top:5px; }
  .howto ol b { color:var(--text); }
  .howto .fine { color:var(--faint); font-size:12.5px; }
  .howto a { color:var(--accent); text-decoration:none; }
  .howto a:hover { text-decoration:underline; }
  footer.site { border-top:1px solid var(--border); padding:24px 0 36px; color:var(--faint); font-size:12.5px;
    display:flex; justify-content:space-between; gap:12px; flex-wrap:wrap; }
  footer.site a { color:var(--muted); text-decoration:none; }
  footer.site a:hover { color:var(--accent); }

  .toast { position:fixed; left:50%; bottom:32px; transform:translateX(-50%) translateY(16px);
    background:var(--card-hi); border:1px solid var(--border-hi); color:var(--text); padding:10px 20px;
    border-radius:12px; font-size:13.5px; opacity:0; transition:.22s; pointer-events:none; z-index:99;
    box-shadow:var(--shadow); }
  .toast.show { opacity:1; transform:translateX(-50%) translateY(0); }

  /* 描述全文悬浮提示 */
  .tip { position:fixed; z-index:50; max-width:min(380px, 88vw); padding:11px 15px; border-radius:13px;
    background:rgba(18,24,38,.96); border:1px solid rgba(148,163,190,.28); color:#eef2fa;
    font-size:13px; line-height:1.65; box-shadow:0 14px 34px -10px rgba(0,0,0,.5);
    opacity:0; translate:0 5px; transition:opacity .16s ease, translate .16s ease;
    pointer-events:none; backdrop-filter:blur(8px); }
  .tip.show { opacity:1; translate:0 0; }

  :focus-visible { outline:2px solid var(--accent); outline-offset:2px; }
</style>
</head>
<body>
<div class="bg-fx" aria-hidden="true"><i class="orb a"></i><i class="orb b"></i><i class="orb c"></i></div>

<header class="topbar">
  <div class="wrap bar-inner">
    <a class="brand" href="#"><span class="logo">🧩</span>QuickDock <em>插件中心</em></a>
    <nav class="bar-links">
      <a href="https://github.com/parieses/quickdock" target="_blank" rel="noopener">主程序</a>
      <a href="__REPO_URL__" target="_blank" rel="noopener">GitHub</a>
      <a href="__REL_PAGE__" target="_blank" rel="noopener">Releases</a>
    </nav>
  </div>
</header>

<main class="wrap">
  <section class="hero">
    <p class="eyebrow"><span class="pulse"></span>QuickDock Plugins · 官方插件市场</p>
    <h1>让 QuickDock<br><span class="grad">长出新的能力</span></h1>
    <p class="lead">官方内置之外的社区插件，下载 zip 后在「插件管理」里一键安装，升级自动备份旧版本。适配 <a href="https://github.com/parieses/quickdock" target="_blank" rel="noopener">QuickDock</a> 主程序。</p>
    <div class="stats">
      <span><b>__PLUGIN_COUNT__</b> 个插件</span><span class="sep"></span>
      <span><b>__CAT_COUNT__</b> 个分类</span><span class="sep"></span>
      <span>Windows 就绪</span><span class="sep"></span>
      <span class="live"><span class="pulse"></span>应用内一键更新</span>
    </div>
  </section>

  <section class="toolbar">
    <div class="search-row">
      <div class="search">
        <div class="search-box">
          <svg viewBox="0 0 24 24" fill="none" stroke-width="2" stroke-linecap="round"><circle cx="11" cy="11" r="7"/><path d="m20 20-3.5-3.5"/></svg>
          <input id="q" type="text" placeholder="搜索插件名称、ID 或功能…" autocomplete="off">
          <kbd>/</kbd>
        </div>
      </div>
      <span class="count" id="count"></span>
    </div>
    <div class="chips" id="chips">
      <button class="chip-f on" data-cat="*">全部</button>__CHIPS__
    </div>
  </section>

  <section class="grid" id="grid">
__CARDS__
  </section>

  <div class="empty" id="empty" style="display:none">
    <div class="big">🔍</div>
    没有找到匹配的插件 —— 换个关键词或清除筛选试试
  </div>

  <section class="howto">
    <h2>📦 如何安装</h2>
    <ol>
      <li>点击卡片上的 <b>Windows</b> 按钮下载 zip 安装包；</li>
      <li>打开 QuickDock → <b>插件管理</b> → <b>从文件安装</b>，选择下载的 zip（也可以直接把 zip 拖进插件管理页）；</li>
      <li>完成！之后可在应用内「插件市场」一键升级。</li>
    </ol>
    <p class="fine">下载 404？对应平台可能还没有 Release 资产，请到 <a href="__REL_PAGE__" target="_blank" rel="noopener">Releases</a> 确认。更多说明见<a href="__REPO_URL__" target="_blank" rel="noopener">仓库主页</a>。</p>
  </section>

  <footer class="site">
    <span>QuickDock Plugins · 本页由 gen_site.py 自动生成</span>
    <span><a href="__REPO_URL__" target="_blank" rel="noopener">github.com/__REPO_PATH__</a></span>
  </footer>
</main>

<div class="toast" id="toast"></div>
<div class="tip" id="tip" role="tooltip"></div>
<script>
(function () {
  var toastTimer;
  function showToast(msg) {
    var t = document.getElementById('toast');
    t.textContent = msg;
    t.classList.add('show');
    clearTimeout(toastTimer);
    toastTimer = setTimeout(function () { t.classList.remove('show'); }, 1600);
  }
  function fallbackCopy(text) {
    var ta = document.createElement('textarea');
    ta.value = text; ta.style.position = 'fixed'; ta.style.opacity = '0';
    document.body.appendChild(ta); ta.select();
    try { document.execCommand('copy'); showToast('链接已复制'); }
    catch (e) { showToast('复制失败，请手动复制'); }
    document.body.removeChild(ta);
  }
  window.copyLink = function (btn) {
    var url = btn.dataset.link;
    var done = function () {
      showToast('链接已复制');
      btn.classList.add('done');
      setTimeout(function () { btn.classList.remove('done'); }, 1200);
    };
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(url).then(done, function () { fallbackCopy(url); });
    } else { fallbackCopy(url); }
  };

  // 搜索 + 分类筛选
  var q = document.getElementById('q');
  var cards = [].slice.call(document.querySelectorAll('.card'));
  var chips = [].slice.call(document.querySelectorAll('.chip-f'));
  var empty = document.getElementById('empty');
  var count = document.getElementById('count');
  var activeCat = '*';
  function apply() {
    var kw = (q.value || '').trim().toLowerCase(), shown = 0;
    cards.forEach(function (c) {
      var ok = (activeCat === '*' || c.dataset.cat === activeCat) &&
               (!kw || c.dataset.hay.indexOf(kw) > -1);
      c.style.display = ok ? '' : 'none';
      if (ok) shown++;
    });
    count.textContent = shown + ' / ' + cards.length + ' 个插件';
    empty.style.display = shown ? 'none' : 'block';
  }
  chips.forEach(function (ch) {
    ch.addEventListener('click', function () {
      chips.forEach(function (x) { x.classList.remove('on'); });
      ch.classList.add('on');
      activeCat = ch.dataset.cat;
      apply();
    });
  });
  q.addEventListener('input', apply);

  // 卡片聚光灯：光晕跟随鼠标
  document.getElementById('grid').addEventListener('pointermove', function (e) {
    var c = e.target.closest ? e.target.closest('.card') : null;
    if (!c) return;
    var r = c.getBoundingClientRect();
    c.style.setProperty('--mx', (e.clientX - r.left) + 'px');
    c.style.setProperty('--my', (e.clientY - r.top) + 'px');
  });

  // 描述截断 → 悬浮显示全文（触屏设备禁用；滚动收起）
  var tip = document.getElementById('tip');
  function hideTip() { tip.classList.remove('show'); }
  if (!window.matchMedia || !window.matchMedia('(hover: none)').matches) {
    var gridEl = document.getElementById('grid');
    gridEl.addEventListener('pointerover', function (e) {
      var d = e.target.closest ? e.target.closest('.desc') : null;
      if (!d || !d.dataset.full) return;
      if (d.scrollHeight - d.clientHeight < 2) return; // 描述没被截断就不弹
      tip.textContent = d.dataset.full;
      var r = d.getBoundingClientRect();
      tip.style.left = '0px'; tip.style.top = '0px';
      var tw = Math.min(380, window.innerWidth * 0.88);
      var th = tip.offsetHeight;
      var x = Math.max(10, Math.min(r.left, window.innerWidth - tw - 10));
      var y = (r.top - th - 10 < 64) ? r.bottom + 10 : r.top - th - 10;
      tip.style.left = x + 'px';
      tip.style.top = y + 'px';
      tip.classList.add('show');
    });
    gridEl.addEventListener('pointerout', function (e) {
      if (e.target.closest && e.target.closest('.desc')) hideTip();
    });
    window.addEventListener('scroll', hideTip, { passive: true });
  }

  document.addEventListener('keydown', function (e) {
    if (e.key === '/' && !/INPUT|TEXTAREA/.test(document.activeElement.tagName)) {
      e.preventDefault(); q.focus();
    }
    if (e.key === 'Escape' && document.activeElement === q) { q.value = ''; apply(); q.blur(); }
  });
  apply();
})();
</script>
</body>
</html>
"""

CARD_TEMPLATE = """    <article class="card" data-cat="@CAT@" data-hay="@HAY@" style="--d:@DELAY@s">
      <div class="card-top">
        <img class="icon" src="@ICON@" alt="" loading="lazy">
        <div class="tt">
          <h3>@NAME@<span class="ver">v@VERSION@</span></h3>
          <code class="pid">@PLUGIN_ID@</code>
        </div>
      </div>
      <p class="desc" data-full="@DESC@">@DESC@</p>
      <div class="card-foot">
        <span class="cat-chip">@CAT_ICON@ @CAT@</span>
        <div class="acts">@DOWNLOADS@</div>
      </div>
    </article>"""

DOWNLOAD_TEMPLATE = ('<a class="dl" href="@HREF@">'
                     '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" '
                     'stroke-linecap="round" stroke-linejoin="round"><path d="M12 3v12"/><path d="m6 11 6 6 6-6"/>'
                     '<path d="M5 21h14"/></svg>@LABEL@</a>'
                     '<button class="copy" data-link="@HREF@" onclick="copyLink(this)" title="复制下载链接" aria-label="复制下载链接">'
                     '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" '
                     'stroke-linejoin="round"><rect x="9" y="9" width="12" height="12" rx="2"/><path d="M5 15V5a2 2 0 0 1 2-2h10"/></svg>'
                     '</button>')


def collect_plugins(repo_root):
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


def build_cards(plugins):
    # 分类 chips（按插件数降序）
    cats = {}
    for p in plugins:
        cat = p["manifest"].get("category") or "未分类"
        cats[cat] = cats.get(cat, 0) + 1
    chip_html = "\n".join(
        f'      <button class="chip-f" data-cat="{h(c)}">{cat_icon(c)} {h(c)} ({n})</button>'
        for c, n in sorted(cats.items(), key=lambda kv: (-kv[1], kv[0]))
    )

    cards = []
    for i, p in enumerate(plugins):
        mf, pdir = p["manifest"], p["dir"]
        safe_id = mf["id"].replace(".", "-").lower()
        name = mf.get("name", mf["id"])
        desc = mf.get("description", "—")
        cat = mf.get("category") or "未分类"

        # 图标：plugin.json 的 icon 字段 → 仓库 raw 链接
        icon = mf.get("icon", "")
        icon_url = f"{RAW_BASE}/{urllib.parse.quote(pdir)}/{urllib.parse.quote(icon)}" if icon else ICON_FALLBACK

        downloads = []
        for plat, label in PLATFORMS:
            href = f"{REL_BASE}/{safe_id}-{plat}.zip"
            downloads.append(DOWNLOAD_TEMPLATE.replace("@HREF@", href).replace("@LABEL@", label))
        downloads_html = "".join(downloads)

        hay = h(f"{name} {mf['id']} {desc}".lower(), quote=True)
        card = (CARD_TEMPLATE
                .replace("@DELAY@", str(round(i * 0.07, 2)))
                .replace("@CAT_ICON@", cat_icon(cat))
                .replace("@CAT@", h(cat))
                .replace("@HAY@", hay)
                .replace("@ICON@", h(icon_url))
                .replace("@NAME@", h(name))
                .replace("@VERSION@", h(mf.get("version", "0.0.0")))
                .replace("@PLUGIN_ID@", h(mf["id"]))
                .replace("@DESC@", h(desc))
                .replace("@DOWNLOADS@", downloads_html))
        cards.append(card)

    html = TEMPLATE
    html = html.replace("__FAVICON__", FAVICON.replace("&", "&amp;"))
    html = html.replace("__REPO_URL__", REPO_URL)
    html = html.replace("__REPO_PATH__", f"{REPO_OWNER}/{REPO_NAME}")
    html = html.replace("__REL_BASE__", REL_BASE)
    html = html.replace("__REL_PAGE__", REL_PAGE)
    html = html.replace("__PLUGIN_COUNT__", str(len(plugins)))
    html = html.replace("__CAT_COUNT__", str(len(cats)))
    html = html.replace("__CHIPS__", chip_html)
    html = html.replace("__CARDS__", "\n".join(cards))
    return html


def build_index(plugins):
    """构建机器可读的插件市场索引（供 QuickDock 应用内"在线安装"拉取）。

    字段对齐 plugin.json，并补充每个平台的下载直链（releases/latest/download）。
    downloads 只包含 PLATFORMS 里声明的平台（主程序目前仅 Windows）。
    """
    items = []
    for p in plugins:
        mf, pdir = p["manifest"], p["dir"]
        safe_id = mf["id"].replace(".", "-").lower()

        # 图标：raw 链接（与主页一致）
        icon = mf.get("icon", "")
        icon_url = f"{RAW_BASE}/{urllib.parse.quote(pdir)}/{urllib.parse.quote(icon)}" if icon else ""

        # 下载直链：仅 PLATFORMS 声明的平台
        downloads = {}
        for plat, _ in PLATFORMS:
            downloads[plat] = f"{REL_BASE}/{safe_id}-{plat}.zip"

        items.append({
            "id": mf["id"],
            "name": mf.get("name", mf["id"]),
            "name_i18n": mf.get("name_i18n"),
            "version": mf.get("version", "0.0.0"),
            "description": mf.get("description", ""),
            "description_i18n": mf.get("description_i18n"),
            "author": mf.get("author", ""),
            "category": mf.get("category", ""),
            "icon": icon_url,
            "platforms": mf.get("platforms", []),
            "permissions": mf.get("permissions", {}),
            "capabilities": mf.get("capabilities", []),
            "downloads": downloads,
        })

    return {
        "name": "QuickDock 插件中心",
        "updated": datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
        "plugins": items,
    }


def main():
    parser = argparse.ArgumentParser(description="QuickDock 插件中心主页生成器")
    parser.add_argument("-o", "--output", default="site/index.html", help="输出路径（默认 site/index.html）")
    parser.add_argument("-j", "--json-output", default="site/index.json", help="市场索引 JSON 输出路径（默认 site/index.json，供应用内在线安装拉取）")
    args = parser.parse_args()

    repo_root = Path(__file__).parent.resolve()
    plugins = collect_plugins(repo_root)
    if not plugins:
        print("❌ 未找到任何插件目录（缺少 plugin.json）")
        sys.exit(1)

    print(f"📋 发现 {len(plugins)} 个插件:")
    for p in plugins:
        print(f"   • {p['manifest'].get('name', p['dir'])} ({p['manifest']['id']})")

    out = Path(args.output).resolve()
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(build_cards(plugins), encoding="utf-8")
    print(f"✅ 主页已生成: {out}")

    # 机器可读的市场索引（QuickDock 应用内"在线安装"拉取）
    index_data = build_index(plugins)
    index_out = Path(args.json_output).resolve()
    index_out.parent.mkdir(parents=True, exist_ok=True)
    index_out.write_text(json.dumps(index_data, ensure_ascii=False, indent=2), encoding="utf-8")
    print(f"✅ 市场索引已生成: {index_out}")


if __name__ == "__main__":
    main()
