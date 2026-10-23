/**
 * icons.css 结构校验（阶段 G 门禁）
 *
 * 校验项：
 *  1. 每个 --qi-* 变量的 data URI 能解回合法 SVG（含 <svg> 根 + viewBox）
 *  2. 每个 .qi-* 类都指向一个已定义的 --qi-* 变量
 *  3. .qi 基类具备 mask 相关属性（否则图标不显示）
 *  4. SVG 内部不残留未编码的 #（会截断 data URI）
 *  5. 注释闭合平衡——未闭合的 /* 会把 :root 和全部变量吞进注释，
 *     图标退化为实心方块（2026-10-23 生产事故根因）
 */
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const file = resolve(process.argv[2] || 'templates/icons.css');
const css = readFileSync(file, 'utf8');

let errors = 0;
const err = (m) => { console.error('  ✗ ' + m); errors++; };

// 1. 变量表
const varRe = /--qi-([\w-]+):\s*url\("data:image\/svg\+xml,([^"]+)"\);/g;
const vars = new Map();
let m;
while ((m = varRe.exec(css))) {
  vars.set(m[1], m[2]);
}
console.log(`变量数：${vars.size}`);
if (vars.size === 0) err('未解析到任何 --qi-* 变量');

// 逐个解回 SVG 校验
for (const [name, enc] of vars) {
  let svg;
  try {
    svg = decodeURIComponent(enc);
  } catch {
    err(`${name}: data URI 百分号解码失败`);
    continue;
  }
  if (!svg.startsWith('<svg ')) err(`${name}: 解码后未以 "<svg " 开头`);
  if (!svg.includes('viewBox=')) err(`${name}: 缺少 viewBox`);
  if (!svg.endsWith('</svg>')) err(`${name}: 未以 </svg> 结尾`);
  if (svg.includes('#')) err(`${name}: SVG 内残留未编码的 #（会截断 data URI）`);
  // 路径数据必须存在
  if (!/<(path|rect|circle|ellipse|line|polyline|polygon)\b/.test(svg)) {
    err(`${name}: SVG 内无任何绘制元素（会是空白图标）`);
  }
  // stroke-width 必须完整
  if (svg.includes("stroke='currentColor'") && !svg.includes('stroke-width=')) {
    err(`${name}: 使用 currentColor 描边但缺 stroke-width`);
  }
}

// 2. 类名 → 变量（尺寸档 .qi-12 等不引用变量，单独放行）
const SIZE_CLASS = new Set(['12', '14', '16', '18', '20', '24']);
// 伪元素基类 .qi-ico 只提供布局，具体 mask 由 .qi-ico-<name>::before 提供
const BASE_CLASS = new Set(['ico']);
const classRe = /\.qi(-ico)?-([\w-]+)\s*\{([^}]*)\}/g;
let clsCount = 0;
while ((m = classRe.exec(css))) {
  const isPseudo = !!m[1];
  const name = m[2];
  const body = m[3];
  clsCount++;
  if (SIZE_CLASS.has(name)) {
    if (!body.includes('width:') || !body.includes('height:')) {
      err(`.qi${isPseudo ? '-ico' : ''}-${name}: 尺寸档需同时设置 width 与 height`);
    }
    continue;
  }
  if (BASE_CLASS.has(name)) {
    continue; // .qi-ico / .qi 基类：布局容器，mask 由子类提供
  }
  const vm = body.match(/var\(--qi-([\w-]+)\)/);
  if (!vm) {
    err(`.qi${isPseudo ? '-ico' : ''}-${name}: 未引用 --qi-* 变量`);
  } else if (!vars.has(vm[1])) {
    err(`.qi${isPseudo ? '-ico' : ''}-${name}: 引用了不存在的 --qi-${vm[1]}`);
  }
}
console.log(`图标类数：${clsCount}`);

// 3. .qi 基类（mask-image 由各子类提供，基类只需布局与着色）
const baseMatch = css.match(/\n\.qi\s*\{([^}]*)\}/);
if (!baseMatch) {
  err('缺少 .qi 基类');
} else {
  const b = baseMatch[1];
  for (const prop of ['mask-size', 'mask-repeat', 'background-color', 'display', 'width', 'height']) {
    if (!b.includes(prop)) err(`.qi 基类缺少 ${prop}`);
  }
}

// 5. 注释闭合平衡 + 去注释后 :root 必须仍可见
//    （文本正则校验无法识别"变量被注释吞掉"，必须做注释感知检查）
const opens = (css.match(/\/\*/g) || []).length;
const closes = (css.match(/\*\//g) || []).length;
if (opens !== closes) {
  err(`注释不闭合：/* ${opens} 处 vs */ ${closes} 处（未闭合注释会吞掉后续所有规则）`);
} else {
  const stripped = css.replace(/\/\*[\s\S]*?\*\//g, '');
  if (!/:\s*root\s*\{/.test(stripped)) err('去除注释后找不到 :root 块（变量声明被注释吞掉）');
  if (!/\.qi\s*\{/.test(stripped)) err('去除注释后找不到 .qi 基类');
}

// 4. 尺寸档
for (const px of [12, 14, 16, 18, 20, 24]) {
  if (!css.includes(`.qi-${px} {`)) err(`缺少尺寸档 .qi-${px}`);
}

console.log(errors === 0 ? '\nOK：icons.css 结构校验通过' : `\n发现 ${errors} 处问题`);
process.exit(errors === 0 ? 0 : 1);
