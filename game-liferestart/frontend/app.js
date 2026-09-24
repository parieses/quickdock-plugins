/* 人生重开模拟器 —— 纯前端文字人生模拟（沙箱安全） */
(function () {
  'use strict';

  var TALENTS = [
    { id: 'rich',   name: '富二代',     desc: '家境 +3，启动资金丰厚', apply: function (s) { s.family += 3; s.money += 6000; } },
    { id: 'genius', name: '天才学霸',   desc: '智力 +3',            apply: function (s) { s.iq += 3; } },
    { id: 'beauty', name: '颜值天花板', desc: '颜值 +3',            apply: function (s) { s.looks += 3; } },
    { id: 'iron',   name: '铁胃',       desc: '初始体质 +25',        apply: function (s) { s.hp += 25; } },
    { id: 'koi',    name: '锦鲤附体',   desc: '运气 +3，祸事减半',    apply: function (s) { s.luck += 3; } },
    { id: 'social', name: '社交牛逼症', desc: '初始快乐 +15',        apply: function (s) { s.happy += 15; } },
    { id: 'coder',  name: '码农预备役', desc: '青年收入更稳更高',    apply: function (s) { s.coder = true; } },
    { id: 'athlete',name: '运动健将',   desc: '体质 +2，更长寿',      apply: function (s) { s.hp += 10; s.longevity = 2; } }
  ];
  var ATTRS = [
    { key: 'con', name: '体质' }, { key: 'iq', name: '智力' },
    { key: 'looks', name: '颜值' }, { key: 'family', name: '家境' }
  ];
  var POOL = 20;

  var s = null;            // 当前人生状态
  var chosen = {};         // 选中的天赋 id
  var els = {};

  function boot() {
    try { init(); }
    catch (e) {
      document.body.innerHTML = '<pre style="color:#e5534b;padding:16px;white-space:pre-wrap">插件启动失败\n' +
        (e && e.stack || e) + '</pre>';
    }
  }

  function init() {
    els.setup = document.getElementById('setup');
    els.play = document.getElementById('play');
    els.end = document.getElementById('end');
    els.talents = document.getElementById('talents');
    els.attrs = document.getElementById('attrs');
    els.pool = document.getElementById('pool');
    els.start = document.getElementById('start');
    els.log = document.getElementById('log');
    els.age = document.getElementById('age');
    els.hp = document.getElementById('hp');
    els.money = document.getElementById('money');
    els.happy = document.getElementById('happy');
    if (!els.start) throw new Error('缺少 #start');

    // 天赋
    els.talents.innerHTML = TALENTS.map(function (t) {
      return '<div class="lr-talent" data-id="' + t.id + '">' + t.name + '</div>';
    }).join('');
    els.talents.addEventListener('click', function (ev) {
      var el = ev.target.closest('.lr-talent'); if (!el) return;
      var id = el.dataset.id;
      if (chosen[id]) { delete chosen[id]; el.classList.remove('on'); }
      else { if (Object.keys(chosen).length >= 3) return; chosen[id] = 1; el.classList.add('on'); }
    });

    // 属性滑块
    els.attrs.innerHTML = ATTRS.map(function (a) {
      return '<div class="lr-attr"><span>' + a.name + '</span>' +
        '<input type="range" min="1" max="10" value="5" data-k="' + a.key + '">' +
        '<b>5</b></div>';
    }).join('');
    els.attrs.addEventListener('input', function (ev) {
      var inp = ev.target; if (inp.tagName !== 'INPUT') return;
      var others = ATTRS.reduce(function (sum, a) {
        if (a.key === inp.dataset.k) return sum;
        return sum + (+inp.parentNode.querySelector('input[data-k="' + a.key + '"]').value);
      }, 0);
      var max = POOL - others;
      if (+inp.value > max) inp.value = max;
      inp.parentNode.querySelector('b').textContent = inp.value;
      updatePool();
    });
    updatePool();

    els.start.addEventListener('click', startLife);
    document.getElementById('restart').addEventListener('click', function () { show(els.setup); });
    document.getElementById('again').addEventListener('click', function () { show(els.setup); });
    document.getElementById('next').addEventListener('click', nextYear);
  }

  function updatePool() {
    var sum = ATTRS.reduce(function (t, a) {
      var inp = els.attrs.querySelector('input[data-k="' + a.key + '"]');
      return t + (+inp.value);
    }, 0);
    els.pool.textContent = '剩余 ' + (POOL - sum);
  }

  function show(el) { [els.setup, els.play, els.end].forEach(function (x) { x.hidden = x !== el; }); }

  function startLife() {
    s = { age: 0, hp: 0, money: 0, happy: 60, iq: 5, looks: 5, family: 5, luck: 0, coder: false, longevity: 0, alive: true };
    ATTRS.forEach(function (a) {
      var v = +els.attrs.querySelector('input[data-k="' + a.key + '"]').value;
      s[a.key] = v;
    });
    s.hp = 60 + s.con * 4;
    s.money = s.family * 800;
    s.happy = 50 + (s.looks - 5) * 2;
    TALENTS.forEach(function (t) { if (chosen[t.id]) t.apply(s); });
    s.hp = Math.min(140, s.hp);
    show(els.play);
    els.log.innerHTML = '';
    log('你出生了。家里' + (s.family >= 8 ? '很有钱' : s.family <= 3 ? '一贫如洗' : '普普通通') + '。', 'stage');
    sync();
  }

  function sync() {
    els.age.textContent = s.age;
    els.hp.textContent = Math.max(0, Math.round(s.hp));
    els.money.textContent = s.money;
    els.happy.textContent = Math.max(0, Math.round(s.happy));
  }

  function log(text, cls) {
    var d = document.createElement('div');
    d.className = 'lr-line' + (cls ? ' ' + cls : '');
    d.textContent = text;
    els.log.appendChild(d);
    els.log.scrollTop = els.log.scrollHeight;
  }

  function stageName(a) {
    if (a < 7) return '幼儿期'; if (a < 13) return '童年'; if (a < 18) return '少年';
    if (a < 36) return '青年'; if (a < 56) return '中年'; if (a < 80) return '老年'; return '晚年';
  }

  var EVENTS = [
    function () { // 学习
      var r = Math.random();
      if (s.iq >= 7 && r > 0.3) { s.happy += 2; return { t: '你埋头苦读，成绩名列前茅，老师总夸你聪明。', c: 'good' }; }
      if (s.iq <= 3) { s.happy -= 3; return { t: '课本上的字像蚂蚁，你皱着眉睡着了。', c: 'bad' }; }
      return { t: '上学路上你认识了几个要好的朋友。', c: '' };
    },
    function () {
      if (s.looks >= 8) { s.happy += 4; return { t: '你长开了，回头率奇高，暗恋你的人排起了队。', c: 'good' }; }
      if (s.looks <= 3) { s.happy -= 2; return { t: '镜子里的自己让你有点自卑，但你学会了用幽默化解。', c: 'bad' }; }
      return { t: '青春期的你开始在意自己的外表。', c: '' };
    },
    function () {
      var gain = 200 + s.iq * 120 + (s.coder ? 400 : 0) + ((s.luck + Math.random() * 4) | 0) * 60;
      s.money += gain;
      return { t: '你参加工作，第一年攒下 ¥' + gain + '。' + (s.coder ? '代码写得飞起。' : ''), c: 'good' };
    },
    function () {
      var cost = 300 + ((Math.random() * 500) | 0);
      s.money = Math.max(0, s.money - cost);
      s.happy += 6;
      return { t: '你谈了一场轰轰烈烈的恋爱，钱包瘦了 ¥' + cost + '，心里却甜。', c: 'good' };
    },
    function () {
      if (Math.random() < 0.5 + s.luck * 0.05) { s.money += 1500; s.happy += 3; return { t: '一次大胆的投资让你小赚一笔。', c: 'good' }; }
      s.money = Math.max(0, s.money - 800); s.happy -= 4; return { t: '跟风炒东西亏了一笔，你长记性了。', c: 'bad' };
    },
    function () {
      s.hp -= 8; s.happy -= 2;
      return { t: '一场小病让你卧床几天，才懂得身体是革命的本钱。', c: 'bad' };
    },
    function () {
      if (s.family >= 8) { s.money += 2000; return { t: '家里长辈帮你付了首付，你有了自己的小窝。', c: 'good' }; }
      return { t: '你和家人吃了顿团圆饭，聊聊近况。', c: '' };
    },
    function () {
      s.happy += 5;
      return { t: '周末你和朋友去了趟远方，风景治愈了疲惫。', c: 'good' };
    },
    function () {
      var luckRoll = Math.random() + s.luck * 0.04;
      if (luckRoll > 0.85) { s.money += 5000; return { t: '走路捡到钱包，失主还给你一笔谢礼。', c: 'good' }; }
      if (luckRoll < 0.25) { s.hp -= 5; return { t: '下楼踩空崴了脚，倒霉但无大碍。', c: 'bad' }; }
      return { t: '平淡的一天，你读了半本书。', c: '' };
    },
    function () {
      s.happy += 3; s.money -= 200;
      return { t: '你养了一只猫/狗，日子多了份牵挂。', c: 'good' };
    }
  ];

  function nextYear() {
    if (!s || !s.alive || s.age >= 80) { finish(); return; }
    var step = 1 + ((Math.random() * 4) | 0);
    s.age = Math.min(80, s.age + step);
    // 自然衰减
    var decay = 1 + Math.floor(s.age / 22) + (s.age > 60 ? 2 : 0) - s.longevity;
    s.hp -= Math.max(0, decay);
    s.happy = Math.max(0, Math.min(100, s.happy + 1));

    var prevStage = stageName(s.age - step), nowStage = stageName(s.age);
    if (prevStage !== nowStage) log('—— ' + nowStage + ' ——', 'stage');

    var ev = EVENTS[(Math.random() * EVENTS.length) | 0]();
    log(ev.t, ev.c);
    if (ev.t.indexOf('¥') >= 0) {} // noop

    // 随机事件死亡（年纪越大概率越高，luck 降低）
    var deathChance = (s.age > 70 ? 0.06 : s.age > 55 ? 0.02 : 0) - s.luck * 0.005;
    if (s.hp <= 0 || Math.random() < deathChance) {
      s.alive = false;
      log('生命走到了尽头。', 'bad');
      finish();
      return;
    }
    if (s.age >= 80) { log('你安然走到了八十岁。', 'stage'); finish(); return; }
    sync();
  }

  function finish() {
    show(els.end);
    var t = document.getElementById('endTitle'), sum = document.getElementById('endSum');
    var title, desc;
    if (!s.alive) {
      title = s.age < 40 ? '⚡ 英年早逝' : '🌫 溘然长逝';
      desc = '你只活了 ' + s.age + ' 岁。' + (s.hp <= 0 ? '身体先一步撑不住了。' : '命运开了个玩笑。');
    } else {
      title = s.age >= 80 ? '🎉 寿终正寝' : '🌟 平凡一生';
      desc = '你走完了 ' + s.age + ' 岁的人生。';
    }
    desc += ' 终局：资产 ¥' + s.money + '，快乐 ' + Math.round(s.happy) + '。天赋：' +
      (Object.keys(chosen).map(function (id) {
        var t2 = TALENTS.filter(function (x) { return x.id === id; })[0]; return t2 ? t2.name : id;
      }).join('、') || '无') + '。';
    t.textContent = title; sum.textContent = desc;
  }

  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', boot);
  else boot();
})();
