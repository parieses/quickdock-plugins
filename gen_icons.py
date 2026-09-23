#!/usr/bin/env python3
"""QuickDock 外部插件图标规范 v2 —— 生成器 / 校验器

规范（「A 家族」唯一形态）：
  - viewBox 固定 `0 0 64 64`，根元素**不写** width/height（尺寸由宿主 CSS 决定）
  - 满幅背景圆角贴片 `<rect width="64" height="64" rx="14" fill="语义色"/>`
  - 图形一律白色，落在安全区 12..52（40x40 居中）
  - 线宽归一：图形内最粗的一笔在 64 网格上恒为 3
  - 白色图形只允许三档不透明度：主 1 / 次 .55 / 衬 .3

两套主题：
  icon.svg        深色主题图标（同时是所有消费方的默认回退）
  icon.light.svg  浅色主题图标
宿主按当前主题取用，缺失则回退 icon.svg（老插件只带一个文件也能正常工作）。

用法：
  python gen_icons.py --gen hash-calc wifi-manager   # 重新生成指定插件
  python gen_icons.py --check                        # 校验全部插件是否符合规范
  python gen_icons.py --gen --all                    # 全量重生成
"""
import argparse
import json
import math
import pathlib
import re
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parent
CHROME = "C:/Program Files/Google/Chrome/Application/chrome.exe"
NODE = "C:/Users/16956/.workbuddy/binaries/node/versions/22.12.0/node.exe"

# ---------------------------------------------------------------- 几何常量
GRID = 64
RADIUS = 14
SAFE = 12.0                       # 安全区内边距 -> 图形可用区 40x40
ART = GRID - 2 * SAFE
BASE_STROKE = 3.0                 # 最粗一笔在 64 网格上的线宽
GEOM_TOL = 0.005                  # 重复生成时的几何吸附容差（≤0.5% 视为已对齐，只换色）
PLATE_MIN = 0.70                  # 卡底贴片：两轴至少占 viewBox 的 70%
PLATE_INSET = 0.16                # 卡底贴片必须基本居中内缩（x/y 不超过 16%）
WHITE_MAX = 45.0                  # 渲染后白像素占比上限（%）—— 超了就是白块盖贴片
PATCH_MIN = 25.0                  # 渲染后贴片色像素占比下限（%）
OPACITY_TIERS = {"main": 1.0, "sub": 0.55, "faint": 0.3}

# ---------------------------------------------------------------- 语义色板
# 6 个语义色 × 深/浅两档。两档均由 OKLCH 求解，硬约束：
#   白色图形对贴片底 >= 3.0:1  且  贴片对各自主题背景 >= 3.0:1
# 深色档目标相对亮度 .235，浅色档 .170；chroma 深 .135 / 浅 .155。
PALETTE = {
    "blue":   {"dark": "#3381ff", "light": "#1d66ff", "name": "通用工具 / 转换 / 文档"},
    "violet": {"dark": "#916aff", "light": "#7a50ff", "name": "开发与代码"},
    "teal":   {"dark": "#1c958a", "light": "#148077", "name": "网络与连通"},
    "amber":  {"dark": "#d46615", "light": "#ba560e", "name": "安全与探测"},
    "green":  {"dark": "#24993e", "light": "#10852b", "name": "数据与系统"},
    "pink":   {"dark": "#ea4683", "light": "#d32f6c", "name": "媒体与图像"},
}
BG = {"dark": (0.086, 0.094, 0.114), "light": (0.953, 0.945, 0.925)}

# ---------------------------------------------------------------- 插件 -> 语义组
GROUP = {}
for _g, _names in {
    "blue": """calcsheet compare color-converter unit-converter time-converter rmb-upper
               batch-rename text-encoder type-trainer hanzi-copybook minesweeper emoji-search
               qrcode markdown-preview md-table-converter pdf-toolkit mindmap""".split(),
    "violet": """code-card formatter json-toolbox regex-extractor git-workbench
                 cron-explainer package-check""".split(),
    "teal": """api-mock api-loadtest http-client ws-tester port-scanner speed-test netdiag
               mail-check wifi-manager hosts-manager curl-converter""".split(),
    "amber": """crypto-toolbox hash-calc jwt-decoder login-tester dir-buster
                subdomain-enum site-audit""".split(),
    "green": "database data-generator disk-analyzer junk-cleaner".split(),
    "pink": "image-studio ocr-tool exif-viewer image-uploader".split(),
}.items():
    for _n in _names:
        GROUP[_n] = _g


# ================================================================ 颜色工具
def _srgb_from_lin(c):
    return c if c > 0.0031308 else 12.92 * c


def _lin_from_srgb(c):
    return c / 12.92 if c <= 0.04045 else ((c + 0.055) / 1.055) ** 2.4


def hex_to_srgb(h):
    h = h.lstrip("#")
    return tuple(int(h[i:i + 2], 16) / 255 for i in (0, 2, 4))


def hex_to_lum(h):
    r, g, b = (_lin_from_srgb(c) for c in hex_to_srgb(h))
    return 0.2126 * r + 0.7152 * g + 0.0722 * b


def contrast(l1, l2):
    a, b = max(l1, l2), min(l1, l2)
    return (a + 0.05) / (b + 0.05)


# ================================================================ SVG 处理
SVG_OPEN = re.compile(r"<svg\b[^>]*>", re.S)
COLOR_ATTR = re.compile(r'(fill|stroke|stop-color)\s*=\s*"([^"]*)"')
STROKE_W = re.compile(r'stroke-width\s*=\s*"([^"]*)"')
OPACITY = re.compile(r'\sopacity\s*=\s*"([^"]*)"')
BG_RECT = re.compile(r'\s*<rect\b[^>]*>', re.S)
DEFS = re.compile(r"\s*<defs\b[\s\S]*?</defs>", re.S)


def read_manifest(name):
    p = ROOT / name / "plugin.json"
    if not p.exists():
        return None
    try:
        return json.loads(p.read_text(encoding="utf-8"))
    except Exception:
        return None


def source_icon_path(name):
    """取用于生成两套新图标的源文件：优先 icon.svg，其次任何 .svg。"""
    d = ROOT / name
    for cand in ("icon.svg", "icon.light.svg"):
        if (d / cand).exists():
            return d / cand
    svgs = sorted(d.glob("*.svg"))
    return svgs[0] if svgs else None


def parse_svg(text):
    m = SVG_OPEN.search(text)
    if not m:
        raise ValueError("no <svg>")
    tag = m.group(0)
    vb = re.search(r'viewBox\s*=\s*"([^"]+)"', tag)
    if not vb:
        raise ValueError("no viewBox")
    box = [float(x) for x in re.split(r"[,\s]+", vb.group(1).strip())]
    inner = text[m.end():text.rindex("</svg>")]
    return box, inner


def strip_background(inner, box):
    """删掉「整块底色贴片」矩形。

    判据（三条全中才删）：
      1. 实心填充 —— fill 是具体颜色或渐变引用；fill="none"/缺省的不算
         （这一条本身就排除了所有线稿画框，所以不需要再按描边区分）
      2. 两轴都 >= PLATE_MIN 倍 viewBox
      3. 基本居中内缩（x/y <= PLATE_INSET 倍）
    删完必须还剩别的图形节点，否则说明这 rect 就是主体，回滚。

    踩过的三个坑（阈值 0.9 + 只认字面色 + 排除渐变时都会漏）：
      · x=6 w=52（81%）的内缩卡底漏掉 -> recolor_white 变白块盖住贴片
      · fill="url(#g)" 的渐变卡底漏掉 -> 渐变 stop 被转白，整片变白
      · 带描边的双层卡底（http-client）漏掉 -> 同样变白块
    """
    W, H = box[2], box[3]
    removed = [False]

    def repl(m):
        tag = m.group(0)
        a = dict(re.findall(r'([a-zA-Z-]+)\s*=\s*"([^"]*)"', tag))
        try:
            ww, hh = float(a["width"]), float(a["height"])
        except (KeyError, ValueError):
            return tag
        fill = a.get("fill", "").strip()
        if fill in ("", "none"):
            return tag
        if ww < W * PLATE_MIN or hh < H * PLATE_MIN:
            return tag
        try:
            xx, yy = float(a.get("x", 0) or 0), float(a.get("y", 0) or 0)
        except ValueError:
            return tag
        if xx > W * PLATE_INSET or yy > H * PLATE_INSET:
            return tag
        removed[0] = True
        return ""

    out = BG_RECT.sub(repl, inner)
    if removed[0]:
        if not re.search(r'<(path|circle|ellipse|line|polyline|polygon|rect|g|text)\b', out):
            return inner                     # 剥完没图形了 -> 刚才是主体，回滚
        # 背景没了，渐变等 defs 往往只剩空壳；只在确实无人引用时删
        ids = re.findall(r'id\s*=\s*"([^"]+)"', out)
        if not any(f"url(#{i})" in out for i in ids):
            out = DEFS.sub("", out)
    return out


def recolor_white(inner):
    """图形一律白色：所有颜色值换成 #fff，保留 fill="none" 与 url(#..) 引用。"""
    def repl(m):
        attr, val = m.group(1), m.group(2).strip()
        if val in ("none", "currentColor", "") or val.startswith("url("):
            return f'{attr}="#fff"' if val == "currentColor" else m.group(0)
        return f'{attr}="#fff"'
    return COLOR_ATTR.sub(repl, inner)


def quantize_opacity(inner):
    """不透明度收敛到三档：>=.9 主 / .45-.9 次 / <.45 衬。"""
    def repl(m):
        v = float(m.group(1))
        if v >= 0.9:
            return ""                       # 主档：直接省掉属性
        tier = OPACITY_TIERS["sub"] if v >= 0.45 else OPACITY_TIERS["faint"]
        return f' opacity="{tier}"'
    return OPACITY.sub(repl, inner)


def measure_bboxes(items):
    """用 Chrome 量图形真实包围盒（含描边外扩），返回 {name: (x0,y0,w,h,max_stroke)}。"""
    payload = []
    for name, svg in items:
        payload.append({"name": name, "svg": svg})
    script = """
const {chromium} = require('playwright-core');
const fs = require('fs');
const data = JSON.parse(fs.readFileSync(process.argv[2], 'utf8'));
(async () => {
  const b = await chromium.launch({executablePath: process.env.QD_CHROME});
  const p = await b.newPage();
  const out = {};
  for (const it of data) {
    await p.setContent(`<div id=w>${it.svg}</div>`);
    const r = await p.evaluate(() => {
      const art = document.querySelector('#w #art');
      if (!art) return null;
      let maxSw = 0;
      for (const el of art.querySelectorAll('*')) {
        const cs = getComputedStyle(el);
        if (cs.stroke !== 'none') maxSw = Math.max(maxSw, parseFloat(cs.strokeWidth) || 0);
      }
      // #art 自身无变换 -> getBBox 结果确定地落在 viewBox 坐标系，且已计入源变换
      const bb = art.getBBox();
      if (!bb || (bb.width === 0 && bb.height === 0)) return null;
      return {x0: bb.x, y0: bb.y, w: bb.width, h: bb.height, maxSw};  // 未含描边外扩，由 Python 侧补
    });
    out[it.name] = r;
  }
  console.log(JSON.stringify(out));
  await b.close();
})();
"""
    tmp = ROOT / "_pw" / "icon-audit"
    tmp.mkdir(parents=True, exist_ok=True)
    js = tmp / "_bbox.js"
    js.write_text(script, encoding="utf-8")
    # payload 走文件：50 个插件的 SVG 拼成 argv 会撞 Windows 32k 命令行上限
    pay = tmp / "_bbox-payload.json"
    pay.write_text(json.dumps(payload, ensure_ascii=False), encoding="utf-8")
    env = dict(__import__("os").environ)
    env["QD_CHROME"] = CHROME
    env["NODE_PATH"] = str(ROOT / "node_modules")
    res = subprocess.run([NODE, str(js), str(pay.resolve())], capture_output=True,
                         text=True, env=env, cwd=str(ROOT))
    if res.returncode != 0:
        raise RuntimeError(f"bbox 测量失败: {res.stderr[:800]}")
    return json.loads(res.stdout)


def build_icon(source_svg, color, bbox):
    """把源图标重构成规范形态。bbox 是「图形（已含源变换）、不含背景」的渲染包围盒。"""
    box, inner0, (t0x, t0y, s0) = prepare(source_svg)
    already_v2 = (t0x, t0y, s0) != (0.0, 0.0, 1.0)

    x0, y0, w, h, max_sw_spec = bbox["x0"], bbox["y0"], bbox["w"], bbox["h"], bbox["maxSw"]
    # 描边外扩：bbox 在 viewBox 坐标系，线宽要按「渲染后」算（指定值 × 源变换 s0）
    pad = max_sw_spec * s0 / 2
    x0, y0, w, h = x0 - pad, y0 - pad, w + pad * 2, h + pad * 2
    w = max(w, 1e-6)
    h = max(h, 1e-6)
    s = ART / max(w, h)
    tx = SAFE + (ART - w * s) / 2 - x0 * s
    ty = SAFE + (ART - h * s) / 2 - y0 * s

    if already_v2 and abs(s - 1) <= GEOM_TOL:
        # 几何已在容差内：只换色，不重新拟合，保证重复生成逐字节一致
        return _render(color, t0x, t0y, s0, inner0)

    # 线宽归一：让「最粗的一笔」在 64 网格上恰为 BASE_STROKE。
    # 渲染后线宽 = 指定值 * 源变换 s0 * 本次变换 s，故补一个系数 k 把它拉回 3。
    if max_sw_spec > 0:
        k = BASE_STROKE / (max_sw_spec * s0 * s)
        inner0 = STROKE_W.sub(lambda m: f'stroke-width="{_fmt(float(m.group(1)) * k)}"', inner0)

    # 与源变换复合，避免每次重生成都多套一层 <g>
    return _render(color, tx + s * t0x, ty + s * t0y, s * s0, inner0)


def _render(color, fx, fy, fs, inner):
    return (
        '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64">\n'
        f'  <rect width="64" height="64" rx="{RADIUS}" fill="{color}"/>\n'
        f'  <g transform="translate({_fmt(fx)},{_fmt(fy)}) scale({_fmt(fs)})">\n'
        f'{_reindent(inner)}\n'
        '  </g>\n'
        '</svg>\n'
    )


V2_HEAD = re.compile(
    r'<rect\b[^>]*width="64"[^>]*height="64"[^>]*rx="14"[^>]*/>\s*'
    r'<g\s+transform="translate\(([-\d.]+),\s*([-\d.]+)\)\s*scale\(([-\d.]+)\)">'
)


def split_source(text):
    """源 svg -> (viewBox, 图形 inner, (t0x,t0y,s0))。已是规范形态时把外层 <g> 拆出来，
    使生成过程幂等（重跑不会层层套 <g>）。"""
    box, inner = parse_svg(text)
    m = V2_HEAD.search(inner)
    if m:
        t0 = (float(m.group(1)), float(m.group(2)), float(m.group(3)))
        rest = inner[m.end():]
        idx = rest.rfind("</g>")
        return box, (rest[:idx] if idx >= 0 else rest), t0
    return box, inner, (0.0, 0.0, 1.0)


ROOT_PRESENT = ("stroke", "fill", "stroke-width", "stroke-linecap", "stroke-linejoin",
                "stroke-miterlimit", "stroke-dasharray", "stroke-opacity",
                "fill-rule", "opacity")


def root_glyph_attrs(source_svg):
    """把根 <svg> 上的表现属性拾回来（parse_svg 只取 inner，会丢掉它们）。

    大量 v1 图标写成 `<svg fill="none" stroke="#4a9eff" stroke-width="2">` + 裸几何，
    属性全在根上。丢掉后几何的 fill 回退成默认黑 -> 整个字形变黑块。
    这里统一归一化：颜色 -> #fff（none 保留），透明度 -> 三档，线宽原值保留
    （随后由线宽归一化按 k 缩放）。
    """
    m = SVG_OPEN.search(source_svg)
    if not m:
        return ""
    tag = m.group(0)
    out = []
    for a in ROOT_PRESENT:
        mm = re.search(rf'\s{a}\s*=\s*"([^"]*)"', tag)
        if not mm:
            continue
        v = mm.group(1).strip()
        if a in ("stroke", "fill"):
            if v == "none":
                val = "none"
            elif v.startswith("url("):
                continue
            else:
                val = "#fff"
        elif a == "opacity":
            try:
                f = float(v)
            except ValueError:
                continue
            if f >= 0.9:
                continue
            val = _fmt(OPACITY_TIERS["sub"] if f >= 0.45 else OPACITY_TIERS["faint"])
        else:
            val = v
        out.append(f'{a}="{val}"')
    return " ".join(out)


def prepare(source_svg):
    """源 svg -> (viewBox, 图形 inner, 源变换)。v1 源需要剥背景/转白/收敛透明度/接回根属性。"""
    box, inner, t0 = split_source(source_svg)
    already_v2 = t0 != (0.0, 0.0, 1.0)
    if not already_v2:
        inner = strip_background(inner, box)
        inner = recolor_white(inner)
        inner = quantize_opacity(inner)
        attrs = root_glyph_attrs(source_svg)
        if attrs:
            inner = f'<g {attrs}>\n{inner}\n</g>'
    return box, inner, t0


def measure_svg(source_svg):
    """构造只含图形（无背景贴片）的 svg 供 Chrome 量盒；额外包一层无变换的 <g id="art">，
    这样 getBBox 结果无歧义地落在 viewBox 坐标系（源变换已计入）。"""
    box, inner, (t0x, t0y, s0) = prepare(source_svg)
    vb = f"{_fmt(box[0])} {_fmt(box[1])} {_fmt(box[2])} {_fmt(box[3])}"
    return (f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="{vb}">'
            f'<g id="art"><g transform="translate({_fmt(t0x)},{_fmt(t0y)}) scale({_fmt(s0)})">'
            f'{inner}</g></g></svg>')


def _fmt(v):
    r = round(v, 4)
    return str(int(r)) if r == int(r) else f"{r:g}"


def _reindent(inner):
    lines = [l.strip() for l in inner.strip().splitlines() if l.strip()]
    return "\n".join("    " + l for l in lines)


# ================================================================ 命令
def cmd_gen(names):
    sources = {}
    for n in names:
        p = source_icon_path(n)
        if not p:
            print(f"  !! {n}: 找不到源 svg")
            continue
        sources[n] = p.read_text(encoding="utf-8")
    if not sources:
        return 1
    print(f"测量图形包围盒 ({len(sources)} 个)...")
    boxes = measure_bboxes([(n, measure_svg(s)) for n, s in sources.items()])
    ok = 0
    for n, src in sources.items():
        bb = boxes.get(n)
        if not bb:
            print(f"  !! {n}: 包围盒为空，跳过")
            continue
        g = GROUP.get(n)
        if not g:
            print(f"  !! {n}: 未在 GROUP 里登记语义色，跳过")
            continue
        d = ROOT / n
        for theme, fname in (("dark", "icon.svg"), ("light", "icon.light.svg")):
            (d / fname).write_text(build_icon(src, PALETTE[g][theme], bb), encoding="utf-8")
        print(f"  OK {n:20s} {g:7s} dark={PALETTE[g]['dark']} light={PALETTE[g]['light']}")
        ok += 1
    return 0 if ok == len(names) else 1


def cmd_check():
    """校验：规范形态 + 语义色正确 + 两套主题齐备。返回 (错误数, 警告数)。"""
    errs, warns = [], []
    palette = {v[t] for v in PALETTE.values() for t in ("dark", "light")}
    plugins = sorted(p.name for p in ROOT.iterdir()
                     if p.is_dir() and (p / "plugin.json").exists())
    for n in plugins:
        d = ROOT / n
        g = GROUP.get(n)
        if not g:
            warns.append(f"{n}: 未登记语义色（GROUP 中缺失）")
        for theme, fname in (("dark", "icon.svg"), ("light", "icon.light.svg")):
            f = d / fname
            if not f.exists():
                if fname == "icon.light.svg":
                    warns.append(f"{n}: 缺 {fname}（浅色主题将回退深色档）")
                else:
                    errs.append(f"{n}: 缺 {fname}")
                continue
            text = f.read_text(encoding="utf-8")
            tag = SVG_OPEN.search(text)
            if not tag:
                errs.append(f"{n}/{fname}: 无 <svg>")
                continue
            tag = tag.group(0)
            if 'viewBox="0 0 64 64"' not in tag:
                errs.append(f"{n}/{fname}: viewBox 不是 0 0 64 64")
            if re.search(r'\swidth\s*=', tag) or re.search(r'\sheight\s*=', tag):
                errs.append(f"{n}/{fname}: 根元素不应声明 width/height")
            if "##" in text:
                errs.append(f"{n}/{fname}: 存在非法颜色值 '##'")
            if "currentColor" in text:
                errs.append(f"{n}/{fname}: 不得使用 currentColor（<img> 下解析为黑色）")
            if "url(#" in text and "<defs" not in text:
                errs.append(f"{n}/{fname}: 引用了不存在的 defs")
            m = re.search(r'<rect\b[^>]*width="64"[^>]*height="64"[^>]*rx="14"[^>]*fill="(#[0-9a-fA-F]{6})"', text)
            if not m:
                errs.append(f"{n}/{fname}: 缺规范贴片 <rect width=64 height=64 rx=14 fill=..>")
            else:
                if m.group(1).lower() not in palette:
                    errs.append(f"{n}/{fname}: 贴片色 {m.group(1)} 不在语义色板内")
                if g and m.group(1).lower() != PALETTE[g][theme]:
                    errs.append(f"{n}/{fname}: 贴片色 {m.group(1)} != {g}/{theme} 应取的 {PALETTE[g][theme]}")
            # 颜色只扫图形部分：背景贴片本身就是语义色，不能一并当「非白色」报错
            m = V2_HEAD.search(text)
            body = text[m.end():] if m else text
            if m:
                cut = body.rfind("</g>")
                if cut >= 0:
                    body = body[:cut]
            for c in set(COLOR_ATTR.findall(body)):
                val = c[1].strip()
                if val in ("none", "#fff", "#ffffff") or val.startswith("url("):
                    continue
                errs.append(f"{n}/{fname}: 图形颜色 {val} 不是白色")
            for sw in set(STROKE_W.findall(text)):
                if abs(float(sw) - 1.0) > 1e-6 and float(sw) > 40:
                    errs.append(f"{n}/{fname}: stroke-width {sw} 异常")
            # 残留的「嵌套卡底」：art 里还有一块两轴都 >= 半幅的实心 rect，
            # 它经 recolor_white 变白后会整块盖住贴片（图形读不出来）。
            for r in re.finditer(r'<rect\b[^>]*/>', body):
                a = dict(re.findall(r'([a-zA-Z-]+)\s*=\s*"([^"]*)"', r.group(0)))
                try:
                    ww, hh = float(a["width"]), float(a["height"])
                except (KeyError, ValueError):
                    continue
                fl = a.get("fill", "").strip()
                if fl in ("", "none") or fl.startswith("url("):
                    continue
                if ww >= GRID * 0.5 and hh >= GRID * 0.5:
                    errs.append(f"{n}/{fname}: 疑似未剥离的卡底 rect {ww:g}x{hh:g} "
                                f"(fill={fl})，会盖住贴片")
    return errs, warns


RENDER_SCRIPT = r"""
const {chromium} = require('playwright-core');
const fs = require('fs');
const data = JSON.parse(fs.readFileSync(process.argv[2], 'utf8'));
(async () => {
  const S = 128;
  const b = await chromium.launch({executablePath: process.env.QD_CHROME});
  const p = await b.newPage();
  await p.goto('about:blank');
  const out = {};
  for (const it of data) {
    const r = await p.evaluate(async ({svg, svgm, pal, S}) => {
      const c = document.createElement('canvas');
      c.width = S; c.height = S;
      const g = c.getContext('2d', {willReadFrequently: true});
      const img = new Image();
      img.src = 'data:image/svg+xml;base64,' + btoa(unescape(encodeURIComponent(svg)));
      await img.decode();
      g.clearRect(0, 0, S, S);
      g.drawImage(img, 0, 0, S, S);
      const d = g.getImageData(0, 0, S, S).data;
      const set = new Set(pal);
      let white = 0, patch = 0;
      for (let i = 0; i < d.length; i += 4) {
        const a = d[i + 3];
        if (a < 200) continue;
        if (d[i] >= 246 && d[i+1] >= 246 && d[i+2] >= 246) white++;
        else if (set.has(d[i] + ',' + d[i+1] + ',' + d[i+2])) patch++;
      }
      const n = S * S;
      // 图形包围盒：getBBox() 返回的是「自身变换之前」的坐标，所以不能用图标本体量。
      // 改用与生成器同构的测量文档：外层 <g transform> 被提到 #art 内部，结果即 viewBox 坐标。
      const host = document.createElement('div');
      host.style.cssText = 'position:absolute;left:-9999px;top:0';
      host.innerHTML = svgm;
      document.body.appendChild(host);
      const art = host.querySelector('#art');
      const bb = art ? art.getBBox() : null;
      host.remove();
      return {white: white / n * 100, patch: patch / n * 100,
              bb: bb && bb.width > 0 ? [bb.x, bb.y, bb.width, bb.height] : null};
    }, {svg: it.svg, svgm: it.svgm, pal: it.pal, S});
    out[it.name] = r;
  }
  console.log(JSON.stringify(out));
  await b.close();
})();
"""


def cmd_check_render():
    """渲染级校验：纯静态规则看不出「白块盖住贴片」，这里按真实像素判。

    · 白像素占比过高 -> 图形被 recolor_white 变成大片白底（卡底没剥干净）
    · 贴片色像素占比过低 -> 贴片被白块盖住，用户在 36px 下看到一块白方
    · 图形包围盒越界 -> 缩放出错，图形溢出安全区
    """
    pal = [",".join(str(int(h[k:k + 2], 16)) for k in (1, 3, 5))
           for c in PALETTE.values() for h in (c["dark"], c["light"])]
    items = []
    for n in sorted(GROUP):
        for fname in ("icon.svg", "icon.light.svg"):
            f = ROOT / n / fname
            if f.exists():
                text = f.read_text(encoding="utf-8")
                items.append({"name": f"{n}/{fname}", "svg": text,
                              "svgm": measure_svg(text), "pal": pal})
    tmp = ROOT / "_pw" / "icon-audit"
    tmp.mkdir(parents=True, exist_ok=True)
    js = tmp / "_render-check.js"
    js.write_text(RENDER_SCRIPT, encoding="utf-8")
    pay = tmp / "_render-payload.json"
    pay.write_text(json.dumps(items, ensure_ascii=False), encoding="utf-8")
    env = dict(__import__("os").environ)
    env["QD_CHROME"] = CHROME
    env["NODE_PATH"] = str(ROOT / "node_modules")
    res = subprocess.run([NODE, str(js), str(pay.resolve())], capture_output=True,
                         text=True, env=env, cwd=str(ROOT))
    if res.returncode != 0:
        raise RuntimeError(f"渲染校验失败: {res.stderr[:800]}")
    stats = json.loads(res.stdout)

    errs = []
    for name, s in stats.items():
        if s["white"] > WHITE_MAX:
            errs.append(f"{name}: 白像素占比 {s['white']:.1f}% > {WHITE_MAX}%，"
                        f"疑似卡底未剥离（会盖住贴片）")
        if s["patch"] < PATCH_MIN:
            errs.append(f"{name}: 贴片色像素占比 {s['patch']:.1f}% < {PATCH_MIN}%，"
                        f"贴片基本被遮住")
        bb = s["bb"]
        if bb:
            x, y, w, h = bb
            if x < SAFE - 1.5 or y < SAFE - 1.5 or x + w > GRID - SAFE + 1.5 or y + h > GRID - SAFE + 1.5:
                errs.append(f"{name}: 图形包围盒 [{x:.1f},{y:.1f},{w:.1f},{h:.1f}] "
                            f"越出安全区 {SAFE:g}..{GRID - SAFE:g}")
    return errs


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--gen", nargs="*", default=None, metavar="PLUGIN")
    ap.add_argument("--all", action="store_true")
    ap.add_argument("--check", action="store_true")
    ap.add_argument("--check-render", action="store_true",
                    help="渲染级校验（需 Chrome），核白块占比与安全区")
    a = ap.parse_args()

    if a.gen is not None:
        names = sorted(GROUP) if a.all else a.gen
        return cmd_gen(names)
    if a.check or a.check_render:
        errs, warns = cmd_check()
        if a.check_render:
            errs = errs + cmd_check_render()
        for w in warns:
            print(f"WARN  {w}")
        for e in errs:
            print(f"ERROR {e}")
        print(f"\n{len(errs)} 错误 / {len(warns)} 警告")
        return 1 if errs else 0
    ap.print_help()
    return 0


if __name__ == "__main__":
    sys.exit(main())
