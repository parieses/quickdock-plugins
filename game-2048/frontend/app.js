(function () {
  'use strict'

  /* ---------- 沙箱安全的 localStorage ---------- */
  var LS = {
    get: function (k) { try { return window.localStorage.getItem(k) } catch (e) { return null } },
    set: function (k, v) { try { window.localStorage.setItem(k, v) } catch (e) {} }
  }

  var SIZE = 4
  var ANIM = 115 // 与 CSS transition 时长一致
  var tileGrid = []        // r×c -> tile | null
  var score = 0
  var best = parseInt(LS.get('game2048.best') || '0', 10) || 0
  var won = false
  var announced = false
  var over = false
  var idSeq = 1

  var elGrid = document.getElementById('grid')
  var elTiles = document.getElementById('tiles')
  var elScore = document.getElementById('score')
  var elBest = document.getElementById('best')
  var elOver = document.getElementById('over')
  var elOverText = document.getElementById('overText')

  function emptyTileGrid() {
    var g = []
    for (var r = 0; r < SIZE; r++) {
      g.push([])
      for (var c = 0; c < SIZE; c++) g[r].push(null)
    }
    return g
  }

  /* 创建一个 tile DOM（.g-tile > .g-tile-inner） */
  function newTile(value, r, c, spawn) {
    var el = document.createElement('div')
    el.className = 'g-tile' + (spawn ? ' spawn' : '')
    var inner = document.createElement('div')
    inner.className = 'g-tile-inner'
    inner.dataset.v = String(value)
    inner.textContent = String(value)
    el.appendChild(inner)
    el.style.setProperty('--r', r)
    el.style.setProperty('--c', c)
    elTiles.appendChild(el)
    var t = { id: idSeq++, value: value, el: el, inner: inner, r: r, c: c, removed: false }
    if (spawn) {
      void el.offsetWidth // 强制 reflow，确保 scale(0) 已生效
      el.classList.remove('spawn') // 过渡到 scale(1)
    }
    return t
  }

  function removeTile(t) {
    if (!t || t.removed) return
    t.removed = true
    if (t.el && t.el.parentNode) t.el.parentNode.removeChild(t.el)
  }

  function setPos(t, r, c) {
    t.r = r; t.c = c
    t.el.style.setProperty('--r', r)
    t.el.style.setProperty('--c', c)
  }

  function buildCells() {
    elGrid.innerHTML = ''
    for (var r = 0; r < SIZE; r++) {
      for (var c = 0; c < SIZE; c++) {
        var d = document.createElement('div')
        d.className = 'g-cell'
        elGrid.appendChild(d)
      }
    }
  }

  function spawn() {
    var free = []
    for (var r = 0; r < SIZE; r++)
      for (var c = 0; c < SIZE; c++)
        if (!tileGrid[r][c]) free.push([r, c])
    if (!free.length) return false
    var p = free[(Math.random() * free.length) | 0]
    var v = Math.random() < 0.9 ? 2 : 4
    tileGrid[p[0]][p[1]] = newTile(v, p[0], p[1], true)
    return true
  }

  function serialize() {
    var s = ''
    for (var r = 0; r < SIZE; r++)
      for (var c = 0; c < SIZE; c++)
        s += tileGrid[r][c] ? tileGrid[r][c].value : '0'
    return s
  }

  /* 收集每条线（行/列）的现有 tile，按“靠墙在前”排序 */
  function collectLines(dir) {
    var lines = []
    var r, c, k, line
    if (dir === 'left' || dir === 'right') {
      for (r = 0; r < SIZE; r++) {
        line = []
        for (k = 0; k < SIZE; k++) {
          c = (dir === 'left') ? k : SIZE - 1 - k
          if (tileGrid[r][c]) line.push(tileGrid[r][c])
        }
        lines.push({ type: 'row', index: r, dir: dir, tiles: line })
      }
    } else {
      for (c = 0; c < SIZE; c++) {
        line = []
        for (k = 0; k < SIZE; k++) {
          r = (dir === 'up') ? k : SIZE - 1 - k
          if (tileGrid[r][c]) line.push(tileGrid[r][c])
        }
        lines.push({ type: 'col', index: c, dir: dir, tiles: line })
      }
    }
    return lines
  }

  /* 线内第 outIndex 个位置对应的逻辑坐标 */
  function rcFor(line, outIndex) {
    if (line.type === 'row') {
      return { r: line.index, c: (line.dir === 'left') ? outIndex : SIZE - 1 - outIndex }
    }
    return { r: (line.dir === 'up') ? outIndex : SIZE - 1 - outIndex, c: line.index }
  }

  function move(dir) {
    if (over) return
    var before = serialize()
    var lines = collectLines(dir)
    var newGrid = emptyTileGrid()
    var merges = []

    for (var li = 0; li < lines.length; li++) {
      var line = lines[li]
      var tiles = line.tiles
      var out = []
      var i = 0
      while (i < tiles.length) {
        var a = tiles[i]
        if (i + 1 < tiles.length && tiles[i + 1].value === a.value) {
          var b = tiles[i + 1]
          var rc = rcFor(line, out.length)
          setPos(a, rc.r, rc.c)
          setPos(b, rc.r, rc.c)
          out.push({ merged: true, r: rc.r, c: rc.c })
          merges.push({ value: a.value * 2, r: rc.r, c: rc.c, a: a, b: b })
          i += 2
        } else {
          var rc2 = rcFor(line, out.length)
          setPos(a, rc2.r, rc2.c)
          out.push({ merged: false, tile: a, r: rc2.r, c: rc2.c })
          i += 1
        }
      }
      for (var k = 0; k < out.length; k++) {
        var item = out[k]
        newGrid[item.r][item.c] = item.merged ? null : item.tile
      }
    }

    // 处理合并：在原位生成放大弹出的新块，并移除两个来源块
    for (var m = 0; m < merges.length; m++) {
      (function (mg) {
        var mt = newTile(mg.value, mg.r, mg.c, false)
        mt.el.classList.add('delayed')
        setTimeout(function () {
          mt.el.classList.remove('delayed')
          mt.el.classList.add('merged')
          removeTile(mg.a)
          removeTile(mg.b)
        }, ANIM)
        newGrid[mg.r][mg.c] = mt
        score += mg.value
        if (mg.value === 2048) won = true
      })(merges[m])
    }

    tileGrid = newGrid

    if (serialize() === before) return // 无变化

    spawn()
    renderScore()
    checkEnd()
  }

  function renderScore() {
    elScore.textContent = score
    if (score > best) { best = score; LS.set('game2048.best', best) }
    elBest.textContent = best
  }

  function canMove() {
    for (var r = 0; r < SIZE; r++)
      for (var c = 0; c < SIZE; c++) {
        if (!tileGrid[r][c]) return true
        if (c + 1 < SIZE && tileGrid[r][c + 1] && tileGrid[r][c].value === tileGrid[r][c + 1].value) return true
        if (r + 1 < SIZE && tileGrid[r + 1][c] && tileGrid[r][c].value === tileGrid[r + 1][c].value) return true
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
    elTiles.innerHTML = ''
    tileGrid = emptyTileGrid()
    score = 0
    won = false
    announced = false
    over = false
    elOver.hidden = true
    renderScore()
    spawn()
    spawn()
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
