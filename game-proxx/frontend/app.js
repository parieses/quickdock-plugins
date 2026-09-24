/* 扫清黑洞 —— 真·六边形扫雷（纯前端，沙箱安全）
   棋盘为六边形网格（axial 坐标），每格最多 6 个邻居；数字提示周围黑洞数。 */
(function () {
  'use strict';

  // 沙箱降级：opaque origin 下 localStorage 抛 SecurityError，退回内存存储
  var Store = (function () {
    var mem = {};
    var ls = null;
    try { ls = window.localStorage; ls.getItem('__t'); } catch (e) { ls = null; }
    return {
      get: function (k) {
        if (ls) { try { return ls.getItem(k); } catch (e) {} }
        return Object.prototype.hasOwnProperty.call(mem, k) ? mem[k] : null;
      },
      set: function (k, v) {
        if (ls) { try { ls.setItem(k, v); } catch (e) {} }
        mem[k] = String(v);
      }
    };
  })();

  // 难度按六边形半径 R（棋盘格数 = 3R²+3R+1）与雷数
  var DIFF = {
    easy:   { R: 3, m: 6 },
    medium: { R: 4, m: 15 },
    hard:   { R: 5, m: 28 }
  };
  var DIRS = [[1, 0], [1, -1], [0, -1], [-1, 0], [-1, 1], [0, 1]]; // 6 邻居
  var SIZE = 30, SVGNS = 'http://www.w3.org/2000/svg';

  var els = {};
  var cur = DIFF.medium, diffKey = 'medium';
  var cells = {};           // "q,r" -> { q, r, mine, adj, open, flag, g, poly, txt }
  var order = [];           // 渲染顺序
  var total = 0;
  var started = false, over = false, won = false;
  var flags = 0, revealed = 0;
  var timer = null, secs = 0;
  var flagMode = false;

  function boot() {
    try { init(); }
    catch (e) {
      document.body.innerHTML = '<pre style="color:#e5534b;padding:16px;white-space:pre-wrap">插件启动失败\n' +
        (e && e.stack || e) + '</pre>';
    }
  }

  function init() {
    els.board = document.getElementById('board');
    els.mines = document.getElementById('mines');
    els.time = document.getElementById('time');
    els.over = document.getElementById('over');
    els.overText = document.getElementById('overText');
    els.best = document.getElementById('best');
    els.flagMode = document.getElementById('flagMode');
    if (!els.board) throw new Error('缺少 #board');

    var bar = document.querySelector('.px-bar');
    bar.addEventListener('click', function (ev) {
      var b = ev.target.closest('button'); if (!b) return;
      if (b.dataset.diff) { diffKey = b.dataset.diff; cur = DIFF[diffKey]; newGame(); }
    });
    document.getElementById('restart').addEventListener('click', newGame);
    document.getElementById('again').addEventListener('click', newGame);
    els.flagMode.addEventListener('click', function () {
      flagMode = !flagMode;
      els.flagMode.textContent = flagMode ? '🔁 标记:开' : '🔁 标记:关';
    });

    els.board.addEventListener('click', onReveal);
    els.board.addEventListener('contextmenu', function (e) { e.preventDefault(); onFlag(e); });

    showBest();
    newGame();
  }

  function inBoard(q, r) {
    var R = cur.R;
    return Math.max(Math.abs(q), Math.abs(r), Math.abs(q + r)) <= R;
  }
  function key(q, r) { return q + ',' + r; }

  function newGame() {
    if (timer) { clearInterval(timer); timer = null; }
    started = false; over = false; won = false; flags = 0; revealed = 0; secs = 0;
    els.over.hidden = true;
    els.time.textContent = '0';
    els.mines.textContent = String(cur.m);
    cells = {}; order = [];
    total = 0;
    // 生成六边形棋盘
    var R = cur.R;
    for (var q = -R; q <= R; q++) {
      for (var r = -R; r <= R; r++) {
        if (!inBoard(q, r)) continue;
        var c = { q: q, r: r, mine: false, adj: 0, open: false, flag: false, g: null, poly: null, txt: null };
        cells[key(q, r)] = c; order.push(c); total++;
      }
    }
    render();
    showBest();
  }

  function placeMines(sq, sr) {
    var placed = 0;
    var safe = {}; safe[key(sq, sr)] = 1;
    var nb = neighbors(sq, sr);
    for (var i = 0; i < nb.length; i++) safe[key(nb[i][0], nb[i][1])] = 1;
    while (placed < cur.m && placed < total - 7) {
      var q = (Math.random() * (2 * cur.R + 1) | 0) - cur.R;
      var r = (Math.random() * (2 * cur.R + 1) | 0) - cur.R;
      if (!inBoard(q, r) || safe[key(q, r)] || cells[key(q, r)].mine) continue;
      cells[key(q, r)].mine = true; placed++;
    }
    for (var k = 0; k < order.length; k++) {
      var c = order[k]; if (c.mine) continue;
      var n = neighbors(c.q, c.r), cnt = 0;
      for (var j = 0; j < n.length; j++) if (cells[key(n[j][0], n[j][1])].mine) cnt++;
      c.adj = cnt;
    }
  }

  function neighbors(q, r) {
    var out = [];
    for (var i = 0; i < DIRS.length; i++) {
      var nq = q + DIRS[i][0], nr = r + DIRS[i][1];
      if (inBoard(nq, nr)) out.push([nq, nr]);
    }
    return out;
  }

  function startTimer() {
    started = true;
    timer = setInterval(function () { secs++; els.time.textContent = String(secs); }, 1000);
  }

  function onReveal(e) {
    if (over) return;
    var g = e.target.closest('g.hx'); if (!g) return;
    var q = +g.dataset.q, r = +g.dataset.r;
    if (flagMode) { toggleFlag(q, r); return; }
    if (cells[key(q, r)].flag || cells[key(q, r)].open) return;
    if (!started) { placeMines(q, r); startTimer(); }
    reveal(q, r);
  }

  function onFlag(e) {
    if (over) return;
    var g = e.target.closest('g.hx'); if (!g) return;
    toggleFlag(+g.dataset.q, +g.dataset.r);
  }

  function toggleFlag(q, r) {
    var c = cells[key(q, r)]; if (c.open) return;
    c.flag = !c.flag;
    flags += c.flag ? 1 : -1;
    els.mines.textContent = String(cur.m - flags);
    paint(c);
  }

  function reveal(q, r) {
    var c = cells[key(q, r)];
    if (c.open || c.flag) return;
    c.open = true; revealed++;
    if (c.mine) { lose(q, r); return; }
    paint(c);
    if (c.adj === 0) {
      var nb = neighbors(q, r);
      for (var i = 0; i < nb.length; i++) reveal(nb[i][0], nb[i][1]);
    }
    checkWin();
  }

  function lose(mq, mr) {
    over = true; won = false;
    if (timer) { clearInterval(timer); timer = null; }
    for (var k = 0; k < order.length; k++) {
      var c = order[k];
      if (c.mine && !c.flag) { c.open = true; paint(c); }
    }
    var boom = cells[key(mq, mr)];
    if (boom && boom.poly) boom.poly.classList.add('boom');
    end(false);
  }

  function checkWin() {
    if (over) return;
    if (revealed === total - cur.m) {
      over = true; won = true;
      if (timer) { clearInterval(timer); timer = null; }
      // 自动给剩余雷插旗
      for (var k = 0; k < order.length; k++) {
        var c = order[k];
        if (c.mine && !c.flag) { c.flag = true; flags++; paint(c); }
      }
      els.mines.textContent = String(cur.m - flags);
      end(true);
    }
  }

  function end(win) {
    els.overText.textContent = win ? '🎉 清空成功！' : '💥 踩中黑洞';
    els.over.hidden = false;
    if (win) {
      var key = 'px-best-' + diffKey, prev = Store.get(key);
      if (prev === null || secs < +prev) Store.set(key, String(secs));
      showBest();
    }
  }

  function showBest() {
    var v = Store.get('px-best-' + diffKey);
    els.best.textContent = v !== null ? ('本难度最佳：' + v + ' 秒') : '本难度暂无记录';
  }

  function hexPoints(cx, cy) {
    var pts = [];
    for (var i = 0; i < 6; i++) {
      var a = (60 * i - 30) * Math.PI / 180;
      pts.push((cx + SIZE * Math.cos(a)).toFixed(2) + ',' + (cy + SIZE * Math.sin(a)).toFixed(2));
    }
    return pts.join(' ');
  }

  function render() {
    var R = cur.R, minX = 1e9, minY = 1e9, maxX = -1e9, maxY = -1e9;
    order.forEach(function (c) {
      c.cx = SIZE * Math.sqrt(3) * (c.q + c.r / 2);
      c.cy = SIZE * 1.5 * c.r;
      if (c.cx < minX) minX = c.cx; if (c.cx > maxX) maxX = c.cx;
      if (c.cy < minY) minY = c.cy; if (c.cy > maxY) maxY = c.cy;
    });
    var pad = SIZE * 1.1;
    els.board.setAttribute('viewBox',
      (minX - pad).toFixed(1) + ' ' + (minY - pad).toFixed(1) + ' ' +
      (maxX - minX + pad * 2).toFixed(1) + ' ' + (maxY - minY + pad * 2).toFixed(1));
    var frag = document.createDocumentFragment();
    order.forEach(function (c) {
      var g = document.createElementNS(SVGNS, 'g');
      g.setAttribute('class', 'hx');
      g.dataset.q = c.q; g.dataset.r = c.r;
      var poly = document.createElementNS(SVGNS, 'polygon');
      poly.setAttribute('points', hexPoints(c.cx, c.cy));
      var txt = document.createElementNS(SVGNS, 'text');
      txt.setAttribute('x', c.cx.toFixed(2));
      txt.setAttribute('y', c.cy.toFixed(2));
      txt.setAttribute('dy', '0.35em');
      txt.setAttribute('text-anchor', 'middle');
      txt.setAttribute('class', 'hx-txt');
      g.appendChild(poly); g.appendChild(txt);
      c.g = g; c.poly = poly; c.txt = txt;
      frag.appendChild(g);
    });
    els.board.innerHTML = '';
    els.board.appendChild(frag);
  }

  function paint(c) {
    if (!c.poly) return;
    c.poly.setAttribute('class', 'hx');
    c.txt.setAttribute('class', 'hx-txt');
    c.txt.textContent = '';
    if (c.open) {
      c.poly.classList.add('open');
      if (c.mine) { c.poly.classList.add('mine'); c.txt.textContent = '✹'; }
      else if (c.adj > 0) { c.txt.classList.add('n' + c.adj); c.txt.textContent = String(c.adj); }
    } else if (c.flag) {
      c.poly.classList.add('flag'); c.txt.textContent = '🚩';
    }
  }

  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', boot);
  else boot();
})();
