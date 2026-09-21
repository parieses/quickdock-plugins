/* 字帖打印 · 前端逻辑
 *
 * 数据链路：
 *   strokes.bin.js = base64(gzip(token-delta-base36 编码的笔顺 JSON))
 *   启动时用 DecompressionStream('gzip') 解出编码态 JSON，单字按需解码并缓存，
 *   避免一次性解析 2484 字的全部路径。
 *
 * 坐标系统：Make Me a Hanzi 为 y 向上，范围 x∈[0,1024] y∈[-124,900]，
 *   故 SVG 用 viewBox="0 0 1024 1024" + transform="translate(0,900) scale(1,-1)"。
 */
(function () {
  'use strict';

  // ---------- 常量 ----------
  var PAGE_W = 210, PAGE_H = 297;      // A4 mm
  var MARGIN = 12;                      // 页边距 mm
  var HEAD_H = 12, HEAD_GAP = 4;        // 页眉高度与间距 mm
  var CONTENT_W = PAGE_W - MARGIN * 2;  // 正文宽 186mm
  var USABLE_H = PAGE_H - MARGIN * 2 - HEAD_H - HEAD_GAP;
  var BLK_GAP = 2.4;                    // 字块（一行一个字）之间的间距 mm，与 app.css 中 .cp-page-body 的 gap 一致
  var HEAD_ROW_GAP = 0.8;               // 辅助行（拼音 + 笔顺）与练习格行之间的间距 mm，与 .cp-blk 的 gap 一致
  var PY_RATIO = 0.27;                  // 拼音字号 / 格子边长
  var PY_LH = 1.15;                     // 拼音行高倍数
  var PY_CH_W = 0.70;                   // 拼音平均字宽 / 字号 —— 宁可估大：估大只让笔顺条右侧留白，估小会把条挤成两行
  var PY_GAP = 1.2;                     // 拼音与笔顺条之间的间距 mm，与 app.css 中 .cp-head 的 gap 一致
  var SW_RATIO = 0.30;                  // 笔顺小格边长上限 / 格子边长
  var SW_MIN_RATIO = 0.14;              // 笔顺小格下限（笔画极多时不再继续缩）
  var SGAP = 0.5;                       // 笔顺小格间距 mm，与 app.css 中 .cp-strokes 的 gap 一致
  var CELL_GAP = 0.6;                   // 同行相邻格子的间距 mm，与 app.css 中 .cp-row 的 gap 一致
  var MAX_STEPS = 20;                   // 笔顺分解最多显示步数
  var LS_KEY = 'hanzi-copybook-opts';
  var B36 = '0123456789abcdefghijklmnopqrstuvwxyz';

  // ---------- 沙箱安全的存储（iframe 无 allow-same-origin，localStorage 会抛错）----------
  var store = {
    get: function (k) { try { return window.localStorage.getItem(k); } catch (e) { return null; } },
    set: function (k, v) { try { window.localStorage.setItem(k, v); } catch (e) { /* 沙箱禁用，忽略 */ } }
  };

  // ---------- 元素 ----------
  var $ = function (id) { return document.getElementById(id); };
  var els = {
    pages: $('pages'), main: $('main'), modal: $('modal'), mStage: $('mStage'),
    mTitle: $('mTitle'), mInfo: $('mInfo'),
    txt: $('txtInput'), charCount: $('charCount'), countInfo: $('countInfo'),
    pageInfo: $('pageInfo'), gradeCount: $('gradeCount'),
    selGrade: $('selGrade'), selVolume: $('selVolume'),
    selGrid: $('selGrid'), selInk: $('selInk'),
    selPractice: $('selPractice'), inpTitle: $('inpTitle'), layoutHint: $('layoutHint'),
    chkPinyin: $('chkPinyin'), chkStroke: $('chkStroke'),
    chkName: $('chkName'), chkDedupe: $('chkDedupe'), chkTrace: $('chkTrace'),
    btnClear: $('btnClear'), btnGradeAdd: $('btnGradeAdd'), btnGradeReplace: $('btnGradeReplace'),
    btnExport: $('btnExport'), btnPrint: $('btnPrint'),
    btnZoomIn: $('btnZoomIn'), btnZoomOut: $('btnZoomOut'),
    mClose: $('mClose'), mPlay: $('mPlay'), mPrev: $('mPrev'), mNext: $('mNext')
  };

  // ---------- 状态 ----------
  // 排版模型：一行恰好一个字块。
  //   字块 = 辅助行（拼音 + 笔顺分解条，横排） + 练习格行（1 个范字格 + N 个练习格，铺满正文宽）
  // N = 「每字练习格」= 唯一的版面参数：N 越大格子越小、一页装越多字。
  var st = {
    chars: [],
    grid: 'tian', ink: 'trace', practice: 5, trace: true,
    pinyin: true, stroke: true, showName: true, dedupe: true, title: '汉字描红字帖'
  };
  var zoom = 1;

  // ---------- 笔顺数据 ----------
  var RAW = null;        // 编码态 { ch: [encPaths[], encMedians] }
  var CACHE = {};        // 解码缓存 { ch: {strokes, medians} }
  var readErr = '';

  function b36dec(s) {
    var neg = s.charAt(0) === '-';
    if (neg) s = s.slice(1);
    var v = 0;
    for (var i = 0; i < s.length; i++) v = v * 36 + B36.indexOf(s.charAt(i));
    return neg ? -v : v;
  }
  var NUMTOK = /^-?\d+(?:\.\d+)?$/;
  var B36TOK = /^-?[0-9a-z]+$/;

  // 数字 token 按奇偶分两条流还原（编解码严格互逆，见 tools/gen_data.py）
  function decPath(s) {
    var toks = s.split(' '), out = [], px = 0, py = 0;
    for (var i = 0; i < toks.length; i++) {
      var t = toks[i];
      if (!B36TOK.test(t)) { out.push(t); continue; }
      var d = b36dec(t);
      if (i % 2 === 0) { px += d; out.push(String(px)); }
      else { py += d; out.push(String(py)); }
    }
    return out.join(' ');
  }

  function decMedians(s) {
    if (!s) return [];
    return s.split('|').map(function (one) {
      var n = one.split(' '), px = 0, py = 0, pts = [];
      for (var i = 0; i + 1 < n.length; i += 2) {
        px += b36dec(n[i]); py += b36dec(n[i + 1]);
        pts.push([px, py]);
      }
      return pts;
    });
  }

  function charData(ch) {
    if (CACHE[ch]) return CACHE[ch];
    var r = RAW && RAW[ch];
    if (!r) return null;
    var d = { strokes: r[0].map(decPath), medians: decMedians(r[1]) };
    CACHE[ch] = d;
    return d;
  }

  function loadStrokes() {
    var b64 = window.QD_STROKES_B64 || '';
    if (!b64) { readErr = '缺少笔顺数据文件'; return Promise.resolve(); }
    if (typeof DecompressionStream !== 'function') {
      readErr = '当前运行环境不支持 DecompressionStream，笔顺数据无法解压';
      return Promise.resolve();
    }
    try {
      var bin = atob(b64);
      var bytes = new Uint8Array(bin.length);
      for (var i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
      var stream = new Blob([bytes]).stream().pipeThrough(new DecompressionStream('gzip'));
      return new Response(stream).text().then(function (txt) {
        RAW = JSON.parse(txt);
      })['catch'](function (e) {
        readErr = '笔顺数据解压失败：' + (e && e.message ? e.message : e);
      });
    } catch (e) {
      readErr = '笔顺数据解析失败：' + (e && e.message ? e.message : e);
      return Promise.resolve();
    }
  }

  // ---------- 拼音 ----------
  var PY = {};
  function pinyinOf(ch) {
    if (ch in PY) return PY[ch];
    var v = '';
    try {
      var pp = window.pinyinPro;
      var fn = pp && (pp.pinyin || (pp['default'] && pp['default'].pinyin));
      if (fn) v = fn(ch, { toneType: 'symbol', type: 'string' }) || '';
    } catch (e) { v = ''; }
    PY[ch] = v;
    return v;
  }

  // ---------- 文本解析 ----------
  var HANZI = /[\u3400-\u4dbf\u4e00-\u9fff\uf900-\ufaff]/;
  function extractChars(text) {
    var out = [], seen = {};
    for (var i = 0; i < text.length; i++) {
      var ch = text.charAt(i);
      if (!HANZI.test(ch)) continue;
      if (st.dedupe) {
        if (seen[ch]) continue;
        seen[ch] = 1;
      }
      out.push(ch);
    }
    return out;
  }

  // ---------- 几何 ----------
  // 一行 = 一个字块。练习格行 = 1 范字格 + practice 个练习格，铺满正文宽。
  // ⚠️ 必须把行内 gap 从正文宽里扣掉：否则总宽 = CONTENT_W + 格数*gap 会超出纸面，
  // 被 .cp-page 的 overflow:hidden 从右侧裁掉（flex 项受 min-width:auto/min-content 限制不会自动收缩）。
  function geo() {
    var cellsPerRow = 1 + st.practice;
    var cell = (CONTENT_W - CELL_GAP * st.practice) / cellsPerRow;
    return { cell: cell, cellsPerRow: cellsPerRow, bodyH: USABLE_H };
  }

  // 辅助行里的笔顺分解条：紧跟在拼音右侧，在「正文宽 - 拼音实占」的剩余宽度内排布。
  // 笔画多时先缩小小格，缩到下限才换行 —— 这样绝大多数字只占一行辅助行。
  function strokeLayout(ch, g) {
    var empty = { n: 0, sw: 0, rows: 0, h: 0 };
    if (!st.stroke) return empty;
    var d = charData(ch);
    var n = d ? Math.min(d.strokes.length, MAX_STEPS) : 0;
    if (!n) return empty;

    var pyW = 0;
    if (st.pinyin) {
      var py = pinyinOf(ch);
      if (py) pyW = py.length * g.cell * PY_RATIO * PY_CH_W + PY_GAP;
    }
    var avail = Math.max(g.cell * 0.6, CONTENT_W - pyW);   // 拼音再长也给笔顺条留一点地
    var sw = Math.min(g.cell * SW_RATIO, (avail - SGAP * (n - 1)) / n);
    if (sw < g.cell * SW_MIN_RATIO) sw = g.cell * SW_MIN_RATIO;
    var perLine = Math.max(1, Math.floor((avail + SGAP) / (sw + SGAP)));
    var rows = Math.ceil(n / perLine);
    return { n: n, sw: sw, rows: rows, h: rows * sw + (rows - 1) * SGAP };
  }

  // 辅助行高度：拼音行与笔顺条取高者，+0.5mm 作为估算误差余量
  // （块高宁可算大：算大会提前分页留白，算小会让最后一页溢出被裁）
  function headH(ch, g) {
    var sl = strokeLayout(ch, g);
    var pyH = st.pinyin ? g.cell * PY_RATIO * PY_LH : 0;
    return Math.max(pyH, sl.h) + 0.5;
  }

  function blockH(ch, g) {
    return headH(ch, g) + HEAD_ROW_GAP + g.cell + BLK_GAP;
  }

  function paginate(g) {
    var pages = [], cur = [], used = 0;
    for (var i = 0; i < st.chars.length; i++) {
      var h = blockH(st.chars[i], g);
      // 单个字块就超过一页时也必须收下，否则死循环
      if (cur.length && used + h > g.bodyH) { pages.push(cur); cur = []; used = 0; }
      cur.push(st.chars[i]); used += h;
    }
    pages.push(cur);
    return pages;
  }

  // ---------- 渲染 ----------
  function esc(s) {
    return String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
  }

  var INK_COLOR = { trace: '#cfcfcf', gray: '#8a8a8a', ink: '#1a1a1a' };
  // 练习格里的描红范字：比范字格再浅一档，既够看清笔形，又不会喧宾夺主。
  // 字帖的标准做法就是每个练习格都印上浅灰范字，让整行都能描。
  var PRAC_INK = '#dedede';

  function gridClass() { return st.grid === 'none' ? 'cp-grid-none' : 'cp-grid-' + st.grid; }

  function strokeStepSvg(strokes, upto) {
    var parts = '';
    for (var i = 0; i < strokes.length; i++) {
      parts += '<path d="' + strokes[i] + '" fill="' + (i < upto ? '#20242b' : '#e6e6e6') + '"/>';
    }
    return '<svg viewBox="0 0 1024 1024"><g transform="translate(0,900) scale(1,-1)">' + parts + '</g></svg>';
  }

  function blockHtml(ch, g) {
    var sl = strokeLayout(ch, g);
    var style = '--cp-headh:' + headH(ch, g).toFixed(3) + 'mm';
    if (sl.n) style += ';--cp-sw:' + sl.sw.toFixed(3) + 'mm';

    var html = '<div class="cp-blk" style="' + style + '">';

    // ---- 辅助行：拼音在前，笔顺分解条紧随其后 ----
    if (st.pinyin || st.stroke) {
      html += '<div class="cp-head">';
      if (st.pinyin) html += '<span class="cp-py">' + esc(pinyinOf(ch)) + '</span>';
      if (st.stroke) {
        if (sl.n) {
          var d = charData(ch);
          html += '<span class="cp-strokes">';
          for (var k = 1; k <= sl.n; k++) {
            html += '<span class="cp-sc" title="第 ' + k + ' 笔">' + strokeStepSvg(d.strokes, k) +
              '<i>' + k + '</i></span>';
          }
          if (d.strokes.length > MAX_STEPS) {
            html += '<span class="cp-nostroke">…共 ' + d.strokes.length + ' 笔</span>';
          }
          html += '</span>';
        } else {
          html += '<span class="cp-strokes"><span class="cp-nostroke">该字暂无笔顺数据</span></span>';
        }
      }
      html += '</div>';
    }

    // ---- 练习格行：1 个范字格 + N 个练习格 ----
    html += '<div class="cp-row">';
    html += '<span class="cp-cell cp-click ' + gridClass() + '" data-ch="' + esc(ch) +
      '" title="点击播放笔顺动画"><span class="cp-ch" style="color:' + INK_COLOR[st.ink] +
      '">' + esc(ch) + '</span></span>';
    for (var p = 0; p < st.practice; p++) {
      // 练习格描红：印上浅灰范字。关掉则是纯空格（自己默写用）。
      html += '<span class="cp-cell ' + gridClass() + '">' +
        (st.trace ? '<span class="cp-ch" style="color:' + PRAC_INK + '">' + esc(ch) + '</span>' : '') +
        '</span>';
    }
    html += '</div></div>';
    return html;
  }

  function pageHtml(chars, pageNo, totalPages, g) {
    var head = '<div class="cp-page-head"><span class="cp-ph-title">' + esc(st.title) + '</span>';
    if (st.showName) {
      head += '<span class="cp-ph-meta"><span>姓名 <u></u></span><span>日期 <u></u></span></span>';
    }
    head += '<span class="cp-ph-meta"><span>第 ' + pageNo + ' / ' + totalPages + ' 页</span></span></div>';

    var body = chars.map(function (ch) { return blockHtml(ch, g); }).join('');
    if (!chars.length) body = '<div class="cp-empty-page"></div>';

    var style = '--cp-cell:' + g.cell.toFixed(3) + 'mm;--cp-pyfs:' + (g.cell * PY_RATIO).toFixed(3) +
      'mm;--cp-blkgap:' + BLK_GAP.toFixed(3) + 'mm';
    return '<div class="cp-page" style="' + style + '">' + head +
      '<div class="cp-page-body">' + body + '</div></div>';
  }

  // 版面提示：让用户直接看到「几格 = 格子多大 = 一页几个字」，避免调参数时预期落空
  function updateLayoutHint(g, pageCount) {
    if (!els.layoutHint) return;
    var perPage = pageCount ? Math.round(st.chars.length / pageCount) : 0;
    els.layoutHint.textContent = '一行 1 字 · 1 范字 + ' + st.practice + ' 练习格（共 ' +
      g.cellsPerRow + ' 格）· 格子 ' + g.cell.toFixed(1) + 'mm' +
      (perPage ? ' · 每页约 ' + perPage + ' 字' : '') +
      (g.cell < 6 ? '（格子偏小）' : '');
  }

  function render() {
    var g = geo();
    var pages = paginate(g);
    var html = '';
    for (var i = 0; i < pages.length; i++) {
      html += pageHtml(pages[i], i + 1, pages.length, g);
    }
    els.pages.innerHTML = html;

    var perPage = pages.length ? Math.round(st.chars.length / pages.length) : 0;
    els.charCount.textContent = st.chars.length + ' 字';
    els.countInfo.textContent = st.chars.length
      ? ('共 ' + st.chars.length + ' 字 · 每页约 ' + perPage + ' 字') : '';
    els.pageInfo.textContent = pages.length + ' 页';
    updateLayoutHint(g, pages.length);
    // ⚠️ 绝不可在此回写 els.txt.value：
    // ① 输入法组字期间 input 事件持续触发，回写会把未上屏的拼音清掉、直接打断组字，
    //    表现为「汉字根本打不进去」；
    // ② extractChars 只保留汉字，回写会把标点/空格/字母也一并抹掉。
    // textarea 保留用户原文，st.chars 只是派生结果，去重只作用于纸面。
    applyZoom();
  }

  function applyZoom() {
    if (!els.pages.firstChild) return;
    var avail = els.main.clientWidth - 40;
    var pagePx = PAGE_W * 96 / 25.4;
    var fit = avail / pagePx;
    els.pages.style.zoom = Math.max(0.2, Math.min(zoom, fit)).toFixed(4);
  }

  // ---------- 设置读写 ----------
  // 一行固定一个字块，所以「每字练习格」是唯一的版面参数：
  // 练习格行 = 1 范字格 + N 练习格 = 铺满正文宽，故 N 越大格子越小、一页装越多字。
  var PRAC_MIN = 1, PRAC_MAX = 13, PRAC_DEF = 5;

  function clampInt(v, lo, hi, def) {
    var n = parseInt(v, 10);
    if (!(n > 0)) n = def;
    if (n < lo) n = lo;
    if (n > hi) n = hi;
    return n;
  }

  function readUI() {
    st.grid = els.selGrid.value;
    st.ink = els.selInk.value;
    st.practice = clampInt(els.selPractice.value, PRAC_MIN, PRAC_MAX, PRAC_DEF);
    els.selPractice.value = String(st.practice);
    st.pinyin = els.chkPinyin.checked;
    st.stroke = els.chkStroke.checked;
    st.showName = els.chkName.checked;
    st.dedupe = els.chkDedupe.checked;
    st.trace = els.chkTrace.checked;
    st.title = els.inpTitle.value || '汉字描红字帖';
    st.chars = extractChars(els.txt.value);
  }

  function saveOpts() {
    store.set(LS_KEY, JSON.stringify({
      grid: st.grid, ink: st.ink, practice: st.practice, trace: st.trace,
      pinyin: st.pinyin, stroke: st.stroke, showName: st.showName,
      dedupe: st.dedupe, title: st.title
    }));
  }

  function loadOpts() {
    try {
      var o = JSON.parse(store.get(LS_KEY) || '{}');
      if (o.grid) els.selGrid.value = o.grid;
      if (o.ink) els.selInk.value = o.ink;
      // 旧存档里的 perRow / perRowChars（每行格子数或每行字数）已无意义：排版改为一行一字，
      // 只按「每字练习格」归一化，超出范围的旧值由 readUI 里的 clampInt 夹回来。
      if (o.practice) els.selPractice.value = o.practice;
      if (typeof o.pinyin === 'boolean') els.chkPinyin.checked = o.pinyin;
      if (typeof o.stroke === 'boolean') els.chkStroke.checked = o.stroke;
      if (typeof o.showName === 'boolean') els.chkName.checked = o.showName;
      if (typeof o.dedupe === 'boolean') els.chkDedupe.checked = o.dedupe;
      if (typeof o.trace === 'boolean') els.chkTrace.checked = o.trace;
      if (o.title) els.inpTitle.value = o.title;
    } catch (e) { /* 忽略损坏的存档 */ }
  }

  // ---------- 年级字表 ----------
  function words() { return window.QD_WORDS || null; }

  function gradeChars() {
    var w = words();
    if (!w) return [];
    var g = parseInt(els.selGrade.value, 10);
    var item = w.grades.filter(function (x) { return x.grade === g; })[0];
    if (!item) return [];
    var v = els.selVolume.value;
    if (v === 'up') return item.up;
    if (v === 'down') return item.down;
    return item.chars;
  }

  function refreshGradeCount() {
    var n = gradeChars().length;
    els.gradeCount.textContent = n ? (n + ' 字') : '';
  }

  // ---------- 导出 / 打印 ----------
  function stamp() {
    var d = new Date();
    var p = function (n) { return (n < 10 ? '0' : '') + n; };
    return d.getFullYear() + p(d.getMonth() + 1) + p(d.getDate()) + '-' + p(d.getHours()) + p(d.getMinutes());
  }

  function collectCss() {
    var out = [];
    var styles = document.querySelectorAll('style');
    for (var i = 0; i < styles.length; i++) {
      var s = styles[i];
      if (s.id && s.id.indexOf('quickdock') === 0) continue;   // 排除宿主注入的 common.css
      out.push(s.textContent);
    }
    return out.join('\n');
  }

  var EXPORT_FORCE = [
    // 纸面永远是浅色，与宿主/系统主题无关。color-scheme 只声明 light，
    // 否则「跟随系统深色」的浏览器可能对这份固定浅色纸面做自动反转/强制深色，
    // 打开就是一片发暗或看不出内容。
    ':root{color-scheme:light only;}',
    'html,body{background:#fff !important;color:#000 !important;height:auto !important;min-height:0 !important;overflow:visible !important;}',
    'body{margin:0;padding:0;}',
    // Windows 高对比度模式会把 background-image（格子内线就是内联 SVG 背景）与
    // 自定义颜色一并抹掉，格子消失、文字与底色撞色 —— 显式关掉强制调色。
    // print-color-adjust:exact 则是为了 Ctrl+P 时保留格子内线（背景图形默认不打印）。
    '.cp-page,.cp-page *{forced-color-adjust:none !important;}',
    'html,body,.cp-page,.cp-page *{-webkit-print-color-adjust:exact !important;print-color-adjust:exact !important;}',
    // zoom 归 1：预览时的自适应缩放是行内样式，不能带进打印上下文
    '#pages{display:block !important;gap:0 !important;padding:0 !important;zoom:1 !important;}',
    '.cp-page{box-shadow:none !important;margin:0 auto !important;}',
    '.cp-click{cursor:default;}',
    '@page{size:A4;margin:0;}'
  ].join('\n');

  function exportHtml() {
    var inner = els.pages.innerHTML;
    return '<!DOCTYPE html>\n<html lang="zh-CN">\n<head>\n<meta charset="utf-8">\n' +
      '<meta name="color-scheme" content="light only">\n<title>' +
      esc(st.title) + '</title>\n<style>\n' + collectCss() + '\n' + EXPORT_FORCE +
      '\n</style>\n</head>\n<body>\n<div id="pages">' + inner +
      '</div>\n</body>\n</html>\n';
  }

  function toast(msg, ok) {
    var t = document.createElement('div');
    t.className = 'p-toast ' + (ok === false ? 'p-toast-error' : 'p-toast-success');
    t.textContent = msg;
    document.body.appendChild(t);
    setTimeout(function () { t.remove(); }, 2400);
  }

  // 纸面样式自检 —— 这是「导出的 HTML 打开是一张白纸」的唯一成因路径：
  // 纸面（格子尺寸、白底、边框）全靠 app.css，而 collectCss() 只能从文档里的
  // <style> 取样式（宿主会把 <link> 内联成无 id 的 <style>）。一旦宿主的 CSS
  // 内联失败（宿主只留一条 <!-- quickdock: css inline failed --> 注释），
  // collectCss() 就悄悄返回一堆不完整的 CSS：纸面没有 210mm 尺寸、没有白底，
  // 范字只剩行内的浅灰 #cfcfcf —— 白纸上的浅灰，看上去就是「什么都没有」。
  // 与其把这样一份白纸交给用户，不如明确报错。
  var CSS_MARK = '.cp-page';
  function cssLooksComplete(css) {
    return css.indexOf(CSS_MARK) >= 0 && css.indexOf('.cp-grid-tian') >= 0;
  }

  function doExport() {
    if (!st.chars.length) { toast('还没有汉字，先输入要练的字', false); return; }
    var html;
    try {
      html = exportHtml();
      if (!cssLooksComplete(collectCss())) {
        toast('插件样式未加载完整，导出的会是一张白纸；请刷新插件后重试', false);
        return;
      }
    } catch (e) {
      toast('导出失败：' + (e && e.message ? e.message : e), false);
      return;
    }
    try {
      var blob = new Blob([html], { type: 'text/html;charset=utf-8' });
      var url = URL.createObjectURL(blob);
      var a = document.createElement('a');
      a.href = url;
      a.download = '字帖-' + stamp() + '.html';
      document.body.appendChild(a);
      a.click();
      a.remove();
      setTimeout(function () { URL.revokeObjectURL(url); }, 5000);
      toast('已导出 HTML，可在浏览器中打开打印');
    } catch (e) {
      toast('导出失败：' + (e && e.message ? e.message : e), false);
    }
  }

  // 打印一律交给宿主：WebView2 / Chromium 的 window.print() 只作用于**顶层文档**，
  // 在插件 iframe 里自己调，打印的是整个 QuickDock 应用（暗色外壳），纸面内容根本
  // 不在打印上下文里 —— 结果就是「打印出来一张白纸」。宿主收到 HTML 后在顶层文档
  // 渲染再调系统打印，用的是与「导出 HTML」完全相同的那份 HTML。
  function doPrint() {
    if (!st.chars.length) { toast('还没有汉字，先输入要练的字', false); return; }
    if (typeof window.qdPrint !== 'function') {
      toast('当前环境未提供打印通道，请改用「导出 HTML」后在浏览器里打印', false);
      return;
    }
    var html;
    try {
      html = exportHtml();
    } catch (e) {
      toast('生成打印内容失败：' + (e && e.message ? e.message : e), false);
      return;
    }
    if (!html) { toast('没有可打印的内容', false); return; }
    if (!cssLooksComplete(collectCss())) {
      toast('插件样式未加载完整，打印出来会是一张白纸；请刷新插件后重试', false);
      return;
    }
    window.qdPrint({ html: html, page: 'A4' })['catch'](function (e) {
      toast('调用打印失败：' + (e && e.message ? e.message : e) +
        '，可改用「导出 HTML」后在浏览器里打印', false);
    });
  }

  // ---------- 笔顺动画 ----------
  var writer = null, animIdx = 0;

  function openAnim(ch) {
    if (!charData(ch)) { toast('「' + ch + '」暂无笔顺数据', false); return; }
    var idx = st.chars.indexOf(ch);
    animIdx = idx < 0 ? 0 : idx;
    els.modal.hidden = false;
    showAnim(st.chars[animIdx] || ch);
  }

  function showAnim(ch) {
    if (!charData(ch)) {
      var nxt = nextWithData(1);
      if (nxt === null) { els.mInfo.textContent = '无可播放的字'; return; }
      ch = nxt;
    }
    els.mTitle.textContent = '笔顺动画 · ' + ch;
    var d = charData(ch);
    els.mInfo.textContent = d ? (d.strokes.length + ' 笔') : '';
    els.mStage.innerHTML = '';
    if (typeof window.HanziWriter !== 'function') {
      els.mInfo.textContent = '未加载动画库';
      return;
    }
    writer = window.HanziWriter.create(els.mStage, ch, {
      width: 220, height: 220, padding: 6,
      showOutline: true,
      strokeColor: '#20242b',
      outlineColor: '#d8d8d8',
      radicalColor: '#2f86f0',
      charDataLoader: function (c, onComplete) {
        var dd = charData(c);
        if (dd) onComplete(dd); else onComplete(null);
      }
    });
    writer.animateCharacter();
  }

  function nextWithData(dir) {
    var n = st.chars.length;
    if (!n) return null;
    for (var i = 1; i <= n; i++) {
      var j = ((animIdx + dir * i) % n + n) % n;
      if (charData(st.chars[j])) { animIdx = j; return st.chars[j]; }
    }
    return null;
  }

  function closeAnim() {
    els.modal.hidden = true;
    els.mStage.innerHTML = '';
    writer = null;
  }

  // ---------- 事件 ----------
  var tmr = null, composing = false;

  // 输入法组字期间既不重排、也不允许任何东西回写 textarea，否则会打断组字，
  // 表现为「汉字根本打不进去」。e.isComposing 在部分浏览器上不够可靠，
  // 所以另外用 compositionstart/end 自己维护一个 composing 标志，两者取并集。
  function onInput(e) {
    if (composing || (e && e.isComposing)) return;
    clearTimeout(tmr);
    tmr = setTimeout(function () {
      if (composing) return;
      readUI();
      saveOpts();
      render();
    }, 160);
  }

  function bind() {
    els.txt.addEventListener('input', onInput);
    els.txt.addEventListener('compositionstart', function () { composing = true; });
    els.txt.addEventListener('compositionend', function () {
      composing = false;
      onInput();       // 组字结束补一次重排（不传事件，避免 isComposing 仍为 true 被跳过）
    });

    [els.selGrid, els.selInk, els.selPractice,
     els.chkPinyin, els.chkStroke, els.chkName, els.chkDedupe, els.chkTrace].forEach(function (el) {
      el.addEventListener('change', onInput);
    });

    els.inpTitle.addEventListener('input', onInput);

    els.btnClear.addEventListener('click', function () { els.txt.value = ''; onInput(); });

    els.btnGradeAdd.addEventListener('click', function () {
      var add = gradeChars().join('');
      if (!add) return;
      els.txt.value = els.txt.value + add;
      onInput();
    });
    els.btnGradeReplace.addEventListener('click', function () {
      var add = gradeChars().join('');
      if (!add) return;
      els.txt.value = add;
      onInput();
    });
    els.selGrade.addEventListener('change', refreshGradeCount);
    els.selVolume.addEventListener('change', refreshGradeCount);

    els.btnExport.addEventListener('click', doExport);
    els.btnPrint.addEventListener('click', doPrint);
    els.btnZoomIn.addEventListener('click', function () { zoom = Math.min(2, zoom + 0.1); applyZoom(); });
    els.btnZoomOut.addEventListener('click', function () { zoom = Math.max(0.3, zoom - 0.1); applyZoom(); });
    window.addEventListener('resize', applyZoom);

    els.pages.addEventListener('click', function (e) {
      var t = e.target;
      while (t && t !== els.pages) {
        if (t.classList && t.classList.contains('cp-click')) { openAnim(t.getAttribute('data-ch')); return; }
        t = t.parentNode;
      }
    });

    els.mClose.addEventListener('click', closeAnim);
    els.mPlay.addEventListener('click', function () { if (writer) writer.animateCharacter(); });
    els.mPrev.addEventListener('click', function () {
      var c = nextWithData(-1); if (c) showAnim(c);
    });
    els.mNext.addEventListener('click', function () {
      var c = nextWithData(1); if (c) showAnim(c);
    });
    els.modal.addEventListener('click', function (e) { if (e.target === els.modal) closeAnim(); });
    document.addEventListener('keydown', function (e) { if (e.key === 'Escape' && !els.modal.hidden) closeAnim(); });
  }

  // ---------- 启动 ----------
  function initGradeSelect() {
    var w = words();
    var opts = '';
    if (w && w.grades && w.grades.length) {
      w.grades.forEach(function (g) {
        opts += '<option value="' + g.grade + '">' + g.grade + ' 年级</option>';
      });
    } else {
      opts = '<option value="1">字表缺失</option>';
    }
    els.selGrade.innerHTML = opts;
  }

  function boot() {
    initGradeSelect();
    loadOpts();
    refreshGradeCount();
    bind();

    // 与 type-trainer 同样的教训：笔顺数据失败也不能让整个插件崩掉
    loadStrokes().then(function () {
      if (readErr) console.warn('[hanzi-copybook]', readErr);
      var w = words();
      if (w && w.grades && w.grades[0]) {
        els.txt.value = w.grades[0].chars.slice(0, 20).join('');
      } else {
        els.txt.value = '永字八法横竖撇捺';
      }
      readUI();
      render();
      if (readErr) toast(readErr, false);
    });
  }

  // 启动守护：任何初始化异常都显式暴露，避免"白屏 + 无任何提示"
  function safeBoot() {
    try {
      boot();
    } catch (e) {
      console.error('[hanzi-copybook] boot failed:', e);
      var m = '初始化失败：' + (e && e.message ? e.message : e);
      try { els.mStage && toast(m, false); } catch (_) { /* 忽略 */ }
    }
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', safeBoot);
  } else {
    safeBoot();
  }
})();
