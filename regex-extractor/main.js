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
    // exec-loop 依赖 global 标志推进 lastIndex；缺 g 时 exec 每次返回同一匹配 → 无限循环挂死
    if (flags.indexOf('g') === -1) flags += 'g'
    try {
      var re = new RegExp(pattern, flags)
      var matches = []
      var maxMatches = 5000
      var m
      while ((m = re.exec(text)) !== null) {
        matches.push({
          index: m.index,
          full: m[0],
          groups: m.slice(1)
        })
        if (m.index === re.lastIndex) re.lastIndex++
        if (matches.length >= maxMatches) break
      }
      return { text: JSON.stringify(matches), matches: matches, truncated: matches.length >= maxMatches }
    } catch (e) {
      return { error: '正则错误: ' + e.message }
    }
  }
  return { error: '未知命令' }
}
