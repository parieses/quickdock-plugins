function handleInitialize(params) {
  return { status: 'ready', version: '0.2.0' }
}

// 标准 Base64 URL 解码。
// 注意：goja 运行时不提供浏览器端的 atob/btoa/decodeURIComponent/escape，
// 因此这里用纯 JS 实现 base64 -> 字节 -> UTF-8 字符串，保证在命令模式下可用。
var B64CHARS = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/'

function b64ToBytes(input) {
  var s = input.replace(/-/g, '+').replace(/_/g, '/')
  var clean = ''
  for (var i = 0; i < s.length; i++) {
    var c = s.charAt(i)
    if (c === '=') break
    if (B64CHARS.indexOf(c) >= 0) clean += c
  }
  var bytes = []
  var buffer = 0, bits = 0
  for (var j = 0; j < clean.length; j++) {
    buffer = (buffer << 6) | B64CHARS.indexOf(clean.charAt(j))
    bits += 6
    if (bits >= 8) { bits -= 8; bytes.push((buffer >> bits) & 0xFF) }
  }
  return bytes
}

function utf8Decode(bytes) {
  var out = ''
  for (var i = 0; i < bytes.length;) {
    var b1 = bytes[i++]
    if (b1 < 0x80) {
      out += String.fromCharCode(b1)
    } else if (b1 >= 0xC0 && b1 < 0xE0) {
      var b2 = bytes[i++]; out += String.fromCharCode(((b1 & 0x1F) << 6) | (b2 & 0x3F))
    } else if (b1 >= 0xE0 && b1 < 0xF0) {
      var b2 = bytes[i++], b3 = bytes[i++]
      out += String.fromCharCode(((b1 & 0x0F) << 12) | ((b2 & 0x3F) << 6) | (b3 & 0x3F))
    } else {
      var b2 = bytes[i++], b3 = bytes[i++], b4 = bytes[i++]
      var cp = ((b1 & 0x07) << 18) | ((b2 & 0x3F) << 12) | ((b3 & 0x3F) << 6) | (b4 & 0x3F)
      cp -= 0x10000
      out += String.fromCharCode(0xD800 + (cp >> 10), 0xDC00 + (cp & 0x3FF))
    }
  }
  return out
}

function b64UrlDecode(str) {
  try { return utf8Decode(b64ToBytes(str)) } catch (e) { return '' }
}

// 解析 JWT
function parseJWT(token) {
  var parts = token.split('.')
  if (parts.length !== 3) return { error: '无效的 JWT Token，需要 3 段（header.payload.signature）' }

  var headerJson = b64UrlDecode(parts[0])
  var payloadJson = b64UrlDecode(parts[1])

  var header, payload
  try { header = JSON.parse(headerJson) } catch (e) { header = { _raw: headerJson } }
  try { payload = JSON.parse(payloadJson) } catch (e) { payload = { _raw: payloadJson } }

  // 验证过期
  var info = {}
  if (payload.exp) {
    var expDate = new Date(payload.exp * 1000)
    var now = Date.now() / 1000
    info.expired = payload.exp < now
    info.expTime = expDate.toISOString().replace('T', ' ').substring(0, 19)
    info.remaining = info.expired
      ? '已过期 ' + Math.floor((now - payload.exp) / 60) + ' 分钟'
      : '剩余 ' + Math.floor((payload.exp - now) / 3600) + ' 小时 ' + Math.floor(((payload.exp - now) % 3600) / 60) + ' 分钟'
  }
  if (payload.iat) {
    info.iatTime = new Date(payload.iat * 1000).toISOString().replace('T', ' ').substring(0, 19)
  }
  if (payload.sub) info.subject = payload.sub
  if (payload.iss) info.issuer = payload.iss

  return {
    header: header,
    payload: payload,
    signature: parts[2].substring(0, 20) + '...',
    info: info,
    raw: { header: parts[0], payload: parts[1], signature: parts[2] }
  }
}

function handleExecute(params) {
  var input = params.input ? params.input.text || '' : ''
  var text = input || ''

  // 自动从输入中提取 JWT
  var match = text.match(/(eyJ[A-Za-z0-9_-]+\.eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+)/)
  if (match) text = match[1]

  if (!text || text.split('.').length !== 3) {
    return { text: '', error: '请输入有效的 JWT Token（以 eyJ 开头）' }
  }

  var result = parseJWT(text)
  if (result.error) {
    return { text: '', error: result.error }
  }

  return {
    text: JSON.stringify({ header: result.header, payload: result.payload }, null, 2),
    display: JSON.stringify({ header: result.header, payload: result.payload, info: result.info }, null, 2)
  }
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
