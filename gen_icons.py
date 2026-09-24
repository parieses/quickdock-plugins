#!/usr/bin/env python3
"""QuickDock 外部插件图标规范 v2 —— 生成器 / 校验器

规范（「A 家族」唯一形态）：
  - viewBox 固定 `0 0 64 64`，根元素**不写** width/height（尺寸由宿主 CSS 决定）
  - 满幅背景圆角贴片 `<rect width="64" height="64" rx="14" fill="语义色"/>`
  - 图形主色：深色档白 / 浅色档品牌色，落在安全区 12..52（40x40 居中）
  - 线宽归一：图形内最粗的一笔在 64 网格上恒为 3
  - 白色图形只允许三档不透明度：主 1 / 次 .55 / 衬 .3

两套主题（几何 / 网格 / 安全区 / 线宽 100% 一致，只差色彩与贴片处理）：
  icon.svg        深色主题图标：饱和同色系渐变贴片 + 白色字形（在暗色外壳上最跳）
  icon.light.svg  浅色主题图标：白底贴片 + 品牌色描边圈 + 品牌色字形（在亮色外壳上清晰）
                    字形主色 = 该组「深锚点」（在白底上对比度 ~3.7:1），强调色用浅档。
                    两套摆在一起明显不同，但仍是同一个图标。
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
LIGHT_RING_W = 2.4                # 浅色档贴片描边圈线宽（给白底贴片在亮背景上勾出边界）

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

# ---------------------------------------------------------------- 色彩层（v3 新增）
# 骨架（网格 / 安全区 / 线宽 / 白色图形）完全继承 v2；v3 只动「色彩」这一层，
# 色彩有三个来源，其它一律白：
#   1. 贴片 = 同色系渐变。锚点仍是原 PALETTE 色（对比度约束不变），第二端沿色环
#      只漂 HUE_DRIFT 度、明度朝背离背景方向走 —— 既不「平」也不「割裂」。
#   2. 挖空/反衬 = 锚点色。图形里需要「透出底色」的元素（锁孔、书脊折痕、角标描边环）。
#   3. 强调色 = 3 个固定语义色，只给真语义元素（成功/失败/告警），不做装饰性着色。
HUE_DRIFT = 18.0                  # 渐变第二端的色相漂移（同色系内，读作「同一个颜色」）
LUM_DRIFT = 0.085                 # 第二端比锚点更深的幅度（两主题同向，见 grad_end）

ACCENT = {
    "green": {"dark": "#35c268", "light": "#1a9a4a"},   # 成功 / 可用 / 已生效
    "red":   {"dark": "#ff5a6e", "light": "#e0324a"},   # 失败 / 删除 / 未读
    "amber": {"dark": "#f5b025", "light": "#c07d00"},   # 告警 / 待定 / 过滤
}

# 每个插件的图形色彩声明：源色（小写）-> 目标
#   "anchor"        贴片锚点色（挖空 / 反衬）
#   "accent:<名>"   语义强调色
#   "#xxxxxx"       直接指定（仅 color-converter 这种「图标本体就是颜色」的例外）
# 源色由 glyphs.py 用固定哨兵写入（见那边的约定表）：
#   #0000ff=anchor  #008000=green  #ff0000=red  #cc8800=amber
# 未在此声明的颜色一律白。改图形后这份表要跟着核一遍。
COLOR_SPEC = {
    "api-mock":        {"#008000": "accent:green"},                          # 已启动
    "port-scanner":    {"#008000": "accent:green"},                          # 已占用
    "mail-check":      {"#008000": "accent:green"},                          # 邮箱有效
    "hosts-manager":   {"#008000": "accent:green"},                          # 条目已生效
    "site-audit":      {"#008000": "accent:green"},                          # 审计通过
    "compare":         {"#008000": "accent:green", "#ff0000": "accent:red"},  # diff 增 / 删
    "disk-analyzer":   {"#cc8800": "accent:amber"},                          # 空间告警区
    "color-converter": {"#ff00ff": "#ff5f6d", "#00ffff": "#4f8bff",
                        "#ffff00": "#3ddc63"},                               # 调色盘的三个色点
}

# 注意：anchor 色（#0000ff）画的是「贴片同色」，压在贴片上会隐形 ——
# 它只适合叠在白色实心图形上做分隔，别拿它画「洞」。
SOURCE_PATCH = {}


def apply_source_patch(name, src, anchor):
    for old, new in SOURCE_PATCH.get(name, []):
        src = src.replace(old, new.replace("@anchor", anchor))
    return src


ACCENT_HEX = {f"accent:{k}": v for k, v in ACCENT.items()}
ANCHOR = "anchor"


def spec_for(name, theme):
    """把 COLOR_SPEC 的符号值解析成该主题的具体色值，得到「源色 -> hex」的直接映射。

    生成/校验都走这一层，避免 resolve_color 里再关心主题。

    双主题差异：
      · 主字形哨兵 #e6e6e6 -> 深色档白字 / 浅色档品牌字（深锚点）
      · 锚点哨兵 #0000ff    -> 品牌字（作标记时等同于深锚点）
      · 强调色 accent:x     -> 深色档取深档、浅色档取浅档
      · color-converter 的三个点色：浅色档（白底）换成对比度更高的深档，避免浅绿点糊掉
    """
    g = GROUP.get(name)
    anchor = PALETTE[g]["dark"] if g else "#fff"   # 浅色档品牌字/描边圈统一用「深锚点」
    out = {
        "#e6e6e6": anchor if theme == "light" else "#fff",   # 主字形
        "#0000ff": anchor,                                   # 锚点（标记用）
    }
    if name == "color-converter":
        # 浅色档在白底上，点色需更深才能读得出
        dots = ({"#ff00ff": "#ff5f6d", "#00ffff": "#4f8bff", "#ffff00": "#3ddc63"}
                if theme == "dark" else
                {"#ff00ff": "#e0324a", "#00ffff": "#1d66ff", "#ffff00": "#1a9a4a"})
        out.update(dots)
        return out
    for src_hex, target in (COLOR_SPEC.get(name) or {}).items():
        if target == ANCHOR:
            out[src_hex] = anchor
        elif target.startswith("accent:"):
            out[src_hex] = ACCENT_HEX[target]["light" if theme == "light" else "dark"]
        else:
            out[src_hex] = target
    return out

# ---------------------------------------------------------------- 插件 -> 语义组
GROUP = {}
for _g, _names in {
    "blue": """calcsheet compare color-converter unit-converter time-converter rmb-upper
               batch-rename text-encoder type-trainer hanzi-copybook minesweeper emoji-search
               qrcode markdown-preview md-table-converter pdf-toolkit mindmap
               game-2048 game-reversi game-liferestart""".split(),
    "violet": """code-card formatter json-toolbox regex-extractor git-workbench
                 cron-explainer package-check game-tetris game-hextris""".split(),
    "teal": """api-mock api-loadtest http-client ws-tester port-scanner speed-test netdiag
               mail-check wifi-manager hosts-manager curl-converter game-sudoku game-adarkroom""".split(),
    "amber": """crypto-toolbox hash-calc jwt-decoder login-tester dir-buster
                subdomain-enum site-audit game-roguelike""".split(),
    "green": "database data-generator disk-analyzer junk-cleaner game-snake game-gomoku game-proxx".split(),
    "pink": "image-studio ocr-tool exif-viewer image-uploader game-code-snake".split(),
}.items():
    for _n in _names:
        GROUP[_n] = _g


# ================================================================ 颜色工具
def _lin_to_srgb(c):
    """线性光 -> sRGB 编码（含 gamma）。"""
    c = max(0.0, min(1.0, c))
    return 12.92 * c if c <= 0.0031308 else 1.055 * c ** (1 / 2.4) - 0.055


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


def _cbrt(x):
    return math.copysign(abs(x) ** (1 / 3), x)


def hex_to_oklch(h):
    """sRGB hex -> OKLCH。用于沿色环微调色相、推导渐变第二端。"""
    r, g, b = (_lin_from_srgb(c) for c in hex_to_srgb(h))
    l = 0.4122214708 * r + 0.5363325363 * g + 0.0514459929 * b
    m = 0.2119034982 * r + 0.6806995451 * g + 0.1073969566 * b
    s = 0.0883024619 * r + 0.2817188376 * g + 0.6299787005 * b
    l_, m_, s_ = _cbrt(l), _cbrt(m), _cbrt(s)
    L = 0.2104542553 * l_ + 0.7936177850 * m_ - 0.0040720468 * s_
    A = 1.9779984951 * l_ - 2.4285922050 * m_ + 0.4505937099 * s_
    B = 0.0259040371 * l_ + 0.7827717662 * m_ - 0.8086757660 * s_
    return L, math.hypot(A, B), math.degrees(math.atan2(B, A)) % 360


def oklch_to_hex(L, C, H):
    a, b = C * math.cos(math.radians(H)), C * math.sin(math.radians(H))
    l_ = L + 0.3963377774 * a + 0.2158037573 * b
    m_ = L - 0.1055613458 * a - 0.0638541728 * b
    s_ = L - 0.0894841775 * a - 1.2914855480 * b
    l, m, s = l_ ** 3, m_ ** 3, s_ ** 3
    lin = (4.0767416621 * l - 3.3077115913 * m + 0.2309699292 * s,
           -1.2684380046 * l + 2.6097574011 * m - 0.3413193965 * s,
           -0.0041960863 * l - 0.7034186147 * m + 1.7076147010 * s)
    # 出界就说明超色域，调用方据此回退
    if any(c < -0.001 or c > 1.001 for c in lin):
        return None
    return "#" + "".join(f"{round(_lin_to_srgb(c) * 255):02x}" for c in lin)


def grad_end(anchor_hex):
    """由锚点色推导渐变第二端。

    方向固定为「**比锚点更深**」，两个主题一致。理由：
      · 深色档锚点白字对比度已经是 3.67（贴近 3:1 下限），再提亮当场击穿；
      · 浅色档同样往下走，才能同时守住「白字/贴片」和「贴片/背景」两条约束。
    方向一致还有个好处：两套主题的明暗指向相同，观感不会「反着来」。
    """
    L, C, H = hex_to_oklch(anchor_hex)
    L2 = max(0.08, L - LUM_DRIFT)
    for k in (1.0, 0.95, 0.9, 0.85, 0.8, 0.7, 0.6, 0.5):
        hexv = oklch_to_hex(L2, C * k, H + HUE_DRIFT)
        if hexv:
            return hexv
    return anchor_hex


for _pv in PALETTE.values():
    for _t in ("dark", "light"):
        _pv[f"grad_{_t}"] = grad_end(_pv[_t])


# ================================================================ SVG 处理
SVG_OPEN = re.compile(r"<svg\b[^>]*>", re.S)
COLOR_ATTR = re.compile(r'(fill|stroke|stop-color)\s*=\s*"([^"]*)"')
STROKE_W = re.compile(r'stroke-width\s*=\s*"([^"]*)"')
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


ELEM_OPEN = re.compile(r'<([a-zA-Z][\w:-]*)\b([^>]*)>', re.S)
OPACITY_ANY = re.compile(r'\s(fill-|stroke-)?opacity\s*=\s*"([^"]*)"')


def resolve_color(val, spec, anchor, theme="dark"):
    """v1 源色 -> 目标色。返回 (hex, force_main)。

    锚点色本身也算命中（SOURCE_PATCH 会把 `@anchor` 直接代成锚点 hex 写进源里）。
    force_main=True 表示这不是锚点色（即强调色 / 直指定色）：彩色元素一律回主档
    不透明度 —— 半透明的彩色叠在彩色贴片上会糊出第三种颜色，语义反而不清。

    浅色档未声明颜色默认回品牌色（深锚点），而非白——浅色档没有白色字形。
    """
    v = val.strip().lower()
    if v == anchor.lower():
        return anchor, False
    t = (spec or {}).get(v)
    if not t:
        return (anchor if theme == "light" else "#fff"), False
    return t, t.lower() != anchor.lower()


def quantize_attrs(attrs):
    """不透明度收敛到三档：>=.9 主（省掉属性）/ .45-.9 次 / <.45 衬。"""
    def repl(m):
        pre, v = m.group(1) or "", m.group(2)
        try:
            f = float(v)
        except ValueError:
            return m.group(0)
        if f >= 0.9:
            return ""
        tier = OPACITY_TIERS["sub"] if f >= 0.45 else OPACITY_TIERS["faint"]
        return f' {pre}opacity="{tier}"'
    return OPACITY_ANY.sub(repl, attrs)


def apply_colors(inner, spec, anchor, theme="dark"):
    """按 COLOR_SPEC 给图形着色，未声明的颜色：深色档白、浅色档品牌色。

    逐**属性**而非逐元素决策 —— 同一元素上可能一个颜色是强调色、另一个是白色描边
    （mail-check 的红角标 + 白环就是），逐元素整块替换会把环也吃掉。
    """
    spec = {k.lower(): v for k, v in (spec or {}).items()}

    def repl_el(m):
        name, attrs = m.group(1), m.group(2)
        if name in ("defs", "linearGradient", "stop", "svg", "style"):
            return m.group(0)
        force_main = [False]

        def swap(mm):
            attr, val = mm.group(1), mm.group(2).strip()
            if val.lower() in ("", "none") or val.startswith("url("):
                return mm.group(0)
            t, fm = resolve_color(val, spec, anchor, theme)
            if fm:
                force_main[0] = True
            return f'{attr}="{t}"'

        new = COLOR_ATTR.sub(swap, attrs)
        return f'<{name}{new if force_main[0] else quantize_attrs(new)}>'

    return ELEM_OPEN.sub(repl_el, inner)


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


def build_icon(name, source_svg, theme, anchor, grad, bbox, spec):
    """把源图标重构成规范形态。bbox 是「图形（已含源变换）、不含背景」的渲染包围盒。

    theme="dark"  -> 饱和同色系渐变贴片 + 白字（几何/线宽不变）
    theme="light" -> 白底贴片 + 品牌色描边圈 + 品牌色字形（几何/线宽不变）
    """
    box, inner0, (t0x, t0y, s0) = prepare(source_svg, name, spec, anchor, theme)
    already_v3 = (t0x, t0y, s0) != (0.0, 0.0, 1.0)

    x0, y0, w, h, max_sw_spec = bbox["x0"], bbox["y0"], bbox["w"], bbox["h"], bbox["maxSw"]
    w = max(w, 1e-6)
    h = max(h, 1e-6)
    # 描边外扩必须发生在**输出坐标系**，不能加在输入 bbox 上再一起缩放 ——
    # 输出图标的「渲染后最粗一笔」恒为 BASE_STROKE（由下面的线宽归一保证），
    # 且 getBBox 不含描边，所以几何盒只该占 ART - BASE_STROKE，两侧各留 1.5 给描边。
    # 早先写成 `w += pad` 再 `s = ART/w` 是错的：视觉盒 = b*s + 3 只在 b 恰好 37 时才等于 40，
    # 其它情况第一轮生成偏大、下一轮才纠正 —— 表现为「第一次生成不幂等」。
    pad = BASE_STROKE if max_sw_spec > 0 else 0.0
    s = (ART - pad) / max(w, h)
    tx = SAFE + (ART - w * s) / 2 - x0 * s
    ty = SAFE + (ART - h * s) / 2 - y0 * s

    if already_v3 and abs(s - 1) <= GEOM_TOL:
        # 几何已在容差内：只换色，不重新拟合，保证重复生成逐字节一致
        return _render(theme, anchor, grad, t0x, t0y, s0, inner0)

    # 线宽归一：让「最粗的一笔」在 64 网格上恰为 BASE_STROKE。
    # 渲染后线宽 = 指定值 * 源变换 s0 * 本次变换 s，故补一个系数 k 把它拉回 3。
    if max_sw_spec > 0:
        k = BASE_STROKE / (max_sw_spec * s0 * s)
        inner0 = STROKE_W.sub(lambda m: f'stroke-width="{_fmt(float(m.group(1)) * k)}"', inner0)

    # 与源变换复合，避免每次重生成都多套一层 <g>
    return _render(theme, anchor, grad, tx + s * t0x, ty + s * t0y, s * s0, inner0)


GRAD_ID = "pg"


def _render(theme, anchor, grad, fx, fy, fs, inner):
    """输出一个规范图标。两主题的几何/线宽完全一致，只差贴片：

    dark  : <rect fill="url(#pg)"> 同色系渐变贴片 + 白字（anchor 是渐变 stop0，grad 是 stop1）
    light : <rect fill="#fff" stroke=品牌色> 白底贴片 + 品牌色描边圈 + 品牌色字形
    """
    head = (
        '  <rect width="64" height="64" rx="{r}" fill="#ffffff" '
        'stroke="{st}" stroke-width="{sw}"/>\n'
    ).format(r=RADIUS, st=anchor, sw=_fmt(LIGHT_RING_W))
    if theme == "light":
        return (
            '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64">\n'
            + head +
            f'  <g transform="translate({_fmt(fx)},{_fmt(fy)}) scale({_fmt(fs)})">\n'
            f'{_reindent(inner)}\n'
            '  </g>\n'
            '</svg>\n'
        )
    return (
        '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64">\n'
        '  <defs>\n'
        f'    <linearGradient id="{GRAD_ID}" x1="0" y1="0" x2="1" y2="1">\n'
        f'      <stop offset="0" stop-color="{anchor}"/>\n'
        f'      <stop offset="1" stop-color="{grad}"/>\n'
        '    </linearGradient>\n'
        '  </defs>\n'
        f'  <rect width="64" height="64" rx="{RADIUS}" fill="url(#{GRAD_ID})"/>\n'
        f'  <g transform="translate({_fmt(fx)},{_fmt(fy)}) scale({_fmt(fs)})">\n'
        f'{_reindent(inner)}\n'
        '  </g>\n'
        '</svg>\n'
    )


V2_HEAD = re.compile(
    r'<rect\b[^>]*width="64"[^>]*height="64"[^>]*rx="14"[^>]*/>\s*'
    r'<g\s+transform="translate\(([-\d.]+),\s*([-\d.]+)\)\s*scale\(([-\d.]+)\)">'
)
V3_HEAD = re.compile(
    r'<defs>\s*<linearGradient[^>]*>[\s\S]*?</linearGradient>\s*</defs>\s*'
    r'<rect\b[^>]*width="64"[^>]*height="64"[^>]*rx="14"[^>]*/>\s*'
    r'<g\s+transform="translate\(([-\d.]+),\s*([-\d.]+)\)\s*scale\(([-\d.]+)\)">'
)
# 浅色档：白底贴片 + 品牌色描边圈，无渐变 defs
V3_HEAD_LIGHT = re.compile(
    r'<rect\b[^>]*width="64"[^>]*height="64"[^>]*rx="14"[^>]*fill="#ffffff"[^>]*'
    r'stroke="#[0-9a-fA-F]{6}"[^>]*stroke-width="[\d.]+"[^>]*/>\s*'
    r'<g\s+transform="translate\(([-\d.]+),\s*([-\d.]+)\)\s*scale\(([-\d.]+)\)">'
)


def split_source(text):
    """源 svg -> (viewBox, 图形 inner, (t0x,t0y,s0))。已是规范形态时把外层 <g> 拆出来，
    使生成过程幂等（重跑不会层层套 <g>、也不会重复着色）。v3 先于 v2 匹配。"""
    box, inner = parse_svg(text)
    for pat in (V3_HEAD, V3_HEAD_LIGHT, V2_HEAD):
        m = pat.search(inner)
        if m:
            t0 = (float(m.group(1)), float(m.group(2)), float(m.group(3)))
            rest = inner[m.end():]
            idx = rest.rfind("</g>")
            return box, (rest[:idx] if idx >= 0 else rest), t0
    return box, inner, (0.0, 0.0, 1.0)


ROOT_PRESENT = ("stroke", "fill", "stroke-width", "stroke-linecap", "stroke-linejoin",
                "stroke-miterlimit", "stroke-dasharray", "stroke-opacity",
                "fill-rule", "opacity")


def root_glyph_attrs(source_svg, spec, anchor, theme="dark"):
    """把根 <svg> 上的表现属性拾回来（parse_svg 只取 inner，会丢掉它们）。

    大量 v1 图标写成 `<svg fill="none" stroke="#4a9eff" stroke-width="2">` + 裸几何，
    属性全在根上。丢掉后几何的 fill 回退成默认黑 -> 整个字形变黑块。
    这里统一归一化：颜色按 COLOR_SPEC 解析（未声明即白/品牌色），透明度 -> 三档，
    线宽原值保留（随后由线宽归一化按 k 缩放）。
    """
    m = SVG_OPEN.search(source_svg)
    if not m:
        return ""
    tag = m.group(0)
    out = []
    force_main = False
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
                val, force_main = resolve_color(v, spec, anchor, theme)
        elif a in ("opacity", "fill-opacity", "stroke-opacity"):
            try:
                f = float(v)
            except ValueError:
                continue
            if f >= 0.9 or force_main:
                continue
            val = _fmt(OPACITY_TIERS["sub"] if f >= 0.45 else OPACITY_TIERS["faint"])
        else:
            val = v
        out.append(f'{a}="{val}"')
    return " ".join(out)


def uncolor(inner, name):
    """已是 v3 形态的源：把「已解析的强调色/锚点色」还原回 v1 源色，供重新着色。

    没有这一步，从 v3 源再生成时会跳过着色 —— 而源只有 icon.svg 一个文件，
    浅色档就会沿用深色档的强调色（表现为「第二次生成浅色档全部跑偏」）。
    """
    g = GROUP.get(name)
    rev = {}
    for theme in ("dark", "light"):
        anchor = PALETTE[g][theme] if g else None
        for src_hex, target in (COLOR_SPEC.get(name) or {}).items():
            if target == ANCHOR:
                rev[anchor.lower()] = src_hex
            elif target.startswith("accent:"):
                rev[ACCENT_HEX[target][theme].lower()] = src_hex
            else:
                rev[target.lower()] = src_hex
    if not rev:
        return inner

    def swap(m):
        v = m.group(2).strip().lower()
        return f'{m.group(1)}="{rev[v]}"' if v in rev else m.group(0)

    return COLOR_ATTR.sub(swap, inner)


def prepare(source_svg, name, spec, anchor, theme="dark"):
    """源 svg -> (viewBox, 已着色的图形 inner, 源变换)。

    v1 源：剥背景 -> 接回根属性 -> 着色。
    v2/v3 源：先把已解析的颜色还原成源色 -> 再按当前主题着色（保证两档都正确）。
    两条路径都收敛到同一形态，所以生成是幂等的。
    """
    box, inner, t0 = split_source(source_svg)
    if t0 == (0.0, 0.0, 1.0):
        inner = strip_background(inner, box)
        attrs = root_glyph_attrs(source_svg, spec, anchor, theme)
        if attrs:
            inner = f'<g {attrs}>\n{inner}\n</g>'
    else:
        inner = uncolor(inner, name)
    return box, apply_colors(inner, spec, anchor, theme), t0


def measure_svg(name, source_svg, spec=None, anchor="#fff", theme="dark"):
    """构造只含图形（无背景贴片）的 svg 供 Chrome 量盒；额外包一层无变换的 <g id="art">，
    这样 getBBox 结果无歧义地落在 viewBox 坐标系（源变换已计入）。"""
    box, inner, (t0x, t0y, s0) = prepare(source_svg, name, spec, anchor, theme)
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
    boxes = measure_bboxes([
        (n, measure_svg(n, apply_source_patch(n, s, PALETTE[GROUP[n]]["dark"]),
                        spec_for(n, "dark"), PALETTE[GROUP[n]]["dark"]))
        for n, s in sources.items() if GROUP.get(n)
    ])
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
        anchor = PALETTE[g]["dark"]   # 两主题都用「深锚点」做品牌色（浅色档作字形/描边）
        for theme, fname in (("dark", "icon.svg"), ("light", "icon.light.svg")):
            grad = PALETTE[g]["grad_dark"] if theme == "dark" else None
            (d / fname).write_text(
                build_icon(n, apply_source_patch(n, src, anchor), theme, anchor,
                           grad, bb, spec_for(n, theme)),
                encoding="utf-8")
        print(f"  OK {n:20s} {g:7s} {anchor} -> {PALETTE[g]['grad_dark']}")
        ok += 1
    return 0 if ok == len(names) else 1


def _lum_of(rgb):
    r, g, b = (_lin_from_srgb(c) for c in rgb)
    return 0.2126 * r + 0.7152 * g + 0.0722 * b


def palette_contrast_errors():
    """色板级硬约束（比逐个图标检查更早暴露问题）。

    深色档（饱和渐变贴片 + 白字）：
      · 白图形 对 贴片两端  >= 3.0:1   （白字在贴片上要读得出）
      · 贴片两端 对 各自主题背景 >= 3.0:1（贴片在面板上要分得开）

    浅色档（白底贴片 + 品牌色字形，显示在近白背景上）：
      · 品牌字（深锚点）对白底 >= 3.0:1
      · 强调色（浅档）对白底   >= 3.0:1
      · 贴片是白底、故意与近白背景接近，不强制「贴片/背景」3:1（靠描边圈勾边界）
    渐变第二端是最亮/最暗的一头，所以两端都过 = 中间任意点都过。
    """
    errs = []
    for name, spec in PALETTE.items():
        anchor = spec["dark"]                      # 浅色档品牌字/描边圈
        # 深色档：白字对饱和贴片 + 贴片对暗背景
        for theme in ("dark",):
            bg = contrast(hex_to_lum(spec[theme]), _lum_of(BG[theme]))
            if contrast(1.0, hex_to_lum(spec[theme])) < 3.0:
                errs.append(f"色板 {name}/{theme}: 白图形对贴片 {spec[theme]} 对比度不足 3:1")
            if bg < 3.0:
                errs.append(f"色板 {name}/{theme}: 贴片 {spec[theme]} 对背景 对比度不足 3:1")
            grad = spec[f"grad_{theme}"]
            if contrast(1.0, hex_to_lum(grad)) < 3.0:
                errs.append(f"色板 {name}/{theme}: 白图形对渐变端 {grad} 对比度不足 3:1")
            if contrast(hex_to_lum(grad), _lum_of(BG[theme])) < 3.0:
                errs.append(f"色板 {name}/{theme}: 渐变端 {grad} 对背景 对比度不足 3:1")
        # 浅色档：品牌字/强调色对白底
        bg_lum = hex_to_lum("#ffffff")
        if contrast(hex_to_lum(anchor), bg_lum) < 3.0:
            errs.append(f"色板 {name}/light: 品牌字 {anchor} 对白底 对比度不足 3:1")
        for acn, ac in ACCENT.items():
            ac_l = ac["light"]
            if contrast(hex_to_lum(ac_l), bg_lum) < 3.0:
                errs.append(f"色板 {name}/light: 强调色 {acn}({ac_l}) 对白底 对比度不足 3:1")
        # color-converter 浅档点色（白底）
        for dot in ("#e0324a", "#1d66ff", "#1a9a4a"):
            if contrast(hex_to_lum(dot), bg_lum) < 3.0:
                errs.append(f"色板 {name}/light: color-converter 点色 {dot} 对白底 对比度不足 3:1")
    return errs


def cmd_check():
    """校验：规范形态 + 语义色正确 + 两档渐变正确 + 两套主题齐备。返回 (错误数, 警告数)。"""
    errs, warns = [], list(palette_contrast_errors())
    plugins = sorted(p.name for p in ROOT.iterdir()
                     if p.is_dir() and (p / "plugin.json").exists())
    for n in plugins:
        d = ROOT / n
        g = GROUP.get(n)
        if not g:
            warns.append(f"{n}: 未登记语义色（GROUP 中缺失）")
        # 该插件每个主题下允许出现的图形颜色：
        #   深色档 -> 白 + 锚点色 + 强调/直指定色
        #   浅色档 -> 品牌色(深锚点) + 强调/直指定色（浅色档无白字）
        allowed_by_theme = {}
        for t in ("dark", "light"):
            if g:
                anchor = PALETTE[g]["dark"]
                allow = {anchor.lower()} | {v.lower() for v in spec_for(n, t).values()}
            else:
                allow = set()
            if t == "dark":
                allow |= {"#fff", "#ffffff"}
            allowed_by_theme[t] = allow
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
            # 贴片：深色档=v3 渐变（两 stop 等于该语义色/主题两端）；浅色档=白底+品牌描边圈
            if theme == "light":
                mp = re.search(
                    r'<rect\b[^>]*width="64"[^>]*height="64"[^>]*rx="14"[^>]*'
                    r'fill="#ffffff"[^>]*stroke="(#[0-9a-fA-F]{6})"[^>]*'
                    r'stroke-width="([\d.]+)"[^>]*/>', text)
                if not mp:
                    errs.append(f"{n}/{fname}: 浅色档贴片应为白底+品牌描边圈 "
                                f"<rect ... fill='#ffffff' stroke='品牌色' stroke-width='..'/>")
                elif g and mp.group(1).lower() != PALETTE[g]["dark"].lower():
                    errs.append(f"{n}/{fname}: 浅色档描边圈颜色 {mp.group(1)} "
                                f"!= {g} 应取的品牌色 {PALETTE[g]['dark']}")
            else:
                mp = re.search(r'<rect\b[^>]*width="64"[^>]*height="64"[^>]*rx="14"[^>]*fill="url\(#([\w-]+)\)"', text)
                if mp:
                    gid = mp.group(1)
                    stops = re.search(rf'<linearGradient[^>]*id="{gid}"[^>]*>([\s\S]*?)</linearGradient>', text)
                    cols = [c for _, c in COLOR_ATTR.findall(stops.group(1))] if stops else []
                    want = [PALETTE[g][theme], PALETTE[g][f"grad_{theme}"]] if g else []
                    if len(cols) != 2:
                        errs.append(f"{n}/{fname}: 贴片渐变应有 2 个 stop，实为 {len(cols)}")
                    elif want and [c.lower() for c in cols] != [w.lower() for w in want]:
                        errs.append(f"{n}/{fname}: 贴片渐变 {cols} != {g}/{theme} 应取的 {want}")
                else:
                    mp2 = re.search(r'<rect\b[^>]*width="64"[^>]*height="64"[^>]*rx="14"[^>]*fill="(#[0-9a-fA-F]{6})"', text)
                    if not mp2:
                        errs.append(f"{n}/{fname}: 缺规范贴片 <rect width=64 height=64 rx=14 fill=..>")
                    else:
                        errs.append(f"{n}/{fname}: 贴片仍是纯色 {mp2.group(1)}，应为同色系渐变")
            # 颜色只扫图形部分：贴片本身就是语义色，不能一并当「非白」报错
            m = V3_HEAD.search(text) or V3_HEAD_LIGHT.search(text) or V2_HEAD.search(text)
            body = text[m.end():] if m else text
            if m:
                cut = body.rfind("</g>")
                if cut >= 0:
                    body = body[:cut]
            allow = allowed_by_theme[theme]
            for c in set(COLOR_ATTR.findall(body)):
                val = c[1].strip()
                if val in ("none",) or val.startswith("url("):
                    continue
                if val.lower() not in allow:
                    errs.append(f"{n}/{fname}: 图形颜色 {val} 既非白色也非声明的锚点/强调色")
            for sw in set(STROKE_W.findall(text)):
                if abs(float(sw) - 1.0) > 1e-6 and float(sw) > 40:
                    errs.append(f"{n}/{fname}: stroke-width {sw} 异常")
            # 残留的「嵌套卡底」：art 里还有一块两轴都 >= 半幅的实心 rect，
            # 它被涂成白色后会整块盖住贴片（图形读不出来）。
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
    const r = await p.evaluate(async ({svg, svgm, S}) => {
      const c = document.createElement('canvas');
      c.width = S; c.height = S;
      const g = c.getContext('2d', {willReadFrequently: true});
      const img = new Image();
      img.src = 'data:image/svg+xml;base64,' + btoa(unescape(encodeURIComponent(svg)));
      await img.decode();
      g.clearRect(0, 0, S, S);
      g.drawImage(img, 0, 0, S, S);
      const d = g.getImageData(0, 0, S, S).data;
      // 贴片 v3 是渐变，不能再按「某几个精确色值」数像素。改用「白 / 非白」二分：
      //   white   白色图形 + 白色高光
      //   colored 贴片渐变本体（含锚点/强调色）—— 取反即「贴片被盖住多少」
      let white = 0, colored = 0;
      for (let i = 0; i < d.length; i += 4) {
        if (d[i + 3] < 200) continue;
        if (d[i] >= 246 && d[i+1] >= 246 && d[i+2] >= 246) white++;
        else colored++;
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
      return {white: white / n * 100, colored: colored / n * 100,
              bb: bb && bb.width > 0 ? [bb.x, bb.y, bb.width, bb.height] : null};
    }, {svg: it.svg, svgm: it.svgm, S});
    out[it.name] = r;
  }
  console.log(JSON.stringify(out));
  await b.close();
})();
"""


def cmd_check_render():
    """渲染级校验：纯静态规则看不出「白块盖住贴片」，这里按真实像素判。

    深色档：
      · 白像素占比过高 -> 卡底没剥干净、被涂白后盖住贴片
      · 非白像素占比过低 -> 贴片基本被遮住，用户在 36px 下看到一块白方
    浅色档：贴片本就是白底，白像素高是正常的——改判「白底在」+「品牌字形在」，
      不再套白像素上限。

    · 图形包围盒越界 -> 缩放出错，图形溢出安全区

    注意贴片在 v3 是渐变，像素颜色是连续分布，所以这里只做「白 / 非白」二分，
    不再按具体色值匹配 —— 那套做法对渐变必然失效。
    """
    items = []
    for n in sorted(GROUP):
        anchor = PALETTE[GROUP[n]]["dark"]
        for fname in ("icon.svg", "icon.light.svg"):
            f = ROOT / n / fname
            if f.exists():
                text = f.read_text(encoding="utf-8")
                items.append({"name": f"{n}/{fname}", "svg": text,
                              "svgm": measure_svg(n, text, spec_for(n, "dark"), anchor)})
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
        is_light = name.endswith("icon.light.svg")
        bb = s["bb"]
        if bb:
            x, y, w, h = bb
            if x < SAFE - 1.5 or y < SAFE - 1.5 or x + w > GRID - SAFE + 1.5 or y + h > GRID - SAFE + 1.5:
                errs.append(f"{name}: 图形包围盒 [{x:.1f},{y:.1f},{w:.1f},{h:.1f}] "
                            f"越出安全区 {SAFE:g}..{GRID - SAFE:g}")
        if is_light:
            # 浅色档：白底贴片（白像素高是正常的）+ 必须有品牌字形（非白像素）
            if s["white"] < 40:
                errs.append(f"{name}: 白像素占比 {s['white']:.1f}% < 40%，"
                            f"浅色档白底贴片疑似缺失/被覆盖")
            if s["colored"] < 4:
                errs.append(f"{name}: 贴片像素占比 {s['colored']:.1f}% < 4%，"
                            f"品牌字形疑似缺失")
        else:
            if s["white"] > WHITE_MAX:
                errs.append(f"{name}: 白像素占比 {s['white']:.1f}% > {WHITE_MAX}%，"
                            f"疑似卡底未剥离（会盖住贴片）")
            if s["colored"] < PATCH_MIN:
                errs.append(f"{name}: 贴片像素占比 {s['colored']:.1f}% < {PATCH_MIN}%，"
                            f"贴片基本被遮住")
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
