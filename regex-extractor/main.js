/**
 * 正则提取工具 — Goja 后端
 * 前端通过 postMessage 调用后端做正则匹配（也可在前端直接做）
 */
function handleInitialize(params) {
  return { status: 'ready', version: '0.2.0' }
}

function handleExecute(params) {
  var command = params.command || ''
  if (command === 'open-regex') {
    return { status: 'ok', frontendOnly: true }
  }
  if (command === 'extract-regex' && params.input) {
    var text = params.input.text || ''
    var pattern = params.input.pattern || ''
    var flags = params.input.flags || 'g'
    if (!text || !pattern) return { error: '需要文本和正则' }
    // ReDoS 防护：限制规模，拦截典型嵌套量词，并对匹配循环设时限（goja 单线程，回溯会冻结插件）
    if (pattern.length > 500) return { error: '正则过长（上限 500 字符）' }
    if (text.length > 200000) return { error: '文本过长（上限 200000 字符）' }
    if (/\(([^()]*[+*])[^()]*\)\s*[+*{]/.test(pattern)) {
      return { error: '检测到疑似灾难性回溯（嵌套量词），请简化正则' }
    }
    // exec-loop 依赖 global 标志推进 lastIndex；缺 g 时 exec 每次返回同一匹配 → 无限循环挂死
    if (flags.indexOf('g') === -1) flags += 'g'
    try {
      var re = new RegExp(pattern, flags)
      var matches = []
      var maxMatches = 5000
      var deadline = Date.now() + 3000
      var timedOut = false
      var m
      while ((m = re.exec(text)) !== null) {
        matches.push({
          index: m.index,
          full: m[0],
          groups: m.slice(1)
        })
        if (m.index === re.lastIndex) re.lastIndex++
        if (matches.length >= maxMatches) break
        if ((matches.length & 63) === 0 && Date.now() > deadline) { timedOut = true; break }
      }
      var out = { text: JSON.stringify(matches), matches: matches, truncated: matches.length >= maxMatches }
      if (timedOut) out.error = '匹配耗时过长已中断，仅返回部分结果'
      return out
    } catch (e) {
      return { error: '正则错误: ' + e.message }
    }
  }
  return { error: '未知命令' }
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
