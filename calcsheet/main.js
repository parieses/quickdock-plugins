/**
 * 计算稿纸 — Goja 后端
 * 提供 SQLite 持久化存储
 */
function handleInitialize(params) {
  // 创建稿纸表
  api.db.exec(
    'CREATE TABLE IF NOT EXISTS sheets (' +
    'id TEXT PRIMARY KEY,' +
    'name TEXT NOT NULL,' +
    'data TEXT NOT NULL,' +
    'created_at TEXT,' +
    'updated_at TEXT' +
    ')'
  )
  // 诊断：打印现有稿纸数量，便于「保存了却看不到」时确认后端确实有数据
  var n = 0
  try {
    var rows = api.db.query('SELECT COUNT(*) AS c FROM sheets')
    if (rows && rows.length > 0) n = rows[0].c
  } catch (e) {}
  api.log('稿纸表就绪, 现有 ' + n + ' 张稿纸')
  return { status: 'ready', version: '0.2.0' }
}

function handleExecute(params) {
  var command = params.command || ''
  var input = params.input || {}

  // open-calc: 仅打开前端窗口，无需后端操作
  if (command === 'open-calc') {
    return { status: 'ok', frontendOnly: true }
  }

  if (command === 'list-sheets') {
    var rows = api.db.query('SELECT id, name, created_at, updated_at FROM sheets ORDER BY updated_at DESC')
    return { sheets: rows || [] }
  }

  if (command === 'save-sheet') {
    var s = input.sheet || {}
    if (!s.id || !s.name) return { error: '缺少 id 或 name' }
    var now = new Date().toISOString()
    var data = JSON.stringify({ lines: s.lines || [], pinned: !!s.pinned })
    api.db.exec(
      "INSERT INTO sheets (id, name, data, created_at, updated_at) VALUES (?, ?, ?, ?, ?) ON CONFLICT(id) DO UPDATE SET name=excluded.name, data=excluded.data, updated_at=excluded.updated_at",
      s.id, s.name, data, now, now
    )
    return { saved: true }
  }

  if (command === 'load-sheet') {
    var id = input.id || ''
    if (!id) return { error: '缺少 id' }
    var rows = api.db.query("SELECT * FROM sheets WHERE id=?", id)
    if (rows && rows.length > 0) {
      var r = rows[0]
      var d = JSON.parse(r.data || '{}')
      return { sheet: { id: r.id, name: r.name, lines: d.lines || [], pinned: !!d.pinned, createdAt: r.created_at, updatedAt: r.updated_at } }
    }
    return { sheet: null }
  }

  if (command === 'delete-sheet') {
    var id = input.id || ''
    if (!id) return { error: '缺少 id' }
    api.db.exec("DELETE FROM sheets WHERE id=?", id)
    return { deleted: true }
  }

  if (command === 'search-sheets') {
    var q = input.query || ''
    var rows = api.db.query(
      "SELECT id, name, created_at, updated_at FROM sheets WHERE name LIKE ? ORDER BY updated_at DESC",
      "%" + q + "%"
    )
    return { sheets: rows || [] }
  }

  throw new Error('未知命令: ' + command)
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
