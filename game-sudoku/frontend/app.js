(function () {
  'use strict'

  var LS = {
    get: function (k) { try { return window.localStorage.getItem(k) } catch (e) { return null } },
    set: function (k, v) { try { window.localStorage.setItem(k, v) } catch (e) {} }
  }

  var TARGET = { easy: 41, medium: 49, hard: 53, expert: 57 }
  var DIFF_LABEL = { easy: '简单', medium: '中等', hard: '困难', expert: '专家' }

  var sol, given, val, notes, sel, notesMode, seconds, timerId, running, solved

  var elBoard = document.getElementById('board')
  var elTimer = document.getElementById('timer')
  var elBest = document.getElementById('best')
  var elMsg = document.getElementById('msg')
  var elDiff = document.getElementById('diff')
  var elNotes = document.getElementById('notes')
  var elPad = document.getElementById('pad')

  /* ---------- 生成 ---------- */
  function shuffle(a) {
    for (var i = a.length - 1; i > 0; i--) {
      var j = (Math.random() * (i + 1)) | 0, t = a[i]; a[i] = a[j]; a[j] = t
    }
    return a
  }

  function valid(g, pos, v) {
    var r = (pos / 9) | 0, c = pos % 9
    for (var i = 0; i < 9; i++) {
      if (g[r * 9 + i] === v) return false
      if (g[i * 9 + c] === v) return false
    }
    var br = ((r / 3) | 0) * 3, bc = ((c / 3) | 0) * 3
    for (var dr = 0; dr < 3; dr++)
      for (var dc = 0; dc < 3; dc++)
        if (g[(br + dr) * 9 + (bc + dc)] === v) return false
    return true
  }

  function fill(g) {
    for (var pos = 0; pos < 81; pos++) {
      if (g[pos] === 0) {
        var nums = shuffle([1, 2, 3, 4, 5, 6, 7, 8, 9])
        for (var k = 0; k < nums.length; k++) {
          var v = nums[k]
          if (valid(g, pos, v)) { g[pos] = v; if (fill(g)) return true; g[pos] = 0 }
        }
        return false
      }
    }
    return true
  }

  function countSolutions(g, limit, budget) {
    if (budget.n-- <= 0) return limit
    var pos = -1
    for (var i = 0; i < 81; i++) { if (g[i] === 0) { pos = i; break } }
    if (pos === -1) return 1
    var cnt = 0
    for (var v = 1; v <= 9; v++) {
      if (valid(g, pos, v)) {
        g[pos] = v
        cnt += countSolutions(g, limit, budget)
        g[pos] = 0
        if (cnt >= limit) return cnt
      }
    }
    return cnt
  }

  function makePuzzle(target) {
    var g = new Array(81).fill(0)
    fill(g)
    var full = g.slice()
    var order = shuffle(Array.from({ length: 81 }, function (_, i) { return i }))
    var removed = 0
    for (var i = 0; i < order.length && removed < target; i++) {
      var pos = order[i]
      if (g[pos] === 0) continue
      var backup = g[pos]
      g[pos] = 0
      var cnt = countSolutions(g.slice(), 2, { n: 20000 })
      if (cnt !== 1) g[pos] = backup
      else removed++
    }
    return { sol: full, puzzle: g }
  }

  /* ---------- 冲突检测 ---------- */
  function conflictSet() {
    var bad = {}
    function unit(idxs) {
      var seen = {}
      for (var i = 0; i < idxs.length; i++) {
        var idx = idxs[i], v = val[idx]
        if (v) {
          if (seen[v] !== undefined) { bad[idx] = 1; bad[seen[v]] = 1 }
          else seen[v] = idx
        }
      }
    }
    for (var r = 0; r < 9; r++) {
      var row = []; for (var c = 0; c < 9; c++) row.push(r * 9 + c); unit(row)
      var col = []; for (var c2 = 0; c2 < 9; c2++) col.push(c2 * 9 + r); unit(col)
    }
    for (var br = 0; br < 3; br++)
      for (var bc = 0; bc < 3; bc++) {
        var box = []
        for (var dr = 0; dr < 3; dr++) for (var dc = 0; dc < 3; dc++) box.push((br * 3 + dr) * 9 + (bc * 3 + dc))
        unit(box)
      }
    return bad
  }

  /* ---------- 渲染 ---------- */
  function render() {
    var bad = conflictSet()
    var selVal = sel >= 0 ? val[sel] : 0
    elBoard.innerHTML = ''
    for (var idx = 0; idx < 81; idx++) {
      var r = (idx / 9) | 0, c = idx % 9
      var d = document.createElement('div')
      d.className = 'sd-cell'
      if (c === 2 || c === 5) d.classList.add('br')
      if (r === 2 || r === 5) d.classList.add('bb')
      if (given[idx]) d.classList.add('given')
      if (idx === sel) d.classList.add('sel')
      else if (selVal && val[idx] === selVal) d.classList.add('peer')
      if (bad[idx]) d.classList.add('bad')

      if (given[idx] || val[idx]) {
        d.textContent = val[idx] || ''
      } else if (notes[idx]) {
        var ng = document.createElement('div')
        ng.className = 'sd-notes'
        for (var n = 1; n <= 9; n++) {
          var s = document.createElement('span')
          s.textContent = notes[idx][n - 1] ? n : ''
          ng.appendChild(s)
        }
        d.appendChild(ng)
      }
      (function (i) { d.addEventListener('click', function () { selectCell(i) }) })(idx)
      elBoard.appendChild(d)
    }
  }

  function selectCell(i) { sel = i; render() }

  function inputNum(n) {
    if (sel < 0 || given[sel] || solved) return
    if (notesMode) {
      if (val[sel]) return
      notes[sel] = notes[sel] || [false, false, false, false, false, false, false, false, false]
      notes[sel][n - 1] = !notes[sel][n - 1]
      if (notes[sel].every(function (x) { return !x })) notes[sel] = null
    } else {
      val[sel] = (val[sel] === n) ? 0 : n
      notes[sel] = null
    }
    render()
    checkWin()
  }

  function erase() {
    if (sel < 0 || given[sel] || solved) return
    val[sel] = 0; notes[sel] = null; render()
  }

  function checkWin() {
    for (var i = 0; i < 81; i++) if (val[i] === 0) return
    if (Object.keys(conflictSet()).length) return
    solved = true
    stopTimer()
    var key = 'gameSudoku.best.' + elDiff.value
    var prev = parseInt(LS.get(key) || '0', 10)
    if (!prev || seconds < prev) { LS.set(key, seconds); elBest.textContent = '最佳 ' + fmt(seconds) }
    elMsg.textContent = '🎉 完成！用时 ' + fmt(seconds)
    elMsg.classList.add('win')
  }

  function hint() {
    if (solved) return
    for (var i = 0; i < 81; i++) {
      if (val[i] === 0) {
        val[i] = sol[i]; given[i] = true; notes[i] = null
        render(); checkWin(); return
      }
    }
  }

  /* ---------- 计时 ---------- */
  function fmt(s) {
    var m = (s / 60) | 0, ss = s % 60
    return (m < 10 ? '0' : '') + m + ':' + (ss < 10 ? '0' : '') + ss
  }
  function startTimer() {
    stopTimer()
    timerId = setInterval(function () {
      seconds++; elTimer.textContent = fmt(seconds)
    }, 1000)
  }
  function stopTimer() { if (timerId) clearInterval(timerId); timerId = null }

  /* ---------- 新局 ---------- */
  function newGame() {
    elMsg.classList.remove('win')
    elMsg.textContent = '点格子后按数字键 / 点数字盘填入'
    var diff = elDiff.value
    var p = makePuzzle(TARGET[diff])
    sol = p.sol
    given = p.puzzle.map(function (v) { return v !== 0 })
    val = p.puzzle.slice()
    notes = new Array(81).fill(null)
    sel = -1
    seconds = 0
    solved = false
    elTimer.textContent = '00:00'
    var best = parseInt(LS.get('gameSudoku.best.' + diff) || '0', 10)
    elBest.textContent = best ? '最佳 ' + fmt(best) : '最佳 —'
    startTimer()
    render()
  }

  /* ---------- 输入 ---------- */
  window.addEventListener('keydown', function (e) {
    if (e.key >= '1' && e.key <= '9') { inputNum(parseInt(e.key, 10)); e.preventDefault(); return }
    if (e.key === 'Backspace' || e.key === 'Delete' || e.key === '0') { erase(); e.preventDefault(); return }
    if (e.key === 'n' || e.key === 'N') { toggleNotes(); return }
    if (sel < 0) return
    var r = (sel / 9) | 0, c = sel % 9
    if (e.key === 'ArrowLeft') { selectCell(r * 9 + Math.max(0, c - 1)); e.preventDefault() }
    else if (e.key === 'ArrowRight') { selectCell(r * 9 + Math.min(8, c + 1)); e.preventDefault() }
    else if (e.key === 'ArrowUp') { selectCell(Math.max(0, r - 1) * 9 + c); e.preventDefault() }
    else if (e.key === 'ArrowDown') { selectCell(Math.min(8, r + 1) * 9 + c); e.preventDefault() }
  })

  function toggleNotes() {
    notesMode = !notesMode
    elNotes.textContent = '笔记：' + (notesMode ? '开' : '关')
    elNotes.classList.toggle('p-btn-primary', notesMode)
  }

  // 数字盘
  for (var n = 1; n <= 9; n++) {
    var b = document.createElement('button')
    b.textContent = n;
    (function (num) { b.addEventListener('click', function () { inputNum(num) }) })(n)
    elPad.appendChild(b)
  }
  var be = document.createElement('button')
  be.className = 'sd-erase'
  be.textContent = '⌫'
  be.addEventListener('click', erase)
  elPad.appendChild(be)

  document.getElementById('new').addEventListener('click', newGame)
  elDiff.addEventListener('change', newGame)
  elNotes.addEventListener('click', toggleNotes)
  document.getElementById('hint').addEventListener('click', hint)

  newGame()
})()
