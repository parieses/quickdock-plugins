/**
 * 对比工具 — Goja 后端
 * 文件元数据对比、图片预览、文本内容 Diff 与文本块逐行 Diff 全部由前端实现，
 * 后端仅保留生命周期桩函数以满足插件加载要求。
 */
function handleInitialize(params) {
  return { status: 'ready', version: '0.2.0' }
}

function handleExecute(params) {
  var command = params.command || ''
  return { error: '该命令需通过前端界面使用: ' + command }
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
