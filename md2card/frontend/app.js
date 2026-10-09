(function () {
  'use strict';

  /* ---------- 沙箱安全：localStorage 在 opaque origin 下会抛错 ---------- */
  var LS = {
    get: function (k) { try { return window.localStorage.getItem(k); } catch (e) { return null; } },
    set: function (k, v) { try { window.localStorage.setItem(k, v); } catch (e) {} }
  };

  /* ---------- i18n ---------- */
  var L10N = {
    'en-US': {
      '模式': 'Mode', '主题': 'Theme', '模板': 'Template', '尺寸': 'Size', '宽度': 'Width', '字号': 'Font', '字体': 'Font family',
      '自动分页': 'Auto paginate', '页脚': 'Footer', '示例': 'Sample', '清空': 'Clear',
      '导出 HTML': 'Export HTML', '打印': 'Print', '导出长图': 'Export long image', '复制图片': 'Copy image',
      '正在生成长图…': 'Generating…', '已导出 PNG': 'PNG exported', '已导出 HTML': 'HTML exported',
      '已发送打印': 'Sent to print', '已复制到剪贴板': 'Copied to clipboard', '剪贴板不可用，已改为下载图片': 'Clipboard blocked, downloaded instead',
      '导出失败': 'Export failed', '截图库未加载，请刷新插件': 'Screenshot lib not loaded, refresh',
      '当前环境未提供打印通道，请改用「导出 HTML」': 'No print channel, use Export HTML',
      '图片文件已放入卡片': 'Image inserted'
    }
  };
  function L(k) {
    var loc = (document.documentElement.getAttribute('lang') || 'zh-CN');
    var map = L10N[loc] || L10N[loc.split('-')[0]] || {};
    return (map && map[k] !== undefined) ? map[k] : k;
  }

  /* ---------- Markdown 库 ---------- */
  function escapeHtml(s) {
    return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
  }
  if (window.marked) {
    marked.use({ renderer: { code: function (code, infostring) {
      var lang = (infostring || '').split(/\s+/)[0];
      var html = null;
      if (lang && window.hljs && hljs.getLanguage(lang)) { try { html = hljs.highlight(code, { language: lang }).value; } catch (e) {} }
      if (!html && window.hljs) { try { html = hljs.highlightAuto(code).value; } catch (e) {} }
      if (!html) html = escapeHtml(code);
      return '<pre><code class="hljs">' + html + '</code></pre>';
    } } });
    marked.setOptions({ gfm: true, breaks: true });
  }

  /* ---------- 主题（按调性分组，下拉用 optgroup） ---------- */
  var THEME_GROUPS = [
    { label: '浅色柔和', items: {
      warm: '温暖柔和', gray: '简约高级灰', mono: '极简黑白', morandi: '莫兰迪',
      sakura: '樱花粉', mint: '薄荷', nature: '清新自然', caramel: '焦糖',
      lemon: '柠檬黄', lavender: '薰衣草', persimmon: '柿子橙'
    } },
    { label: '深色质感', items: {
      dark: '暗黑科技', aurora: '极光', ocean: '海洋', business: '商务简报',
      indigo: '靛蓝', forest: '森林', graphite: '石墨', starry: '星空'
    } },
    { label: '缤纷渐变', items: {
      gradient: '梦幻渐变', xhs: '小红书', sunset: '晚霞', rosegold: '玫瑰金', chinesered: '中国红'
    } },
    { label: '材质 / 复古', items: {
      kraft: '牛皮纸', newspaper: '报纸风', parchment: '羊皮纸', bamboo: '竹青'
    } }
  ];
  var THEME_NAMES = {};
  THEME_GROUPS.forEach(function (g) {
    Object.keys(g.items).forEach(function (k) { THEME_NAMES[k] = g.items[k]; });
  });
  THEME_NAMES.custom = '自定义主题';

  /* ---------- 尺寸预设 ---------- */
  var SIZES = {
    free:   { w: 0,     ratio: 0 },       // 无固定比例，随内容
    xhs:    { w: 600,   ratio: 4 / 3 },   // 小红书 3:4
    mp:     { w: 900,   ratio: 9 / 16 },  // 公众号封面 16:9
    square: { w: 700,   ratio: 1 },       // 方图 1:1
    banner: { w: 1000,  ratio: 9 / 21 }   // 横幅 21:9
  };

  /* ---------- 文字模板（仅文字模式） ---------- */
  var TPL_NAMES = {
    paragraph: '段落', quote: '金句', title: '标题卡', note: '便签', announce: '公告',
    quote2: '引用卡', todo: '待办', timeline: '时间轴', steps: '步骤'
  };

  var DEMO_MD = [
    '# Markdown 知识卡片',
    '',
    '把 **Markdown** 一键变身高颜值图文，适合发到小红书 / 公众号 / 朋友圈。',
    '',
    '## 它能做什么',
    '',
    '- 多套主题：温暖柔和、简约灰、暗黑科技…',
    '- **自动分页**：长文自动切成多张',
    '- 实时预览，导出长图 / HTML，也能直接打印',
    '',
    '> 提示：左边写 Markdown，右边实时预览，点「外观 ▾」可在右侧调主题与样式。',
    '',
    '### 代码示例',
    '',
    '```js',
    'function greet(name) {',
    '  return `Hello, ${name}!`;',
    '}',
    'console.log(greet("QuickDock"));',
    '```',
    '',
    '| 功能 | 支持 |',
    '| --- | --- |',
    '| 主题 | ✅ |',
    '| 分页 | ✅ |',
    '| 导出 | ✅ |',
    '',
    '更多玩法，自己试试 👇'
  ].join('\n');

  var DEMO_TEXT = [
    '生活不是等待暴风雨过去，',
    '而是学会在雨中跳舞。',
    '',
    '—— 佚名'
  ].join('\n');

  // 文字模式下，每个模板的示例（切换模式互换用）
  var DEMO_TPL = {
    quote2:   ['这是一段值得被记住的话，', '它在某个深夜突然击中了你。', '', '—— 某位作家'],
    todo:     ['今日清单', '- 写完周报', '- 健身 30 分钟', '- 读 20 页书'],
    timeline: ['2020 入职第一家创业公司', '2022 独立负责核心模块', '2024 开始带团队', '2026 做自己喜欢的产品'],
    steps:    ['打开应用并登录', '选择喜欢的主题', '写下你的内容', '一键导出分享']
  };

  /* ---------- 工具 ---------- */
  function $(id) { return document.getElementById(id); }
  function blockHeight(el) {
    var cs = getComputedStyle(el);
    var h = el.getBoundingClientRect().height;
    var mt = parseFloat(cs.marginTop) || 0, mb = parseFloat(cs.marginBottom) || 0;
    return h + mt + mb;
  }
  function hexAlpha(hex, a) {
    var m = /^#?([0-9a-f]{6})$/i.exec(hex || '');
    if (!m) return 'rgba(0,0,0,' + a + ')';
    var n = parseInt(m[1], 16);
    return 'rgba(' + ((n >> 16) & 255) + ',' + ((n >> 8) & 255) + ',' + (n & 255) + ',' + a + ')';
  }

  /* ---------- 文字模板解析 ---------- */
  function linesOf(text) { return text.split(/\r?\n/); }

  function tplParagraph(text) {
    var out = [];
    linesOf(text).forEach(function (l) {
      var s = l.replace(/\s+$/, '');
      if (!s.trim()) out.push('<div class="t-blank"></div>');
      else out.push('<p class="t-line">' + escapeHtml(s) + '</p>');
    });
    return out.join('');
  }
  function tplQuote(text) {
    return tplParagraph(text); // 居中大字由 CSS .tpl-quote 处理
  }
  function tplTitle(text) {
    var out = [], first = true;
    linesOf(text).forEach(function (l) {
      var s = l.replace(/\s+$/, '');
      if (!s.trim()) { out.push('<div class="t-blank"></div>'); return; }
      if (first) { out.push('<div class="t-title">' + escapeHtml(s) + '</div>'); first = false; }
      else out.push('<p class="t-line">' + escapeHtml(s) + '</p>');
    });
    return out.join('');
  }
  function tplNote(text) { return tplParagraph(text); }
  function tplAnnounce(text) {
    var out = [], first = true;
    linesOf(text).forEach(function (l) {
      var s = l.replace(/\s+$/, '');
      if (!s.trim()) { out.push('<div class="t-blank"></div>'); return; }
      if (first) { out.push('<div class="t-title">' + escapeHtml(s) + '</div>'); first = false; }
      else out.push('<p class="t-line">' + escapeHtml(s) + '</p>');
    });
    return out.join('');
  }
  // 引用卡：末行以 —— 或 -- 开头作作者
  function tplQuote2(text) {
    var ls = linesOf(text).filter(function (l) { return l.replace(/\s+$/, '').trim(); });
    var author = '', body = ls;
    if (ls.length && /^—{2,}|--/.test(ls[ls.length - 1].trim())) {
      author = ls.pop().replace(/^—{2,}\s*|^--\s*/, '').trim();
      body = ls;
    }
    var html = '<blockquote class="t-quote2-body">' + escapeHtml(body.join('\n').replace(/\n/g, '<br>')) + '</blockquote>';
    if (author) html += '<div class="t-author">—— ' + escapeHtml(author) + '</div>';
    return html;
  }
  // 待办：每行一项，支持首行「标题:」作标题
  function tplTodo(text) {
    var ls = linesOf(text), out = [], title = '';
    ls.forEach(function (l) {
      var s = l.replace(/\s+$/, '').trim();
      if (!s) return;
      var m = s.match(/^(.+?)[:：]\s*$/);
      if (m && !title && !/^[-*]\s/.test(s)) { title = m[1]; return; }
      var txt = s.replace(/^[-*]\s*/, '');
      out.push('<div class="t-todo"><span class="box"></span><span class="t-todo-txt">' + escapeHtml(txt) + '</span></div>');
    });
    return (title ? '<div class="t-title">' + escapeHtml(title) + '</div>' : '') + out.join('');
  }
  // 时间轴：每行「时间 事件」（空格 / | / - 分割）
  function tplTimeline(text) {
    var out = [];
    linesOf(text).forEach(function (l) {
      var s = l.replace(/\s+$/, '').trim();
      if (!s) return;
      var m = s.match(/^(.+?)[\s\|\-]+(.+)$/);
      var time = m ? m[1] : '', ev = m ? m[2] : s;
      out.push('<div class="t-tl-item"><span class="t-dot"></span><div class="t-tl-body"><div class="t-time">' + escapeHtml(time) + '</div><div class="t-ev">' + escapeHtml(ev) + '</div></div></div>');
    });
    return out.join('');
  }
  // 步骤：每行一步，自动编号
  var STEP_N = 0;
  function tplSteps(text) {
    var out = [], n = 0;
    linesOf(text).forEach(function (l) {
      var s = l.replace(/\s+$/, '').trim();
      if (!s) return;
      n++;
      out.push('<div class="t-step"><span class="t-num">' + n + '</span><span class="t-step-txt">' + escapeHtml(s) + '</span></div>');
    });
    return out.join('');
  }

  function renderText(text, tpl) {
    switch (tpl) {
      case 'quote': return tplQuote(text);
      case 'title': return tplTitle(text);
      case 'note': return tplNote(text);
      case 'announce': return tplAnnounce(text);
      case 'quote2': return tplQuote2(text);
      case 'todo': return tplTodo(text);
      case 'timeline': return tplTimeline(text);
      case 'steps': return tplSteps(text);
      default: return tplParagraph(text);
    }
  }

  /* ---------- 二维码（qrcode-generator） ---------- */
  try { if (window.qrcode && qrcode.stringToBytesFuncs && qrcode.stringToBytesFuncs['UTF-8']) qrcode.stringToBytes = qrcode.stringToBytesFuncs['UTF-8']; } catch (e) {}
  function genQr(text) {
    try {
      var qr = qrcode(0, 'M');
      qr.addData(text);
      qr.make();
      return qr.createDataURL(5, 6); // gif dataURL
    } catch (e) { return ''; }
  }

  /* ---------- 持久化 ---------- */
  var KEY = 'md2card.v1';
  function save() {
    LS.set(KEY, JSON.stringify({
      mode: curMode, text: editor.value, theme: selTheme.value, template: selTemplate.value,
      sizeKey: sizeKey, width: selWidth.value, font: selFont.value, cardFont: selCardFont.value,
      paginate: chkPaginate.checked, footer: chkFooter.checked,
      customBg: customBg, bgOverlay: bgOverlay, bgScale: bgScale, bgX: bgX, bgY: bgY,
      round: round, shadow: shadow, pad: pad,
      wmText: wmText, wmPos: wmPos, qrText: qrText, qrPos: qrPos,
      customThemeOn: customThemeOn, ct: ct
    }));
  }
  function load() {
    try { var s = LS.get(KEY); return s ? JSON.parse(s) : null; } catch (e) { return null; }
  }

  /* ---------- 渲染状态 ---------- */
  var editor, selTheme, selTemplate, selSize, selWidth, selFont, selCardFont,
      chkPaginate, chkFooter, stage, toolbar, advPanel, dropHint;
  var curMode = 'md';

  /* 自定义背景图 */
  var customBg = '', bgOverlay = 28, bgScale = 100, bgX = 50, bgY = 50;
  /* 外观滑块 */
  var round = 16, shadow = 2, pad = 40;
  var SHADOW = ['none', '0 4px 14px rgba(0,0,0,.10)', '0 12px 40px rgba(0,0,0,.14)', '0 20px 60px rgba(0,0,0,.22)'];
  /* 尺寸预设 */
  var sizeKey = 'free';
  /* 水印 / 二维码 */
  var wmText = '', wmPos = 'tile', qrText = '', qrPos = 'br';
  /* 自定义主题 */
  var customThemeOn = false;
  var ct = { bg: '#ffffff', fg: '#222222', heading: '#111111', accent: '#e0324a', codeBg: '#f0f0f0' };

  // 上传图片经 canvas 压缩（背景图与拖入嵌图复用）
  function loadBgFile(file) {
    return new Promise(function (resolve, reject) {
      var fr = new FileReader();
      fr.onload = function () {
        var img = new Image();
        img.onload = function () {
          var maxW = 1600, w = img.width, h = img.height;
          if (w > maxW) { h = Math.round(h * maxW / w); w = maxW; }
          var cv = document.createElement('canvas'); cv.width = w; cv.height = h;
          var ctx = cv.getContext('2d'); ctx.drawImage(img, 0, 0, w, h);
          var url;
          try { url = cv.toDataURL('image/jpeg', 0.85); } catch (e) { url = fr.result; }
          resolve(url);
        };
        img.onerror = function () { reject(new Error('图片加载失败')); };
        img.src = fr.result;
      };
      fr.onerror = function () { reject(new Error('读取图片失败')); };
      fr.readAsDataURL(file);
    });
  }

  // 把自定义背景应用到单张卡片（inline style，导出自动携带）
  function applyCardBg(card) {
    if (!customBg) return;
    card.classList.add('has-custom-bg');
    card.style.backgroundImage = 'url(' + JSON.stringify(customBg) + ')';
    card.style.backgroundRepeat = 'no-repeat';
    if (bgScale > 100) { card.style.backgroundSize = bgScale + '%'; }
    else { card.style.backgroundSize = 'cover'; }
    card.style.backgroundPosition = bgX + '% ' + bgY + '%';
    card.style.setProperty('--bg-overlay', (bgOverlay / 100).toString());
  }

  // 遮罩色自动跟随主题文字亮度：深字用白遮罩、浅字用黑遮罩
  function resolveOverlayColor(card) {
    var m = /rgba?\((\d+),\s*(\d+),\s*(\d+)/.exec(getComputedStyle(card).color || '');
    if (!m) return '0,0,0';
    var lum = (0.299 * +m[1] + 0.587 * +m[2] + 0.114 * +m[3]) / 255;
    return lum < 0.5 ? '255,255,255' : '0,0,0';
  }

  // 自定义主题变量
  function setThemeVars(card) {
    card.style.setProperty('--card-bg', ct.bg);
    card.style.setProperty('--card-fg', ct.fg);
    card.style.setProperty('--card-heading', ct.heading);
    card.style.setProperty('--card-accent', ct.accent);
    card.style.setProperty('--card-muted', hexAlpha(ct.fg, .7));
    card.style.setProperty('--code-bg', ct.codeBg);
    card.style.setProperty('--code-fg', ct.fg);
    card.style.setProperty('--quote-bg', hexAlpha(ct.accent, .1));
    card.style.setProperty('--table-border', hexAlpha(ct.fg, .25));
    card.style.setProperty('--table-head', hexAlpha(ct.accent, .12));
    // 高亮色从强调色派生，简单够用
    var a = ct.accent;
    ['--hl-kw', '--hl-str', '--hl-num', '--hl-fn', '--hl-type', '--hl-attr'].forEach(function (p) { card.style.setProperty(p, a); });
    card.style.setProperty('--hl-com', hexAlpha(ct.fg, .6));
  }

  // 统一把视觉变量应用到卡片（测量卡与正式卡一致）
  function applyVisual(card, width) {
    if (customThemeOn) setThemeVars(card);
    if (selCardFont.value) card.style.fontFamily = selCardFont.value;
    card.style.setProperty('--card-radius', round + 'px');
    card.style.setProperty('--card-shadow', SHADOW[shadow]);
    card.style.setProperty('--card-pad', pad + 'px');
    applyCardBg(card);
    var ratio = (SIZES[sizeKey] || {}).ratio || 0;
    if (ratio > 0) card.style.minHeight = Math.round(width * ratio) + 'px';
  }

  function measureAll(html, theme, tpl, width, font) {
    var card = document.createElement('div');
    card.className = 'md-card theme-' + theme + (tpl ? ' tpl-' + tpl : '');
    card.style.cssText = 'position:absolute;left:-99999px;top:0;visibility:hidden;width:' + width + 'px;';
    card.innerHTML = '<div class="md-card-inner"><div class="md-content" style="font-size:' + font + 'px">' + html + '</div></div>';
    applyVisual(card, width);
    document.body.appendChild(card);
    var blocks = Array.prototype.slice.call(card.querySelectorAll('.md-content > *'));
    var heights = blocks.map(blockHeight);
    document.body.removeChild(card);
    return { blocks: blocks, heights: heights };
  }

  function paginate(blocks, heights, maxH) {
    var pages = [], cur = [], acc = 0;
    for (var i = 0; i < blocks.length; i++) {
      var h = heights[i];
      if (acc > 0 && acc + h > maxH) { pages.push(cur); cur = []; acc = 0; }
      cur.push(blocks[i]); acc += h;
    }
    if (cur.length) pages.push(cur);
    return pages;
  }

  function buildWatermark(text, pos) {
    var wm = document.createElement('div');
    wm.className = 'md-watermark ' + (pos === 'corner' ? 'wm-corner' : 'wm-tile');
    if (pos === 'corner') { wm.textContent = text; }
    else {
      var lines = [];
      for (var r = 0; r < 7; r++) { var line = ''; for (var c = 0; c < 4; c++) line += (text + '    '); lines.push(line); }
      wm.textContent = lines.join('\n');
    }
    return wm;
  }

  function buildCard(blocks, theme, tpl, width, font, footer, idx, total) {
    var card = document.createElement('div');
    card.className = 'md-card theme-' + theme + (tpl ? ' tpl-' + tpl : '');
    card.style.width = width + 'px';
    applyVisual(card, width);
    var inner = document.createElement('div');
    inner.className = 'md-card-inner';
    var content = document.createElement('div');
    content.className = 'md-content';
    content.style.fontSize = font + 'px';
    blocks.forEach(function (b) { content.appendChild(b.cloneNode(true)); });
    if (footer) {
      var f = document.createElement('div');
      f.className = 'md-footer';
      f.innerHTML = '<span class="brand">由 QuickDock 生成</span><span>' + (idx + 1) + ' / ' + total + '</span>';
      content.appendChild(f);
    }
    inner.appendChild(content);
    card.appendChild(inner);
    // 叠加层（绝对定位，不影响分页高度）
    if (wmText) card.appendChild(buildWatermark(wmText, wmPos));
    if (qrText) {
      var d = genQr(qrText);
      if (d) {
        var img = document.createElement('img');
        img.src = d; img.className = 'md-qr ' + (qrPos === 'bl' ? 'qr-bl' : 'qr-br');
        card.appendChild(img);
      }
    }
    return card;
  }

  function sanitizeHtml(html) {
    return String(html)
      .replace(/<\/?(script|style|iframe|object|embed|link|meta|base)[^>]*>/gi, '')
      .replace(/\son\w+\s*=\s*"[^"]*"/gi, '')
      .replace(/\son\w+\s*=\s*'[^']*'/gi, '')
      .replace(/\son\w+\s*=\s*[^\s>]+/gi, '')
      .replace(/(href|src)\s*=\s*"javascript:[^"]*"/gi, '')
      .replace(/(href|src)\s*=\s*'javascript:[^']*'/gi, '')
  }
  function render() {
    var theme = selTheme.value, width = +selWidth.value, font = +selFont.value;
    var footer = chkFooter.checked, doPaginate = chkPaginate.checked;
    var tpl = (curMode === 'text') ? selTemplate.value : '';
    var text = editor.value;
    var html = '';
    if (curMode === 'md') {
      html = text.trim() ? sanitizeHtml(window.marked ? marked.parse(text) : escapeHtml(text)) : '';
    } else {
      html = text.trim() ? renderText(text, tpl) : '';
    }
    stage.innerHTML = '';
    if (!html) {
      var ph = document.createElement('div');
      ph.style.cssText = 'color:var(--text-muted);font-size:13px;margin-top:40px';
      ph.textContent = curMode === 'md' ? '在左侧输入 Markdown（可直接拖入图片），这里实时预览卡片' : '在左侧输入文字，每行一段，这里实时预览卡片';
      stage.appendChild(ph);
      return;
    }
    var m = measureAll(html, theme, tpl, width, font);
    // 文字模式下非「段落」模板为单卡设计，不自动分页
    var allowPaginate = (curMode === 'md') || (tpl === 'paragraph');
    var pages = (doPaginate && allowPaginate) ? paginate(m.blocks, m.heights, Math.round(width * 1.45)) : [m.blocks];
    var total = pages.length;
    pages.forEach(function (blks, i) {
      var card = buildCard(blks, theme, tpl, width, font, footer, i, total);
      stage.appendChild(card);
      if (customBg) card.style.setProperty('--bg-overlay-rgb', resolveOverlayColor(card));
    });
  }

  /* ---------- 导出 / 打印 ---------- */
  function collectCss() {
    var css = '';
    var styles = document.querySelectorAll('style');
    for (var i = 0; i < styles.length; i++) css += styles[i].textContent + '\n';
    return css;
  }
  function buildPrintDocument() {
    var css = collectCss();
    if (css.indexOf('.md-card') < 0) {
      throw new Error('插件样式未加载完整，导出的会是一张白纸；请刷新插件后重试');
    }
    var cardsHtml = stage.innerHTML;
    return '<!DOCTYPE html><html lang="zh-CN"><head><meta charset="UTF-8">'
      + '<meta name="color-scheme" content="light only">'
      + '<style>:root{color-scheme:light only}'
      + 'body{margin:0;background:#fff}'
      + '.md-stage{background:transparent;padding:24px;display:flex;flex-direction:column;align-items:center;gap:24px}'
      + '.md-card,.md-card *{forced-color-adjust:none!important;-webkit-print-color-adjust:exact!important;print-color-adjust:exact!important}'
      + css + '</style></head><body><div class="md-stage">' + cardsHtml + '</div></body></html>';
  }

  function exportPng() {
    if (typeof window.htmlToImage === 'undefined') { toast(L('截图库未加载，请刷新插件'), true); return; }
    toast(L('正在生成长图…'), true);
    window.htmlToImage.toBlob(stage, { pixelRatio: 2, backgroundColor: null, cacheBust: true, skipFonts: true })
      .then(function (blob) {
        if (!blob) { toast(L('导出失败'), true); return; }
        var url = URL.createObjectURL(blob);
        var a = document.createElement('a'); a.href = url; a.download = 'md2card.png';
        document.body.appendChild(a); a.click(); a.remove();
        setTimeout(function () { URL.revokeObjectURL(url); }, 1000);
        toast(L('已导出 PNG'));
      })
      .catch(function (e) { toast(L('导出失败') + '：' + (e && e.message || e), true); });
  }

  function exportHtml() {
    try {
      var doc = buildPrintDocument();
      var blob = new Blob([doc], { type: 'text/html' });
      var url = URL.createObjectURL(blob);
      var a = document.createElement('a'); a.href = url; a.download = 'md2card.html';
      document.body.appendChild(a); a.click(); a.remove();
      setTimeout(function () { URL.revokeObjectURL(url); }, 1000);
      toast(L('已导出 HTML'));
    } catch (e) { toast(e.message, true); }
  }

  function doPrint() {
    if (typeof window.qdPrint !== 'function') { toast(L('当前环境未提供打印通道，请改用「导出 HTML」'), true); return; }
    try {
      var doc = buildPrintDocument();
      window.qdPrint({ html: doc, page: 'A4' })
        .then(function () { toast(L('已发送打印')); })
        .catch(function (e) { toast(L('导出失败') + '：' + (e && e.message || e), true); });
    } catch (e) { toast(e.message, true); }
  }

  // 复制到剪贴板（沙箱 opaque origin 下可能失败 → 降级为下载）
  function copyImage() {
    if (typeof window.htmlToImage === 'undefined') { toast(L('截图库未加载，请刷新插件'), true); return; }
    toast(L('正在生成长图…'), true);
    window.htmlToImage.toBlob(stage, { pixelRatio: 2, backgroundColor: null, cacheBust: true, skipFonts: true })
      .then(function (blob) {
        if (!blob) { toast(L('导出失败'), true); return; }
        var downloaded = false;
        function fallback() {
          if (downloaded) return; downloaded = true;
          var url = URL.createObjectURL(blob);
          var a = document.createElement('a'); a.href = url; a.download = 'md2card.png';
          document.body.appendChild(a); a.click(); a.remove();
          setTimeout(function () { URL.revokeObjectURL(url); }, 1000);
          toast(L('剪贴板不可用，已改为下载图片'));
        }
        try {
          if (navigator.clipboard && navigator.clipboard.write && window.ClipboardItem) {
            navigator.clipboard.write([new window.ClipboardItem({ 'image/png': blob })])
              .then(function () { toast(L('已复制到剪贴板')); })
              .catch(function () { fallback(); });
          } else { fallback(); }
        } catch (e) { fallback(); }
      })
      .catch(function (e) { toast(L('导出失败') + '：' + (e && e.message || e), true); });
  }

  /* ---------- toast ---------- */
  function toast(msg, isErr) {
    var t = $('qd-toast');
    if (!t) {
      t = document.createElement('div'); t.id = 'qd-toast';
      t.style.cssText = 'position:fixed;top:12px;right:12px;padding:8px 14px;border-radius:8px;font-size:13px;z-index:9999;box-shadow:var(--shadow-2)';
      document.body.appendChild(t);
    }
    t.textContent = msg;
    t.style.background = isErr ? 'var(--danger)' : 'var(--success)';
    t.style.color = '#fff';
    t.style.display = 'block';
    clearTimeout(t._tm);
    if (!isErr) t._tm = setTimeout(function () { t.style.display = 'none'; }, 2200);
  }

  /* ---------- 事件 ---------- */
  var composing = false, tmr = null;
  function onInput() { clearTimeout(tmr); tmr = setTimeout(function () { if (!composing) { save(); render(); } }, 160); }

  function setMode(mode) {
    curMode = mode;
    document.querySelectorAll('#segMode .seg-btn').forEach(function (b) {
      b.classList.toggle('active', b.getAttribute('data-mode') === mode);
    });
    toolbar.classList.toggle('hide-template', mode !== 'text');
    editor.placeholder = (mode === 'md') ? '在这里输入 Markdown（可直接把图片拖进来）' : '输入文字，每行一段；金句/标题卡模板支持首行作标题';
    // 切换模式时把「另一模式的默认示例」换成当前模式的示例
    var dm = (mode === 'md') ? DEMO_MD : DEMO_TEXT;
    if (editor.value === DEMO_MD && mode === 'text') editor.value = DEMO_TEXT;
    else if (editor.value === DEMO_TEXT && mode === 'md') editor.value = DEMO_MD;
    else if (!editor.value.trim()) editor.value = dm;
    save(); render();
  }

  function init() {
    editor = $('editor'); selTheme = $('selTheme'); selTemplate = $('selTemplate'); selSize = $('selSize');
    selWidth = $('selWidth'); selFont = $('selFont'); selCardFont = $('selCardFont');
    chkPaginate = $('chkPaginate'); chkFooter = $('chkFooter'); stage = $('stage'); toolbar = $('toolbar');
    advPanel = $('advPanel'); dropHint = $('dropHint');
    var btnBg = $('btnBg'), bgInput = $('bgInput'), btnBgClear = $('btnBgClear'), rngOverlay = $('rngOverlay');
    var btnAdv = $('btnAdv');

    THEME_GROUPS.forEach(function (g) {
      var og = document.createElement('optgroup'); og.label = g.label;
      Object.keys(g.items).forEach(function (k) {
        var o = document.createElement('option'); o.value = k; o.textContent = g.items[k]; og.appendChild(o);
      });
      selTheme.appendChild(og);
    });
    var oc = document.createElement('option'); oc.value = 'custom'; oc.textContent = '自定义主题'; selTheme.appendChild(oc);
    Object.keys(TPL_NAMES).forEach(function (k) {
      var o = document.createElement('option'); o.value = k; o.textContent = TPL_NAMES[k]; selTemplate.appendChild(o);
    });

    var saved = load();
    if (saved) {
      if (saved.mode) curMode = saved.mode;
      if (saved.text != null) editor.value = saved.text;
      if (saved.theme) selTheme.value = saved.theme;
      if (saved.template) selTemplate.value = saved.template;
      if (saved.sizeKey) sizeKey = saved.sizeKey;
      if (saved.width) selWidth.value = saved.width;
      if (saved.font) selFont.value = saved.font;
      if (saved.cardFont != null) selCardFont.value = saved.cardFont;
      if (typeof saved.paginate === 'boolean') chkPaginate.checked = saved.paginate;
      if (typeof saved.footer === 'boolean') chkFooter.checked = saved.footer;
      if (saved.customBg) customBg = saved.customBg;
      if (typeof saved.bgOverlay === 'number') bgOverlay = saved.bgOverlay;
      if (typeof saved.bgScale === 'number') bgScale = saved.bgScale;
      if (typeof saved.bgX === 'number') bgX = saved.bgX;
      if (typeof saved.bgY === 'number') bgY = saved.bgY;
      if (typeof saved.round === 'number') round = saved.round;
      if (typeof saved.shadow === 'number') shadow = saved.shadow;
      if (typeof saved.pad === 'number') pad = saved.pad;
      if (saved.wmText != null) wmText = saved.wmText;
      if (saved.wmPos) wmPos = saved.wmPos;
      if (saved.qrText != null) qrText = saved.qrText;
      if (saved.qrPos) qrPos = saved.qrPos;
      if (typeof saved.customThemeOn === 'boolean') customThemeOn = saved.customThemeOn;
      if (saved.ct) { ct.bg = saved.ct.bg || ct.bg; ct.fg = saved.ct.fg || ct.fg; ct.heading = saved.ct.heading || ct.heading; ct.accent = saved.ct.accent || ct.accent; ct.codeBg = saved.ct.codeBg || ct.codeBg; }
    }
    if (!editor.value) editor.value = DEMO_MD;

    // 把状态同步到控件
    selSize.value = sizeKey;
    rngOverlay.value = bgOverlay;
    btnBgClear.classList.toggle('hide', !customBg);
    $('rngRound').value = round; $('rngShadow').value = shadow; $('rngPad').value = pad;
    $('rngBgScale').value = bgScale; $('rngBgX').value = bgX; $('rngBgY').value = bgY;
    $('wmText').value = wmText; $('selWmPos').value = wmPos;
    $('qrText').value = qrText; $('selQrPos').value = qrPos;
    $('ctBg').value = ct.bg; $('ctFg').value = ct.fg; $('ctHeading').value = ct.heading; $('ctAccent').value = ct.accent; $('ctCodeBg').value = ct.codeBg;
    if (customThemeOn) selTheme.value = 'custom';

    setMode(curMode);

    // 输入法安全：组字期间不重排、不回写
    editor.addEventListener('compositionstart', function () { composing = true; });
    editor.addEventListener('compositionend', function () { composing = false; onInput(); });
    editor.addEventListener('input', function (e) { if (composing || (e && e.isComposing)) return; onInput(); });

    document.querySelectorAll('#segMode .seg-btn').forEach(function (b) {
      b.addEventListener('click', function () { setMode(b.getAttribute('data-mode')); });
    });
    [selWidth, selFont, selCardFont, chkPaginate, chkFooter].forEach(function (el) {
      el.addEventListener('change', function () { save(); render(); });
    });
    // 主题切换：只有选中「自定义主题」才启用自定义配色，否则关闭，
    // 避免 customThemeOn 残留导致自定义 inline 变量覆盖内置主题（表现为选主题没反应）
    selTheme.addEventListener('change', function () {
      customThemeOn = (selTheme.value === 'custom');
      save(); render();
    });
    selTemplate.addEventListener('change', function () { save(); render(); });
    selSize.addEventListener('change', function () {
      sizeKey = selSize.value;
      var s = SIZES[sizeKey]; if (s && s.w) selWidth.value = String(s.w);
      save(); render();
    });

    $('btnSample').addEventListener('click', function () {
      if (curMode === 'text' && TPL_NAMES[selTemplate.value] && DEMO_TPL[selTemplate.value]) {
        editor.value = DEMO_TPL[selTemplate.value].join('\n');
      } else {
        editor.value = (curMode === 'md') ? DEMO_MD : DEMO_TEXT;
      }
      save(); render(); editor.focus();
    });
    $('btnClear').addEventListener('click', function () { editor.value = ''; save(); render(); editor.focus(); });
    $('btnPng').addEventListener('click', exportPng);
    $('btnHtml').addEventListener('click', exportHtml);
    $('btnPrint').addEventListener('click', doPrint);
    $('btnCopy').addEventListener('click', copyImage);

    // 外观面板开关
    btnAdv.addEventListener('click', function () { advPanel.classList.toggle('hide'); btnAdv.textContent = advPanel.classList.contains('hide') ? '外观 ▾' : '外观 ▴'; });

    // 自定义背景图
    btnBg.addEventListener('click', function () { bgInput.click(); });
    bgInput.addEventListener('change', function () {
      var f = bgInput.files && bgInput.files[0];
      if (!f) return;
      loadBgFile(f).then(function (url) {
        customBg = url; btnBgClear.classList.remove('hide'); bgInput.value = '';
        save(); render();
      }).catch(function (e) { toast((e && e.message || e), true); });
    });
    btnBgClear.addEventListener('click', function () {
      customBg = ''; btnBgClear.classList.add('hide'); save(); render();
    });
    rngOverlay.addEventListener('input', function () { bgOverlay = +rngOverlay.value; save(); render(); });

    // 外观滑块
    $('rngRound').addEventListener('input', function () { round = +this.value; save(); render(); });
    $('rngShadow').addEventListener('input', function () { shadow = +this.value; save(); render(); });
    $('rngPad').addEventListener('input', function () { pad = +this.value; save(); render(); });
    $('rngBgScale').addEventListener('input', function () { bgScale = +this.value; save(); render(); });
    $('rngBgX').addEventListener('input', function () { bgX = +this.value; save(); render(); });
    $('rngBgY').addEventListener('input', function () { bgY = +this.value; save(); render(); });

    // 水印
    $('wmText').addEventListener('input', function () { wmText = this.value; save(); render(); });
    $('selWmPos').addEventListener('change', function () { wmPos = this.value; save(); render(); });

    // 二维码
    $('qrText').addEventListener('input', function () { qrText = this.value; save(); render(); });
    $('selQrPos').addEventListener('change', function () { qrPos = this.value; save(); render(); });

    // 自定义主题
    ['ctBg', 'ctFg', 'ctHeading', 'ctAccent', 'ctCodeBg'].forEach(function (id) {
      $(id).addEventListener('input', function () {
        // 去掉 'ct' 前缀后只把首字母小写，保留内部驼峰（否则 ctCodeBg -> codebg，
        // 而渲染读的是 ct.codeBg，导致“码底”配色改了不生效）。
        var key = id.charAt(2).toLowerCase() + id.slice(3);
        ct[key] = this.value;
        customThemeOn = true; selTheme.value = 'custom';
        save(); render();
      });
    });
    $('btnApplyCustom').addEventListener('click', function () {
      customThemeOn = true; selTheme.value = 'custom'; save(); render();
    });

    // 拖入图片（仅 Markdown 模式）
    editor.addEventListener('dragover', function (e) { if (curMode === 'md') { e.preventDefault(); dropHint.classList.add('show'); } });
    editor.addEventListener('dragleave', function () { dropHint.classList.remove('show'); });
    editor.addEventListener('drop', function (e) {
      if (curMode !== 'md') return;
      e.preventDefault(); dropHint.classList.remove('show');
      var files = e.dataTransfer && e.dataTransfer.files;
      if (!files || !files.length) return;
      var imgs = [].slice.call(files).filter(function (f) { return /^image\//.test(f.type); });
      if (!imgs.length) return;
      var i = 0;
      (function next() {
        if (i >= imgs.length) return;
        loadBgFile(imgs[i++]).then(function (url) {
          insertAtCursor(editor, '\n![](' + url + ')\n');
          onInput();
          toast(L('图片文件已放入卡片'));
          next();
        }).catch(function () { next(); });
      })();
    });

    // 宿主桥
    window.addEventListener('message', function (e) {
      var d = e.data; if (!d) return;
      if (d.type === 'plugin:init') {
        var p = d.data || {};
        if (p.text) { editor.value = p.text; save(); render(); }
      } else if (d.type === 'plugin:theme') {
        var dd = d.data || {};
        if (dd.locale) document.documentElement.setAttribute('lang', dd.locale);
      }
    });

    render();
  }

  function insertAtCursor(ta, text) {
    var s = ta.selectionStart, e = ta.selectionEnd;
    ta.value = ta.value.slice(0, s) + text + ta.value.slice(e);
    ta.selectionStart = ta.selectionEnd = s + text.length;
  }

  function boot() {
    try { init(); }
    catch (e) {
      document.body.innerHTML = '<pre style="color:#e5534b;padding:16px;white-space:pre-wrap">插件启动失败\n' + (e && e.stack || e) + '</pre>';
    }
  }

  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', boot);
  else boot();
})();
