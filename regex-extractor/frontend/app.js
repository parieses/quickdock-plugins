/**
 * 正则提取工具 — 前端逻辑
 * 输入文本 + 正则表达式 → 高亮匹配 + 列出结果
 */
(function() {
  'use strict'

  function escapeHtml(s) {
    return String(s == null ? '' : s)
      .replace(/&/g, '&amp;').replace(/</g, '&lt;')
      .replace(/>/g, '&gt;').replace(/"/g, '&quot;').replace(/'/g, '&#39;')
  }

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
    var firstOnly = !re.global

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
      if (firstOnly) break
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

  /* ---------- AI 生成正则（复用宿主 AI 档案，调用前用 host.ai.available 预检） ---------- */
  var aiDesc = document.getElementById('aiDesc')
  var aiStatus = document.getElementById('aiStatus')
  var btnAiGen = document.getElementById('btnAiGen')
  var aiUseText = document.getElementById('aiUseText')
  function setAiStatus(msg, kind) {
    aiStatus.style.display = 'block'; aiStatus.textContent = msg
    aiStatus.className = 're-ai-status' + (kind ? ' ' + kind : '')
  }
  // 从 AI 返回文本里截取正则：优先 /pattern/flags，否则取首行
  function extractRegex(text) {
    if (!text) return null
    var m = text.match(/\/((?:\\\/|[^\/\n])+)\/([gimsuy]*)/)
    if (m) return { pattern: m[1], flags: m[2] || 'g' }
    var line = text.split('\n').map(function(s){ return s.trim() }).filter(Boolean)[0] || ''
    line = line.replace(/^`+|`+$/g, '').replace(/^```(?:regex|js)?\s*|\s*```$/g, '')
    if (line) return { pattern: line, flags: 'g' }
    return null
  }
  function aiGen() {
    var desc = aiDesc.value.trim()
    if (!desc) { setAiStatus('请输入要提取的内容描述', 'warn'); return }
    setAiStatus('正在生成正则…', 'loading')
    var pre = window.qdHostCall ? window.qdHostCall('host.ai.available', {}) : Promise.reject(new Error('no bridge'))
    Promise.resolve(pre).then(function(r){
      if (r && r.available === false) { setAiStatus('宿主未配置 AI 档案，请到「设置 → AI 配置」中填写后重试', 'err'); return }
      doAsk()
    }).catch(function(){ doAsk() })
  }
  function doAsk() {
    var sample = (aiUseText && aiUseText.checked) ? textInput.value.trim() : ''
    var system = '你是正则表达式专家。根据用户用自然语言描述的提取需求，生成一条 JavaScript 风格的正则表达式。' +
      '优先以 /pattern/flags 格式返回（flags 常用 g）；若无法确定 flags 则只返回正则体。' +
      '只返回正则本身，不要任何解释、不要 markdown、不要多余文字。'
    var question = aiDesc.value.trim()
    if (sample) question += '\n\n待匹配文本示例（仅供理解语境，不要写死具体内容）：\n' + sample.slice(0, 2000)
    var onDelta = function(){}
    var ask = window.qdAiAsk ? window.qdAiAsk({ question: question, system: system, timeoutSec: 60 }, onDelta)
      : Promise.reject(new Error('宿主未注入 AI 桥（qdAiAsk 不可用）'))
    Promise.resolve(ask).then(function(res){
      var text = (res && res.text) ? res.text : ''
      var r = extractRegex(text)
      if (!r) { setAiStatus('AI 未返回有效正则，原始返回：' + text.slice(0, 200), 'err'); return }
      patternInput.value = r.pattern
      flagsInput.value = r.flags || 'g'
      setAiStatus('已生成正则：/' + r.pattern + '/' + (r.flags || 'g') + '（已填入上方，自动执行）', 'ok')
      if (textInput.value.trim()) doProcess()
    }).catch(function(err){
      var msg = (err && err.message) || 'AI 调用失败'
      if (/未配置|不可用|no_profile|no_endpoint|AI 能力|qdAiAsk/.test(msg)) setAiStatus('宿主未配置 AI 档案，请到「设置 → AI 配置」中填写后重试', 'err')
      else setAiStatus('AI 调用失败：' + msg, 'err')
    })
  }
  btnAiGen.addEventListener('click', aiGen)
  aiDesc.addEventListener('keydown', function(e){ if (e.key === 'Enter') aiGen() })
})()
