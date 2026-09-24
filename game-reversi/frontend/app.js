(function () {
  'use strict'

  // ---------- 安全 localStorage ----------
  var LS = {
    get: function (k, d) {
      try { var v = localStorage.getItem(k); return v == null ? d : v; } catch (e) { return d; }
    },
    set: function (k, v) {
      try { localStorage.setItem(k, v); } catch (e) {}
    }
  }

  var HUMAN = 1, AI = 2, N = 8
  var DIRS = [[-1, -1], [-1, 0], [-1, 1], [0, -1], [0, 1], [1, -1], [1, 0], [1, 1]]

  // 位置权重：角最高，角旁（X/C 位）为负
  var W = [
    [100, -20, 10, 5, 5, 10, -20, 100],
    [-20, -50, -2, -2, -2, -2, -50, -20],
    [10, -2, 1, 1, 1, 1, -2, 10],
    [5, -2, 1, 0, 0, 1, -2, 5],
    [5, -2, 1, 0, 0, 1, -2, 5],
    [10, -2, 1, 1, 1, 1, -2, 10],
    [-20, -50, -2, -2, -2, -2, -50, -20],
    [100, -20, 10, 5, 5, 10, -20, 100]
  ]

  function idx(r, c) { return r * N + c }
  function inB(r, c) { return r >= 0 && r < N && c >= 0 && c < N }
  function opp(p) { return p === 1 ? 2 : 1 }

  function initialBoard() {
    var b = new Array(64).fill(0)
    b[idx(3, 3)] = AI; b[idx(3, 4)] = HUMAN
    b[idx(4, 3)] = HUMAN; b[idx(4, 4)] = AI
    return b
  }

  // 计算在 (r,c) 落子 p 会翻转的格子；非法返回 null
  function flipsFor(b, p, r, c) {
    if (b[idx(r, c)] !== 0) return null
    var o = opp(p), out = []
    for (var d = 0; d < DIRS.length; d++) {
      var dr = DIRS[d][0], dc = DIRS[d][1]
      var tr = r + dr, tc = c + dc, line = []
      while (inB(tr, tc) && b[idx(tr, tc)] === o) {
        line.push(idx(tr, tc))
        tr += dr; tc += dc
      }
      if (line.length && inB(tr, tc) && b[idx(tr, tc)] === p) {
        for (var i = 0; i < line.length; i++) out.push(line[i])
      }
    }
    return out.length ? out : null
  }

  function legalMoves(b, p) {
    var mv = []
    for (var r = 0; r < N; r++) {
      for (var c = 0; c < N; c++) {
        var f = flipsFor(b, p, r, c)
        if (f) mv.push({ r: r, c: c, flips: f })
      }
    }
    return mv
  }

  function applyMove(b, p, r, c) {
    var f = flipsFor(b, p, r, c)
    var nb = b.slice()
    nb[idx(r, c)] = p
    for (var i = 0; i < f.length; i++) nb[f[i]] = p
    return nb
  }

  function counts(b) {
    var a = 0, h = 0
    for (var i = 0; i < 64; i++) { if (b[i] === AI) a++; else if (b[i] === HUMAN) h++; }
    return { ai: a, human: h }
  }

  function evaluate(b, ai) {
    var o = opp(ai), pos = 0, empty = 0
    for (var r = 0; r < N; r++) {
      for (var c = 0; c < N; c++) {
        var v = b[idx(r, c)]
        if (v === ai) pos += W[r][c]
        else if (v === o) pos -= W[r][c]
        else empty++
      }
    }
    if (empty <= 10) {
      var ct = counts(b)
      return (ct.ai - ct.human) * 1000 + pos
    }
    var ml = legalMoves(b, ai).length
    var mo = legalMoves(b, o).length
    return pos + (ml - mo) * 8
  }

  function minimax(b, p, depth, alpha, beta, ai) {
    if (depth === 0) return evaluate(b, ai)
    var moves = legalMoves(b, p)
    if (moves.length === 0) {
      if (legalMoves(b, opp(p)).length === 0) return evaluate(b, ai)
      return minimax(b, opp(p), depth - 1, alpha, beta, ai) // 跳过（pass）
    }
    // 着法排序：翻得多的先试，提升剪枝效率
    moves.sort(function (x, y) { return y.flips.length - x.flips.length })
    var maximizing = (p === ai), best
    if (maximizing) {
      best = -Infinity
      for (var i = 0; i < moves.length; i++) {
        var nb = applyMove(b, p, moves[i].r, moves[i].c)
        var v = minimax(nb, opp(p), depth - 1, alpha, beta, ai)
        if (v > best) best = v
        if (best > alpha) alpha = best
        if (beta <= alpha) break
      }
    } else {
      best = Infinity
      for (var j = 0; j < moves.length; j++) {
        var nb2 = applyMove(b, p, moves[j].r, moves[j].c)
        var v2 = minimax(nb2, opp(p), depth - 1, alpha, beta, ai)
        if (v2 < best) best = v2
        if (best < beta) beta = best
        if (beta <= alpha) break
      }
    }
    return best
  }

  function bestMove(b, ai, depth) {
    var moves = legalMoves(b, ai)
    if (!moves.length) return null
    moves.sort(function (x, y) { return y.flips.length - x.flips.length })
    var best = -Infinity, bm = null
    for (var i = 0; i < moves.length; i++) {
      var nb = applyMove(b, ai, moves[i].r, moves[i].c)
      var v = minimax(nb, opp(ai), depth - 1, -Infinity, Infinity, ai)
      if (v > best) { best = v; bm = moves[i] }
    }
    return bm
  }

  // ---------- 视图 ----------
  var grid = document.getElementById('grid')
  var elYou = document.getElementById('you')
  var elAi = document.getElementById('ai')
  var elStatus = document.getElementById('status')
  var elOver = document.getElementById('over')
  var elOverText = document.getElementById('overText')
  var elBest = document.getElementById('best')
  var elDiff = document.getElementById('difficulty')
  var cells = []

  var board, prevBoard, turn, over, lock

  function buildGrid() {
    grid.innerHTML = ''
    cells = []
    for (var i = 0; i < 64; i++) {
      var cell = document.createElement('div')
      cell.className = 'g-cell'
      cell.dataset.i = i
      cell.addEventListener('click', onCellClick)
      grid.appendChild(cell)
      cells.push(cell)
    }
  }

  function render() {
    var playable = (turn === HUMAN && !over) ? legalMoves(board, HUMAN) : []
    var playSet = {}
    for (var k = 0; k < playable.length; k++) playSet[idx(playable[k].r, playable[k].c)] = 1
    for (var i = 0; i < 64; i++) {
      var v = board[i], cell = cells[i]
      var prev = prevBoard ? prevBoard[i] : 0
      if (v === 0) {
        cell.innerHTML = (playSet[i] && !lock) ? '<div class="g-hint"></div>' : ''
        cell.classList.remove('playable')
        if (playSet[i] && !lock) cell.classList.add('playable')
        continue
      }
      var cls = (v === HUMAN) ? 'black' : 'white'
      var anim = ''
      if (prev !== v) anim = (prev === 0) ? ' spawn' : ' flip'
      cell.classList.remove('playable')
      cell.innerHTML = '<div class="g-disc ' + cls + anim + '"></div>'
    }
    var ct = counts(board)
    elYou.textContent = ct.human
    elAi.textContent = ct.ai
    if (over) {
      var txt
      if (ct.human > ct.ai) txt = '你赢了！ ' + ct.human + ' : ' + ct.ai
      else if (ct.ai > ct.human) txt = '电脑获胜 ' + ct.human + ' : ' + ct.ai
      else txt = '平局 ' + ct.human + ' : ' + ct.ai
      elOverText.textContent = txt
      elOver.hidden = false
      elStatus.textContent = '对局结束'
      saveBest(ct.human - ct.ai)
    } else {
      elOver.hidden = true
      elStatus.textContent = (turn === HUMAN) ? '轮到你落子' : '电脑思考中…'
      if (turn === HUMAN && !legalMoves(board, HUMAN).length) {
        elStatus.textContent = '你无子可下，跳过'
      }
    }
  }

  function onCellClick(e) {
    if (lock || over || turn !== HUMAN) return
    var i = parseInt(e.currentTarget.dataset.i, 10)
    var r = (i / N) | 0, c = i % N
    if (!flipsFor(board, HUMAN, r, c)) return
    prevBoard = board
    board = applyMove(board, HUMAN, r, c)
    turn = AI
    render()
    scheduleAI()
  }

  function scheduleAI() {
    if (over) return
    if (!legalMoves(board, AI).length) {
      // 电脑跳过
      if (!legalMoves(board, HUMAN).length) { endGame(); return }
      turn = HUMAN
      render()
      return
    }
    lock = true
    elStatus.textContent = '电脑思考中…'
    setTimeout(function () {
      var depth = parseInt(elDiff.value, 10) || 4
      var mv = bestMove(board, AI, depth)
      lock = false
      if (!mv) { endGame(); return }
      prevBoard = board
      board = applyMove(board, AI, mv.r, mv.c)
      if (!legalMoves(board, HUMAN).length) {
        if (!legalMoves(board, AI).length) { turn = HUMAN; endGame(); return }
        turn = AI
        render()
        scheduleAI()
        return
      }
      turn = HUMAN
      render()
    }, 60)
  }

  function endGame() {
    over = true
    turn = 0
    render()
  }

  function saveBest(diff) {
    var wins = parseInt(LS.get('revWins', '0'), 10)
    var bestDiff = parseInt(LS.get('revBestDiff', '-999'), 10)
    if (diff > 0) wins++
    if (diff > bestDiff) bestDiff = diff
    LS.set('revWins', String(wins))
    LS.set('revBestDiff', String(bestDiff))
    showBest(wins, bestDiff)
  }

  function showBest(wins, bestDiff) {
    if (!wins && bestDiff <= -999) { elBest.textContent = '最佳：—'; return }
    elBest.textContent = '最佳：胜 ' + wins + ' 局 · 最高净胜 ' +
      (bestDiff > 0 ? '+' : '') + bestDiff + ' 子'
  }

  function newGame() {
    board = initialBoard()
    prevBoard = null
    turn = HUMAN
    over = false
    lock = false
    var w = parseInt(LS.get('revWins', '0'), 10)
    var bd = parseInt(LS.get('revBestDiff', '-999'), 10)
    showBest(w, bd)
    render()
    // 若人类先手但无合法步（极少），交给电脑
    if (!legalMoves(board, HUMAN).length && legalMoves(board, AI).length) scheduleAI()
  }

  document.getElementById('new').addEventListener('click', newGame)
  document.getElementById('retry').addEventListener('click', newGame)
  elDiff.addEventListener('change', function () { /* 下一手生效 */ })

  buildGrid()
  newGame()
})()
