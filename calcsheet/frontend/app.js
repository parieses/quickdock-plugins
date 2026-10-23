/** 计算稿纸 — 前端核心引擎 */
const $ = s => document.querySelector(s)

// ---- postMessage 桥接（调用 goja 后端）----
let _nextId = 1, _pending = {}
// 桥接就绪门控：宿主在 iframe 真正打通桥接后才会下发 plugin:init。
// 在此之前发出的 plugin:execute 会被丢弃，表现为「首屏 / 重开空白、数据丢」。
// 因此所有 RPC 必须排队，等 plugin:init（或兜底超时）真正就绪后再发送。
let _bridgeReady = false
let _readyWaiters = []
function _markBridgeReady() {
  if (_bridgeReady) return
  _bridgeReady = true
  const ws = _readyWaiters; _readyWaiters = []
  ws.forEach(fn => { try { fn() } catch (e) {} })
}
// 兜底：宿主若迟迟不发 plugin:init，1.5s 后强制认为桥接可用，避免白屏
setTimeout(_markBridgeReady, 1500)

/**
 * 调用后端命令。支持 opts.tries 重试（默认 1）与 opts.timeout 单次超时（默认 8s）。
 * 桥接未就绪时把发送动作压入队列，待 _markBridgeReady 后统一冲刷，
 * 从根本上消除「启动期 RPC 被宿主丢弃」导致的空白 / 数据丢失。
 */
function pluginExec(command, input, opts) {
  opts = opts || {}
  const tries = opts.tries || 1
  const timeout = opts.timeout || 8000
  function attempt(n) {
    return new Promise((resolve, reject) => {
      const id = _nextId++
      const timer = setTimeout(() => {
        delete _pending[id]
        if (n < tries) { attempt(n + 1).then(resolve, reject) }
        else { reject(new Error('请求超时: ' + command)) }
      }, timeout)
      _pending[id] = {
        resolve: (v) => { clearTimeout(timer); resolve(v) },
        reject: (e) => { clearTimeout(timer); reject(e) }
      }
      const send = () => {
        try { window.parent.postMessage({ type: 'plugin:execute', id, command, input }, '*') } catch (e) {}
      }
      if (_bridgeReady) send(); else _readyWaiters.push(send)
    })
  }
  return attempt(1)
}
window.addEventListener('message', (e) => {
  const p = _pending[e.data?.id]
  if (e.data?.type === 'plugin:result' && p) {
    if (e.data.error) p.reject(new Error(e.data.error)); else p.resolve(e.data.data)
    delete _pending[e.data.id]
  }
  // 宿主桥接就绪信号：标记就绪并冲刷排队中的 RPC；随后处理命令面板传入的文本
  if (e.data?.type === 'plugin:init') {
    _markBridgeReady()
    if (e.data?.data?.text) {
      if (typeof app !== 'undefined' && app && app.activeSheet) {
        commitEdit()
        const l = app._add(e.data.data.text)
        if (l) startEdit(l.id)
      }
    }
  }
})

// ---- 内置轻量求值器 ----
class CalcEngine {
  static FUNCS = { sqrt:Math.sqrt, sin:Math.sin, cos:Math.cos, tan:Math.tan, asin:Math.asin, acos:Math.acos, atan:Math.atan, log:Math.log10, ln:Math.log, log2:Math.log2, exp:Math.exp, abs:Math.abs, round:Math.round, floor:Math.floor, ceil:Math.ceil, min:Math.min, max:Math.max, pow:Math.pow }
  static eval(expr, lv={}, vars={}) {
    expr = expr.replace(/(?<=\d),(?=\d{3}(?!\d))/g, '') // 仅去千位分隔逗号，保留函数实参逗号
    expr = expr.replace(/#(\d{3,})/g, (_, id) => { if (lv[id]===undefined) throw new Error('行 #'+id+' 无值'); return lv[id] })
    for (const [k,v] of Object.entries(vars)) expr = expr.replace(new RegExp('\\b'+k+'\\b','g'), v)
    return CalcEngine._parse(expr)
  }
  static _parse(s) {
    let pos = 0; s = s.trim(); if (!s) return 0
    const sk = () => { while (pos < s.length && s[pos] === ' ') pos++ }
    const pk = () => pos < s.length ? s[pos] : '\0'
    const ex = (c) => { sk(); if (pk() !== c) throw new Error('期望 "'+c+'" 在 '+pos); pos++ }
    function pn() { const st=pos; let d=false; while (pos < s.length) { const c=s[pos]; if (c>='0'&&c<='9')pos++; else if (c==='.'&&!d){d=true;pos++} else break } if (pos===st) throw new Error('非法字符 '+s[st]); return parseFloat(s.slice(st,pos)) }
    function pi() {
      sk(); const ch = pk()
      if (ch==='#') { pos++; while (pos<s.length && s[pos]>='0'&&s[pos]<='9') pos++; throw new Error('行号引用内部错误') }
      if ((ch>='a'&&ch<='z')||(ch>='A'&&ch<='Z')||ch==='_') {
        const st=pos; while (pos<s.length && /[a-zA-Z0-9_]/.test(s[pos])) pos++; const name=s.slice(st,pos); sk()
        if (pk()==='(') { pos++; const args=[]; sk(); if (pk()!==')') { args.push(pe()); while (pk()===',') { pos++; args.push(pe()) } } ex(')'); const fn=CalcEngine.FUNCS[name]; if (!fn) throw new Error('未知函数: '+name); return fn(...args) }
        throw new Error('未知标识符: '+name)
      } return null
    }
    function pa() {
      sk(); const ch=pk()
      if (ch==='-') { pos++; sk(); if (pk()==='('){pos++;const v=pe();ex(')');return -v}; if (/[0-9.]/.test(pk())) return -pn(); const id=pi(); if (id!==null) return -id; throw new Error('一元负号后缺表达式') }
      if (ch==='(') { pos++; const v=pe(); ex(')'); return v }
      if (/[0-9.]/.test(ch)) return pn(); const id=pi(); if (id!==null) return id
      throw new Error('非法字符 "'+ch+'" 在 '+pos)
    }
    function pp() { let l=pa(); sk(); while (pk()==='^') { pos++; const r=pp(); l=Math.pow(l,r); sk() } return l }
    function pm() { let l=pp(); sk(); while (pos<s.length) { const c=pk(); if (c!=='*'&&c!=='/'&&c!=='%') break; pos++; const r=pp(); if (c==='*') l*=r; else if (c==='/'){if(r===0) throw new Error('除以零'); l/=r} else l%=r; sk() } return l }
    function pe() { let l=pm(); sk(); while (pos<s.length) { const c=pk(); if (c!=='+'&&c!=='-') break; pos++; const r=pm(); if (c==='+') l+=r; else l-=r; sk() } return l }
    const r=pe(); sk(); if (pos<s.length) throw new Error('非法字符 "'+s[pos]+'" 在 '+pos); return r
  }
}

// ---- 工具函数 ----
function fmtNum(v) {
  if (v===Infinity||v===-Infinity) return v>0?'∞':'-∞'
  if (isNaN(v)) return 'NaN'
  if (Number.isInteger(v)&&Math.abs(v)<1e15) return v.toLocaleString('en-US')
  let raw = v.toPrecision(12)
  if (raw.includes('.')&&!raw.includes('e')) raw = raw.replace(/\.?0+$/,'')
  const p = raw.split('.'); p[0] = p[0].replace(/\B(?=(\d{3})+(?!\d))/g,','); return p.join('.')
}
function splitComment(raw) {
  let d=0
  for (let i=0;i<raw.length;i++) {
    if (raw[i]==='(') d++; else if (raw[i]===')') d--
    else if (d===0 && raw[i]==='/' && i+1<raw.length && raw[i+1]==='/') return { expr: raw.slice(0,i).trimEnd(), comment: raw.slice(i+2).trim() }
    else if (d===0 && raw[i]==='#' && (i===0||raw[i-1]===' ') && !/^\d{3,}/.test(raw.slice(i+1))) return { expr: raw.slice(0,i).trimEnd(), comment: raw.slice(i+1).trim() }
  }
  return { expr: raw.trim(), comment: '' }
}
function isVarDef(raw) { const m = raw.trim().match(/^([a-zA-Z_]\w*)\s*=\s*(.+)$/); return m ? { name:m[1], expr:m[2] } : null }

/**
 * 统一稿纸形状。后端 / 本地缓存里的老数据可能缺 lines、updatedAt、dependencies
 * （早期版本写库时字段没对齐），渲染层不该为脏数据买单：类型不符会让
 * renderTable / sortedSheets 抛异常，renderAll 整个中断，界面停在旧画面。
 */
function normalizeSheet(s) {
  if (!s || !s.id) return null
  const now = new Date().toISOString()
  const lines = Array.isArray(s.lines) ? s.lines : []
  return {
    id: String(s.id),
    name: s.name || '未命名',
    pinned: !!s.pinned,
    createdAt: s.createdAt || now,
    updatedAt: s.updatedAt || s.createdAt || now,
    lines: lines.map((l, i) => ({
      id: (l && l.id) ? String(l.id) : String(i + 1).padStart(3, '0'),
      raw: (l && l.raw) || '',
      remark: (l && l.remark) || '',
      result: (l && typeof l.result === 'number') ? l.result : null,
      error: (l && l.error) || null,
      indentLevel: (l && l.indentLevel) | 0,
      dependencies: (l && Array.isArray(l.dependencies)) ? l.dependencies : []
    }))
  }
}

/**
 * 宿主确认框。window.qdConfirm 由宿主桥接注入（见 quickdock frontend/src/utils/pluginBridge.ts）。
 * 老宿主 / 直接双击 index.html 调试时它不存在 —— 此时裸调用会抛 ReferenceError，
 * 而调用点都在 async 点击处理器里，异常变成 rejected promise 被静默吞掉，
 * 表现就是「点删除毫无反应」（连确认框都不弹）。这里统一兜底到原生 confirm。
 */
function csConfirm(msg) {
  try {
    if (typeof qdConfirm === 'function') return qdConfirm(msg)
  } catch (e) {}
  return Promise.resolve(typeof window.confirm === 'function' ? window.confirm(msg) : true)
}

/** 宿主提示（toast）；缺失时退回 console，绝不抛错打断后续逻辑 */
function csAlert(msg) {
  try {
    if (typeof qdAlert === 'function') { qdAlert(msg); return }
  } catch (e) {}
  console.warn('[calcsheet]', msg)
}

// 内联输入弹窗（替代 window.prompt：沙箱 iframe 内 prompt 格式异常）
function csPrompt(title, def, cb) {
  const modal = $('#csModal'), input = $('#csModalInput'), titleEl = $('#csModalTitle')
  titleEl.textContent = title || '请输入'
  input.value = def || ''
  modal.hidden = false
  setTimeout(() => input.focus(), 0)
  function cleanup() {
    modal.hidden = true
    $('#csModalCancel').onclick = null
    $('#csModalOk').onclick = null
    input.onkeydown = null
    modal.onclick = null
  }
  $('#csModalCancel').onclick = () => { cleanup(); cb(null) }
  $('#csModalOk').onclick = () => { const v = input.value.trim(); cleanup(); cb(v || null) }
  input.onkeydown = (e) => {
    if (e.key === 'Enter') { e.preventDefault(); const v = input.value.trim(); cleanup(); cb(v || null) }
    else if (e.key === 'Escape') { e.preventDefault(); cleanup(); cb(null) }
  }
  modal.onclick = (e) => { if (e.target === modal) { cleanup(); cb(null) } }
}

// ---- 稿纸数据模型 ----
const STORAGE_KEY = 'calc_sheets_plugin'

class CalcSheetApp {
  constructor() {
    this.sheets = []; this.activeId = null
    this.undoStack = []; this.redoStack = []
    this.editingId = null
  }

  /** 首次渲染（构造之后调用，确保 DOM 和全局 app 就绪） */
  init() {
    this._bindEvents()
    // 关键修复：必须先等后端数据就绪，再决定是否新建默认稿纸、再渲染。
    // 此前 init 会提前新建一个空白稿纸并立即渲染，后端已存的数据异步回来后
    // 却从未触发重渲染 —— 表现为「数据已保存，但打开看不到」。
    // （插件 iframe 为 opaque origin，localStorage 不可用，后端 api.db 才是可信源）
    this._load().then(() => {
      if (this.sheets.length === 0) {
        this._create('计算稿纸 1')
      } else if (!this.activeId) {
        this.activeId = this.sortedSheets[0].id
      }
      renderAll(this)
    })
  }

  /**
   * 从后端 api.db 加载全部稿纸。返回一个在所有数据就绪后才 resolve 的 Promise，
   * 以便 init 在其后渲染。后端是可信源（iframe 为 opaque origin，localStorage
   * 每会话清空/不可用）；localStorage 仅作「未同步到后端的本地草稿」兜底。
   */
  _load() {
    return pluginExec('list-sheets', {}, { tries: 3, timeout: 6000 })
      .then(r => {
        const backend = (r && r.sheets) || []
        if (backend.length === 0) return this._loadLocalFallback()
        // 逐个拉取完整稿纸（含 lines / pinned）
        return Promise.all(backend.map(rs =>
          pluginExec('load-sheet', { id: rs.id }, { tries: 3, timeout: 6000 })
            .then(r2 => (r2 && r2.sheet) ? r2.sheet : null)
            .catch(() => null)
        )).then(sheets => {
          sheets = sheets.map(normalizeSheet).filter(Boolean)
          // 合并 localStorage 中可能「未同步到后端」的本地草稿
          try {
            const raw = localStorage.getItem(STORAGE_KEY)
            const local = JSON.parse(raw || '{"sheets":[]}')
            const byId = new Map(sheets.map(s => [s.id, s]))
            for (const ls of (local.sheets || [])) {
              const nls = normalizeSheet(ls); if (!nls) continue
              const ex = byId.get(nls.id)
              if (!ex) byId.set(nls.id, nls)
              else if ((nls.updatedAt || '') > (ex.updatedAt || '')) byId.set(nls.id, nls)
            }
            sheets = [...byId.values()]
          } catch (e) {}
          this.sheets = sheets
          if (!this.activeId && sheets.length) this.activeId = sheets[0].id
          this._persistAll() // 把合并结果写回后端 + 本地缓存，保证下次仍可从后端加载
        })
      })
      .catch(() => this._loadLocalFallback())
  }

  /** 后端不可用时的纯 localStorage 兜底（本地草稿不会丢） */
  _loadLocalFallback() {
    try {
      const raw = localStorage.getItem(STORAGE_KEY)
      if (raw) {
        const p = JSON.parse(raw)
        const list = (p.sheets || []).map(normalizeSheet).filter(Boolean)
        if (list.length) {
          this.sheets = list
          this.activeId = (p.activeId && list.some(s => s.id === p.activeId)) ? p.activeId : list[0].id
          this._persistAll() // 把本地草稿补进后端，保证下次能从后端加载
        }
      }
    } catch (e) {}
    return Promise.resolve()
  }

  _save() {
    try { localStorage.setItem(STORAGE_KEY, JSON.stringify({ sheets: this.sheets, activeId: this.activeId })) } catch (e) {}
    this._syncBackend()
  }

  /** 把全部稿纸同步到后端（修复「只存 active 稿纸」的局限，避免切换稿纸丢数据） */
  _persistAll() {
    try { localStorage.setItem(STORAGE_KEY, JSON.stringify({ sheets: this.sheets, activeId: this.activeId })) } catch (e) {}
    for (const s of this.sheets) {
      pluginExec('save-sheet', { sheet: { id: s.id, name: s.name, lines: s.lines, pinned: s.pinned } }, { tries: 2, timeout: 6000 }).catch(() => {})
    }
  }

  _syncBackend() {
    const s = this.activeSheet; if (!s) return
    pluginExec('save-sheet', { sheet: { id: s.id, name: s.name, lines: s.lines, pinned: s.pinned } }, { tries: 2, timeout: 6000 }).catch(() => {})
  }

  get activeSheet() { return this.sheets.find(s=>s.id===this.activeId)||null }
  get sortedSheets() {
    const p=this.sheets.filter(s=>s.pinned), o=this.sheets.filter(s=>!s.pinned)
    // updatedAt 可能缺失（早期版本写进库的稿纸没有该字段，或后端列为 NULL）：
    // 直接 localeCompare 会抛 TypeError，renderAll 整个中断 —— 表现是「点了删除页面毫无变化」。
    const cmp = (a,b) => String(b.updatedAt||'').localeCompare(String(a.updatedAt||''))
    p.sort(cmp); o.sort(cmp)
    return [...p,...o]
  }

  _create(name) {
    const s = { id: Date.now().toString(36)+Math.random().toString(36).slice(2,6), name: name||'未命名', lines:[], createdAt:new Date().toISOString(), updatedAt:new Date().toISOString(), pinned:false }
    this.sheets.push(s); this.activeId=s.id; this._save(); return s
  }
  _delete(id) {
    const i=this.sheets.findIndex(s=>s.id===id); if(i<0) return false
    const name = this.sheets[i].name || ''
    this.sheets.splice(i,1)
    if(this.activeId===id) this.activeId=this.sheets[0]?.id||null
    this._save()
    // 后端删除失败以前被 .catch(()=>{}) 完全吞掉：界面删掉了、库还在，
    // 下次打开稿纸「又回来了」，看起来就像删除没生效。这里必须让失败可见。
    pluginExec('delete-sheet',{id},{tries:2,timeout:6000}).catch((e) => {
      console.error('[calcsheet] 后端删除稿纸失败', id, e)
      csAlert('删除稿纸失败：' + ((e && e.message) || e) + '（重新打开后可能再次出现）')
    })
    // 删掉最后一张时立刻补一张空白稿纸：若留空态，下次启动 init 又会新建一张
    // 同名「计算稿纸 1」，用户会以为「删掉的稿纸自己回来了」（删除没生效的错觉）。
    // 当下补一张，删除动作有即时可见的结果，重开也还是这一张。
    if (this.sheets.length === 0) this._create('计算稿纸 1')
    renderAll(this)
    return name
  }
  _select(id) {
    if(id===this.activeId) return
    commitEdit(); this.activeId=id; this.undoStack=[]; this.redoStack=[]
    renderAll(this)
  }

  _pushUndo() { const s=this.activeSheet; if(!s) return; this.undoStack.push(JSON.parse(JSON.stringify(s.lines))); if(this.undoStack.length>50) this.undoStack.shift(); this.redoStack=[] }
  _undo() { const s=this.activeSheet; if(!s||!this.undoStack.length) return; this.redoStack.push(JSON.parse(JSON.stringify(s.lines))); s.lines=this.undoStack.pop(); this._recalc() }
  _redo() { const s=this.activeSheet; if(!s||!this.redoStack.length) return; this.undoStack.push(JSON.parse(JSON.stringify(s.lines))); s.lines=this.redoStack.pop(); this._recalc() }
  _pad(n) { return String(n).padStart(3,'0') }

  _add(raw='', indent=0) {
    const s=this.activeSheet; if(!s||s.lines.length>=1000) return null
    this._pushUndo()
    const l = { id:this._pad(s.lines.length+1), raw, remark:'', result:null, error:null, indentLevel:indent, dependencies:[] }
    s.lines.push(l); this._recalc(); return l
  }
  _insertAfter(id, raw='') {
    const s=this.activeSheet; if(!s) return null
    const i=s.lines.findIndex(l=>l.id===id); if(i<0) return this._add(raw)
    this._pushUndo()
    const l = { id:this._pad(s.lines.length+1), raw, remark:'', result:null, error:null, indentLevel:0, dependencies:[] }
    s.lines.splice(i+1,0,l); this._renum(); this._recalc(); return l
  }
  _renum() { const s=this.activeSheet; if(!s) return; s.lines.forEach((l,i)=>l.id=this._pad(i+1)) }
  _clear() { const s=this.activeSheet; if(!s||!s.lines.length) return; this._pushUndo(); s.lines=[]; this._recalc() }
  _update(id, raw) { const s=this.activeSheet; if(!s) return false; const l=s.lines.find(x=>x.id===id); if(!l||l.raw===raw) return false; this._pushUndo(); l.raw=raw; this._recalc(); return true }

  _recalc() {
    const s=this.activeSheet; if(!s) return; const vl={},vr={}
    for (let r=0;r<3;r++) for (const l of s.lines) { const vd=isVarDef(l.raw); if(!vd) continue; try { const v=CalcEngine.eval(vd.expr,vl,vr); vr[vd.name]=v; vl[l.id]=v; l.result=v; l.error=null } catch(e) { l.error=e.message; l.result=null } }
    for (const l of s.lines) {
      if (isVarDef(l.raw)) continue
      if (!l.raw.trim()) { l.result=null; l.error=null; l.dependencies=[]; continue }
      const {expr}=splitComment(l.raw); if (!expr) { l.result=null; l.error=null; l.dependencies=[]; continue }
      l.dependencies=[...expr.matchAll(/#(\d{3,})/g)].map(m=>m[1])
      try { const v=CalcEngine.eval(expr,vl,vr); l.result=v; l.error=null; vl[l.id]=v }
      catch(e) { l.error=e.message; l.result=null; delete vl[l.id] }
    }
    s.updatedAt=new Date().toISOString(); this._save()
  }


  _bindEvents() {
    const self = this
    // 全局键盘
    document.addEventListener('keydown', (e) => {
      const ctrl = e.ctrlKey||e.metaKey
      if (ctrl && e.key==='z') { e.preventDefault(); if(e.shiftKey) self._redo(); else self._undo(); renderAll(self) }
      else if (ctrl && e.key==='y') { e.preventDefault(); self._redo(); renderAll(self) }
      else if (e.key==='Enter' && self.editingId===null) { const s=self.activeSheet; if(s&&s.lines.length>0) startEdit(s.lines[s.lines.length-1].id) }
    })
    // 容器点击
    // 没有稿纸时（删光了）点击区域也必须能落笔：否则 _add 因 activeSheet 为 null 静默返回，
    // 整个界面「点了没反应」，用户只能去点「新建」才能继续。
    const ensureSheet = () => self.activeSheet || self._create('计算稿纸 1')
    $('#linesContainer').addEventListener('click', (e) => {
      if (e.target.closest('.line-row')||e.target.closest('.empty-hint')) return
      commitEdit(); ensureSheet(); const l=self._add(''); if(l) startEdit(l.id)
    })
    $('#emptyHint').addEventListener('click', () => { commitEdit(); ensureSheet(); const l=self._add(''); if(l) startEdit(l.id) })
    // 输入区键盘
    $('#linesContainer').addEventListener('keydown', onEditKeydown)
    // 工具栏
    $('#btnNew').addEventListener('click', () => { csPrompt('新建稿纸', '新建稿纸', (n) => { if (n) { commitEdit(); self._create(n); renderAll(self) } }) })
    $('#btnRename').addEventListener('click', () => { const s = self.activeSheet; if (!s) return; csPrompt('重命名稿纸', s.name, (n) => { if (n && n !== s.name) { s.name = n; self._save(); renderAll(self) } }) })
    $('#btnSave').addEventListener('click', () => { self._save(); const b=$('#btnSave'); b.innerHTML='✓ 已保存'; setTimeout(()=>{b.innerHTML='💾 保存'},1500) })
    $('#btnDel').addEventListener('click', async () => {
      const s = self.activeSheet
      if (!s) { csAlert('没有可删除的稿纸'); return }
      let ok = false
      try { ok = await csConfirm('删除稿纸「' + s.name + '」？删除后不可恢复') } catch (e) { console.error('[calcsheet] 确认框异常', e); ok = false }
      if (!ok) return
      commitEdit()
      const removed = self._delete(s.id)
      if (removed) csAlert('已删除稿纸「' + removed + '」')
    })
    $('#btnClear').addEventListener('click', async () => {
      if(!self.activeSheet||!self.activeSheet.lines.length) return
      let ok = false
      try { ok = await csConfirm('清空所有行？此操作不可撤销') } catch (e) { console.error('[calcsheet] 确认框异常', e); ok = false }
      if(!ok) return
      commitEdit(); self._clear(); renderAll(self)
    })
    $('#btnUndo').addEventListener('click', () => { self._undo(); renderAll(self) })
    $('#btnRedo').addEventListener('click', () => { self._redo(); renderAll(self) })
    $('#sheetSelect').addEventListener('change', (e) => { commitEdit(); self._select(e.target.value) })
  }
}

// ---- 渲染函数（接受 ctx = app，不依赖全局 app） ----
function renderSheetSelect(ctx) {
  const sel = $('#sheetSelect'); if(!sel) return; sel.innerHTML = ''
  for (const s of ctx.sortedSheets) {
    const o = document.createElement('option'); o.value=s.id; o.textContent=(s.pinned?'📌 ':'')+s.name
    if (s.id===ctx.activeId) o.selected=true; sel.appendChild(o)
  }
}

function renderTable(ctx) {
  const ct = $('#linesContainer'); if(!ct) return
  // 只移除 .line-row，不碰 #emptyHint
  ct.querySelectorAll('.line-row').forEach(el => el.remove())
  const s = ctx.activeSheet
  const eh = $('#emptyHint')
  if (!s || !s.lines.length) {
    if (eh) eh.style.display = 'flex'
    const lc = $('#lineCount'); if(lc) lc.textContent = '0 行'
    return
  }
  if (eh) eh.style.display = 'none'
  const lc = $('#lineCount'); if(lc) lc.textContent = s.lines.length+' 行'

  for (const l of s.lines) {
    const row = document.createElement('div'); row.className='line-row'+(ctx.editingId===l.id?' editing':''); row.dataset.id=l.id
    // 行号列
    const cl = document.createElement('span'); cl.className='cl'
    const num = document.createElement('span'); num.className='line-num'; num.textContent='#'+l.id; cl.appendChild(num)
    const deps = Array.isArray(l.dependencies) ? l.dependencies : []
    if (deps.length>0) { const d=document.createElement('span'); d.className='dep-dot'; d.title='↳ '+deps.map(x=>'#'+x).join(', '); cl.appendChild(d) }
    row.appendChild(cl)
    // 备注列
    const cm = document.createElement('span'); cm.className='cm'
    const rminp = document.createElement('input'); rminp.className='remark-input'; rminp.type='text'; rminp.placeholder='备注'; rminp.value=l.remark||''
    cm.appendChild(rminp)
    row.appendChild(cm)
    // 算式列
    const ci = document.createElement('span'); ci.className='ci'
    if (ctx.editingId===l.id) {
      const inp=document.createElement('input'); inp.className='line-input'; inp.type='text'; inp.value=l.raw; inp.spellcheck=false; ci.appendChild(inp)
      setTimeout(()=>{inp.focus();inp.setSelectionRange(inp.value.length,inp.value.length)},0)
    } else {
      const t=document.createElement('span'); t.className='line-text'
      let txt=''; if (l.indentLevel>0) txt='\u00A0'.repeat(l.indentLevel*2)
      txt+=l.raw||'↵ 点击编辑'; t.textContent=txt
      if (!l.raw) t.style.color='var(--text3)'; ci.appendChild(t)
    }
    row.appendChild(ci)
    // 结果列
    const cr = document.createElement('span'); cr.className='cr'; cr.title='点击复制结果'
    if (l.error) { const e=document.createElement('span'); e.className='result-error'; e.textContent='⚠ '+l.error; cr.appendChild(e) }
    else if (l.result!==null) { const v=document.createElement('span'); v.className='result-value'; v.textContent='= '+fmtNum(l.result); cr.appendChild(v) }
    row.appendChild(cr)
    // 事件绑定
    if (ctx.editingId!==l.id) ci.addEventListener('click',(e)=>{e.stopPropagation();startEdit(l.id)})
    cr.addEventListener('click',()=>{if(l.result!==null){copyText(fmtNum(l.result));cr.style.opacity='0.5';setTimeout(()=>cr.style.opacity='1',200)}})
    // 备注变更自动保存
    rminp.addEventListener('change', () => { if(l.remark!==rminp.value){const s=ctx.activeSheet;if(s){l.remark=rminp.value;s.updatedAt=new Date().toISOString();ctx._save()}} })
    ct.appendChild(row)
  }
}

function renderAll(ctx) { renderSheetSelect(ctx); renderTable(ctx) }

// ---- 编辑交互 ----
function startEdit(id) {
  const s = app.activeSheet; if (!s) return
  const l = s.lines.find(x=>x.id===id); if (!l) return
  app.editingId = id; renderAll(app)
}

function commitEdit() {
  if (app.editingId===null) return
  const row = document.querySelector('.line-row[data-id="'+app.editingId+'"] .line-input')
  const val = row ? row.value : ''
  app._update(app.editingId, val||'')
  app.editingId = null
  renderAll(app)
}

function onEditKeydown(e) {
  const s=app.activeSheet; if (!s||app.editingId===null) return
  const idx=s.lines.findIndex(l=>l.id===app.editingId)
  const inp = document.querySelector('.line-row[data-id="'+app.editingId+'"] .line-input'); if (!inp) return
  if (e.key==='Enter'&&!e.shiftKey) {
    e.preventDefault(); const val=inp.value
    if (val.trim()) { app._update(app.editingId,val); const pl=s.lines.find(l=>l.id===app.editingId); const ind=pl?.indentLevel||0; const nl=app._insertAfter(app.editingId,''); if(nl){nl.indentLevel=ind;startEdit(nl.id)}}
    else if (idx<s.lines.length-1) startEdit(s.lines[idx+1].id)
  } else if (e.key==='Tab') { e.preventDefault(); const st=inp.selectionStart||0,en=inp.selectionEnd||0; inp.value=inp.value.slice(0,st)+'\t'+inp.value.slice(en); setTimeout(()=>inp.setSelectionRange(st+1,st+1),0) }
  else if (e.key==='ArrowUp') { e.preventDefault(); if (idx>0) { commitEdit(); startEdit(s.lines[idx-1].id) } }
  else if (e.key==='ArrowDown') { e.preventDefault(); if (idx<s.lines.length-1) { commitEdit(); startEdit(s.lines[idx+1].id) } }
  else if (e.key==='Escape') { commitEdit() }
}

// ---- 启动 ----
const app = new CalcSheetApp()
app._commit = commitEdit
app.init()
