(function () {
  'use strict'

  var LS = {
    get: function (k) { try { return window.localStorage.getItem(k) } catch (e) { return null } },
    set: function (k, v) { try { window.localStorage.setItem(k, v) } catch (e) {} }
  }

  var COLS = 20, ROWS = 20, CELL = 22
  var cv = document.getElementById('cv')
  var ctx = cv.getContext('2d')

  var snake, dir, nextDir, food, score, alive, paused
  var best = parseInt(LS.get('gameSnake.best') || '0', 10) || 0
  var bestLen = parseInt(LS.get('gameSnake.bestLen') || '0', 10) || 0
  var timer = null, tickMs = 130

  var elScore = document.getElementById('score')
  var elBest = document.getElementById('best')
  var elBestLen = document.getElementById('bestLen')
  var elTip = document.getElementById('tip')
  var elOver = document.getElementById('over')
  var elOverText = document.getElementById('overText')

  function colors() {
    var cs = getComputedStyle(document.documentElement)
    function v(n, f) { return (cs.getPropertyValue(n) || '').trim() || f }
    return {
      bg: v('--bg-secondary', '#1e1f23'),
      grid: v('--border', '#2b2d33'),
      snake: v('--accent', '#4a9eff'),
      head: v('--success', '#28c864'),
      food: v('--danger', '#e24b4a')
    }
  }

  function reset() {
    snake = [{ x: 8, y: 10 }, { x: 7, y: 10 }, { x: 6, y: 10 }]
    dir = { x: 1, y: 0 }
    nextDir = dir
    score = 0
    alive = true
    paused = false
    tickMs = 130
    placeFood()
    elOver.hidden = true
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
    food = free[(Math.random() * free.length) | 0]
  }

  function updateHud() {
    elScore.textContent = score
    elBest.textContent = best
    elBestLen.textContent = bestLen
  }

  function step() {
    if (!alive || paused) return
    dir = nextDir
    var head = snake[0]
    var nx = head.x + dir.x
    var ny = head.y + dir.y

    // 撞墙
    if (nx < 0 || ny < 0 || nx >= COLS || ny >= ROWS) return die()
    // 撞自己（尾部这步会移走，除非正在吃长）
    var eating = (nx === food.x && ny === food.y)
    var body = eating ? snake : snake.slice(0, snake.length - 1)
    for (var i = 0; i < body.length; i++) {
      if (body[i].x === nx && body[i].y === ny) return die()
    }

    snake.unshift({ x: nx, y: ny })
    if (eating) {
      score += 10
      if (score % 50 === 0 && tickMs > 70) tickMs -= 8
      placeFood()
    } else {
      snake.pop()
    }
    if (snake.length > bestLen) { bestLen = snake.length; LS.set('gameSnake.bestLen', bestLen) }
    if (score > best) { best = score; LS.set('gameSnake.best', best) }
    updateHud()
    draw()
  }

  function die() {
    alive = false
    elOver.hidden = false
    elOverText.textContent = '💀 游戏结束 · 得分 ' + score
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

    // 网格
    ctx.strokeStyle = c.grid
    ctx.lineWidth = 1
    for (var i = 1; i < COLS; i++) {
      ctx.beginPath(); ctx.moveTo(i * CELL, 0); ctx.lineTo(i * CELL, cv.height); ctx.stroke()
      ctx.beginPath(); ctx.moveTo(0, i * CELL); ctx.lineTo(cv.width, i * CELL); ctx.stroke()
    }

    // 食物
    ctx.fillStyle = c.food
    roundRect(food.x * CELL + 3, food.y * CELL + 3, CELL - 6, CELL - 6, 5)
    ctx.fill()

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
    // 不能直接反向
    if (dir.x === -x && dir.y === -y) return
    nextDir = { x: x, y: y }
  }

  function loop() {
    if (timer) clearInterval(timer)
    timer = setInterval(step, tickMs)
  }

  /* ---------- 输入 ---------- */
  var KEYMAP = {
    ArrowLeft: [-1, 0], ArrowRight: [1, 0], ArrowUp: [0, -1], ArrowDown: [0, 1],
    a: [-1, 0], d: [1, 0], w: [0, -1], s: [0, 1],
    A: [-1, 0], D: [1, 0], W: [0, -1], S: [0, 1]
  }
  window.addEventListener('keydown', function (e) {
    if (e.key === ' ') {
      e.preventDefault()
      togglePause()
      return
    }
    var d = KEYMAP[e.key]
    if (!d) return
    e.preventDefault()
    setDir(d[0], d[1])
  })

  var tsx = 0, tsy = 0
  cv.addEventListener('touchstart', function (e) {
    tsx = e.touches[0].clientX; tsy = e.touches[0].clientY
  }, { passive: true })
  cv.addEventListener('touchend', function (e) {
    var dx = e.changedTouches[0].clientX - tsx
    var dy = e.changedTouches[0].clientY - tsy
    if (Math.max(Math.abs(dx), Math.abs(dy)) < 20) return
    if (Math.abs(dx) > Math.abs(dy)) setDir(dx > 0 ? 1 : -1, 0)
    else setDir(0, dy > 0 ? 1 : -1)
  }, { passive: true })

  function togglePause() {
    if (!alive) return
    paused = !paused
    document.getElementById('pause').textContent = paused ? '继续' : '暂停'
    elTip.textContent = paused ? '已暂停' : '方向键 / WASD 移动 · 空格暂停'
  }

  document.getElementById('pause').addEventListener('click', togglePause)
  document.getElementById('restart').addEventListener('click', function () { reset(); loop() })
  document.getElementById('retry').addEventListener('click', function () { reset(); loop() })

  // 主题切换时重绘
  if (window.MutationObserver) {
    new MutationObserver(function () { if (alive) draw() }).observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] })
  }

  reset()
  loop()
  draw()
})()
