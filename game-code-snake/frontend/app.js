(function () {
  'use strict'

  var LS = {
    get: function (k) { try { return window.localStorage.getItem(k) } catch (e) { return null } },
    set: function (k, v) { try { window.localStorage.setItem(k, v) } catch (e) {} }
  }

  var TOKENS = [
    { t: '=>', tip: '箭头函数：const f = x => x * 2，单参数可省括号，单表达式省略 return。' },
    { t: 'const', tip: 'const 声明常量绑定，不可重新赋值（但对象内部属性可变）。' },
    { t: '?.', tip: '可选链 a?.b?.c 在 null/undefined 时短路返回 undefined，免去层层判空。' },
    { t: '??', tip: '空值合并 a ?? b：仅 a 为 null/undefined 才取 b，比 || 更安全（0/\'\' 不触发）。' },
    { t: 'async', tip: 'async 函数永远返回 Promise；用 await 等它，错误用 try/catch 接。' },
    { t: 'for', tip: '遍历数组优先 for…of；需要索引用 entries()：[i, v] of arr.entries()。' },
    { t: 'map', tip: 'arr.map 产出等长新数组；只想做副作用遍历请用 forEach。' },
    { t: '??=', tip: '逻辑空赋值 a ??= b：a 为空才赋值，等价于 a = a ?? b。' },
    { t: 'git', tip: 'git rebase -i 可整理提交；对已推送的提交别随便 rebase。' },
    { t: 'SQL', tip: '复合索引遵循最左前缀：WHERE a=1 AND b=2 才能用到 (a,b) 索引。' },
    { t: 'Go', tip: 'Go 的 := 短声明仅限函数内；零值让变量开箱即用，少写 new。' },
    { t: 'PHP', tip: 'PHP 8 命名参数 fn(a: 1) 可读性强，还能跳过可选参数。' },
    { t: 'RegExp', tip: '正则带 g 后 lastIndex 会残留，循环 exec 前记得重置。' },
    { t: 'JSON', tip: 'JSON.parse 失败会抛异常，生产环境务必 try/catch 包裹。' },
    { t: 'try', tip: '别吞异常：catch 里至少打日志，否则问题像黑洞一样消失。' }
  ]

  var COLS = 20, ROWS = 20, CELL = 22
  var cv = document.getElementById('cv')
  var ctx = cv.getContext('2d')

  var snake, dir, nextDir, food, score, snips, alive, paused
  var best = parseInt(LS.get('codeSnake.best') || '0', 10) || 0
  var bestSnips = parseInt(LS.get('codeSnake.bestSnips') || '0', 10) || 0
  var timer = null, tickMs = 130

  var elScore = document.getElementById('score')
  var elBest = document.getElementById('best')
  var elSnips = document.getElementById('snips')
  var elTip = document.getElementById('tip')
  var elOver = document.getElementById('over')
  var elOverText = document.getElementById('overText')
  var elCardToken = document.getElementById('cardToken')
  var elCardTip = document.getElementById('cardTip')

  function colors() {
    var cs = getComputedStyle(document.documentElement)
    function v(n, f) { return (cs.getPropertyValue(n) || '').trim() || f }
    return {
      bg: v('--bg-secondary', '#1e1f23'),
      grid: v('--border', '#2b2d33'),
      snake: v('--accent', '#4a9eff'),
      head: v('--success', '#28c864'),
      foodBg: '#6b4fb0',
      foodText: '#f3eaff'
    }
  }

  function reset() {
    snake = [{ x: 8, y: 10 }, { x: 7, y: 10 }, { x: 6, y: 10 }]
    dir = { x: 1, y: 0 }
    nextDir = dir
    score = 0
    snips = 0
    alive = true
    paused = false
    tickMs = 130
    placeFood()
    elOver.hidden = true
    elCardToken.textContent = '// 吃下代码碎片看提示'
    elCardTip.textContent = '用方向键操控蛇，去吞掉散落的代码片段。每吃一个，都会解锁一条开发小贴士。'
    updateHud()
    elTip.textContent = '方向键 / WASD 移动 · 空格暂停'
    document.getElementById('pause').textContent = '暂停'
  }

  function placeFood() {
    var occupied = {}
    snake.forEach(function (s) { occupied[s.x + ',' + s.y] = 1 })
    var free = []
    for (var x = 0; x < COLS; x++)
      for (var y = 0; y < ROWS; y++)
        if (!occupied[x + ',' + y]) free.push({ x: x, y: y })
    if (!free.length) { win(); return }
    var p = free[(Math.random() * free.length) | 0]
    food = { x: p.x, y: p.y, tok: (Math.random() * TOKENS.length) | 0 }
  }

  function updateHud() {
    elScore.textContent = score
    elBest.textContent = best
    elSnips.textContent = snips
  }

  function step() {
    if (!alive || paused) return
    dir = nextDir
    var head = snake[0]
    var nx = head.x + dir.x
    var ny = head.y + dir.y
    if (nx < 0 || ny < 0 || nx >= COLS || ny >= ROWS) return die()
    var eating = (nx === food.x && ny === food.y)
    var body = eating ? snake : snake.slice(0, snake.length - 1)
    for (var i = 0; i < body.length; i++)
      if (body[i].x === nx && body[i].y === ny) return die()

    snake.unshift({ x: nx, y: ny })
    if (eating) {
      score += 10
      snips++
      showTip(TOKENS[food.tok])
      if (score % 50 === 0 && tickMs > 70) tickMs -= 8
      placeFood()
    } else {
      snake.pop()
    }
    if (score > best) { best = score; LS.set('codeSnake.best', best) }
    if (snips > bestSnips) { bestSnips = snips; LS.set('codeSnake.bestSnips', bestSnips) }
    updateHud()
    draw()
  }

  function showTip(tok) {
    elCardToken.textContent = tok.t
    elCardTip.textContent = tok.tip
  }

  function die() {
    alive = false
    elOver.hidden = false
    elOverText.textContent = '💀 游戏结束 · 分数 ' + score + ' · 片段 ' + snips
  }
  function win() {
    alive = false
    elOver.hidden = false
    elOverText.textContent = '🏆 通关！蛇填满了棋盘'
  }

  function draw() {
    var c = colors()
    ctx.fillStyle = c.bg
    ctx.fillRect(0, 0, cv.width, cv.height)

    ctx.strokeStyle = c.grid
    ctx.lineWidth = 1
    for (var i = 1; i < COLS; i++) {
      ctx.beginPath(); ctx.moveTo(i * CELL, 0); ctx.lineTo(i * CELL, cv.height); ctx.stroke()
      ctx.beginPath(); ctx.moveTo(0, i * CELL); ctx.lineTo(cv.width, i * CELL); ctx.stroke()
    }

    // 食物 = 代码片段
    if (alive && food) {
      var fx = food.x * CELL, fy = food.y * CELL
      roundRect(fx + 2, fy + 2, CELL - 4, CELL - 4, 5)
      ctx.fillStyle = c.foodBg; ctx.fill()
      ctx.fillStyle = c.foodText
      ctx.font = '700 11px ' + (getComputedStyle(document.documentElement).getPropertyValue('--font-mono') || 'monospace')
      ctx.textAlign = 'center'
      ctx.textBaseline = 'middle'
      ctx.fillText(TOKENS[food.tok].t, fx + CELL / 2, fy + CELL / 2 + 1)
    }

    // 蛇
    for (var s = snake.length - 1; s >= 0; s--) {
      ctx.fillStyle = s === 0 ? c.head : c.snake
      var pad = s === 0 ? 1 : 2
      roundRect(snake[s].x * CELL + pad, snake[s].y * CELL + pad, CELL - pad * 2, CELL - pad * 2, 5)
      ctx.fill()
    }
  }

  function roundRect(x, y, w, h, r) {
    ctx.beginPath()
    ctx.moveTo(x + r, y)
    ctx.arcTo(x + w, y, x + w, y + h, r)
    ctx.arcTo(x + w, y + h, x, y + h, r)
    ctx.arcTo(x, y + h, x, y, r)
    ctx.arcTo(x, y, x + w, y, r)
    ctx.closePath()
  }

  function setDir(x, y) {
    if (dir.x === -x && dir.y === -y) return
    nextDir = { x: x, y: y }
  }
  function togglePause() {
    if (!alive) return
    paused = !paused
    document.getElementById('pause').textContent = paused ? '继续' : '暂停'
    elTip.textContent = paused ? '已暂停' : '方向键 / WASD 移动 · 空格暂停'
  }
  function loop() { if (timer) clearInterval(timer); timer = setInterval(step, tickMs) }

  /* ---------- 输入 ---------- */
  var KEYMAP = {
    ArrowLeft: [-1, 0], ArrowRight: [1, 0], ArrowUp: [0, -1], ArrowDown: [0, 1],
    a: [-1, 0], d: [1, 0], w: [0, -1], s: [0, 1],
    A: [-1, 0], D: [1, 0], W: [0, -1], S: [0, 1]
  }
  window.addEventListener('keydown', function (e) {
    if (e.key === ' ') { e.preventDefault(); togglePause(); return }
    var d = KEYMAP[e.key]
    if (!d) return
    e.preventDefault()
    setDir(d[0], d[1])
  })

  var tsx = 0, tsy = 0
  cv.addEventListener('touchstart', function (e) { tsx = e.touches[0].clientX; tsy = e.touches[0].clientY }, { passive: true })
  cv.addEventListener('touchend', function (e) {
    var dx = e.changedTouches[0].clientX - tsx, dy = e.changedTouches[0].clientY - tsy
    if (Math.max(Math.abs(dx), Math.abs(dy)) < 20) return
    if (Math.abs(dx) > Math.abs(dy)) setDir(dx > 0 ? 1 : -1, 0)
    else setDir(0, dy > 0 ? 1 : -1)
  }, { passive: true })

  document.getElementById('pause').addEventListener('click', togglePause)
  document.getElementById('restart').addEventListener('click', function () { reset(); loop() })
  document.getElementById('retry').addEventListener('click', function () { reset(); loop() })

  if (window.MutationObserver) {
    new MutationObserver(function () { if (alive) draw() }).observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] })
  }

  reset()
  loop()
  draw()
})()
