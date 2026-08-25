/**
 * 时间转换插件 — 前端 UI
 * 通过 postMessage 与 PluginPage 桥接，调用 goja 后端执行转换
 */
const WEEKDAY = ['日', '一', '二', '三', '四', '五', '六']

function $(s) { return document.querySelector(s) }

// ---- postMessage 桥接 ----
let _nextId = 1, _pending = {}
function pluginExec(command, input) {
  return new Promise((resolve, reject) => {
    const id = _nextId++; _pending[id] = { resolve, reject }
    const timer = setTimeout(() => {
      if (_pending[id]) {
        delete _pending[id]
        reject(new Error('请求超时（后端未响应）'))
      }
    }, 10000)
    _pending[id].timer = timer
    window.parent.postMessage({ type: 'plugin:execute', id, command, input }, '*')
  })
}
window.addEventListener('message', (e) => {
  const p = _pending[e.data?.id]
  if (e.data?.type === 'plugin:result' && p) {
    if (p.timer) clearTimeout(p.timer)
    if (e.data.error) p.reject(new Error(e.data.error)); else p.resolve(e.data.data)
    delete _pending[e.data.id]
  }
  // 从命令面板传入的文本
  if (e.data?.type === 'plugin:init' && e.data?.data?.text) {
    // 只处理符合时间格式的数据，非匹配文本不转换
    var initText = e.data.data.text
    var expectedRE = /^(\d{4}[-/]\d{2}[-/]\d{2}(?:[T ]\d{1,2}:\d{2}(:\d{2})?(?:Z|[+-]\d{2}:?\d{2})?|\s+\d{1,2}:\d{2}(:\d{2})?)?|\d{10}|\d{13}|\d{8}(?:\d{6})?|\d{4}年\d{1,2}月\d{1,2}日|now)$/i
    if (expectedRE.test(initText.trim())) {
      $('#timeInput').value = initText
      doConvert(initText)
    } else {
      $('#timeInput').value = ''
      $('#results').innerHTML = ''
    }
  }
})

// ---- 时间转换 ----
function parseTime(text) {
  if (!text || !text.trim()) return null
  text = text.trim()
  if (text.toLowerCase() === 'now') return new Date()
  if (/^\d+$/.test(text)) {
    var num = parseInt(text, 10)
    return num > 1e12 ? new Date(num) : new Date(num * 1000)
  }
  var iso = text.match(/^(\d{4})-(\d{2})-(\d{2})(?:\s+(\d{1,2}):(\d{2}):?(\d{2})?)?$/)
  if (iso) return new Date(parseInt(iso[1],10), parseInt(iso[2],10)-1, parseInt(iso[3],10), parseInt(iso[4]||'0',10), parseInt(iso[5]||'0',10), parseInt(iso[6]||'0',10))
  // ISO 8601（T 分隔，无时区 → 本地）: 2024-01-15T14:30:00
  var tiso = text.match(/^(\d{4})-(\d{2})-(\d{2})T(\d{1,2}):(\d{2})(:\d{2})?$/)
  if (tiso) return new Date(parseInt(tiso[1],10), parseInt(tiso[2],10)-1, parseInt(tiso[3],10), parseInt(tiso[4],10), parseInt(tiso[5],10), parseInt(tiso[6]||'0',10))
  var slash = text.match(/^(\d{4})\/(\d{2})\/(\d{2})(?:\s+(\d{1,2}):(\d{2}):?(\d{2})?)?$/)
  if (slash) return new Date(parseInt(slash[1],10), parseInt(slash[2],10)-1, parseInt(slash[3],10), parseInt(slash[4]||'0',10), parseInt(slash[5]||'0',10), parseInt(slash[6]||'0',10))
  var cn = text.match(/^(\d{4})年\s*(\d{1,2})月\s*(\d{1,2})日?\s*(\d{1,2})?[:：]?\s*(\d{2})?[:：]?\s*(\d{2})?$/)
  if (cn) return new Date(parseInt(cn[1],10), parseInt(cn[2],10)-1, parseInt(cn[3],10), parseInt(cn[4]||'0',10), parseInt(cn[5]||'0',10), parseInt(cn[6]||'0',10))
  return null
}

function pad2(n) { return n < 10 ? '0' + n : '' + n }

function getTzOffset() {
  var sel = document.getElementById('tzSelect')
  if (!sel) return 8
  var v = sel.value
  if (v === 'custom') {
    var inp = document.getElementById('tzCustom')
    var n = parseFloat(inp ? inp.value : '')
    if (isNaN(n)) return 8
    if (n < -12) n = -12
    if (n > 14) n = 14
    return n
  }
  return parseFloat(v)
}
function fmtTzOffset(o) {
  var sign = o >= 0 ? '+' : '-'
  var a = Math.abs(o)
  var h = Math.floor(a)
  var m = Math.round((a - h) * 60)
  return sign + pad2(h) + ':' + pad2(m)
}
function tzLabel(o) {
  return 'UTC' + (o >= 0 ? '+' : '') + o
}

function formatAll(d) {
  var y = d.getFullYear(), mo = d.getMonth() + 1, da = d.getDate()
  var h = d.getHours(), mi = d.getMinutes(), s = d.getSeconds()
  var unixSec = Math.floor(d.getTime() / 1000)
  var unixMs = d.getTime()
  var isoUTC = d.toISOString().replace(/\.\d+Z$/, 'Z')
  var tz = getTzOffset()
  var td = new Date(d.getTime() + tz * 3600000)
  var tdStr = td.getUTCFullYear() + '-' + pad2(td.getUTCMonth()+1) + '-' + pad2(td.getUTCDate()) + 'T' + pad2(td.getUTCHours()) + ':' + pad2(td.getUTCMinutes()) + ':' + pad2(td.getUTCSeconds()) + fmtTzOffset(tz)

  var now = Date.now(), diff = d.getTime() - now
  var absSec = Math.floor(Math.abs(diff) / 1000)
  var rel = ''
  if (Math.abs(diff) < 1000) rel = '刚刚'
  else if (absSec < 60) rel = absSec + ' 秒' + (diff > 0 ? '后' : '前')
  else if (absSec < 3600) rel = Math.floor(absSec / 60) + ' 分钟' + (diff > 0 ? '后' : '前')
  else if (absSec < 86400) rel = Math.floor(absSec / 3600) + ' 小时' + (diff > 0 ? '后' : '前')
  else if (absSec < 2592000) rel = Math.floor(absSec / 86400) + ' 天' + (diff > 0 ? '后' : '前')
  else rel = Math.floor(absSec / 2592000) + ' 个月' + (diff > 0 ? '后' : '前')

  return [
    ['Unix 时间戳 (秒)', '' + unixSec],
    ['Unix 时间戳 (毫秒)', '' + unixMs],
    ['ISO 8601 (' + tzLabel(tz) + ')', tdStr],
    ['ISO 8601 (UTC)', isoUTC],
    ['ISO 8601 (本地)', y + '-' + pad2(mo) + '-' + pad2(da) + 'T' + pad2(h) + ':' + pad2(mi) + ':' + pad2(s)],
    ['日期 (中文)', y + '年' + mo + '月' + da + '日'],
    ['星期', '星期' + WEEKDAY[d.getDay()]],
    ['相对时间', rel],
  ]
}

function renderResults(formats) {
  var el = $('#results'); el.innerHTML = ''
  for (var i = 0; i < formats.length; i++) {
    (function(f) {
      var row = document.createElement('div'); row.className = 'result-row'
      row.innerHTML = '<span class="rk">' + f[0] + '</span><span class="rv">' + f[1] + '</span><span class="copied-tip">已复制</span>'
      row.addEventListener('click', function() {
        navigator.clipboard.writeText(f[1]).catch(function(){})
        var tip = row.querySelector('.copied-tip'); tip.classList.add('show')
        setTimeout(function(){ tip.classList.remove('show') }, 1200)
      })
      el.appendChild(row)
    })(formats[i])
  }
}

function doConvert(text) {
  if (!text) { $('#results').innerHTML = '<div class="result-row" style="cursor:default;color:var(--text3)">请输入时间</div>'; return }
  var d = parseTime(text)
  if (!d) { $('#results').innerHTML = '<div class="result-row" style="cursor:default;color:var(--text3)">无法解析: ' + text + '</div>'; return }
  renderResults(formatAll(d))
}

$('#btnConvert').addEventListener('click', function() { doConvert($('#timeInput').value) })
$('#timeInput').addEventListener('keydown', function(e) { if (e.key === 'Enter') doConvert(this.value) })

// 时区选择：自定义偏移输入显示 + 变更后重算
var tzSel = document.getElementById('tzSelect')
if (tzSel) tzSel.addEventListener('change', function() {
  var c = document.getElementById('tzCustom')
  if (c) c.style.display = (this.value === 'custom') ? '' : 'none'
  var t = $('#timeInput').value
  if (t) doConvert(t)
})
var tzCst = document.getElementById('tzCustom')
if (tzCst) tzCst.addEventListener('change', function() {
  var t = $('#timeInput').value
  if (t) doConvert(t)
})

$('#timeInput').focus()
