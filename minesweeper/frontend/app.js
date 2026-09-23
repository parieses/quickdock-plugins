(function () {
  'use strict'

  /* ---------- 沙箱安全的 localStorage ---------- */
  var LS = {
    get: function (k) { try { return window.localStorage.getItem(k) } catch (e) { return null } },
    set: function (k, v) { try { window.localStorage.setItem(k, v) } catch (e) {} }
  }

  /* ---------- 难度预设 ---------- */
  var PRESETS = {
    beginner:     { rows: 9,  cols: 9,  mines: 10 },
    intermediate: { rows: 16, cols: 16, mines: 40 },
    expert:       { rows: 16, cols: 30, mines: 99 }
  }

  var EMOJI = { normal: '🙂', waiting: '😮', won: '😎', lost: '😵' }
  var MSG = {
    ready: '左键挖开 · 右键插旗 · 数字上点击快速展开',
    won:   '🎉 通关！',
    lost:  '💥 踩雷了，点笑脸再来一局'
  }

  /* ---------- DOM ---------- */
  var elBoard, elMine, elTime, elSmiley, elStatus, elBest,
      elDifficulty, elCustomBox, elRows, elCols, elMines

  /* ---------- 状态 ---------- */
  var rows = 9, cols = 9, mineTotal = 10
  var grid = []          // 扁平数组，索引 r*cols+c
  var cellEls = []       // cellEls[r][c]
  var state = 'ready'    // ready | playing | won | lost
  var flags = 0
  var revealedCount = 0
  var time = 0
  var timerId = null

  function idx(r, c) { return r * cols + c }

  function neighbors(r, c) {
    var out = []
    for (var dr = -1; dr <= 1; dr++) {
      for (var dc = -1; dc <= 1; dc++) {
        if (!dr && !dc) continue
        var nr = r + dr, nc = c + dc
        if (nr >= 0 && nr < rows && nc >= 0 && nc < cols) out.push({ r: nr, c: nc })
      }
    }
    return out
  }

  /* ---------- 数字显示（3 位，负数带符号） ---------- */
  function fmt(n) {
    if (n < 0) {
      var a = Math.min(99, -n)
      return '-' + (a < 10 ? '0' : '') + a
    }
    var b = Math.min(999, n)
    return ('00' + b).slice(-3)
  }

  /* ---------- 新局 ---------- */
  function newGame() {
    var area = rows * cols
    // 至少留出 9 格安全区，保证首点必展开
    if (mineTotal > area - 9) mineTotal = Math.max(0, area - 9)
    if (mineTotal < 1 && area > 9) mineTotal = 1

    grid = []
    for (var r = 0; r < rows; r++) {
      for (var c = 0; c < cols; c++) {
        grid.push({ r: r, c: c, mine: false, adj: 0, revealed: false, flagged: false, exploded: false, wrong: false })
      }
    }
    state = 'ready'
    flags = 0
    revealedCount = 0
    time = 0
    stopTimer()
    buildBoardDOM()
    updateCounters()
    setSmiley('normal')
    setStatus(MSG.ready, '')
    updateBestLabel()
  }

  function buildBoardDOM() {
    elBoard.style.setProperty('--cols', cols)
    elBoard.innerHTML = ''
    cellEls = []
    var frag = document.createDocumentFragment()
    for (var r = 0; r < rows; r++) {
      cellEls[r] = []
      for (var c = 0; c < cols; c++) {
        var d = document.createElement('div')
        d.className = 'cell hidden'
        d.dataset.r = r
        d.dataset.c = c
        frag.appendChild(d)
        cellEls[r][c] = d
      }
    }
    elBoard.appendChild(frag)
  }

  /* ---------- 布雷（首点安全区：点击格 + 8 邻格） ---------- */
  function placeMines(safeR, safeC) {
    var safe = {}
    safe[idx(safeR, safeC)] = 1
    neighbors(safeR, safeC).forEach(function (n) { safe[idx(n.r, n.c)] = 1 })

    var pool = []
    for (var i = 0; i < grid.length; i++) if (!safe[i]) pool.push(i)
    // Fisher-Yates
    for (var j = pool.length - 1; j > 0; j--) {
      var k = Math.floor(Math.random() * (j + 1))
      var t = pool[j]; pool[j] = pool[k]; pool[k] = t
    }
    var n = Math.min(mineTotal, pool.length)
    for (var m = 0; m < n; m++) grid[pool[m]].mine = true

    // 计算相邻雷数
    for (var p = 0; p < grid.length; p++) {
      var g = grid[p]
      if (g.mine) continue
      var cnt = 0
      neighbors(g.r, g.c).forEach(function (nb) { if (grid[idx(nb.r, nb.c)].mine) cnt++ })
      g.adj = cnt
    }
  }

  /* ---------- 亮开 ---------- */
  function reveal(r, c) {
    if (state === 'won' || state === 'lost') return
    var g = grid[idx(r, c)]
    if (g.revealed || g.flagged) return

    if (state === 'ready') {
      placeMines(r, c)
      state = 'playing'
      startTimer()
    }

    if (g.mine) {
      g.revealed = true
      g.exploded = true
      revealedCount++
      updateCell(r, c)
      loseGame()
      return
    }
    floodFrom(r, c)
    checkWin()
  }

  function floodFrom(r, c) {
    var stack = [[r, c]]
    while (stack.length) {
      var p = stack.pop()
      var cr = p[0], cc = p[1]
      var cg = grid[idx(cr, cc)]
      if (cg.revealed || cg.flagged) continue
      cg.revealed = true
      revealedCount++
      updateCell(cr, cc)
      if (cg.adj === 0) {
        neighbors(cr, cc).forEach(function (nb) {
          var ng = grid[idx(nb.r, nb.c)]
          if (!ng.revealed && !ng.flagged && !ng.mine) stack.push([nb.r, nb.c])
        })
      }
    }
  }

  /* ---------- 插旗 ---------- */
  function toggleFlag(r, c) {
    if (state === 'won' || state === 'lost') return
    var g = grid[idx(r, c)]
    if (g.revealed) return
    g.flagged = !g.flagged
    flags += g.flagged ? 1 : -1
    updateCell(r, c)
    updateCounters()
  }

  /* ---------- 和棋（数字上快速展开） ---------- */
  function chord(r, c) {
    if (state === 'won' || state === 'lost') return
    var g = grid[idx(r, c)]
    if (!g.revealed || g.adj === 0) return
    var f = 0
    neighbors(r, c).forEach(function (nb) { if (grid[idx(nb.r, nb.c)].flagged) f++ })
    if (f !== g.adj) return
    neighbors(r, c).forEach(function (nb) {
      var ng = grid[idx(nb.r, nb.c)]
      if (!ng.flagged && !ng.revealed) reveal(nb.r, nb.c)
    })
  }

  /* ---------- 胜负 ---------- */
  function checkWin() {
    if (state === 'playing' && revealedCount === rows * cols - mineTotal) winGame()
  }

  function winGame() {
    state = 'won'
    stopTimer()
    for (var i = 0; i < grid.length; i++) if (grid[i].mine) grid[i].flagged = true
    flags = mineTotal
    renderAll()
    updateCounters()
    setSmiley('won')
    var rec = recordBest()
    setStatus(MSG.won + ' 用时 ' + time + ' 秒' + (rec ? '（新纪录）' : ''), 'win')
  }

  function loseGame() {
    state = 'lost'
    stopTimer()
    for (var i = 0; i < grid.length; i++) {
      var g = grid[i]
      if (g.mine) { if (!g.flagged) g.revealed = true }
      else if (g.flagged) { g.wrong = true; g.revealed = true }
    }
    renderAll()
    setSmiley('lost')
    setStatus(MSG.lost, 'lose')
  }

  /* ---------- 渲染 ---------- */
  function updateCell(r, c) {
    var g = grid[idx(r, c)]
    var el = cellEls[r][c]
    if (!el) return
    el.className = 'cell'
    if (g.revealed) {
      el.classList.add('revealed')
      if (g.mine) {
        el.classList.add('mine')
        if (g.exploded) el.classList.add('exploded')
        el.textContent = '💣'
      } else if (g.wrong) {
        el.classList.add('wrong')
        el.textContent = '❌'
      } else if (g.adj > 0) {
        el.classList.add('n' + g.adj)
        el.textContent = String(g.adj)
      } else {
        el.textContent = ''
      }
    } else {
      el.classList.add('hidden')
      el.textContent = g.flagged ? '🚩' : ''
    }
  }

  function renderAll() {
    for (var r = 0; r < rows; r++) for (var c = 0; c < cols; c++) updateCell(r, c)
  }

  function updateCounters() {
    elMine.textContent = fmt(mineTotal - flags)
    elTime.textContent = fmt(time)
  }

  function setSmiley(mood) { elSmiley.textContent = EMOJI[mood] || EMOJI.normal }

  function setStatus(text, cls) {
    elStatus.textContent = text
    elStatus.className = 'ms-status' + (cls ? ' ' + cls : '')
  }

  /* ---------- 计时 ---------- */
  function startTimer() {
    stopTimer()
    timerId = setInterval(function () {
      time++
      if (time > 999) time = 999
      elTime.textContent = fmt(time)
    }, 1000)
  }
  function stopTimer() { if (timerId) { clearInterval(timerId); timerId = null } }

  /* ---------- 最佳成绩（按难度存） ---------- */
  function bestKey() { return 'qdms:best:' + rows + 'x' + cols + ':' + mineTotal }
  function getBest() {
    var v = LS.get(bestKey())
    return v == null || v === '' ? null : parseInt(v, 10)
  }
  function recordBest() {
    var b = getBest()
    if (b === null || time < b) { LS.set(bestKey(), String(time)); updateBestLabel(); return true }
    updateBestLabel()
    return false
  }
  function updateBestLabel() {
    var b = getBest()
    elBest.textContent = b === null ? '最佳 —' : '最佳 ' + fmt(b) + ' 秒'
  }

  /* ---------- 难度切换 ---------- */
  function applyDifficulty() {
    var v = elDifficulty.value
    if (v === 'custom') {
      elCustomBox.classList.add('show')
      readCustom()
    } else {
      elCustomBox.classList.remove('show')
      rows = PRESETS[v].rows
      cols = PRESETS[v].cols
      mineTotal = PRESETS[v].mines
    }
    newGame()
  }

  function clamp(v, lo, hi) { v = parseInt(v, 10); if (isNaN(v)) v = lo; return Math.max(lo, Math.min(hi, v)) }

  function readCustom() {
    rows = clamp(elRows.value, 5, 30)
    cols = clamp(elCols.value, 5, 40)
    mineTotal = clamp(elMines.value, 1, rows * cols - 9 < 1 ? 1 : rows * cols - 9)
    elRows.value = rows; elCols.value = cols; elMines.value = mineTotal
  }

  /* ---------- 交互 ---------- */
  function cellFromEvent(e) {
    var t = e.target
    if (!t || !t.closest) return null
    var cell = t.closest('.cell')
    if (!cell) return null
    return { r: +cell.dataset.r, c: +cell.dataset.c, el: cell }
  }

  function onMouseDown(e) {
    if (e.button !== 0) return
    var cell = cellFromEvent(e)
    if (!cell) return
    var g = grid[idx(cell.r, cell.c)]
    if ((state === 'ready' || state === 'playing') && !g.revealed && !g.flagged) setSmiley('waiting')
  }

  function onMouseUp(e) {
    if (e.button === 0) {
      var cell = cellFromEvent(e)
      if (cell && (state === 'ready' || state === 'playing')) {
        var g = grid[idx(cell.r, cell.c)]
        if (!g.revealed && !g.flagged) reveal(cell.r, cell.c)
        else if (g.revealed && g.adj > 0) chord(cell.r, cell.c)
      }
      if (state === 'won') setSmiley('won')
      else if (state === 'lost') setSmiley('lost')
      else setSmiley('normal')
    } else if (e.button === 1) {
      e.preventDefault()
      var cell2 = cellFromEvent(e)
      if (cell2) chord(cell2.r, cell2.c)
    }
  }

  function onContextMenu(e) {
    e.preventDefault()
    var cell = cellFromEvent(e)
    if (cell) toggleFlag(cell.r, cell.c)
  }

  /* ---------- 启动 ---------- */
  function boot() {
    elBoard = document.getElementById('board')
    elMine = document.getElementById('mineCounter')
    elTime = document.getElementById('timeCounter')
    elSmiley = document.getElementById('smiley')
    elStatus = document.getElementById('status')
    elBest = document.getElementById('best')
    elDifficulty = document.getElementById('difficulty')
    elCustomBox = document.getElementById('customBox')
    elRows = document.getElementById('inRows')
    elCols = document.getElementById('inCols')
    elMines = document.getElementById('inMines')

    elBoard.addEventListener('mousedown', onMouseDown)
    window.addEventListener('mouseup', onMouseUp)
    elBoard.addEventListener('contextmenu', onContextMenu)
    elSmiley.addEventListener('click', function () { newGame() })
    elDifficulty.addEventListener('change', applyDifficulty)
    ;[elRows, elCols, elMines].forEach(function (inp) {
      inp.addEventListener('change', function () {
        if (elDifficulty.value === 'custom') { readCustom(); newGame() }
      })
    })

    // 阻止棋盘上的默认拖拽/选中
    elBoard.addEventListener('dragstart', function (e) { e.preventDefault() })

    newGame()
  }

  try {
    if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', boot)
    else boot()
  } catch (e) {
    document.body.innerHTML = '<pre style="color:#e5534b;padding:16px;white-space:pre-wrap">'
      + '插件启动失败\n' + (e && e.stack || e) + '</pre>'
  }
})()
