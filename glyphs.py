# -*- coding: utf-8 -*-
"""图标字形源（按插件功能手绘，24 网格）。

这里是图标「图形」的唯一权威来源。改图形只改这个文件，然后：

    python glyphs.py          # 把字形写进 <插件>/icon.svg（v1 形态）
    python gen_icons.py --gen # 由 gen_icons.py 做规范化（缩放/线宽/渐变贴片/两套主题）

设计约定（生成器只认这些，写错会被 --check 拦下）：
  · 24 网格，线条式；内容大致落在 2..22，生成器会自动等比缩放填满 64 网格安全区。
  · 线宽写 2 即可 —— 生成器会把「最粗一笔」归一到 64 网格上的 3。
  · 层次只靠透明度：主档（不写） / 次档 opacity="0.55" / 衬档 opacity="0.3"。
  · 颜色用下面这几个哨兵值，未声明的颜色会被生成器涂白：
        #e6e6e6  主笔（白）
        #0000ff  anchor  —— 贴片锚点色，用于「挖空 / 反衬」
        #008000  accent:green —— 成功 / 可用 / 已生效
        #ff0000  accent:red   —— 失败 / 未读 / 删除
        #cc8800  accent:amber —— 告警 / 待定 / 过滤
    哨兵 -> 目标的映射写在 gen_icons.py 的 COLOR_SPEC 里（逐插件）。
  · 强调色只给有状态语义的元素，不做装饰性着色（color-converter 例外：它的内容就是颜色）。
  · 不要用 <text> / currentColor / fill 渐变 —— 都会在 <img>/沙箱下失效。
"""

import pathlib
import re

ROOT = pathlib.Path(__file__).resolve().parent

HEAD = ('<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" '
        'stroke="#e6e6e6" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">')

# 插件 id 短名（去掉 io.github.parieses. 前缀）-> 图形内容
GLYPH = {}

GLYPH["api-mock"] = """
  <rect x="2.5" y="3" width="19" height="18" rx="3"/>
  <path d="M2.5 8h19" opacity="0.3"/>
  <circle cx="5.9" cy="5.5" r=".9" fill="#e6e6e6" stroke="none"/>
  <circle cx="8.7" cy="5.5" r=".9" fill="#e6e6e6" stroke="none" opacity="0.55"/>
  <path d="M6.5 12.4h6.2"/>
  <path d="M6.5 16.6h4.4" opacity="0.55"/>
  <path d="M15.4 12.9l3.9 2.9-3.9 2.9z" fill="#008000" stroke="none"/>
"""

GLYPH["api-loadtest"] = """
  <path d="M3.5 17.6a8.5 8.5 0 0 1 17 0"/>
  <path d="M12 17.6l4.7-6"/>
  <circle cx="12" cy="17.6" r="1.5" fill="#e6e6e6" stroke="none"/>
  <path d="M12 3.6v2.2" opacity="0.55"/>
  <path d="M4.9 8.1l1.5 1.5" opacity="0.3"/>
  <path d="M19.1 8.1l-1.5 1.5" opacity="0.3"/>
"""

GLYPH["http-client"] = """
  <path d="M21 3L3 10.7l7.1 2.5L12.6 21z"/>
  <path d="M21 3l-10.9 9.7"/>
"""

GLYPH["ws-tester"] = """
  <rect x="2.2" y="8.6" width="4.6" height="6.8" rx="1.7"/>
  <rect x="17.2" y="8.6" width="4.6" height="6.8" rx="1.7"/>
  <path d="M6.8 12h3l1.3-3 2.1 6 1.4-3h3.1"/>
"""

GLYPH["port-scanner"] = """
  <path d="M8.1 3.4h7.8a2.5 2.5 0 0 1 2.5 2.5v5.5a4 4 0 0 1-2.3 3.6l-.9 4.3a1.2 1.2 0 0 1-1.2 1h-3.9a1.2 1.2 0 0 1-1.2-1l-.9-4.3A4 4 0 0 1 5.6 11.4V5.9a2.5 2.5 0 0 1 2.5-2.5z"/>
  <path d="M9.9 7.2v3.4" opacity="0.55"/>
  <path d="M14.1 7.2v3.4" stroke="#008000"/>
"""

GLYPH["speed-test"] = """
  <path d="M9.8 3.4v10.8"/>
  <path d="M6 10.2l3.8 3.9 3.8-3.9"/>
  <path d="M16.4 8.6a6.6 6.6 0 0 1 0 9.4" opacity="0.55"/>
  <path d="M19.8 5.6a11.2 11.2 0 0 1 0 15.2" opacity="0.3"/>
"""

GLYPH["netdiag"] = """
  <circle cx="12" cy="12" r="9" opacity="0.3"/>
  <circle cx="12" cy="12" r="5.2" opacity="0.55"/>
  <circle cx="12" cy="12" r="1.6" fill="#e6e6e6" stroke="none"/>
  <path d="M12 12l6.4-6.4"/>
"""

GLYPH["mail-check"] = """
  <rect x="2.5" y="4.4" width="19" height="12.6" rx="2.5"/>
  <path d="M3.3 6.2l8.7 5.8 8.7-5.8"/>
  <path d="M14.4 19.4l2 2 3.6-4" stroke="#008000"/>
"""

GLYPH["wifi-manager"] = """
  <path d="M2.4 9a14 14 0 0 1 19.2 0" opacity="0.3"/>
  <path d="M5.8 12.6a9 9 0 0 1 12.4 0" opacity="0.55"/>
  <path d="M9.2 16.2a4.2 4.2 0 0 1 5.6 0"/>
  <circle cx="12" cy="19.6" r="1.4" fill="#e6e6e6" stroke="none"/>
"""

GLYPH["hosts-manager"] = """
  <path d="M6.5 2.6h7.6L20.4 8.8V20a1.6 1.6 0 0 1-1.6 1.6H6.5A1.6 1.6 0 0 1 4.9 20V4.2A1.6 1.6 0 0 1 6.5 2.6z"/>
  <path d="M13.9 2.8v6.2h6.3"/>
  <path d="M8.4 13.4l1.6 1.6 2.7-3" stroke="#008000"/>
  <path d="M15.4 13.6h2.3" opacity="0.55"/>
  <path d="M8.5 18.4h9.2" opacity="0.3"/>
"""

GLYPH["curl-converter"] = """
  <rect x="2.4" y="4.6" width="19.2" height="15" rx="3"/>
  <path d="M2.4 8.9h19.2" opacity="0.3"/>
  <path d="M6.6 12.4l2.6 2.6-2.6 2.6"/>
  <path d="M11.8 17.6h5.6"/>
"""

GLYPH["code-card"] = """
  <rect x="2.6" y="4.4" width="18.8" height="15.2" rx="3"/>
  <path d="M2.6 9.2h18.8" opacity="0.3"/>
  <circle cx="6.2" cy="6.8" r=".9" fill="#e6e6e6" stroke="none"/>
  <circle cx="9" cy="6.8" r=".9" fill="#e6e6e6" stroke="none" opacity="0.55"/>
  <path d="M7 13.2l2.5 2.3L7 17.8"/>
  <path d="M12.6 17.8h4.6"/>
"""

GLYPH["formatter"] = """
  <path d="M4.6 5v14" opacity="0.3"/>
  <path d="M8 7.2h11.4"/>
  <path d="M8 11.6h7.6" opacity="0.55"/>
  <path d="M11.6 16h7.8"/>
"""

GLYPH["json-toolbox"] = """
  <path d="M8.8 3.8H7.6A2.4 2.4 0 0 0 5.2 6.2v3.2c0 1.4-1.1 2.6-2.4 2.6 1.3 0 2.4 1.2 2.4 2.6v3.2a2.4 2.4 0 0 0 2.4 2.4h1.2"/>
  <path d="M15.2 3.8h1.2a2.4 2.4 0 0 1 2.4 2.4v3.2c0 1.4 1.1 2.6 2.4 2.6-1.3 0-2.4 1.2-2.4 2.6v3.2a2.4 2.4 0 0 1-2.4 2.4h-1.2"/>
  <circle cx="12" cy="12" r="1.5" fill="#e6e6e6" stroke="none"/>
"""

GLYPH["regex-extractor"] = """
  <path d="M6.6 4.4L3.8 19.6"/>
  <path d="M17.4 4.4l2.8 15.2"/>
  <path d="M12 8.6v6.8"/>
  <path d="M9.1 10.3l5.8 3.4"/>
  <path d="M14.9 10.3l-5.8 3.4"/>
"""

GLYPH["git-workbench"] = """
  <path d="M7.2 4.2v15.6"/>
  <circle cx="7.2" cy="7.2" r="2.4"/>
  <circle cx="7.2" cy="16.8" r="2.4"/>
  <path d="M9.6 7.2h2.4a4.2 4.2 0 0 1 4.2 4.2v1.2a4.2 4.2 0 0 1-4.2 4.2H9.6"/>
"""

GLYPH["cron-explainer"] = """
  <circle cx="12" cy="12" r="7.6"/>
  <path d="M12 6.4V12l3.4 2.2"/>
  <path d="M12 1.9v1.9M12 20.2v1.9M1.9 12h1.9M20.2 12h1.9" opacity="0.55"/>
"""

GLYPH["package-check"] = """
  <path d="M12 2.8l8.6 3.9v10.6L12 21.2 3.4 17.3V6.7z"/>
  <path d="M3.6 6.8L12 10.7l8.4-3.9"/>
  <path d="M12 10.7v10.3"/>
"""

GLYPH["crypto-toolbox"] = """
  <path d="M12 2.6l7.6 2.8v6.2c0 4.9-3.2 8.4-7.6 9.8-4.4-1.4-7.6-4.9-7.6-9.8V5.4z"/>
  <circle cx="12" cy="10.2" r="2.2" fill="#e6e6e6" stroke="none"/>
  <path d="M12 12.4v4.2"/>
"""

GLYPH["hash-calc"] = """
  <path d="M9.6 3.4L7.2 20.6"/>
  <path d="M16.8 3.4l-2.4 17.2"/>
  <path d="M4.2 8.6h16.2"/>
  <path d="M3.4 15.4h16.2"/>
"""

GLYPH["jwt-decoder"] = """
  <rect x="2.6" y="4.6" width="18.8" height="14.8" rx="3.2"/>
  <path d="M8.9 4.8v14.4" opacity="0.55"/>
  <path d="M15.1 4.8v14.4" opacity="0.55"/>
"""

GLYPH["login-tester"] = """
  <path d="M7.4 10.4V7.8a4.6 4.6 0 0 1 9.2 0v2.6"/>
  <rect x="4.8" y="10.4" width="14.4" height="10.8" rx="2.8"/>
  <circle cx="12" cy="15" r="1.9" fill="#e6e6e6" stroke="none"/>
  <path d="M12 16.6v2.8"/>
"""

GLYPH["dir-buster"] = """
  <path d="M3.4 7.4a2.4 2.4 0 0 1 2.4-2.4h3.4l2.2 2.6h6.8a2.4 2.4 0 0 1 2.4 2.4v8.2a2.4 2.4 0 0 1-2.4 2.4H5.8a2.4 2.4 0 0 1-2.4-2.4z"/>
  <path d="M7.6 12.6h7.4"/>
  <path d="M7.6 16.2h4.4" opacity="0.55"/>
"""

GLYPH["subdomain-enum"] = """
  <circle cx="12" cy="11.4" r="3.6"/>
  <circle cx="4.2" cy="4.6" r="2"/>
  <circle cx="19.8" cy="4.6" r="2"/>
  <circle cx="12" cy="20.8" r="2"/>
  <path d="M9.7 8.9L6.2 6.4"/>
  <path d="M14.3 8.9l3.5-2.5"/>
  <path d="M12 15v3.9"/>
"""

GLYPH["site-audit"] = """
  <circle cx="10.8" cy="10.8" r="8.2"/>
  <path d="M2.6 10.8h16.4"/>
  <path d="M10.8 2.6c2.3 2.4 3.5 5.2 3.5 8.2s-1.2 5.8-3.5 8.2c-2.3-2.4-3.5-5.2-3.5-8.2s1.2-5.8 3.5-8.2z"/>
  <path d="M14.2 19.4l2 2 3.8-4.2" stroke="#008000"/>
"""

GLYPH["database"] = """
  <ellipse cx="12" cy="5.9" rx="8.4" ry="3.1"/>
  <path d="M3.6 5.9v12.2c0 1.7 3.8 3.1 8.4 3.1s8.4-1.4 8.4-3.1V5.9"/>
  <path d="M3.6 12c0 1.7 3.8 3.1 8.4 3.1s8.4-1.4 8.4-3.1"/>
"""

GLYPH["data-generator"] = """
  <rect x="3.4" y="3.4" width="17.2" height="17.2" rx="4.2"/>
  <circle cx="8.3" cy="8.3" r="1.3" fill="#e6e6e6" stroke="none"/>
  <circle cx="12" cy="12" r="1.3" fill="#e6e6e6" stroke="none" opacity="0.55"/>
  <circle cx="15.7" cy="15.7" r="1.3" fill="#e6e6e6" stroke="none"/>
"""

GLYPH["disk-analyzer"] = """
  <path d="M12 12V3.4A8.6 8.6 0 0 1 20.6 12z" stroke="#cc8800"/>
  <path d="M12 12l8.6 0A8.6 8.6 0 0 1 12 20.6z"/>
  <path d="M12 12v8.6A8.6 8.6 0 0 1 3.4 12z" opacity="0.55"/>
  <path d="M12 12H3.4A8.6 8.6 0 0 1 12 3.4z" opacity="0.3"/>
"""

GLYPH["junk-cleaner"] = """
  <path d="M3.6 6.6h16.8"/>
  <path d="M9.2 6.6V4.9a1.5 1.5 0 0 1 1.5-1.5h2.6a1.5 1.5 0 0 1 1.5 1.5v1.7"/>
  <path d="M5.8 6.6l1 12.3a1.8 1.8 0 0 0 1.8 1.6h6.8a1.8 1.8 0 0 0 1.8-1.6l1-12.3"/>
  <path d="M10.2 10.6v6.2" opacity="0.55"/>
  <path d="M13.8 10.6v6.2" opacity="0.55"/>
"""

GLYPH["image-studio"] = """
  <rect x="2.6" y="4.4" width="18.8" height="15.2" rx="2.8"/>
  <path d="M12 4.6v14.8" opacity="0.3"/>
  <circle cx="7.4" cy="9.6" r="1.9"/>
  <path d="M3.4 18.4l4-4 2.8 2.8"/>
  <path d="M13.4 18.4l3.8-4.2 3.6 3.6" opacity="0.55"/>
"""

GLYPH["ocr-tool"] = """
  <path d="M3.6 7.6V5.4a1.8 1.8 0 0 1 1.8-1.8h2.2"/>
  <path d="M16.4 3.6h2.2a1.8 1.8 0 0 1 1.8 1.8v2.2"/>
  <path d="M20.4 16.4v2.2a1.8 1.8 0 0 1-1.8 1.8h-2.2"/>
  <path d="M7.6 20.4H5.4a1.8 1.8 0 0 1-1.8-1.8v-2.2"/>
  <path d="M8 9.6h8"/>
  <path d="M8 13h5.6"/>
  <path d="M8 16.4h8" opacity="0.55"/>
"""

GLYPH["exif-viewer"] = """
  <path d="M8.4 5.6l1.4-2.2h4.4l1.4 2.2"/>
  <rect x="2.6" y="5.4" width="18.8" height="13.2" rx="3.2"/>
  <circle cx="12" cy="12" r="3.9"/>
  <circle cx="12" cy="12" r="1.2" fill="#e6e6e6" stroke="none" opacity="0.55"/>
"""

GLYPH["image-uploader"] = """
  <path d="M12 2.6v9.2"/>
  <path d="M8.1 6.4L12 2.5l3.9 3.9"/>
  <rect x="2.6" y="12.6" width="18.8" height="8.8" rx="2.6"/>
  <circle cx="7.2" cy="16.3" r="1.4"/>
  <path d="M5.2 21.4l3.2-3.8 2.3 2.3 1.6-1.6 4.1 4.1" opacity="0.55"/>
"""

GLYPH["calcsheet"] = """
  <rect x="2.8" y="4.4" width="18.4" height="15.2" rx="3"/>
  <path d="M2.8 9.4h18.4" opacity="0.55"/>
  <path d="M12 9.6v9.8" opacity="0.3"/>
  <path d="M7.4 12.6v3.6M5.6 14.4h3.6"/>
  <path d="M15.2 13.4h3.6M15.2 15.6h3.6"/>
"""

GLYPH["compare"] = """
  <rect x="2.6" y="3.6" width="7" height="16.8" rx="2.4"/>
  <rect x="14.4" y="3.6" width="7" height="16.8" rx="2.4"/>
  <path d="M12 5.6v12.8" opacity="0.55"/>
  <path d="M5.6 8.4h3.6" stroke="#008000"/>
  <path d="M5.6 12h2.2" opacity="0.55"/>
  <path d="M5.6 15.6h3.6" opacity="0.3"/>
  <path d="M14.4 8.4h3.6" opacity="0.3"/>
  <path d="M14.4 12h2.2" stroke="#ff0000"/>
  <path d="M14.4 15.6h3.6" opacity="0.55"/>
"""

GLYPH["color-converter"] = """
  <path d="M12 21.6a9.6 9.6 0 1 1 9.6-9.6c0 2.1-1.7 3.8-3.8 3.8h-1.7a2.3 2.3 0 0 0-1.7 3.9c.4.4.6.9.6 1.4 0 .5-.5.5-1.2.5z"/>
  <circle cx="7.6" cy="11.4" r="1.5" fill="#ff00ff" stroke="none"/>
  <circle cx="10.6" cy="7.2" r="1.5" fill="#00ffff" stroke="none"/>
  <circle cx="15.6" cy="8.2" r="1.5" fill="#ffff00" stroke="none"/>
  <circle cx="7.6" cy="16.4" r="1.5" fill="#e6e6e6" stroke="none" opacity="0.55"/>
"""

GLYPH["unit-converter"] = """
  <rect x="3" y="3.6" width="18" height="6.8" rx="2.2"/>
  <path d="M7.4 7v3.4" opacity="0.55"/>
  <path d="M12 7v3.4" opacity="0.55"/>
  <path d="M16.6 7v3.4" opacity="0.55"/>
  <path d="M4 17.4h16"/>
  <path d="M7.4 14.6L4 17.4l3.4 2.8"/>
  <path d="M16.6 14.6L20 17.4l-3.4 2.8"/>
"""

GLYPH["time-converter"] = """
  <rect x="3" y="4.6" width="18" height="16.4" rx="3"/>
  <path d="M3 9.8h18" opacity="0.55"/>
  <path d="M8.2 2.8v3.6"/>
  <path d="M15.8 2.8v3.6"/>
  <path d="M8.6 14.6h6.4"/>
  <path d="M12.8 12.3l2.3 2.3-2.3 2.3"/>
"""

GLYPH["rmb-upper"] = """
  <rect x="2.6" y="5.8" width="18.8" height="12.4" rx="2.6"/>
  <path d="M5.4 9v6" opacity="0.3"/>
  <path d="M18.6 9v6" opacity="0.3"/>
  <path d="M9 8.8l3 3.2 3-3.2"/>
  <path d="M12 12v4.2"/>
  <path d="M9.6 13.2h4.8"/>
  <path d="M9.6 15h4.8"/>
"""

GLYPH["batch-rename"] = """
  <path d="M3.4 5.4h8.6"/>
  <path d="M3.4 9.8h5.4" opacity="0.55"/>
  <path d="M3.4 14.2h3.6" opacity="0.3"/>
  <path d="M21.6 11a2.2 2.2 0 0 0-3.1-3.1l-8.3 8.3-.9 4 4-.9z"/>
  <path d="M17.4 9.4l3.1 3.1"/>
"""

GLYPH["text-encoder"] = """
  <path d="M8.6 7.4L3.4 12l5.2 4.6"/>
  <path d="M15.4 7.4L20.6 12l-5.2 4.6"/>
  <path d="M13.6 4.6l-3.2 14.8"/>
"""

GLYPH["type-trainer"] = """
  <path d="M6.4 3.4h4.8"/>
  <path d="M6.4 6.4h8.6" opacity="0.55"/>
  <rect x="2.6" y="9.4" width="18.8" height="11.4" rx="2.8"/>
  <path d="M6.6 13.4h10.8" opacity="0.55"/>
  <path d="M6.6 17.2h6.6" opacity="0.55"/>
"""

GLYPH["hanzi-copybook"] = """
  <rect x="3.2" y="3.2" width="17.6" height="17.6" rx="2.4"/>
  <path d="M12 3.4v17.2" opacity="0.3"/>
  <path d="M3.4 12h17.2" opacity="0.3"/>
  <path d="M7.2 8h9.6"/>
  <path d="M12 8v8.4"/>
  <path d="M8.6 12.8h6.8" opacity="0.55"/>
"""

GLYPH["minesweeper"] = """
  <rect x="3" y="3" width="18" height="18" rx="2.6"/>
  <path d="M9 3.4v17.2" opacity="0.3"/>
  <path d="M15 3.4v17.2" opacity="0.3"/>
  <path d="M3.4 9h17.2" opacity="0.3"/>
  <path d="M3.4 15h17.2" opacity="0.3"/>
  <circle cx="12" cy="12" r="2.1"/>
  <path d="M12 8.7v1M12 14.3v1M8.7 12h1M14.3 12h1" opacity="0.55"/>
"""

GLYPH["emoji-search"] = """
  <circle cx="10.6" cy="10.6" r="7.4"/>
  <path d="M16.2 16.2l5.4 5.4"/>
  <circle cx="8" cy="9.2" r=".95" fill="#e6e6e6" stroke="none"/>
  <circle cx="13.2" cy="9.2" r=".95" fill="#e6e6e6" stroke="none"/>
  <path d="M7.5 12.6a4.3 4.3 0 0 0 6.2 0"/>
"""

GLYPH["qrcode"] = """
  <rect x="3" y="3" width="6.6" height="6.6" rx="1.4"/>
  <circle cx="6.3" cy="6.3" r="1.3" fill="#e6e6e6" stroke="none"/>
  <rect x="3" y="14.4" width="6.6" height="6.6" rx="1.4"/>
  <circle cx="6.3" cy="17.7" r="1.3" fill="#e6e6e6" stroke="none"/>
  <rect x="14.4" y="3" width="6.6" height="6.6" rx="1.4"/>
  <circle cx="17.7" cy="6.3" r="1.3" fill="#e6e6e6" stroke="none"/>
  <path d="M14.4 14.4h2.3v2.3h-2.3z" fill="#e6e6e6" stroke="none" opacity="0.55"/>
  <path d="M18.7 14.4h2.3v2.3h-2.3z" fill="#e6e6e6" stroke="none" opacity="0.55"/>
  <path d="M14.4 18.7h2.3v2.3h-2.3z" fill="#e6e6e6" stroke="none" opacity="0.55"/>
"""

GLYPH["markdown-preview"] = """
  <rect x="2.6" y="5.6" width="18.8" height="12.8" rx="2.8"/>
  <path d="M6.4 15.2V8.4l2.8 3.6 2.8-3.6v6.8"/>
  <path d="M15.4 8.4v6.8"/>
  <path d="M13.4 13.2l2 2 2-2"/>
"""

GLYPH["md-table-converter"] = """
  <rect x="2.8" y="4.6" width="18.4" height="14.8" rx="2.6"/>
  <path d="M2.8 9.4h18.4"/>
  <path d="M2.8 14.2h18.4" opacity="0.55"/>
  <path d="M9.4 9.4v10" opacity="0.55"/>
  <path d="M15.4 9.4v10" opacity="0.55"/>
"""

GLYPH["pdf-toolkit"] = """
  <path d="M6.6 2.6h7.4L20.4 8.8V15a1.6 1.6 0 0 1-1.6 1.6H6.6A1.6 1.6 0 0 1 5 15V4.2a1.6 1.6 0 0 1 1.6-1.6z"/>
  <path d="M13.6 2.8v6.2h6.2"/>
  <path d="M2.8 17.4h18.4" stroke-dasharray="2.6 2.4" opacity="0.55"/>
"""

GLYPH["mindmap"] = """
  <rect x="9.4" y="9.4" width="5.2" height="5.2" rx="1.8"/>
  <path d="M9.4 10.6H7a2.2 2.2 0 0 1-2.2-2.2V7.4"/>
  <path d="M9.4 13.4H7a2.2 2.2 0 0 0-2.2 2.2v1.4"/>
  <path d="M14.6 10.6h2.4a2.2 2.2 0 0 0 2.2-2.2V7.4"/>
  <path d="M14.6 13.4h2.4a2.2 2.2 0 0 1 2.2 2.2v1.4"/>
  <circle cx="4.6" cy="5.6" r="1.8"/>
  <circle cx="4.6" cy="18.6" r="1.8"/>
  <circle cx="19.4" cy="5.6" r="1.8"/>
  <circle cx="19.4" cy="18.6" r="1.8"/>
"""


def main():
    made = []
    for name, body in sorted(GLYPH.items()):
        d = ROOT / name
        if not d.is_dir():
            print(f"  !! {name}: 没有这个插件目录")
            continue
        (d / "icon.svg").write_text(HEAD + body + "</svg>\n", encoding="utf-8")
        made.append(name)
    print(f"写入 {len(made)} 个字形源")


if __name__ == "__main__":
    main()
