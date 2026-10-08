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
