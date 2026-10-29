/**
 * 代码格式化 — Goja 后端
 * code-minify / code-prettify / format-sql 三条命令在这里真实执行，
 * 算法与前端 formatter/frontend/index.html 内的实现保持一致
 * （前端用于面板内即时预览，后端用于命令面板 / AI 调用）。
 * 改动算法时两边都要同步。
 */
function handleInitialize(params) {
  return { status: 'ready', version: '0.3.0' }
}

// 字符串感知的 JS 注释剥离：跳过 '...' / "..." / `...` 内的 // 和 /*，
// 避免把 URL（https://、//cdn）、含斜杠的字符串误判为注释。
function stripComments(src) {
  var out = '', i = 0, n = src.length, q = ''
  while (i < n) {
    var c = src[i], d = src[i + 1]
    if (q) {
      out += c
      if (c === '\\') { if (i + 1 < n) { out += src[i + 1]; i += 2; continue } i++; continue }
      if (c === q) { q = '' }
      i++; continue
    }
    if (c === '"' || c === "'" || c === '`') { q = c; out += c; i++; continue }
    if (c === '/' && d === '/') { while (i < n && src[i] !== '\n') i++; continue }
    if (c === '/' && d === '*') { i += 2; while (i < n && !(src[i] === '*' && src[i + 1] === '/')) i++; i += 2; continue }
    out += c; i++
  }
  return out
}

function detectLang(code) {
  var t = code.trim()
  if (/^</.test(t) || /^<!DOCTYPE/i.test(t)) return 'html'
  if (/^[.#@]\w+\s*\{/.test(t) || /\{[\s\S]*:[^;]+;/.test(t)) return 'css'
  return 'js'
}

function minifyCode(code, lang) {
  if (lang === 'html') {
    return code.replace(/<!--[\s\S]*?-->/g, '').replace(/>\s+</g, '><').replace(/\s{2,}/g, ' ').trim()
  }
  if (lang === 'css') {
    return code.replace(/\/\*[\s\S]*?\*\//g, '').replace(/\s+/g, ' ').replace(/\s*([{}:;,>~+])\s*/g, '$1').trim()
  }
  return stripComments(code).replace(/\s+/g, ' ')
    .replace(/\s*([=+\-*/%&|^~!?:;{}()\[\],<>])\s*/g, '$1').replace(/;}/g, '}').trim()
}

function prettifyCode(code, lang) {
  var i, s
  if (lang === 'html') {
    var out = '', ind = 0, k = 0
    while (k < code.length) {
      if (code[k] === '<') {
        var tag = ''
        while (k < code.length && code[k] !== '>') { tag += code[k]; k++ }
        tag += '>'
        if (tag.indexOf('</') === 0) ind = Math.max(0, ind - 1)
        if (out.length > 0 && out[out.length - 1] !== '\n') out += '\n'
        for (s = 0; s < ind; s++) out += '  '
        out += tag
        if (tag.indexOf('</') < 0 && tag.indexOf('/>') < 0 && tag.indexOf('<!') !== 0) ind++
      } else {
        out += code[k]
      }
      k++
    }
    return out.trim()
  }
  if (lang === 'css') {
    code = code.replace(/\/\*[\s\S]*?\*\//g, '')
    var out2 = ''
    for (i = 0; i < code.length; i++) {
      var c = code[i]
      if (c === '{') out2 += ' {\n  '
      else if (c === '}') out2 += '\n}\n'
      else if (c === ';') out2 += ';\n  '
      else out2 += c
    }
    return out2.trim()
  }
  code = stripComments(code)
  var out3 = '', indent = 0, inStr = false, sc = ''
  for (i = 0; i < code.length; i++) {
    var ch = code[i], next = code[i + 1] || ''
    if (inStr) {
      out3 += ch
      if (ch === '\\' && next) { out3 += next; i++ } else if (ch === sc) inStr = false
      continue
    }
    if (ch === '"' || ch === "'" || ch === '`') { inStr = true; sc = ch; out3 += ch; continue }
    if (ch === '{') { out3 += ' {\n'; indent++; for (s = 0; s < indent; s++) out3 += '  '; continue }
    if (ch === '}') { indent = Math.max(0, indent - 1); out3 += '\n'; for (s = 0; s < indent; s++) out3 += '  '; out3 += '}'; continue }
    if (ch === ';') { out3 += ';\n'; for (s = 0; s < indent; s++) out3 += '  '; continue }
    if (ch === ',' && i > 0) { out3 += ',\n'; for (s = 0; s < indent; s++) out3 += '  '; continue }
    if (ch === '\n' || ch === '\r') continue
    if (ch === ' ' || ch === '\t') {
      if (out3.length > 0 && out3[out3.length - 1] !== ' ' && out3[out3.length - 1] !== '\n') out3 += ' '
      continue
    }
    out3 += ch
  }
  return out3.replace(/\n{3,}/g, '\n\n').trim()
}

var SQL_CLAUSES = ['SELECT', 'FROM', 'WHERE', 'ORDER BY', 'GROUP BY', 'HAVING', 'LIMIT', 'OFFSET', 'SET',
  'VALUES', 'INSERT INTO', 'UPDATE', 'DELETE FROM', 'CREATE TABLE', 'ALTER TABLE', 'DROP TABLE',
  'INNER JOIN', 'LEFT JOIN', 'RIGHT JOIN', 'FULL JOIN', 'CROSS JOIN', 'JOIN', 'UNION', 'UNION ALL',
  'ON', 'USING', 'RETURNING']

function sqlEscRE(s) { return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&') }

function formatSQL(sql) {
  sql = sql.toUpperCase()
  var i, re
  for (i = 0; i < SQL_CLAUSES.length; i++) {
    re = new RegExp('\\b' + sqlEscRE(SQL_CLAUSES[i]) + '\\b', 'gi')
    sql = sql.replace(re, '\n' + SQL_CLAUSES[i])
  }
  sql = sql.replace(/\nAND\b/gi, '\n  AND').replace(/\nOR\b/gi, '\n  OR')
  sql = sql.replace(/\n{3,}/g, '\n').replace(/^\n+/, '').replace(/\n+$/, '')
  var lines = sql.split('\n'), result = []
  for (var k = 0; k < lines.length; k++) {
    var line = lines[k], depth = 0, chars = ''
    if (/^SELECT/.test(line.trim()) || /^\s*,/.test(line)) {
      for (var ci = 0; ci < line.length; ci++) {
        var ch = line[ci]
        if (ch === '(') depth++
        if (ch === ')') depth--
        if (ch === ',' && depth === 0) { chars += ',\n  ' } else { chars += ch }
      }
      result.push(chars)
    } else {
      result.push(line)
    }
  }
  return result.join('\n').trim()
}

function handleExecute(params) {
  var input = (params && params.input) || {}
  var command = (params && params.command) || ''
  var text = (input.text || '')
  if (typeof text !== 'string') text = String(text)
  text = text.trim()
  if (!text) return { error: '请输入要处理的文本（text）' }

  if (command === 'format-sql') {
    return { text: formatSQL(text), language: 'sql', mode: 'format' }
  }
  if (command !== 'code-minify' && command !== 'code-prettify') {
    return { error: '未知命令: ' + command + '（支持 code-minify / code-prettify / format-sql）' }
  }
  var langIn = String(input.language || '').toLowerCase()
  if (langIn && langIn !== 'auto' && langIn !== 'js' && langIn !== 'css' && langIn !== 'html') {
    return { error: 'language 只支持 auto / js / css / html，收到: ' + langIn }
  }
  var lang = (!langIn || langIn === 'auto') ? detectLang(text) : langIn
  var mode = command === 'code-prettify' ? 'prettify' : 'minify'
  var out = mode === 'minify' ? minifyCode(text, lang) : prettifyCode(text, lang)
  return { text: out, language: lang, mode: mode }
}

/* ==================== 运行期日志（qdlog shim） ====================
 * 目标：把「命令开始/完成/失败、initialize 结果」写进宿主的 plugin-YYYYMMDD.log，
 * 与宿主主日志（何时拉起/重启插件）对到同一条时间线。goja 里：
 *   api.log   -> I 级    api.warn -> W 级    api.error -> E 级
 * 只包裹已有的 handleExecute / handleInitialize，不改变任何业务返回值。
 */
;(function () {
  function qdErrText(e) {
    if (!e) return 'unknown'
    return e.stack ? String(e.stack) : String(e)
  }
  var rawExec = typeof handleExecute === 'function' ? handleExecute : null
  if (rawExec) {
    handleExecute = function (params) {
      var cmd = (params && params.command) || ''
      var t0 = Date.now()
      var out
      try {
        out = rawExec(params)
      } catch (e) {
        api.error('命令失败 command=' + cmd + ' err=' + qdErrText(e))
        throw e
      }
      var cost = Date.now() - t0
      if (out && typeof out === 'object' && out.error) {
        api.warn('命令返回错误 command=' + cmd + ' err=' + out.error + ' 耗时=' + cost + 'ms')
      } else {
        api.log('命令完成 command=' + cmd + ' 耗时=' + cost + 'ms')
      }
      return out
    }
  }
  var rawInit = typeof handleInitialize === 'function' ? handleInitialize : null
  if (rawInit) {
    handleInitialize = function (params) {
      try {
        var r = rawInit(params)
        api.log('initialize 完成')
        return r
      } catch (e) {
        api.error('initialize 失败 err=' + qdErrText(e))
        throw e
      }
    }
  }
})()
