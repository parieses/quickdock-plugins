/* 小黑屋 —— 极简文字经营冒险（纯前端，沙箱安全） */
(function () {
  'use strict';

  var Store = (function () {
    var mem = {};
    var ls = null;
    try { ls = window.localStorage; ls.getItem('__t'); } catch (e) { ls = null; }
    return {
      get: function (k) { if (ls) { try { return ls.getItem(k); } catch (e) {} } return Object.prototype.hasOwnProperty.call(mem, k) ? mem[k] : null; },
      set: function (k, v) { if (ls) { try { ls.setItem(k, v); } catch (e) {} } mem[k] = String(v); }
    };
  })();

  var STAT_DEFS = [
    { k: 'heat', n: '热量', low: 15 }, { k: 'wood', n: '木柴', low: 3 },
    { k: 'pop', n: '村民', low: 1 }, { k: 'food', n: '食物', low: 2 },
    { k: 'fur', n: '皮毛', low: 0 }, { k: 'metal', n: '金属', low: 0 }
  ];
  var BUILD = [
    { id: 'hut',  n: '小屋',   cost: { wood: 10 }, desc: '村民上限+3' },
    { id: 'farm', n: '农场',   cost: { wood: 8 }, desc: '每日+2食物' },
    { id: 'trap', n: '陷阱',   cost: { wood: 6 }, desc: '每日+1肉+1皮' },
    { id: 'mine', n: '矿场',   cost: { wood: 12 }, desc: '每日+1金属' },
    { id: 'forge',n: '铁匠铺', cost: { wood: 6, metal: 8 }, desc: '采集+2，解锁繁荣' }
  ];
  // 流浪商人随机出价：付出 g，得到 get（label 仅展示）
  var TRADES = [
    { g: { fur: 5 },  get: { metal: 3 }, label: '5 皮毛 ⇄ 3 金属' },
    { g: { food: 8 }, get: { pop: 1 },   label: '8 食物 ⇄ 1 村民' },
    { g: { wood: 6 }, get: { fur: 4 },   label: '6 木柴 ⇄ 4 皮毛' },
    { g: { metal: 4 },get: { wood: 10 }, label: '4 金属 ⇄ 10 木柴' },
    { g: { fur: 6 },  get: { pop: 1 },   label: '6 皮毛 ⇄ 1 村民' }
  ];

  var s = null, els = {}, over = false, won = false, toolBonus = 0;

  function boot() { try { init(); } catch (e) { document.body.innerHTML = '<pre style="color:#e5534b;padding:16px;white-space:pre-wrap">插件启动失败\n' + (e && e.stack || e) + '</pre>'; } }

  function init() {
    els.stats = document.getElementById('stats');
    els.log = document.getElementById('log');
    els.build = document.getElementById('build');
    els.turn = document.getElementById('turn');
    els.over = document.getElementById('over');
    els.overText = document.getElementById('overText');
    els.trade = document.getElementById('trade');
    els.tradeText = document.getElementById('tradeText');
    if (!els.log) throw new Error('缺少 #log');

    document.querySelector('.dr-actions').addEventListener('click', function (ev) {
      var b = ev.target.closest('button'); if (!b || !b.dataset.act) return;
      if (!over) act(b.dataset.act);
    });
    els.build.addEventListener('click', function (ev) {
      var b = ev.target.closest('button'); if (!b || !b.dataset.id) return;
      if (!over) build(b.dataset.id);
    });
    els.trade.addEventListener('click', function (ev) {
      var b = ev.target.closest('button'); if (!b || !b.dataset.trade) return;
      if (!over) trade(b.dataset.trade === 'yes');
    });
    document.getElementById('again').addEventListener('click', newGame);
    newGame();
  }

  function newGame() {
    over = false; won = false; toolBonus = 0;
    s = { turn: 0, heat: 50, wood: 5, pop: 1, food: 3, fur: 0, metal: 0, popCap: 3,
          b: { hut: false, farm: false, trap: false, mine: false, forge: false },
          merchant: null };
    els.over.hidden = true;
    els.log.innerHTML = '';
    log('你在黑暗中醒来，身边只有一堆将熄的柴火。', 'stage');
    render();
  }

  function canAfford(cost) { for (var k in cost) if (s[k] < cost[k]) return false; return true; }
  function pay(cost) { for (var k in cost) s[k] -= cost[k]; }

  function act(a) {
    if (a === 'fire') {
      if (s.wood >= 2) { s.wood -= 2; s.heat = Math.min(100, s.heat + 25); log('你添了把柴，火苗窜高。'); }
      else { s.heat = Math.min(100, s.heat + 6); log('柴火不多了，你小心地续了点。', 'bad'); }
    } else if (a === 'chop') {
      s.wood += 3 + toolBonus; s.heat -= 6; log('你砍来 ' + (3 + toolBonus) + ' 捆木柴，手冻得发红。');
    } else if (a === 'hunt') {
      s.food += 2; s.fur += 1; s.heat -= 8; log('狩猎归来，带回 2 份肉和 1 张皮毛。', 'good');
    } else if (a === 'explore') {
      explore();
    }
    tick();
  }

  function explore() {
    var roll = Math.random();
    if (roll < 0.22) { var w = 4 + toolBonus; s.wood += w; log('你在林边拾到 ' + w + ' 捆散落的木柴。', 'good'); }
    else if (roll < 0.40) { s.metal += 3; log('废墟里翻出 3 块金属，沉甸甸的。', 'good'); }
    else if (roll < 0.56) { s.food += 3; s.fur += 2; log('你布下的陷阱有了收获：3 食物、2 皮毛。', 'good'); }
    else if (roll < 0.72) { if (s.pop < s.popCap) { s.pop++; log('一个迷路的旅人加入村落，村民+1。', 'good'); } else log('遇到旅人，但村子已满，他挥手告别。'); }
    else if (roll < 0.86) { var d = 6 + ((Math.random() * 8) | 0); s.heat -= d; log('夜里狼嚎阵阵，寒气逼人，热量-' + d + '。', 'bad'); }
    else if (roll < 0.95) { if (s.pop > 1) { s.pop--; log('一场疫病带走了一位村民。', 'bad'); } else log('你病了一场，所幸挺了过来。', 'bad'); }
    else { log('外头风平浪静，你空手而归，但心静了些。'); }
  }

  function build(id) {
    var b = BUILD.filter(function (x) { return x.id === id; })[0];
    if (s.b[id] || !canAfford(b.cost)) return;
    pay(b.cost); s.b[id] = true;
    if (id === 'hut') { s.popCap += 3; s.pop++; log('小屋落成，能容纳更多村民了。', 'good'); }
    else if (id === 'farm') log('农场开荒，以后每天都有收成。', 'good');
    else if (id === 'trap') log('陷阱布好，坐等猎物上门。', 'good');
    else if (id === 'mine') log('矿场挖通，金属有了稳定来源。', 'good');
    else if (id === 'forge') { toolBonus = 2; log('铁匠铺叮当作响，工具更趁手了。', 'good'); }
    tick();
  }

  function tick() {
    s.turn++;
    var decay = 4 + (s.pop > 3 ? 1 : 0);
    s.heat -= decay;
    if (s.b.farm) s.food += 2;
    if (s.b.trap) { s.food += 1; s.fur += 1; }
    if (s.b.mine) s.metal += 1;
    s.food -= s.pop;
    if (s.food < 0) { s.food = 0; s.pop = Math.max(0, s.pop - 1); log('粮仓见底，一位村民饿走了。', 'bad'); }
    els.turn.textContent = s.turn;
    if (s.heat <= 0) { s.heat = 0; return end(false, '火灭了，黑暗吞没了一切。'); }
    if (s.pop <= 0) return end(false, '最后的村民也离开了，小屋归于沉寂。');
    if (s.pop >= 8 && s.b.forge) return end(true, '炊烟袅袅，八户人家安居乐业——你的村庄繁荣了！');
    maybeMerchant();
    render();
  }

  function maybeMerchant() {
    if (s.merchant) return;
    if (s.turn >= 4 && s.turn % 5 === 0) {
      s.merchant = TRADES[(Math.random() * TRADES.length) | 0];
      log('一位裹着毛皮的商人叩响了门，想做笔交易。', 'stage');
    }
  }

  function trade(accept) {
    if (!s.merchant || over) return;
    var o = s.merchant;
    if (accept) {
      if (!canAfford(o.g)) { log('资源不够，商人摇摇头。', 'bad'); return; }
      pay(o.g);
      for (var k in o.get) s[k] += o.get[k];
      log('你与商人成交：' + o.label + '。', 'good');
    } else {
      log('你婉拒了商人，他裹紧斗篷消失在雪中。');
    }
    s.merchant = null;
    render();
  }

  function end(win, msg) {
    over = true; won = win;
    log(msg, win ? 'good' : 'bad');
    els.overText.textContent = (win ? '🏡 村庄繁荣！' : '💀 游戏结束') + '\n坚持了 ' + s.turn + ' 天';
    els.over.hidden = false;
    if (won) { var best = Store.get('dr-best'); if (best === null || s.turn < +best) Store.set('dr-best', String(s.turn)); }
    render();
  }

  function log(t, c) {
    var d = document.createElement('div');
    d.className = 'dr-line' + (c ? ' ' + c : '');
    d.textContent = t;
    els.log.appendChild(d);
    els.log.scrollTop = els.log.scrollHeight;
  }

  function render() {
    els.stats.innerHTML = STAT_DEFS.map(function (d) {
      var v = Math.round(s[d.k]);
      return '<div class="dr-stat' + (v <= d.low ? ' low' : '') + '">' + d.n + ' <b>' + v + '</b></div>';
    }).join('');
    els.build.innerHTML = BUILD.map(function (b) {
      var done = s.b[b.id], ok = canAfford(b.cost);
      var cost = Object.keys(b.cost).map(function (k) { var d = STAT_DEFS.filter(function (x) { return x.k === k; })[0]; return (d ? d.n : k) + b.cost[k]; }).join(' ');
      return '<button class="p-btn" data-id="' + b.id + '"' + ((done || !ok) ? ' disabled' : '') + '>' +
        (done ? '✓' : '') + b.n + '<span class="cost">' + (done ? b.desc : cost) + '</span></button>';
    }).join('');
    if (s.merchant) {
      els.trade.hidden = false;
      els.tradeText.textContent = '商人出价：' + s.merchant.label + (canAfford(s.merchant.g) ? '' : '（资源不足）');
    } else {
      els.trade.hidden = true;
    }
  }

  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', boot);
  else boot();
})();
