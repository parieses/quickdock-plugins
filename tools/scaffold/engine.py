# -*- coding: utf-8 -*-
"""QuickDock 插件脚手架 —— 生成引擎（单一逻辑来源）。

generate_plugin(spec, repo_root, integrate=False) 负责：
  1. 按 spec 在 <repo_root>/<dir>/ 下写出一套「能通过 check_plugins.py 门禁」的插件文件；
  2. 若 integrate=True，还把新插件接入仓库发布流水线：
     - 在 glyphs.py 追加 GLYPH（图标字形源）
     - 在 gen_icons.py 的 PALETTE/GROUP 追加语义色
     - 跑 gen_icons.py --gen <dir> 生成规范图标
     - 跑 gen_tables.py / gen_site.py 刷新 README 与在线市场索引
     - 跑 check_plugins.py 做静态校验（仅报告，不阻断）

spec 字段（均由 CLI 表单或插件 UI 收集）：
  dir         插件目录名（kebab-case），同时用作 go module / native entry 基名
  name_zh     中文显示名
  name_en     英文显示名
  desc_zh     中文简介
  desc_en     英文简介
  runtime     none | goja | native
  category    11 个分类之一
  author      作者（默认 QuickDock）
  width,height 前端窗口尺寸（默认 960x600）
  permissions 字典：clipboard/network/filesystem/shell/process/processKill 布尔开关
  commands    命令列表，每项 {id, title_zh, title_en, keywords:[]}
  integrate   是否接入仓库流水线（CLI 自行决定，不走 spec）
"""
import json
import shutil
import subprocess
import sys
from pathlib import Path

# ---------------------------------------------------------------- 常量

CATEGORIES = [
    "开发者日常", "开发工具", "办公效率", "内容创作", "文件工具",
    "网络工具", "安全工具", "系统工具", "效率工具", "教育学习", "游戏娱乐",
]

# 分类 -> 图标语义色（与 gen_icons.py 的 PALETTE 对齐）
CATEGORY_COLOR = {
    "开发者日常": "violet",
    "开发工具": "violet",
    "办公效率": "blue",
    "内容创作": "pink",
    "文件工具": "pink",
    "网络工具": "teal",
    "安全工具": "amber",
    "系统工具": "green",
    "效率工具": "teal",
    "教育学习": "blue",
    "游戏娱乐": "pink",
}

# 默认图标字形（24 网格手绘，占位用；接入流水线后由 gen_icons 规范化覆盖）
DEFAULT_GLYPH = """
  <rect x="3" y="3" width="18" height="18" rx="4"/>
  <path d="M12 8v8M8 12h8" opacity="0.55"/>
"""


# ---------------------------------------------------------------- 工具

def slugify(name: str) -> str:
    """任意输入 -> kebab-case 目录名。"""
    s = name.strip().lower()
    s = s.replace(" ", "-").replace("_", "-")
    out = []
    for ch in s:
        # 仅保留 ASCII 字母数字：str.isalnum() 对中文也返回 True，
        # 会让目录名 / go module 路径变成中文（native 模板的 go.mod 直接编译失败）。
        if (ch.isascii() and ch.isalnum()) or ch == "-":
            out.append(ch)
    s = "".join(out)
    while "--" in s:
        s = s.replace("--", "-")
    return s.strip("-") or "my-plugin"


def _r(tpl: str, **kw) -> str:
    """把模板里的 __KEY__ 占位符替换为值。"""
    for k, v in kw.items():
        tpl = tpl.replace("__%s__" % k.upper(), str(v))
    return tpl


# ---------------------------------------------------------------- 各文件模板

def build_permissions(spec: dict) -> dict:
    perms = {}
    p = spec.get("permissions") or {}
    for key in ("clipboard", "network", "filesystem", "shell", "process", "processKill"):
        if p.get(key):
            perms[key] = True
    return perms


def build_commands(spec: dict) -> list:
    cmds = spec.get("commands") or []
    if not cmds:
        cmds = [{"id": spec["dir"], "title_zh": spec["name_zh"],
                 "title_en": spec["name_en"], "keywords": []}]
    out = []
    for c in cmds:
        item = {
            "id": c["id"],
            "title": c.get("title_zh", spec["name_zh"]),
            "keywords": c.get("keywords", []),
        }
        if c.get("title_en"):
            item["title_i18n"] = {"zh-CN": c.get("title_zh", spec["name_zh"]),
                                  "en-US": c["title_en"]}
        out.append(item)
    return out


def render_plugin_json(spec: dict) -> str:
    runtime = spec["runtime"]
    entry = {"none": None, "goja": "main.js", "native": spec["dir"]}[runtime]
    backend = {"runtime": runtime}
    if entry:
        backend["entry"] = entry
        if runtime == "native":
            backend["args"] = []
    data = {
        "id": "io.github.parieses.%s" % spec["dir"],
        "name": spec["name_zh"],
        "name_i18n": {"zh-CN": spec["name_zh"], "en-US": spec["name_en"]},
        "version": "0.1.0",
        "description": spec["desc_zh"],
        "description_i18n": {"zh-CN": spec["desc_zh"], "en-US": spec["desc_en"]},
        "author": spec.get("author") or "QuickDock",
        "icon": "icon.svg",
        "category": spec["category"],
        "platforms": ["windows"],
        "capabilities": ["command", "frontend"],
        "backend": backend,
        "frontend": {
            "enabled": True,
            "entry": "frontend/index.html",
            "width": spec.get("width", 960),
            "height": spec.get("height", 600),
        },
        "permissions": build_permissions(spec),
        "commands": build_commands(spec),
        "changelog": "## v0.1.0\n\n- 初始版本（由 plugin-scaffold 生成）",
    }
    return json.dumps(data, ensure_ascii=False, indent=2) + "\n"


def render_index_html(spec: dict) -> str:
    runtime = spec["runtime"]
    cmd = (spec.get("commands") or [{"id": spec["dir"]}])[0]["id"]
    html = """<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>__NAME_ZH__</title>
<link rel="stylesheet" href="qd-theme.css">
<link rel="stylesheet" href="components.css">
<style>
.app{display:flex;flex-direction:column;height:100%;box-sizing:border-box;gap:var(--sp-2);padding:var(--sp-3)}
.bar{display:flex;flex-wrap:wrap;gap:var(--sp-2);align-items:center}
label{display:flex;align-items:center;gap:4px;font-size:var(--fs-sm);color:var(--text-secondary)}
input{flex:1;min-width:200px;padding:8px 10px;background:var(--bg-primary);border:1px solid var(--border);border-radius:var(--radius);color:var(--text-primary);font-size:var(--fs-md);outline:none}
input:focus-visible{border-color:var(--accent,#4a9eff)}
.foot{display:flex;gap:var(--sp-2);align-items:center}
.btn{padding:8px 18px;background:var(--accent,#4a9eff);color:#fff;border:none;border-radius:var(--radius);cursor:pointer;font-size:var(--fs-md);font-weight:600}
.hint{font-size:var(--fs-sm);color:var(--text-muted)}
pre{flex:1;margin:0;padding:var(--sp-3);background:var(--bg-primary);border:1px solid var(--border);border-radius:var(--radius-lg);color:var(--text-primary);font-size:var(--fs-sm);line-height:1.6;overflow:auto;white-space:pre-wrap;word-break:break-all}
</style>
</head>
<body>
<div class="app">
  <div class="bar">
    <label><span id="t-in">输入</span></label>
    <input id="text" placeholder="__NAME_ZH__ 演示输入">
  </div>
  <pre id="out">__NAME_ZH__ · 由 plugin-scaffold 生成（runtime=__RT__）</pre>
  <div class="foot">
    <button id="run" class="btn">执行</button>
    <span class="spacer" style="flex:1"></span>
    <span id="hint" class="hint"></span>
  </div>
</div>
<script>
(function(){
  'use strict'
  var CMD = '__CMD__'
  var DIR = '__DIR__'
  var L10N = { 'en-US': { '输入':'Input', '执行':'Run', '你输入了':'You typed:' } }
  function L(k){ var loc=document.documentElement.getAttribute('lang')||'zh-CN'; var m=L10N[loc]||L10N[loc.split('-')[0]]||{}; return (m&&m[k]!==undefined)?m[k]:k }
  function renderStatic(){ document.getElementById('t-in').textContent=L('输入'); document.getElementById('run').textContent=L('执行') }
  var out=document.getElementById('out'), hint=document.getElementById('hint')
  function flash(m){ hint.textContent=m; setTimeout(function(){hint.textContent=''},2200) }

  // i18n 主题热更新（宿主注入 plugin:theme 时刷新文案）
  window.addEventListener('message', function(e){
    var d=e.data; if(!d) return
    if(d.type==='plugin:init'){ var p=d.data||{}; if(p.text){ document.getElementById('text').value=p.text; run() } }
    else if(d.type==='plugin:theme' && d.data && d.data.locale){ document.documentElement.setAttribute('lang', d.data.locale); renderStatic() }
  })

  function run(){
    var v=document.getElementById('text').value
"""

    if runtime == "none":
        html += """
    out.textContent = L('你输入了') + ' ' + v
"""
    else:
        # goja / native：前端把命令发给后端，后端写 db，前端轮询 db.get 取结果
        html += """
    var id = Math.floor(Math.random()*1e9)
    // 后端经 host.db.set 写回结果（key=plugin:<dir>:result），前端轮询
    window.parent.postMessage({ type:'plugin:execute', id:id, command:CMD, input:{ text:v } }, '*')
    var n=0
    var t=setInterval(function(){
      n++
      qdHostCall('db.get', { key:'plugin:'+DIR+':result' }).then(function(r){
        if(r && r.value!=null){ out.textContent=String(r.value); clearInterval(t) }
      }).catch(function(){})
      if(n>20) clearInterval(t)
    }, 300)
"""
    html += """
  }
  document.getElementById('run').addEventListener('click', run)
  renderStatic()
})()
</script>
</body>
</html>
"""
    return _r(html, name_zh=spec["name_zh"], rt=runtime, cmd=cmd, dir=spec["dir"])


def render_main_js(spec: dict) -> str:
    cmd = (spec.get("commands") or [{"id": spec["dir"]}])[0]["id"]
    return _r("""// __NAME_ZH__ - __DESC_ZH__
// goja 后端骨架（由 plugin-scaffold 生成）
// 调用宿主能力：api.host('http.get'/{...}) / api.db.* / api.aiStream(...)
// 日志：api.log() / api.warn() / api.error()

function handleInitialize(params) {
    api.log('插件初始化完成')
    return { status: 'ready', version: '0.1.0' }
}

function handleExecute(params) {
    var command = (params.command || '').toLowerCase()
    var input = params.input || {}

    if (command === '__CMD__') {
        var text = input.text || ''
        // 结果回传前端：写 db，前端轮询 db.get（详见 frontend/index.html 注释）
        api.host('db.set', { key: 'plugin:__DIR__:result', value: text })
        return { ok: true, echo: text }
    }
    if (command === 'backend-ping') {
        return { ok: true }
    }
    throw new Error('未知命令: ' + command)
}
""", name_zh=spec["name_zh"], desc_zh=spec["desc_zh"], cmd=cmd, dir=spec["dir"])


def render_main_go(spec: dict) -> str:
    cmd = (spec.get("commands") or [{"id": spec["dir"]}])[0]["id"]
    return _r("""// __NAME_ZH__ - __DESC_ZH__
// JSON-RPC 2.0 over stdin/stdout（QuickDock native 插件骨架，由 plugin-scaffold 生成）
//
// 与宿主约定：initialize 握手 / host.ping 健康检查 / plugin.execute 业务入口。
// 日志走 templates/qdlog.go 的 qdLog*（落 plugin-YYYYMMDD.log），不要自己 fmt.Println。
// 命令名统一小写（宿主会 strings.ToLower 后再分发）。

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}
type executeParams struct {
	Command string                 `json:"command"`
	Input   map[string]interface{} `json:"input"`
}

var stdout = bufio.NewWriter(os.Stdout)
var stdoutMu sync.Mutex
var cbSeq int64 = 1_000_000_000

func main() {
	qdLogStart()
	defer qdLogExit()
	reader := bufio.NewReader(os.Stdin)
	for {
		line, err := reader.ReadString('\\n')
		if err != nil {
			break
		}
		data := strings.TrimSpace(line)
		if data == "" {
			continue
		}
		if qdObserveLine(data) {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal([]byte(data), &req); err != nil {
			respondError(0, -32700, "parse error: "+err.Error())
			continue
		}
		if req.Method == "" {
			continue
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					qdLogPanic("__DIR__ dispatch panic", r)
					respondError(req.ID, -32000, fmt.Sprintf("internal error: %v", r))
				}
			}()
			handleRequest(req)
		}()
	}
}

// hostCall 向宿主发起回调（如 db.set / host.notify），fire-and-forget。
func hostCall(method string, params map[string]interface{}) {
	cbSeq++
	payload, err := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0", "id": cbSeq, "method": method, "params": params,
	})
	if err != nil {
		return
	}
	stdoutMu.Lock()
	defer stdoutMu.Unlock()
	stdout.Write(payload)
	stdout.WriteByte('\\n')
	stdout.Flush()
}

func handleRequest(req rpcRequest) {
	switch req.Method {
	case "initialize":
		respond(req.ID, map[string]interface{}{"status": "ready", "name": "__NAME_ZH__"})
	case "host.ping":
		respond(req.ID, map[string]interface{}{"pong": true})
	case "plugin.execute":
		var p executeParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			respondError(req.ID, -32602, "invalid params: "+err.Error())
			return
		}
		result, errMsg := execute(p)
		if errMsg != "" {
			respondError(req.ID, -32603, errMsg)
			return
		}
		respond(req.ID, result)
	default:
		respondError(req.ID, -32601, "method not found: "+req.Method)
	}
}

// execute 分发命令（命令名统一小写；前端也用小写发送）。
func execute(p executeParams) (map[string]interface{}, string) {
	command := strings.ToLower(strings.ReplaceAll(p.Command, ".", "-"))
	switch command {
	case "__CMD__":
		text := ""
		if p.Input != nil {
			if t, ok := p.Input["text"].(string); ok {
				text = t
			}
		}
		// 结果回传前端：写 db，前端轮询 db.get（详见 frontend/index.html 注释）
		hostCall("db.set", map[string]interface{}{"key": "plugin:__DIR__:result", "value": text})
		return map[string]interface{}{"ok": true, "echo": text}, ""
	case "backend-ping":
		return map[string]interface{}{"ok": true}, ""
	default:
		return nil, "unknown command: " + p.Command
	}
}

func respond(id int64, result interface{}) {
	defer qdFinishRequest(id, 0, "")
	payload, err := json.Marshal(result)
	if err != nil {
		respondError(id, -32603, "marshal result failed: "+err.Error())
		return
	}
	writeResponse(rpcResponse{JSONRPC: "2.0", ID: id, Result: payload})
}

func respondError(id int64, code int, msg string) {
	defer qdFinishRequest(id, code, msg)
	writeResponse(rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}})
}

func writeResponse(resp rpcResponse) {
	data, err := json.Marshal(resp)
	if err != nil {
		return
	}
	stdoutMu.Lock()
	defer stdoutMu.Unlock()
	stdout.Write(data)
	stdout.WriteByte('\\n')
	stdout.Flush()
}
""", name_zh=spec["name_zh"], desc_zh=spec["desc_zh"], cmd=cmd, dir=spec["dir"])


def render_gomod(spec: dict) -> str:
    return "module %s\n\ngo 1.25.10\n" % spec["dir"]


def render_readme(spec: dict) -> str:
    return _r("""# __NAME_ZH__

> __DESC_ZH__

由 plugin-scaffold 生成的 QuickDock 插件骨架。

- 运行时：`__RT__`
- 命令：`__CMD__`
- 分类：`__CATEGORY__`

## 开发

```bash
# 本地调试：在 QuickDock 插件管理页「从文件安装」本目录（需先压缩为 zip）
python build.py __DIR__            # 打包
python gen_icons.py --gen __DIR__  # 重新生成图标
```
""", name_zh=spec["name_zh"], desc_zh=spec["desc_zh"], rt=spec["runtime"],
     cmd=(spec.get("commands") or [{"id": spec["dir"]}])[0]["id"],
     category=spec["category"], dir=spec["dir"])


def render_placeholder_icon(dark: bool) -> str:
    """合规占位图标（带渐变贴片，过 --check 文本规则；接入流水线后由 gen_icons 覆盖）。

    形状是通用的「方框 + 加号」，仅作占位；接入仓库后 gen_icons.py --gen 会按 glyphs.py
    的 GLYPH 字形源重新生成规范图标（本机若无 Chrome，--check-render 无法跑，但 --check
    文本规则已满足）。
    """
    glyph = ('<g fill="none" stroke="%s" stroke-width="3" stroke-linecap="round" stroke-linejoin="round">'
             '<rect x="18" y="18" width="28" height="28" rx="6"/>'
             '<path d="M32 24v16M24 32h16"/></g>')
    if dark:
        return ('<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64">'
                '<defs><linearGradient id="pg" x1="0" y1="0" x2="1" y2="1">'
                '<stop offset="0" stop-color="#3381ff"/><stop offset="1" stop-color="#5957e0"/>'
                '</linearGradient></defs>'
                '<rect width="64" height="64" rx="14" fill="url(#pg)"/>'
                + glyph % "#ffffff" + '</svg>\n')
    return ('<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64">'
            '<rect width="64" height="64" rx="14" fill="#ffffff" stroke="#3381ff" stroke-width="2.4"/>'
            + glyph % "#3381ff" + '</svg>\n')


# ---------------------------------------------------------------- 生成主流程

def _write(p: Path, content: str):
    p.parent.mkdir(parents=True, exist_ok=True)
    p.write_text(content, encoding="utf-8")


def _copy(src: Path, dst: Path):
    dst.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(src, dst)


def generate_plugin(spec: dict, repo_root: str, integrate: bool = False) -> dict:
    repo = Path(repo_root).resolve()
    if not (repo / "plugin-dev-guide.md").exists():
        raise SystemExit("repo_root 不是 quickdock-plugins 仓库根：%s" % repo)
    spec = dict(spec)
    spec["dir"] = slugify(spec.get("dir") or spec.get("name_zh") or "my-plugin")
    spec.setdefault("name_zh", spec["dir"])
    spec.setdefault("name_en", spec["dir"])
    spec.setdefault("desc_zh", spec["name_zh"])
    spec.setdefault("desc_en", spec["name_en"])
    spec.setdefault("runtime", "none")
    spec.setdefault("category", "开发者日常")
    if spec["category"] not in CATEGORIES:
        raise SystemExit("未知分类：%s（应为 %s 之一）" % (spec["category"], "/".join(CATEGORIES)))

    target = repo / spec["dir"]
    if target.exists():
        raise SystemExit("目标目录已存在：%s" % target)
    (target / "frontend").mkdir(parents=True, exist_ok=True)

    written = []
    _write(target / "plugin.json", render_plugin_json(spec))
    written.append("plugin.json")
    _write(target / "frontend" / "index.html", render_index_html(spec))
    written.append("frontend/index.html")
    # 样式自包含：从仓库 templates/ 拷 qd-theme.css + components.css
    _copy(repo / "templates" / "qd-theme.css", target / "frontend" / "qd-theme.css")
    _copy(repo / "templates" / "components.css", target / "frontend" / "components.css")
    written += ["frontend/qd-theme.css", "frontend/components.css"]
    _write(target / "icon.svg", render_placeholder_icon(True))
    _write(target / "icon.light.svg", render_placeholder_icon(False))
    written += ["icon.svg", "icon.light.svg"]
    _write(target / "README.md", render_readme(spec))
    written.append("README.md")

    if spec["runtime"] == "goja":
        _write(target / "main.js", render_main_js(spec))
        written.append("main.js")
    elif spec["runtime"] == "native":
        _write(target / "main.go", render_main_go(spec))
        _write(target / "go.mod", render_gomod(spec))
        _copy(repo / "templates" / "qdlog.go", target / "qdlog.go")
        written += ["main.go", "go.mod", "qdlog.go"]

    steps = []
    if integrate:
        steps = _integrate(repo, spec["dir"], spec["category"])

    return {"dir": spec["dir"], "target": str(target), "written": written, "steps": steps}


def _integrate(repo: Path, dir_name: str, category: str) -> list:
    steps = []
    color = CATEGORY_COLOR.get(category, "blue")

    # 1) glyphs.py 追加 GLYPH 字形源
    glyphs = repo / "glyphs.py"
    if glyphs.exists():
        block = '\nGLYPH["%s"] = """%s"""\n' % (dir_name, DEFAULT_GLYPH)
        with glyphs.open("a", encoding="utf-8") as f:
            f.write(block)
        steps.append("glyphs.py 追加 GLYPH[%s]" % dir_name)

    # 2) gen_icons.py 追加语义色（在 PALETTE 构建完成后注入 GROUP）
    gicons = repo / "gen_icons.py"
    if gicons.exists():
        block = '\n# plugin-scaffold 注入\nGROUP["%s"] = "%s"\n' % (dir_name, color)
        with gicons.open("a", encoding="utf-8") as f:
            f.write(block)
        steps.append("gen_icons.py 追加 GROUP[%s]=%s" % (dir_name, color))

    # 3) 跑流水线
    for script, args in (
        ("gen_icons.py", ["--gen", dir_name]),
        ("gen_tables.py", []),
        ("gen_site.py", []),
    ):
        try:
            r = subprocess.run([sys.executable, script] + args, cwd=str(repo),
                                capture_output=True, text=True, timeout=300)
            ok = r.returncode == 0
            steps.append("%s %s -> %s" % (script, " ".join(args), "OK" if ok else "FAIL(%d)" % r.returncode))
            if not ok and r.stderr.strip():
                steps.append("  stderr: " + r.stderr.strip().splitlines()[-1][:200])
        except Exception as e:  # noqa: BLE001
            steps.append("%s 异常: %s" % (script, e))

    # 4) 静态校验（仅报告）
    try:
        r = subprocess.run([sys.executable, "tools/check_plugins.py"], cwd=str(repo),
                           capture_output=True, text=True, timeout=300)
        steps.append("check_plugins.py -> %s" % ("OK" if r.returncode == 0 else "WARN/FAIL"))
    except Exception as e:  # noqa: BLE001
        steps.append("check_plugins.py 异常: %s" % e)

    return steps


if __name__ == "__main__":
    import argparse
    ap = argparse.ArgumentParser(description="QuickDock 插件脚手架生成引擎")
    ap.add_argument("--dir")
    ap.add_argument("--name-zh", default="")
    ap.add_argument("--name-en", default="")
    ap.add_argument("--desc-zh", default="")
    ap.add_argument("--desc-en", default="")
    ap.add_argument("--runtime", default="none", choices=["none", "goja", "native"])
    ap.add_argument("--category", default="开发者日常")
    ap.add_argument("--author", default="QuickDock")
    ap.add_argument("--repo", required=True)
    ap.add_argument("--integrate", action="store_true")
    ap.add_argument("--spec-b64", help="base64(JSON spec)，供插件 UI 调用")
    a = ap.parse_args()

    if a.spec_b64:
        import base64
        spec = json.loads(base64.b64decode(a.spec_b64).decode("utf-8"))
    else:
        spec = {
            "dir": a.dir, "name_zh": a.name_zh, "name_en": a.name_en,
            "desc_zh": a.desc_zh, "desc_en": a.desc_en, "runtime": a.runtime,
            "category": a.category, "author": a.author,
        }
    res = generate_plugin(spec, a.repo, integrate=a.integrate)
    print(json.dumps(res, ensure_ascii=False, indent=2))
