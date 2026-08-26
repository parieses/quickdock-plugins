/**
 * 正则提取工具 — 前端逻辑
 * 输入文本 + 正则表达式 → 高亮匹配 + 列出结果
 */
(function() {
  'use strict'

  var patternInput = document.getElementById('patternInput')
  var flagsInput = document.getElementById('flagsInput')
  var textInput = document.getElementById('textInput')
  var btnExtract = document.getElementById('btnExtract')
  var btnClear = document.getElementById('btnClear')
  var btnCopy = document.getElementById('btnCopyResult')
  var resultArea = document.getElementById('resultArea')
  var matchResults = document.getElementById('matchResults')
  var matchStats = document.getElementById('matchStats')
  var replacementInput = document.getElementById('replacementInput')
  var replaceRow = document.getElementById('replaceRow')
  var replaceArea = document.getElementById('replaceArea')
  var replaceBody = document.getElementById('replaceBody')
  var replaceStats = document.getElementById('replaceStats')
  var modeExtract = document.getElementById('modeExtract')
  var modeReplace = document.getElementById('modeReplace')
  var mode = 'extract'

  // 快捷键
  document.addEventListener('keydown', function(e) {
    if (e.ctrlKey && e.key === 'Enter') { e.preventDefault(); doExtract() }
    if (e.key === 'Escape') { textInput.focus() }
  })

  btnExtract.addEventListener('click', doProcess)
  btnClear.addEventListener('click', function() {
    patternInput.value = ''
    flagsInput.value = 'g'
    replacementInput.value = ''
    textInput.value = ''
    resultArea.style.display = 'none'
    replaceArea.style.display = 'none'
    matchResults.innerHTML = ''
    replaceBody.innerHTML = ''
    patternInput.focus()
  })

  // 模式切换：提取 / 替换
  modeExtract.addEventListener('click', function() { setMode('extract') })
  modeReplace.addEventListener('click', function() { setMode('replace') })
  function setMode(m) {
    mode = m
    modeExtract.classList.toggle('active', m === 'extract')
    modeReplace.classList.toggle('active', m === 'replace')
    replaceRow.style.display = (m === 'replace') ? '' : 'none'
    resultArea.style.display = 'none'
    replaceArea.style.display = 'none'
    if (patternInput.value.trim() && textInput.value.trim()) doProcess()
  }

  // 自动提取（输入变化后延迟触发）
  var autoTimer = null
  patternInput.addEventListener('input', function() { scheduleAuto() })
  flagsInput.addEventListener('input', function() { scheduleAuto() })
  textInput.addEventListener('input', function() { scheduleAuto() })

  function scheduleAuto() {
    if (autoTimer) clearTimeout(autoTimer)
    if (patternInput.value.trim() && textInput.value.trim()) {
      autoTimer = setTimeout(doProcess, 400)
    }
  }

  function doProcess() {
    if (mode === 'replace') doReplace()
    else doExtract()
  }

  function doExtract() {
    var pattern = patternInput.value.trim()
    var flags = flagsInput.value.trim() || 'g'
    var text = textInput.value

    if (!pattern) {
      resultArea.style.display = 'none'
      return
    }

    try {
      var re = new RegExp(pattern, flags)
    } catch (e) {
      resultArea.style.display = ''
      matchResults.innerHTML = '<div class="re-error">正则语法错误: ' + escapeHtml(e.message) + '</div>'
      matchStats.textContent = '错误'
      return
    }

    // 提取所有匹配
    var matches = []
    var textMatches = []
    var m

    // Reset lastIndex
    re.lastIndex = 0

    while ((m = re.exec(text)) !== null) {
      var captureGroups = []
      for (var i = 1; i < m.length; i++) {
        captureGroups.push(m[i] !== undefined ? m[i] : '')
      }
      matches.push({
        index: m.index,
        full: m[0],
        groups: captureGroups
      })
      textMatches.push({
        start: m.index,
        end: m.index + m[0].length
      })
      if (m.index === re.lastIndex) re.lastIndex++
    }

    if (matches.length === 0) {
      resultArea.style.display = ''
      matchResults.innerHTML = '<div class="re-error">未找到匹配</div>'
      matchStats.textContent = '0 个'
      return
    }

    // 渲染匹配列表
    var html = ''
    for (var j = 0; j < matches.length; j++) {
      var mt = matches[j]
      var groupsStr = mt.groups.length > 0 ? ' (组: ' + mt.groups.join(', ') + ')' : ''
      html += '<div class="re-match-item">' +
        '<span class="re-match-idx">#' + (j + 1) + '</span>' +
        '<span class="re-match-text">' + escapeHtml(mt.full.substring(0, 200)) + '</span>' +
        '<span class="re-match-groups">' + escapeHtml(groupsStr.substring(0, 100)) + '</span>' +
        '</div>'
    }

    matchResults.innerHTML = html
    matchStats.textContent = matches.length + ' 个匹配'
    resultArea.style.display = 'flex'

    // 同时高亮原文中的匹配
    highlightText(text, textMatches)
  }

  function doReplace() {
    var pattern = patternInput.value.trim()
    var flags = flagsInput.value.trim() || 'g'
    var text = textInput.value
    var replacement = replacementInput.value

    if (!pattern) {
      replaceArea.style.display = 'none'
      resultArea.style.display = 'none'
      return
    }

    var re
    try {
      re = new RegExp(pattern, flags)
    } catch (e) {
      resultArea.style.display = 'none'
      replaceArea.style.display = ''
      replaceStats.textContent = '错误'
      replaceBody.innerHTML = '<div class="re-error">正则语法错误: ' + escapeHtml(e.message) + '</div>'
      return
    }

    // 统计替换处数（即使 flags 无 g 也统计全部匹配）
    var cntRe = new RegExp(pattern, flags.indexOf('g') >= 0 ? flags : flags + 'g')
    cntRe.lastIndex = 0
    var count = 0, mm
    while ((mm = cntRe.exec(text)) !== null) { count++; if (mm.index === cntRe.lastIndex) cntRe.lastIndex++ }

    var out = text.replace(re, replacement)
    replaceStats.textContent = count + ' 处替换'
    replaceBody.innerHTML = '<div class="re-replace-out">' + escapeHtml(out) + '</div>'
    replaceArea.style.display = 'flex'
    resultArea.style.display = 'none'
  }

  function highlightText(text, matches) {
    if (matches.length === 0) return
    var html = '<div class="re-highlight-text">'
    var lastEnd = 0

    for (var i = 0; i < matches.length; i++) {
      var m = matches[i]
      // 之间的文本
      if (m.start > lastEnd) {
        html += escapeHtml(text.substring(lastEnd, m.start))
      }
      // 匹配的文本（交替颜色避免重叠区域混淆）
      var cls = (i % 2 === 0) ? 're-highlight-match' : 're-highlight-match-alt'
      html += '<span class="' + cls + '">' + escapeHtml(text.substring(m.start, m.end)) + '</span>'
      lastEnd = m.end
    }

    // 剩余文本
    if (lastEnd < text.length) {
      html += escapeHtml(text.substring(lastEnd))
    }

    html += '</div>'
    matchResults.innerHTML += '<div style="border-top:1px solid var(--border);margin:4px 0;padding-top:4px">' + html + '</div>'
  }

  // 复制结果
  btnCopy.addEventListener('click', function() {
    var lines = []
    var items = matchResults.querySelectorAll('.re-match-item')
    items.forEach(function(item) {
      var text = item.querySelector('.re-match-text')
      if (text) lines.push(text.textContent)
    })
    if (lines.length > 0) {
      var copyText = lines.join('\n')
      // 走宿主 copy 桥接（sandbox 禁 navigator.clipboard）
      window.copyViaHost(copyText, function(ok){ if(!ok) fallbackCopy(copyText) })
    }
  })

  // 复制替换结果
  document.getElementById('btnCopyReplace').addEventListener('click', function() {
    var out = replaceBody.textContent
    if (out) { window.copyViaHost(out, function(){}) }
  })

  // 聚焦入口
  patternInput.focus()
})()
