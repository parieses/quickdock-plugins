(function () {
  'use strict'

  var LS = {
    get: function (k, d) {
      try { var v = localStorage.getItem(k); return v == null ? d : v; } catch (e) { return d; }
    },
    set: function (k, v) { try { localStorage.setItem(k, v); } catch (e) {} }
  }

  var HUMAN = 1, AI = 2, N = 15
  var DIRS = [[0, 1], [1, 0], [1, 1], [1, -1]]
  var WIN = 1e9

  function idx(r, c) { return r * N + c }
  function inB(r, c) { return r >= 0 && r < N && c >= 0 && c < N }
  function opp(p) { return p === 1 ? 2 : 1 }

  // 在 (r,c) 落 p 是否形成 >=5 连，返回连线格子数组或 null
  function lineAt(b, r, c, p) {
    for (var d = 0; d < DIRS.length; d++) {
      var dr = DIRS[d][0], dc = DIRS[d][1]
      var cells = [[r, c]], k
      for (k = 1; k < 5; k++) {
        var nr = r + dr * k, nc = c + dc * k
        if (inB(nr, nc) && b[idx(nr, nc)] === p) cells.push([nr, nc]); else break
      }
      for (k = 1; k < 5; k++) {
        var pr = r - dr * k, pc = c - dc * k
        if (inB(pr, pc) && b[idx(pr, pc)] === p) cells.unshift([pr, pc]); else break
      }
      if (cells.length >= 5) return cells
    }
    return null
  }

  function candidates(b) {
    var has = false, set = {}
    for (var r = 0; r < N; r++) {
      for (var c = 0; c < N; c++) {
        if (b[idx(r, c)] === 0) continue
        has = true
        for (var dr = -2; dr <= 2; dr++) {
          for (var dc = -2; dc <= 2; dc++) {
            var nr = r + dr, nc = c + dc
            if (inB(nr, nc) && b[idx(nr, nc)] === 0) set[idx(nr, nc)] = 1
          }
        }
      }
    }
    if (!has) return [{ r: 7, c: 7 }]
    var out = []
    for (var key in set) {
      if (set.hasOwnProperty(key)) out.push({ r: (key / N) | 0, c: key % N })
    }
    return out
  }

  // 在 (r,c) 想象落 p 的评分：某方向连子数 + 两端开放情况
  function lineScoreAt(b, r, c, p) {
    var total = 0
    for (var d = 0; d < DIRS.length; d++) {
      var dr = DIRS[d][0], dc = DIRS[d][1]
      var count = 1, openEnds = 0, k, nr, nc
      for (k = 1; k < 5; k++) {
        nr = r + dr * k; nc = c + dc * k
        if (!inB(nr, nc)) break
        var v = b[idx(nr, nc)]
        if (v === p) count++
        else if (v === 0) { openEnds++; break }
        else break
      }
      for (k = 1; k < 5; k++) {
        nr = r - dr * k; nc = c - dc * k
        if (!inB(nr, nc)) break
        var v2 = b[idx(nr, nc)]
        if (v2 === p) count++
        else if (v2 === 0) { openEnds++; break }
        else break
      }
      total += scoreOf(count, openEnds)
    }
    return total
  }

  function scoreOf(count, openEnds) {
    if (count >= 5) return WIN
    if (count === 4) return openEnds === 2 ? 1e8 : (openEnds === 1 ? 1e6 : 0)
    if (count === 3) return openEnds === 2 ? 1e5 : (openEnds === 1 ? 1e3 : 0)
    if (count === 2) return openEnds === 2 ? 1e3 : (openEnds === 1 ? 100 : 0)
    return openEnds === 2 ? 100 : 10
  }

  function chooseMove(b, ai) {
    var cands = candidates(b)
    var best = -1, bm = null
    for (var i = 0; i < cands.length; i++) {
      var r = cands[i].r, c = cands[i].c
      var off = lineScoreAt(b, r, c, ai)
      var def = lineScoreAt(b, r, c, opp(ai))
      var total = off + def * 0.9
      if (total > best) { best = total; bm = cands[i] }
    }
    return bm
  }

  // ---------- 视图 ----------
  var grid = document.getElementById('grid')
  var elStatus = document.getElementById('status')
  var elOver = document.getElementById('over')
  var elOverText = document.getElementById('overText')
  var elBest = document.getElementById('best')
  var cells = []

  var board, lastMove, winCells, over, lock

  function buildGrid() {
    grid.innerHTML = ''
    cells = []
    for (var r = 0; r < N; r++) {
      for (var c = 0; c < N; c++) {
        var cell = document.createElement('div')
        cell.className = 'g-cell'
        if (r === 0) cell.classList.add('edge-top')
        if (c === 0) cell.classList.add('edge-left')
        cell.dataset.i = idx(r, c)
        cell.addEventListener('click', onCellClick)
        grid.appendChild(cell)
        cells.push(cell)
      }
    }
  }

  function render() {
    for (var i = 0; i < N * N; i++) {
      var v = board[i]
      var cell = cells[i]
      if (v === 0) { cell.innerHTML = ''; continue }
      var cls = (v === HUMAN) ? 'black' : 'white'
      var extra = ''
      if (lastMove && lastMove.r * N + lastMove.c === i) extra += ' last'
      if (winCells && winCells.indexOf(i) >= 0) extra += ' win'
      cell.innerHTML = '<div class="g-stone ' + cls + extra + '"></div>'
    }
    if (over) {
      elOver.hidden = false
    } else {
      elOver.hidden = true
      elStatus.textContent = (turn === HUMAN) ? '轮到你落子' : '电脑思考中…'
    }
  }

  var turn

  function onCellClick(e) {
    if (lock || over || turn !== HUMAN) return
    var i = parseInt(e.currentTarget.dataset.i, 10)
    if (board[i] !== 0) return
    place(HUMAN, (i / N) | 0, i % N)
    if (over) return
    turn = AI
    render()
    scheduleAI()
  }

  function place(p, r, c) {
    board[idx(r, c)] = p
    lastMove = { r: r, c: c }
    var line = lineAt(board, r, c, p)
    if (line) {
      winCells = line.map(function (x) { return x[0] * N + x[1] })
      over = true
      var youWin = (p === HUMAN)
      elOverText.textContent = youWin ? '你赢了！' : '电脑获胜'
      if (youWin) record('w')
      else record('l')
      render()
      return
    }
    if (isFull()) {
      over = true
      winCells = null
      elOverText.textContent = '和棋'
      record('d')
      render()
      return
    }
    render()
  }

  function isFull() {
    for (var i = 0; i < N * N; i++) if (board[i] === 0) return false
    return true
  }

  function scheduleAI() {
    if (over) return
    lock = true
    elStatus.textContent = '电脑思考中…'
    setTimeout(function () {
      lock = false
      var mv = chooseMove(board, AI)
      if (!mv) { over = true; elOverText.textContent = '和棋'; record('d'); render(); return }
      place(AI, mv.r, mv.c)
      if (over) return
      turn = HUMAN
      render()
    }, 60)
  }

  function record(res) {
    var w = parseInt(LS.get('gmW', '0'), 10)
    var l = parseInt(LS.get('gmL', '0'), 10)
    var d = parseInt(LS.get('gmD', '0'), 10)
    if (res === 'w') w++; else if (res === 'l') l++; else d++
    LS.set('gmW', String(w)); LS.set('gmL', String(l)); LS.set('gmD', String(d))
    showBest(w, l, d)
  }

  function showBest(w, l, d) {
    if (!w && !l && !d) { elBest.textContent = '战绩：—'; return }
    elBest.textContent = '战绩：' + w + ' 胜 / ' + l + ' 负 / ' + d + ' 和'
  }

  function newGame() {
    board = new Array(N * N).fill(0)
    lastMove = null
    winCells = null
    over = false
    lock = false
    turn = HUMAN
    var w = parseInt(LS.get('gmW', '0'), 10)
    var l = parseInt(LS.get('gmL', '0'), 10)
    var d = parseInt(LS.get('gmD', '0'), 10)
    showBest(w, l, d)
    render()
  }

  document.getElementById('new').addEventListener('click', newGame)
  document.getElementById('retry').addEventListener('click', newGame)

  buildGrid()
  newGame()
})()
