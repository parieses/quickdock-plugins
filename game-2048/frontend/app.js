(function () {
  'use strict'

  /* ---------- 沙箱安全的 localStorage ---------- */
  var LS = {
    get: function (k) { try { return window.localStorage.getItem(k) } catch (e) { return null } },
    set: function (k, v) { try { window.localStorage.setItem(k, v) } catch (e) {} }
  }

  var SIZE = 4
  var grid = []           // 二维 [r][c]
  var cells = []          // DOM 单元 [r][c]
  var score = 0
  var best = parseInt(LS.get('game2048.best') || '0', 10) || 0
  var won = false
  var announced = false
  var over = false

  var elGrid = document.getElementById('grid')
  var elScore = document.getElementById('score')
  var elBest = document.getElementById('best')
  var elOver = document.getElementById('over')
  var elOverText = document.getElementById('overText')

  function emptyGrid() {
    var g = []
    for (var r = 0; r < SIZE; r++) {
      g.push([])
      for (var c = 0; c < SIZE; c++) g[r].push(0)
    }
    return g
  }

  function buildCells() {
    elGrid.innerHTML = ''
    cells = []
    for (var r = 0; r < SIZE; r++) {
      cells.push([])
      for (var c = 0; c < SIZE; c++) {
        var d = document.createElement('div')
        d.className = 'g-cell'
        d.dataset.v = '0'
        elGrid.appendChild(d)
        cells[r].push(d)
      }
    }
  }

  function spawn() {
    var free = []
    for (var r = 0; r < SIZE; r++)
      for (var c = 0; c < SIZE; c++)
        if (grid[r][c] === 0) free.push([r, c])
    if (!free.length) return false
    var p = free[(Math.random() * free.length) | 0]
    grid[p[0]][p[1]] = Math.random() < 0.9 ? 2 : 4
    return true
  }

  function render(popList) {
    var pops = popList || {}
    for (var r = 0; r < SIZE; r++) {
      for (var c = 0; c < SIZE; c++) {
        var v = grid[r][c]
        var cell = cells[r][c]
        cell.dataset.v = v ? String(v) : '0'
        cell.textContent = v ? String(v) : ''
        if (pops[r + ',' + c]) {
          cell.classList.remove('pop')
          void cell.offsetWidth
          cell.classList.add('pop')
        }
      }
    }
    elScore.textContent = score
    if (score > best) { best = score; LS.set('game2048.best', best) }
    elBest.textContent = best
  }

  /* 把一条线（从墙到内）压缩并合并 */
  function slide(line) {
    var nums = line.filter(function (v) { return v })
    var res = []
    for (var i = 0; i < nums.length; i++) {
      if (nums[i] === nums[i + 1]) {
        res.push(nums[i] * 2)
        score += nums[i] * 2
        if (nums[i] * 2 === 2048) won = true
        i++
      } else {
        res.push(nums[i])
      }
    }
    while (res.length < SIZE) res.push(0)
    return res
  }

  function move(dir) {
    if (over) return
    var before = JSON.stringify(grid)
    var r, c, row, line

    if (dir === 'left' || dir === 'right') {
      for (r = 0; r < SIZE; r++) {
        row = grid[r].slice()
        if (dir === 'right') row.reverse()
        line = slide(row)
        if (dir === 'right') line.reverse()
        grid[r] = line
      }
    } else {
      for (c = 0; c < SIZE; c++) {
        var col = []
        for (r = 0; r < SIZE; r++) col.push(grid[r][c])
        if (dir === 'down') col.reverse()
        line = slide(col)
        if (dir === 'down') line.reverse()
        for (r = 0; r < SIZE; r++) grid[r][c] = line[r]
      }
    }

    if (JSON.stringify(grid) === before) return  // 无变化

    spawn()
    render({})
    checkEnd()
  }

  function canMove() {
    for (var r = 0; r < SIZE; r++)
      for (var c = 0; c < SIZE; c++) {
        if (grid[r][c] === 0) return true
        if (c + 1 < SIZE && grid[r][c] === grid[r][c + 1]) return true
        if (r + 1 < SIZE && grid[r][c] === grid[r + 1][c]) return true
      }
    return false
  }

  function showToast(msg) {
    var t = document.createElement('div')
    t.className = 'p-toast p-toast-success'
    t.textContent = msg
    document.body.appendChild(t)
    setTimeout(function () { t.remove() }, 2600)
  }

  function checkEnd() {
    if (won && !announced) {
      announced = true
      showToast('🎉 合成 2048！可继续挑战')
    }
    if (!canMove()) {
      over = true
      elOver.hidden = false
      elOverText.textContent = '游戏结束'
    }
  }

  function newGame() {
    grid = emptyGrid()
    score = 0
    won = false
    announced = false
    over = false
    elOver.hidden = true
    spawn()
    spawn()
    render()
  }

  /* ---------- 输入 ---------- */
  var KEYMAP = {
    ArrowLeft: 'left', ArrowRight: 'right', ArrowUp: 'up', ArrowDown: 'down',
    a: 'left', d: 'right', w: 'up', s: 'down',
    A: 'left', D: 'right', W: 'up', S: 'down'
  }
  window.addEventListener('keydown', function (e) {
    var dir = KEYMAP[e.key]
    if (!dir) return
    e.preventDefault()
    move(dir)
  })

  // 触屏滑动
  var tx = 0, ty = 0
  window.addEventListener('touchstart', function (e) {
    tx = e.touches[0].clientX; ty = e.touches[0].clientY
  }, { passive: true })
  window.addEventListener('touchend', function (e) {
    var dx = e.changedTouches[0].clientX - tx
    var dy = e.changedTouches[0].clientY - ty
    if (Math.max(Math.abs(dx), Math.abs(dy)) < 24) return
    if (Math.abs(dx) > Math.abs(dy)) move(dx > 0 ? 'right' : 'left')
    else move(dy > 0 ? 'down' : 'up')
  }, { passive: true })

  document.getElementById('new').addEventListener('click', newGame)
  document.getElementById('retry').addEventListener('click', newGame)

  buildCells()
  newGame()
})()
