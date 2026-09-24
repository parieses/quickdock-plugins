(function () {
  'use strict'

  var LS = {
    get: function (k) { try { return window.localStorage.getItem(k) } catch (e) { return null } },
    set: function (k, v) { try { window.localStorage.setItem(k, v) } catch (e) {} }
  }

  var COLS = 10, ROWS = 20, CELL = 26
  var cv = document.getElementById('cv')
  var ctx = cv.getContext('2d')
  var ncv = document.getElementById('next')
  var nctx = ncv.getContext('2d')

  var SHAPES = {
    I: [[0,0,0,0],[1,1,1,1],[0,0,0,0],[0,0,0,0]],
    O: [[1,1],[1,1]],
    T: [[0,1,0],[1,1,1],[0,0,0]],
    S: [[0,1,1],[1,1,0],[0,0,0]],
    Z: [[1,1,0],[0,1,1],[0,0,0]],
    J: [[1,0,0],[1,1,1],[0,0,0]],
    L: [[0,0,1],[1,1,1],[0,0,0]]
  }
  var COLOR = {
    I: '#31c7ef', O: '#f7d308', T: '#ad4d9c', S: '#42b642',
    Z: '#ef2029', J: '#5a65ad', L: '#ef7921'
  }
  var TYPES = Object.keys(SHAPES)

  var board, cur, nextType, score, lines, level, alive, paused, timer, tickMs
  var best = parseInt(LS.get('gameTetris.best') || '0', 10) || 0

  var elScore = document.getElementById('score')
  var elLines = document.getElementById('lines')
  var elLevel = document.getElementById('level')
  var elBest = document.getElementById('best')
  var elOver = document.getElementById('over')
  var elOverText = document.getElementById('overText')

  function bgColor() {
    var c = getComputedStyle(document.documentElement).getPropertyValue('--bg-secondary')
    return (c || '').trim() || '#1e1f23'
  }
  function gridColor() {
    var c = getComputedStyle(document.documentElement).getPropertyValue('--border')
    return (c || '').trim() || '#2b2d33'
  }

  function clone(m) { return m.map(function (r) { return r.slice() }) }
  function rotate(m) {
    var n = m.length, r = []
    for (var i = 0; i < n; i++) { r.push([]); for (var j = 0; j < n; j++) r[i].push(m[n - 1 - j][i]) }
    return r
  }
  function randType() { return TYPES[(Math.random() * TYPES.length) | 0] }

  function reset() {
    board = []
    for (var r = 0; r < ROWS; r++) board.push(new Array(COLS).fill(0))
    score = 0; lines = 0; level = 1; alive = true; paused = false
    tickMs = 800
    nextType = randType()
    elOver.hidden = true
    updateHud()
    spawn()
    resetTimer()
    draw()
  }

  function updateHud() {
    elScore.textContent = score
    elLines.textContent = lines
    elLevel.textContent = level
    elBest.textContent = best
  }

  function collide(m, x, y) {
    for (var r = 0; r < m.length; r++)
      for (var c = 0; c < m.length; c++) {
        if (!m[r][c]) continue
        var bx = x + c, by = y + r
        if (bx < 0 || bx >= COLS || by >= ROWS) return true
        if (by >= 0 && board[by][bx]) return true
      }
    return false
  }

  function spawn() {
    var t = nextType
    cur = { type: t, m: clone(SHAPES[t]), x: Math.floor((COLS - SHAPES[t].length) / 2), y: 0 }
    nextType = randType()
    drawNext()
    if (collide(cur.m, cur.x, cur.y)) gameOver()
  }

  function lock() {
    for (var r = 0; r < cur.m.length; r++)
      for (var c = 0; c < cur.m.length; c++) {
        if (!cur.m[r][c]) continue
        var by = cur.y + r
        if (by >= 0) board[by][cur.x + c] = COLOR[cur.type]
      }
    clearLines()
    spawn()
  }

  function clearLines() {
    var cleared = 0
    for (var r = ROWS - 1; r >= 0; r--) {
      if (board[r].every(function (v) { return v })) {
        board.splice(r, 1)
        board.unshift(new Array(COLS).fill(0))
        cleared++
        r++
      }
    }
    if (cleared) {
      var pts = [0, 100, 300, 500, 800][cleared] * level
      score += pts
      lines += cleared
      level = Math.floor(lines / 10) + 1
      tickMs = Math.max(90, 800 - (level - 1) * 70)
      if (score > best) { best = score; LS.set('gameTetris.best', best) }
      updateHud()
    }
  }

  function step() {
    if (!alive || paused) return
    if (!collide(cur.m, cur.x, cur.y + 1)) { cur.y++; draw() }
    else lock(), draw()
  }

  function move(dx) {
    if (!alive || paused) return
    if (!collide(cur.m, cur.x + dx, cur.y)) { cur.x += dx; draw() }
  }
  function rotateCur() {
    if (!alive || paused) return
    var rm = rotate(cur.m)
    var kicks = [0, -1, 1, -2, 2]
    for (var i = 0; i < kicks.length; i++) {
      if (!collide(rm, cur.x + kicks[i], cur.y)) { cur.m = rm; cur.x += kicks[i]; draw(); return }
    }
  }
  function softDrop() {
    if (!alive || paused) return
    if (!collide(cur.m, cur.x, cur.y + 1)) { cur.y++; score += 1; updateHud(); draw() }
    else lock(), draw()
    resetTimer()
  }
  function hardDrop() {
    if (!alive || paused) return
    while (!collide(cur.m, cur.x, cur.y + 1)) cur.y++
    lock(); draw(); resetTimer()
  }

  function ghostY() {
    var y = cur.y
    while (!collide(cur.m, cur.x, y + 1)) y++
    return y
  }

  function block(c, x, y, color, alpha) {
    ctx.globalAlpha = alpha
    ctx.fillStyle = color
    ctx.fillRect(x + 1, y + 1, CELL - 2, CELL - 2)
    ctx.globalAlpha = 1
    // 高光
    ctx.fillStyle = 'rgba(255,255,255,0.18)'
    ctx.fillRect(x + 1, y + 1, CELL - 2, 4)
  }

  function draw() {
    ctx.fillStyle = bgColor()
    ctx.fillRect(0, 0, cv.width, cv.height)
    ctx.strokeStyle = gridColor()
    ctx.lineWidth = 1
    for (var i = 1; i < COLS; i++) { ctx.beginPath(); ctx.moveTo(i * CELL, 0); ctx.lineTo(i * CELL, cv.height); ctx.stroke() }
    for (var j = 1; j < ROWS; j++) { ctx.beginPath(); ctx.moveTo(0, j * CELL); ctx.lineTo(cv.width, j * CELL); ctx.stroke() }

    // 已落定
    for (var r = 0; r < ROWS; r++)
      for (var c = 0; c < COLS; c++)
        if (board[r][c]) block(c * CELL, c * CELL, r * CELL, board[r][c], 1)

    if (alive && cur) {
      // ghost
      var gy = ghostY()
      for (var a = 0; a < cur.m.length; a++)
        for (var b = 0; b < cur.m.length; b++)
          if (cur.m[a][b] && gy + a >= 0)
            block(b * CELL, (cur.x + b) * CELL, (gy + a) * CELL, COLOR[cur.type], 0.22)
      // 当前
      for (var p = 0; p < cur.m.length; p++)
        for (var q = 0; q < cur.m.length; q++)
          if (cur.m[p][q] && cur.y + p >= 0)
            block(q * CELL, (cur.x + q) * CELL, (cur.y + p) * CELL, COLOR[cur.type], 1)
    }

    if (paused && alive) {
      ctx.fillStyle = 'rgba(0,0,0,0.5)'
      ctx.fillRect(0, 0, cv.width, cv.height)
      ctx.fillStyle = '#fff'
      ctx.font = '700 26px sans-serif'
      ctx.textAlign = 'center'
      ctx.fillText('暂停', cv.width / 2, cv.height / 2)
    }
  }

  function drawNext() {
    nctx.fillStyle = bgColor()
    nctx.fillRect(0, 0, ncv.width, ncv.height)
    var m = SHAPES[nextType], n = m.length, s = 22
    var off = (ncv.width - n * s) / 2
    for (var r = 0; r < n; r++)
      for (var c = 0; c < n; c++)
        if (m[r][c]) {
          nctx.fillStyle = COLOR[nextType]
          nctx.fillRect(off + c * s + 1, off + r * s + 1, s - 2, s - 2)
        }
  }

  function gameOver() {
    alive = false
    elOver.hidden = false
    elOverText.textContent = '💥 游戏结束 · ' + score + ' 分'
  }

  function resetTimer() {
    if (timer) clearInterval(timer)
    timer = setInterval(step, tickMs)
  }

  /* ---------- 输入 ---------- */
  window.addEventListener('keydown', function (e) {
    var k = e.key
    if (k === 'p' || k === 'P') { if (alive) { paused = !paused; resetTimer(); draw() } return }
    if (!alive && (k === ' ' || k === 'Enter')) { reset(); return }
    if (k === 'ArrowLeft') { move(-1); e.preventDefault() }
    else if (k === 'ArrowRight') { move(1); e.preventDefault() }
    else if (k === 'ArrowDown') { softDrop(); e.preventDefault() }
    else if (k === 'ArrowUp' || k === 'x' || k === 'X') { rotateCur(); e.preventDefault() }
    else if (k === ' ') { hardDrop(); e.preventDefault() }
  })

  document.getElementById('restart').addEventListener('click', reset)
  document.getElementById('retry').addEventListener('click', reset)

  if (window.MutationObserver) {
    new MutationObserver(function () { draw(); drawNext() }).observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] })
  }

  reset()
})()
