const fs = require('fs');
const nodeCrypto = require('crypto');

let idMap = {};
function stubEl(id){
  const o = {
    id: id || '', value:'', textContent:'', innerHTML:'', className:'', style:{}, dataset:{},
    checked:false, disabled:false, clientWidth:900, clientHeight:600, _h:{},
    addEventListener(t,fn){ (o._h[t] = o._h[t] || []).push(fn) },
    removeEventListener(){}, dispatchEvent(){ return true },
    classList:{add(){},remove(){},toggle(){},contains(){return false}},
    setAttribute(){}, getAttribute(){return null}, removeAttribute(){},
    appendChild(){}, removeChild(){}, click(){}, focus(){}, select(){},
    querySelector(){return stubEl()}, querySelectorAll(){return []}, closest(){return null}
  };
  o.trigger = function(t, ev){
    return Promise.all((o._h[t] || []).map(f => { try { return f(ev || { target:o, preventDefault(){} }) } catch(e){ return null } }));
  };
  return o;
}
function stubWin(){
  globalThis.document = {
    documentElement:{ getAttribute:(n)=> n==='lang' ? 'zh-CN' : null, setAttribute(){}, style:{} },
    getElementById:(id)=> idMap[id] || (idMap[id] = stubEl(id)),
    querySelector:()=>stubEl(), querySelectorAll:()=>[],
    createElement:()=>stubEl(), createElementNS:()=>stubEl(),
    body:{ appendChild(){}, removeChild(){} }, addEventListener(){}
  };
  globalThis.window = { addEventListener(){}, crypto: nodeCrypto.webcrypto };
  globalThis.crypto = nodeCrypto.webcrypto;
  globalThis.getComputedStyle = ()=>({ getPropertyValue:()=>'' });
  globalThis.XMLSerializer = class { serializeToString(){ return '' } };
  globalThis.URL.createObjectURL = ()=>'blob:x';
  globalThis.URL.revokeObjectURL = ()=>{};
  globalThis.Image = class { set src(v){} };
  globalThis.Event = class { constructor(t){ this.type = t } };
  globalThis.btoa = (s)=>Buffer.from(s,'binary').toString('base64');
  globalThis.atob = (b)=>Buffer.from(b,'base64').toString('binary');
}
function resetDom(){ idMap = {}; stubWin(); }
function scripts(file){
  const html = fs.readFileSync(file,'utf8');
  return [...html.matchAll(/<script>([\s\S]*?)<\/script>/g)].map(m=>m[1]);
}
function loadIIFE(file, exportExpr){
  const code = scripts(file).pop();
  const i = code.lastIndexOf('})();');
  if (i < 0) throw new Error('no IIFE tail in ' + file);
  return (0, eval)(code.slice(0, i) + 'globalThis.__X = ' + exportExpr + ';\n' + code.slice(i));
}
let PASS = 0, FAIL = 0;
function ok(cond, label, extra){
  if (cond) PASS++;
  else { FAIL++; console.log('  FAIL  ' + label + (extra !== undefined ? ('   got: ' + extra) : '')) }
}
function near(a, b, label, eps){
  eps = eps || 1e-9;
  ok(Math.abs(a - b) / Math.max(1, Math.abs(b)) <= eps, label, a);
}
const $ = (id) => document.getElementById(id);

(async function(){

// ==================== unit-converter ====================
console.log('\n== unit-converter 换算正确性 ==');
resetDom();
(0, eval)(scripts('unit-converter/frontend/index.html').pop());
ok(typeof globalThis.convert === 'function', 'convert() 全局可用');
const CATS = globalThis.CATS, convert = globalThis.convert;
const cat = (k) => CATS.find(c => c.k === k);
[
  ['length','m','km',1,0.001], ['length','km','m',100,100000],
  ['length','in','cm',1,2.54], ['length','ft','m',1,0.3048], ['length','mi','km',1,1.609344],
  ['length','nmi','m',1,1852], ['length','里','m',1,500], ['length','尺','cm',1,33.333333333],
  ['length','ly','km',1,9.4607304725808e12],
  ['mass','kg','斤',1,2], ['mass','lb','g',1,453.59237], ['mass','oz','g',1,28.349523125], ['mass','t','kg',1,1000],
  ['area','ha','亩',1,15], ['area','acre','m²',1,4046.8564224], ['area','km²','ha',1,100],
  ['volume','m³','L',1,1000], ['volume','gal','L',1,3.785411784], ['volume','galUK','L',1,4.54609],
  ['speed','km/h','m/s',1,0.2777777778], ['speed','mi/h','km/h',1,1.609344], ['speed','kn','km/h',1,1.852],
  ['data','GB','MB',1,1000], ['data','GiB','MiB',1,1024], ['data','MiB','KB',1,1048.576], ['data','B','bit',1,8],
  ['time','h','min',1,60], ['time','d','h',1,24], ['time','yr','d',1,365.25],
  ['pressure','atm','psi',1,14.6959487755], ['pressure','bar','kPa',1,100], ['pressure','mmHg','Pa',1,133.322387415],
  ['energy','kWh','J',1,3600000], ['energy','kcal','J',1,4184], ['energy','BTU','J',1,1055.05585262],
  ['power','hp','W',1,745.6998715823], ['power','PS','W',1,735.49875], ['power','kW','W',1,1000],
  ['angle','rad','°',1,57.29577951308232], ['angle','grad','°',1,0.9]
].forEach(function(t){
  const [k, from, to, v, exp] = t;
  near(convert(cat(k), v, from, to), exp, k + ': ' + v + from + ' -> ' + to, 1e-8);
});
near(convert(cat('temperature'), 0, '°C', '°F'), 32, '0°C -> 32°F');
near(convert(cat('temperature'), 100, '°C', 'K'), 373.15, '100°C -> 373.15K');
near(convert(cat('temperature'), 212, '°F', '°C'), 100, '212°F -> 100°C');
near(convert(cat('temperature'), 0, 'K', '°C'), -273.15, '0K -> -273.15°C');
near(convert(cat('temperature'), -40, '°C', '°F'), -40, '-40°C -> -40°F（交叉点）');
CATS.forEach(function(c){
  c.units.slice(0, 6).forEach(function(u){
    near(convert(c, convert(c, 7.5, c.units[0][0], u[0]), u[0], c.units[0][0]), 7.5, '往返 ' + c.k + '/' + u[0]);
  });
});
const pi = globalThis.parseInput('100km');
ok(pi && pi.num === 100 && pi.unit && pi.unit.sym === 'km' && pi.unit.cat.k === 'length', '解析 "100km"');
const pz = globalThis.parseInput('2.5 米');
ok(pz && pz.num === 2.5 && pz.unit && pz.unit.cat.k === 'length', '解析 "2.5 米"（中文单位）');
ok(globalThis.parseInput('abc') === null, '非数值返回 null');
ok(globalThis.fmtNum(0.1) === '0.1' && globalThis.fmtNum(100) === '100', 'fmtNum 常规');
ok(globalThis.fmtNum(1e-9).indexOf('e') > 0, 'fmtNum 极小值转科学计数', globalThis.fmtNum(1e-9));

// ==================== mindmap ====================
console.log('\n== mindmap 解析与布局 ==');
resetDom();
loadIIFE('mindmap/frontend/index.html',
  '{ parseOutline, layout, assignY, countDesc, textW, trunc }');
const MM = globalThis.__X;
ok(!!MM, '__X 导出成功');
const r1 = MM.parseOutline('A\n  B\n    C\n  D\nE');
ok(r1.children.length === 2 && r1.children[0].text === 'A' && r1.children[1].text === 'E', '缩进分层 A,E');
ok(r1.children[0].children.length === 2 && r1.children[0].children[1].text === 'D', 'A 下 B,D');
ok(r1.children[0].children[0].children[0].text === 'C', 'B 下 C');
const r2 = MM.parseOutline('# 根\n## 甲\n### 甲1\n## 乙');
ok(r2.children.length === 1 && r2.children[0].children.length === 2, '# 语法分层');
ok(r2.children[0].children[0].children[0].text === '甲1', '# 语法三级');
ok(MM.parseOutline('A\n      B').children[0].children[0].text === 'B', '断层压缩（6 空格 -> 1 层）');
ok(MM.parseOutline('- 甲\n  - 乙\n  - 丙').children[0].children.length === 2, '剥离 - 列表标记');
ok(MM.parseOutline('') === null && MM.parseOutline('  \n ') === null, '空/纯空白返回 null');
const root = MM.parseOutline('中心\n  甲\n    甲1\n    甲2\n  乙\n  丙\n    丙1');
MM.layout(root, 0, [], '#000', '#000'); MM.assignY(root, 0);
const center = root.children[0];
ok(center.text === '中心' && center.children.map(c=>c.text).join() === '甲,乙,丙', '根=/中心, 分支=甲,乙,丙');
ok(MM.countDesc(center.children[0]) === 2, 'countDesc(甲)=2', MM.countDesc(center.children[0]));
ok(MM.countDesc(center) === 6, 'countDesc(中心)=6', MM.countDesc(center));
ok(MM.countDesc(root) === 7, 'countDesc(root)=7', MM.countDesc(root));
const byDepth = {};
(function walk(n){ (byDepth[n._depth] = byDepth[n._depth] || []).push(n._y); (n._collapsed?[]:n.children).forEach(walk) })(root);
let overlap = [];
Object.keys(byDepth).forEach(function(d){
  const a = byDepth[d].slice().sort((x,y)=>x-y);
  for (let i = 1; i < a.length; i++) if (a[i] - a[i-1] < 28) overlap.push(d + ':' + a[i-1] + '/' + a[i]);
});
ok(overlap.length === 0, '同一纵深节点不重叠', overlap.join(' '));
center.children[0]._collapsed = true;
MM.layout(root, 0, [], '#000', '#000'); MM.assignY(root, 0);
const vis = [];
(function w2(n){ vis.push(n.text); (n._collapsed?[]:n.children).forEach(w2) })(root);
ok(vis.indexOf('甲1') < 0 && vis.indexOf('甲2') < 0, '折叠「甲」后其子节点出局');
ok(vis.indexOf('乙') > 0 && vis.indexOf('丙1') > 0, '折叠「甲」后其他分支保留', vis.join(','));

// ==================== crypto-toolbox ====================
console.log('\n== crypto-toolbox 密码与加解密 ==');
resetDom();
loadIIFE('crypto-toolbox/frontend/index.html', '{ genPassword, pwSets, randomInt, WORDS, bufToB64, b64ToBuf, bufToHex }');
const CT = globalThis.__X;
ok(!!CT, 'crypto IIFE 导出成功');
const WORDS_ALL = new Set(CT.WORDS);

// --- 密码生成 ---
$('pwLen').value = '24'; $('pwCount').value = '5';
$('pwUpper').checked = true; $('pwLower').checked = true; $('pwDigit').checked = true;
$('pwSymbol').checked = true; $('pwNoAmb').checked = false; $('pwEach').checked = true;
await $('pwGen').trigger('click');
let pwRaw = [...$('pwOut').innerHTML.matchAll(/data-v="([^"]*)"/g)].map(m => m[1]);
let pws = pwRaw.map(v => decodeURIComponent(v));
ok(pws.length === 5, '生成 5 条密码', pws.length);
ok(pws.every(p => p.length === 24), '长度均为 24', pws[0] && pws[0].length);
ok(pws.every(p => /[A-Z]/.test(p) && /[a-z]/.test(p) && /[0-9]/.test(p) && /[^A-Za-z0-9]/.test(p)), '每条含大小写+数字+符号');
ok(new Set(pws).size === 5, '批量结果互不相同');
ok(pwRaw.every(v => !/[<>&"]/.test(v)), 'data-v 属性值已 URI 编码（无 < > & " 残留）', pwRaw.find(v => /[<>&"]/.test(v)));
$('pwLen').value = '8'; $('pwNoAmb').checked = true; $('pwEach').checked = false;
$('pwCount').value = '30';
await $('pwGen').trigger('click');
pws = [...$('pwOut').innerHTML.matchAll(/data-v="([^"]*)"/g)].map(m => decodeURIComponent(m[1]));
ok(pws.every(p => p.length === 8), '改长度后生效');
ok(pws.every(p => !/[0O1lI|]/.test(p)), '排除易混淆字符生效');
$('pwUpper').checked = false; $('pwLower').checked = false; $('pwDigit').checked = false; $('pwSymbol').checked = false;
await $('pwGen').trigger('click');
ok($('status').className.indexOf('err') >= 0, '全部取消勾选时报错而非崩溃');

// --- 口令 ---
$('phWords').value = '5'; $('phSep').value = '-'; $('phCap').checked = true; $('phNum').checked = true; $('phCount').value = '4';
await $('phGen').trigger('click');
let phs = [...$('phOut').innerHTML.matchAll(/data-v="([^"]*)"/g)].map(m => decodeURIComponent(m[1]));
ok(phs.length === 4, '生成 4 条口令', phs.length);
ok(phs.every(p => p.split('-').length >= 6), '5 词 + 数字后缀，分隔符 -', phs[0]);
ok(phs.every(p => /^[A-Z]/.test(p)), '首字母大写生效');
ok(phs.every(p => WORDS_ALL.has(p.split('-')[0].toLowerCase())), '词均来自内置词表', phs[0]);

// --- AES-GCM 往返 ---
const PLAIN = '你好 QuickDock 🔐 <b>&amp;</b>';
$('aesPass').value = 'correct horse battery';
$('aesMode').value = 'GCM'; $('aesIter').value = '2000';
$('aesPlain').value = PLAIN;
await $('aesEnc').trigger('click');
const gcmCipher = $('aesCipher').value;
ok(gcmCipher.split('.').length === 5 && gcmCipher.split('.')[0] === 'GCM', 'AES-GCM 密文 5 段', gcmCipher.slice(0, 24));
$('aesPlain').value = '';
await $('aesDec').trigger('click');
ok($('aesPlain').value === PLAIN, 'AES-GCM 解密还原（含中文/emoji/HTML）', JSON.stringify($('aesPlain').value));
$('aesPass').value = 'wrong pass';
$('aesPlain').value = '';
await $('aesDec').trigger('click');
ok($('aesPlain').value === '' && $('status').className.indexOf('err') >= 0, '错误口令被拒绝（GCM 认证）');

// --- AES-CBC 往返 ---
$('aesPass').value = 'correct horse battery';
$('aesMode').value = 'CBC'; $('aesPlain').value = PLAIN;
await $('aesEnc').trigger('click');
const cbcCipher = $('aesCipher').value;
ok(cbcCipher.split('.')[0] === 'CBC', 'AES-CBC 模式标记正确');
$('aesPlain').value = '';
await $('aesDec').trigger('click');
ok($('aesPlain').value === PLAIN, 'AES-CBC 解密还原');
ok(gcmCipher !== cbcCipher, 'GCM/CBC 密文不同');
$('aesCipher').value = 'garbage';
await $('aesDec').trigger('click');
ok($('status').className.indexOf('err') >= 0, '畸形密文被拒绝');

// --- RSA ---
$('rsaBits').value = '2048';
await $('rsaGen').trigger('click');
const pub = $('rsaPub').value, priv = $('rsaPriv').value;
ok(pub.indexOf('BEGIN PUBLIC KEY') > 0 && pub.indexOf('END PUBLIC KEY') > 0, '公钥 PEM 头尾完整');
ok(priv.indexOf('BEGIN PRIVATE KEY') > 0, '私钥 PEM 头尾完整');
$('rsaIn').value = 'top-secret 机密';
await $('rsaEnc').trigger('click');
const rsaCipher = $('rsaIn').value;
ok(rsaCipher !== 'top-secret 机密' && !/secret/.test(rsaCipher), 'RSA 加密输出非明文');
await $('rsaDec').trigger('click');
ok($('rsaIn').value === 'top-secret 机密', 'RSA 解密还原', JSON.stringify($('rsaIn').value));

// --- PBKDF2 ---
$('kdfPass').value = 'password'; $('kdfSalt').value = 'salt';
$('kdfIter').value = '1000'; $('kdfLen').value = '32'; $('kdfHash').value = 'SHA-256';
await $('kdfRun').trigger('click');
const k1 = $('kdfOut').value;
ok(/key\(hex\)\s+= [0-9a-f]{64}/.test(k1), 'PBKDF2 输出 32 字节 hex', k1);
await $('kdfRun').trigger('click');
ok($('kdfOut').value === k1, 'PBKDF2 同输入可复现');
$('kdfLen').value = '16';
await $('kdfRun').trigger('click');
ok(/key\(hex\)\s+= [0-9a-f]{32}/.test($('kdfOut').value), 'PBKDF2 长度参数生效');
$('kdfSalt').value = '';
await $('kdfRun').trigger('click');
ok(/salt\s+= [0-9a-f]{32}/.test($('kdfOut').value), '空盐时输出随机盐');

console.log('\n================================');
console.log('PASS ' + PASS + '   FAIL ' + FAIL);
process.exit(FAIL ? 1 : 0);
})();
