#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
hanzi-copybook 数据生成器（可重复运行，用于更新字表 / 笔顺 / 拼音库）。

产出：
  frontend/data/words.js        年级字表（按人教版统编版《写字表》上/下册边界切分）
  frontend/data/strokes.bin.js  笔顺数据：token-delta-base36 编码 → gzip → base64
  frontend/lib/pinyin-pro.js    拼音库（UMD，浏览器直接可用）
  frontend/lib/hanzi-writer.js  笔顺动画库

用法：
  python tools/gen_data.py            # 使用默认路径
  python tools/gen_data.py --np-dir <node_modules 路径>

数据来源：
  字表  https://github.com/zispace/hanzi-chars-ext  人教版《义务教育教科书·语文》（统编版）写字表
  笔顺  npm hanzi-writer-data@2.0.1（Make Me a Hanzi），经 jsdelivr 逐字拉取
"""
import argparse
import base64
import gzip
import json
import os
import re
import shutil
import sys
import urllib.parse
import urllib.request
from concurrent.futures import ThreadPoolExecutor

HERE = os.path.dirname(os.path.abspath(__file__))
PLUGIN = os.path.dirname(HERE)
DATA_DIR = os.path.join(PLUGIN, "frontend", "data")
LIB_DIR = os.path.join(PLUGIN, "frontend", "lib")

CHARSET_BASE = "https://cdn.jsdelivr.net/gh/zispace/hanzi-chars-ext@main/"
XIEZI_REL = "地区字表/中国大陆人民教育出版社《义务教育教科书·语文》（统编版）写字表.txt"
STROKE_CDN = "https://cdn.jsdelivr.net/npm/hanzi-writer-data@2.0.1/"

# 每册最后一个字（来自字表表头的 "起始～结束" 注释），用于按教材顺序切分年级
VOLUMES = ["厂", "房", "路", "通", "险", "偏", "溉", "拆", "某", "憎", "陡", "怖"]
VOLUME_NAMES = ["一年级上", "一年级下", "二年级上", "二年级下", "三年级上", "三年级下",
                "四年级上", "四年级下", "五年级上", "五年级下", "六年级上", "六年级下"]

B36 = "0123456789abcdefghijklmnopqrstuvwxyz"
NUM_RE = re.compile(r"-?\d+(?:\.\d+)?")
B36_RE = re.compile(r"-?[0-9a-z]+")


def i2b(n):
    if n == 0:
        return "0"
    neg = n < 0
    n = abs(n)
    s = ""
    while n:
        s = B36[n % 36] + s
        n //= 36
    return ("-" + s) if neg else s


def b2i(s):
    neg = s.startswith("-")
    if neg:
        s = s[1:]
    v = 0
    for c in s:
        v = v * 36 + B36.index(c)
    return -v if neg else v


def enc_path(path):
    """数字 token 按奇偶分两条流做 delta + base36；命令字母原样保留。

    安全性：SVG 路径命令元数均为偶数（M/L=2, Q=4, C=6, Z=0），因此 token 下标
    奇偶在整笔内恒定对应 x / y，无需解析路径语义即可无损还原。
    """
    toks = path.split()
    out = []
    px = py = 0
    for i, t in enumerate(toks):
        if not NUM_RE.fullmatch(t):
            out.append(t)
            continue
        v = int(round(float(t)))
        if i % 2 == 0:
            out.append(i2b(v - px))
            px = v
        else:
            out.append(i2b(v - py))
            py = v
    return " ".join(out)


def dec_path(s):
    toks = s.split()
    out = []
    px = py = 0
    for i, t in enumerate(toks):
        if not B36_RE.fullmatch(t):
            out.append(t)
            continue
        d = b2i(t)
        if i % 2 == 0:
            px += d
            out.append(str(px))
        else:
            py += d
            out.append(str(py))
    return " ".join(out)


def enc_medians(medians):
    out = []
    for st in medians:
        px = py = 0
        parts = []
        for pt in st:
            x, y = int(pt[0]), int(pt[1])
            parts.append(i2b(x - px))
            parts.append(i2b(y - py))
            px, py = x, y
        out.append(" ".join(parts))
    return "|".join(out)


def fetch(url, timeout=30):
    req = urllib.request.Request(url, headers={"User-Agent": "Mozilla/5.0"})
    with urllib.request.urlopen(req, timeout=timeout) as r:
        return r.read()


def parse_chars(text):
    out = []
    for line in text.splitlines():
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        for ch in line:
            if "\u4e00" <= ch <= "\u9fff":
                out.append(ch)
    return out


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--np-dir", default=os.path.join(os.path.dirname(PLUGIN), "node_modules"),
                    help="node_modules 路径（用于拷贝 pinyin-pro / hanzi-writer）")
    args = ap.parse_args()

    os.makedirs(DATA_DIR, exist_ok=True)
    os.makedirs(LIB_DIR, exist_ok=True)

    # ---- 1. 写字表 ----
    url = CHARSET_BASE + "/".join(urllib.parse.quote(s) for s in XIEZI_REL.split("/"))
    print("拉取写字表 ...")
    xiezi = parse_chars(fetch(url).decode("utf-8"))
    print("  共 %d 字（去重 %d）" % (len(xiezi), len(set(xiezi))))

    # ---- 2. 按边界字切分年级 ----
    groups, cursor = [], 0
    for name, boundary in zip(VOLUME_NAMES, VOLUMES):
        try:
            idx = xiezi.index(boundary, cursor)
        except ValueError:
            sys.exit("错误：字表中找不到边界字 %s（数据源可能已更新，请核对 VOLUMES）" % boundary)
        groups.append((name, xiezi[cursor:idx + 1]))
        cursor = idx + 1
    if cursor != len(xiezi):
        print("  警告：边界切分后剩余 %d 字未归类" % (len(xiezi) - cursor))

    grades = []
    for g in range(1, 7):
        up, down = groups[(g - 1) * 2][1], groups[(g - 1) * 2 + 1][1]
        merged = list(dict.fromkeys(up + down))
        grades.append({"grade": g, "up": up, "down": down, "chars": merged})
        print("  %d 年级: 上 %d / 下 %d / 去重 %d" % (g, len(up), len(down), len(merged)))

    words = {
        "source": "人教版《义务教育教科书·语文》（统编版）写字表",
        "note": "按教材顺序与上/下册末字边界切分，共 %d 字" % len(xiezi),
        "grades": grades,
    }
    with open(os.path.join(DATA_DIR, "words.js"), "w", encoding="utf-8") as f:
        f.write("window.QD_WORDS=" + json.dumps(words, ensure_ascii=False, separators=(",", ":")) + ";\n")
    print("  写入 words.js")

    # ---- 3. 笔顺数据 ----
    want = sorted(set(xiezi))

    def get(ch):
        for _ in range(3):
            try:
                d = json.loads(fetch(STROKE_CDN + urllib.parse.quote(ch) + ".json").decode("utf-8"))
                return ch, (d if d.get("strokes") else None)
            except Exception:
                pass
        return ch, None

    print("拉取笔顺数据（%d 字）..." % len(want))
    strokes, missing = {}, []
    with ThreadPoolExecutor(max_workers=16) as ex:
        for n, (ch, d) in enumerate(ex.map(get, want), 1):
            (strokes.__setitem__(ch, d) if d else missing.append(ch))
            if n % 500 == 0:
                print("  %d/%d" % (n, len(want)))
    print("  取到 %d，缺失 %d" % (len(strokes), len(missing)))
    if missing:
        print("  缺失字：" + "".join(missing))

    encoded = {ch: [[enc_path(s) for s in v["strokes"]], enc_medians(v.get("medians", []))]
               for ch, v in strokes.items()}
    payload = json.dumps(encoded, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
    gz = gzip.compress(payload, 9)
    b64 = base64.b64encode(gz).decode("ascii")
    with open(os.path.join(DATA_DIR, "strokes.bin.js"), "w", encoding="utf-8") as f:
        f.write("window.QD_STROKES_B64=\"%s\";\n" % b64)
    print("  strokes.bin.js: 原始 %.1f MB → gzip+b64 %.1f MB"
          % (len(payload) / 1048576, len(b64) / 1048576))

    # ---- 4. 第三方库 ----
    for src, dst in [("pinyin-pro/dist/index.js", "pinyin-pro.js"),
                     ("hanzi-writer/dist/hanzi-writer.min.js", "hanzi-writer.js")]:
        p = os.path.join(args.np_dir, src.replace("/", os.sep))
        if os.path.isfile(p):
            shutil.copyfile(p, os.path.join(LIB_DIR, dst))
            print("  拷贝 %s" % dst)
        else:
            print("  警告：找不到 %s（先 npm i pinyin-pro hanzi-writer 或传 --np-dir）" % p)

    print("完成。")


if __name__ == "__main__":
    main()
