(function () {
  'use strict';

  // localStorage 安全封装（沙箱不透明源会抛错）
  var LS = (function () {
    var ok = true;
    try {
      var k = '__rl_probe';
      window.localStorage.setItem(k, '1');
      window.localStorage.removeItem(k);
    } catch (e) { ok = false; }
    return {
      get: function (k, d) {
        if (!ok) return d;
        try {
          var v = window.localStorage.getItem(k);
          return v === null ? d : v;
        } catch (e) { return d; }
      },
      set: function (k, v) {
        if (!ok) return;
        try { window.localStorage.setItem(k, v); } catch (e) {}
      }
    };
  })();

  var W = 21, H = 13, MAX_DEPTH = 6;

  var tiles, monsters, items, stairs, player, state, logs, bestDepth, bestGold;
  var mapEl, hudEl, logEl, bestEl;

  // ---- 工具 ----
  function rnd(a, b) { return Math.floor(Math.random() * (b - a + 1)) + a; }
  function repeat(s, n) { var o = ''; for (var i = 0; i < n; i++) o += s; return o; }
  function key(x, y) { return y * W + x; }
  function occupied() {
    var set = {};
    set[key(player.x, player.y)] = 1;
    if (stairs) set[key(stairs.x, stairs.y)] = 1;
    monsters.forEach(function (m) { set[key(m.x, m.y)] = 1; });
    items.forEach(function (it) { set[key(it.x, it.y)] = 1; });
    return set;
  }
  function monsterAt(x, y) {
    for (var i = 0; i < monsters.length; i++) if (monsters[i].x === x && monsters[i].y === y) return monsters[i];
    return null;
  }
  function itemAt(x, y) {
    for (var i = 0; i < items.length; i++) if (items[i].x === x && items[i].y === y) return items[i];
    return null;
  }
  function isStairs(x, y) { return stairs && stairs.x === x && stairs.y === y; }

  // ---- 地图生成 ----
  function randomOpen() {
    for (var t = 0; t < 400; t++) {
      var x = rnd(1, W - 2), y = rnd(1, H - 2);
      if (tiles[y][x] === 1) return { x: x, y: y };
    }
    return { x: 1, y: 1 };
  }

  function bfs(sx, sy) {
    var seen = {}, q = [{ x: sx, y: sy, d: 0 }], out = [];
    seen[key(sx, sy)] = 1;
    while (q.length) {
      var c = q.shift();
      out.push(c);
      var nb = [[c.x + 1, c.y], [c.x - 1, c.y], [c.x, c.y + 1], [c.x, c.y - 1]];
      for (var i = 0; i < nb.length; i++) {
        var nx = nb[i][0], ny = nb[i][1];
        if (nx < 1 || ny < 1 || nx >= W - 1 || ny >= H - 1) continue;
        if (tiles[ny][nx] !== 1) continue;
        if (seen[key(nx, ny)]) continue;
        seen[key(nx, ny)] = 1;
        q.push({ x: nx, y: ny, d: c.d + 1 });
      }
    }
    return out;
  }

  function farthest(reach, start) {
    var best = reach[0], bd = -1;
    for (var i = 0; i < reach.length; i++) {
      if (reach[i].d > bd) { bd = reach[i].d; best = reach[i]; }
    }
    return best;
  }

  function freeCell(reach, occ) {
    var pool = [];
    for (var i = 0; i < reach.length; i++) {
      var c = reach[i];
      if (!occ[key(c.x, c.y)]) pool.push(c);
    }
    if (!pool.length) return null;
    return pool[rnd(0, pool.length - 1)];
  }

  function makeMonster(x, y, depth) {
    var pool = depth <= 1 ? ['r', 'b'] : (depth <= 3 ? ['r', 'b', 'g', 's'] : ['g', 's', 'O', 'b']);
    var ch = pool[rnd(0, pool.length - 1)];
    var T = {
      r: { name: '老鼠', hp: 0.7, atk: 0.8 },
      b: { name: '蝙蝠', hp: 0.8, atk: 0.9 },
      g: { name: '哥布林', hp: 1.0, atk: 1.0 },
      s: { name: '骷髅', hp: 1.1, atk: 1.05 },
      O: { name: '兽人', hp: 1.3, atk: 1.2 }
    }[ch];
    var hp = Math.round((4 + depth * 3) * T.hp);
    return {
      x: x, y: y, ch: ch, name: T.name, boss: false,
      maxhp: hp, hp: hp,
      atk: Math.max(1, Math.round((1 + Math.floor(depth / 2)) * T.atk)),
      def: Math.floor(depth / 3),
      xp: 2 + depth * 2, gold: depth + rnd(0, depth)
    };
  }

  function makeBoss(x, y, depth) {
    var hp = 28 + depth * 8;
    return {
      x: x, y: y, ch: 'D', name: '巨龙', boss: true,
      maxhp: hp, hp: hp,
      atk: 4 + depth, def: 1 + Math.floor(depth / 2),
      xp: 100, gold: 200
    };
  }

  function makeItem(x, y, depth) {
    var roll = Math.random();
    if (roll < 0.4) return { x: x, y: y, kind: 'potion', ch: '+' };
    if (roll < 0.65) return { x: x, y: y, kind: 'weapon', ch: ')' };
    if (roll < 0.85) return { x: x, y: y, kind: 'armor', ch: '/' };
    return { x: x, y: y, kind: 'gold', ch: '$' };
  }

  function genFloor(depth) {
    tiles = [];
    for (var y = 0; y < H; y++) {
      var row = [];
      for (var x = 0; x < W; x++) {
        row.push((x === 0 || y === 0 || x === W - 1 || y === H - 1) ? 0 : 1);
      }
      tiles.push(row);
    }
    for (var yy = 1; yy < H - 1; yy++) {
      for (var xx = 1; xx < W - 1; xx++) {
        if (Math.random() < 0.13) tiles[yy][xx] = 0;
      }
    }
    var start = randomOpen();
    player.x = start.x; player.y = start.y;
    var reach = bfs(start.x, start.y);
    var far = farthest(reach, start);
    var isBoss = (depth === MAX_DEPTH);
    // BOSS 层不放楼梯（终点即巨龙），否则楼梯字形会盖住巨龙显示
    stairs = isBoss ? null : far;
    monsters = [];
    items = [];
    var occ = occupied();
    if (isBoss) {
      monsters.push(makeBoss(far.x, far.y, depth));
      occ[key(far.x, far.y)] = 1;
      log('你踏入巨龙的巢穴……空气灼热，鳞甲在黑暗中反光。', 'ln-bad');
    }
    var mcount = 3 + depth;
    for (var i = 0; i < mcount; i++) {
      var c = freeCell(reach, occ);
      if (!c) break;
      monsters.push(makeMonster(c.x, c.y, depth));
      occ[key(c.x, c.y)] = 1;
    }
    var icount = 2 + Math.floor(depth / 2);
    for (var j = 0; j < icount; j++) {
      var ci = freeCell(reach, occ);
      if (!ci) break;
      items.push(makeItem(ci.x, ci.y, depth));
      occ[key(ci.x, ci.y)] = 1;
    }
  }

  // ---- 战斗与移动 ----
  function attackMonster(m) {
    var dmg = Math.max(1, player.atk - m.def + rnd(-1, 1));
    m.hp -= dmg;
    if (m.hp > 0) {
      log('你攻击' + m.name + '，造成 ' + dmg + ' 伤害（剩 ' + m.hp + '）', m.boss ? 'ln-bad' : 'ln-hi');
      var mdmg = Math.max(1, m.atk - player.def + rnd(-1, 1));
      player.hp -= mdmg;
      if (player.hp > 0) {
        log(m.name + '反击，你受到 ' + mdmg + ' 伤害（剩 ' + player.hp + '）', 'ln-bad');
      } else {
        player.hp = 0;
        log(m.name + '反击，你受到 ' + mdmg + ' 伤害——你倒下了。', 'ln-bad');
        gameOver();
      }
    } else {
      player.xp += m.xp;
      player.gold += m.gold;
      log('你击杀' + m.name + '，获得 ' + m.xp + ' 经验、' + m.gold + ' 金币。', 'ln-good');
      monsters = monsters.filter(function (o) { return o !== m; });
      checkLevelUp();
      if (m.boss) win();
    }
  }

  function pickup(it) {
    if (it.kind === 'potion') {
      var h = rnd(5, 10);
      player.hp = Math.min(player.maxhp, player.hp + h);
      log('喝下药水，恢复 ' + h + ' 点生命（' + player.hp + '/' + player.maxhp + '）。', 'ln-good');
    } else if (it.kind === 'weapon') {
      player.atk += 1;
      log('拾取武器，攻击 +1（当前 ' + player.atk + '）。', 'ln-good');
    } else if (it.kind === 'armor') {
      player.def += 1;
      log('穿上护甲，防御 +1（当前 ' + player.def + '）。', 'ln-good');
    } else if (it.kind === 'gold') {
      var g = rnd(3, 8) + player.depth;
      player.gold += g;
      log('捡到 ' + g + ' 金币。', '');
    }
    items = items.filter(function (o) { return o !== it; });
  }

  function tryMove(dx, dy) {
    var nx = player.x + dx, ny = player.y + dy;
    if (nx < 0 || ny < 0 || nx >= W || ny >= H) return;
    if (tiles[ny][nx] === 0) return;
    var m = monsterAt(nx, ny);
    if (m) { attackMonster(m); return; }
    player.x = nx; player.y = ny;
    var it = itemAt(nx, ny);
    if (it) pickup(it);
    if (state.alive && !state.win && isStairs(nx, ny) && player.depth < MAX_DEPTH) descend();
  }

  function descend() {
    player.depth += 1;
    log('你走下楼梯，进入第 ' + player.depth + ' 层。', '');
    genFloor(player.depth);
    if (player.depth > bestDepth) {
      bestDepth = player.depth;
      LS.set('gameRogue.bestDepth', String(bestDepth));
    }
  }

  function checkLevelUp() {
    var need = 5 + (player.level - 1) * 5;
    while (player.xp >= need) {
      player.xp -= need;
      player.level += 1;
      player.maxhp += 4;
      player.hp = player.maxhp;
      player.atk += 1;
      if (player.level % 2 === 0) player.def += 1;
      log('升级！等级 ' + player.level + '，生命上限 +4 并回满，攻击 +1。', 'ln-good');
      need = 5 + (player.level - 1) * 5;
    }
  }

  function gameOver() {
    state.alive = false;
    updateBest();
    log('你倒在了第 ' + player.depth + ' 层。按 R 重新开始。', 'ln-bad');
  }

  function win() {
    state.win = true;
    updateBest();
    log('巨龙轰然倒地！你征服了地牢，荣耀属于你。按 R 再来一局。', 'ln-good');
  }

  function updateBest() {
    if (player.depth > bestDepth) {
      bestDepth = player.depth;
      LS.set('gameRogue.bestDepth', String(bestDepth));
    }
    if (player.gold > bestGold) {
      bestGold = player.gold;
      LS.set('gameRogue.bestGold', String(bestGold));
    }
    renderBest();
  }

  // ---- 渲染 ----
  function log(text, cls) {
    logs.push({ text: text, cls: cls || '' });
    if (logs.length > 60) logs.shift();
    renderLog();
  }

  function renderMap() {
    var lines = [];
    for (var y = 0; y < H; y++) {
      var row = '';
      for (var x = 0; x < W; x++) {
        var ch, cls;
        if (isStairs(x, y)) { ch = '>'; cls = 'm-stair'; }
        else {
          var m = monsterAt(x, y);
          if (m) { ch = m.ch; cls = m.boss ? 'm-boss' : 'm-mon'; }
          else {
            var it = itemAt(x, y);
            if (it) { ch = it.ch; cls = 'm-item'; }
            else if (player.x === x && player.y === y) { ch = '@'; cls = 'm-player'; }
            else if (tiles[y][x] === 0) { ch = '#'; cls = 'm-wall'; }
            else { ch = '·'; cls = 'm-floor'; }
          }
        }
        row += '<span class="' + cls + '">' + ch + '</span>';
      }
      lines.push(row);
    }
    mapEl.innerHTML = lines.join('\n');
  }

  function renderHud() {
    var p = Math.max(0, player.hp);
    var seg = 10;
    var filled = Math.round(p / player.maxhp * seg);
    if (filled < 0) filled = 0; if (filled > seg) filled = seg;
    var bar = '[' + repeat('█', filled) + repeat('░', seg - filled) + ']';
    var status = '';
    if (!state.alive) status = ' <span class="rl-dead">已死亡</span>';
    else if (state.win) status = ' <span class="rl-win">通关！</span>';
    hudEl.innerHTML = 'HP <b>' + bar + '</b> ' + p + '/' + player.maxhp
      + ' · Lv<b>' + player.level + '</b> · ATK <b>' + player.atk + '</b> · DEF <b>' + player.def + '</b>'
      + ' · 层 <b>' + player.depth + '</b> · 金 <b>' + player.gold + '</b>' + status;
  }

  function renderLog() {
    var start = Math.max(0, logs.length - 6);
    var html = '';
    for (var i = start; i < logs.length; i++) {
      html += '<div class="ln ' + logs[i].cls + '">&gt; ' + logs[i].text + '</div>';
    }
    logEl.innerHTML = html;
    logEl.scrollTop = logEl.scrollHeight;
  }

  function renderBest() {
    bestEl.textContent = '最深 ' + bestDepth + ' · 最多金 ' + bestGold;
  }

  function renderAll() {
    renderMap();
    renderHud();
    renderBest();
  }

  // ---- 输入 ----
  function onKey(e) {
    var k = e.key;
    if (k === 'r' || k === 'R') { newGame(); e.preventDefault(); return; }
    if (!state.alive || state.win) return;
    var dx = 0, dy = 0, acted = false;
    if (k === 'ArrowUp' || k === 'w' || k === 'W') { dy = -1; acted = true; }
    else if (k === 'ArrowDown' || k === 's' || k === 'S') { dy = 1; acted = true; }
    else if (k === 'ArrowLeft' || k === 'a' || k === 'A') { dx = -1; acted = true; }
    else if (k === 'ArrowRight' || k === 'd' || k === 'D') { dx = 1; acted = true; }
    else if (k === ' ') { acted = true; log('你原地戒备，环顾四周。', ''); }
    if (acted) {
      e.preventDefault();
      if (dx || dy) tryMove(dx, dy);
      renderAll();
    }
  }

  // ---- 启动 ----
  function newGame() {
    player = { x: 1, y: 1, hp: 20, maxhp: 20, atk: 3, def: 0, level: 1, xp: 0, gold: 0, depth: 1 };
    state = { alive: true, win: false };
    logs = [];
    genFloor(1);
    log('你醒来在一座地牢的第 1 层。探索、变强，活到第 ' + MAX_DEPTH + ' 层斩杀巨龙。', '');
    renderAll();
  }

  function init() {
    mapEl = document.getElementById('map');
    hudEl = document.getElementById('hud');
    logEl = document.getElementById('log');
    bestEl = document.getElementById('best');
    bestDepth = parseInt(LS.get('gameRogue.bestDepth', '0'), 10) || 0;
    bestGold = parseInt(LS.get('gameRogue.bestGold', '0'), 10) || 0;
    document.addEventListener('keydown', onKey);
    newGame();
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
})();
