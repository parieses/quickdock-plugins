#!/usr/bin/env python3
"""插件静态校验：把「断链 / 缺样式 / 缺变量 / 清单不合法」这类低级但高发的问题挡在打包之前。

这些问题是真实踩过的：
  - api-loadtest 引用了 qd-theme.css 但文件不存在 → 230 处 CSS 变量全失效，界面排版塌；
  - 13 个插件前端引用不存在的样式表 → 30 处 404，靠人眼看才发现；
  - 6 个插件用了未定义的 p-* 类（其中 .p-tab-active 缺失让用户看不出当前在哪个标签页）。

用法：
    python tools/check_plugins.py            # 全量检查
    python tools/check_plugins.py hash-calc   # 只查指定插件目录
退出码 0 = 通过（可能有 warning），1 = 有 error。
"""
import glob
import json
import os
import re
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import check_design  # noqa: E402  设计系统门禁（共享类影子重定义）

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

# 少数类名不用于样式、只作为 JS 的事件钩子（点击后 closest('.p-kill') 之类），不算缺失
CLASS_ALLOW = {
    ("port-scanner", "p-kill"),
}

RUNTIMES = ("native", "goja", "none")

errors = []
warnings = []


def err(plugin, msg):
    errors.append(f"[ERROR] {plugin}: {msg}")


def warn(plugin, msg):
    warnings.append(f"[WARN ] {plugin}: {msg}")


def read(path):
    try:
        with open(path, "r", encoding="utf-8", errors="ignore") as f:
            return f.read()
    except OSError:
        return ""


# ---- commands[].params：宿主支持的 JSON Schema 子集 ----
#
# 宿主（主程序 internal/plugin/types.go 的 Command.Params）不引 JSON Schema 库，
# 只在加载期做浅校验：不合规就丢掉该字段并记警告，**不会**让插件装不上。
# 于是「不合规」会以最坏的方式落地——安装成功、插件一切正常，只有 AI 客户端
# 看不到参数声明，或者看到一个被悄悄丢掉一半的声明。所以真正的门禁在这里。
PARAM_TYPES = ("string", "number", "integer", "boolean")
PARAM_TOP_KEYS = ("type", "properties", "required")
PARAM_PROP_KEYS = ("type", "description", "default", "enum")
# runtime=none 的插件没有后端，宿主只能经 plugin:init 把纯文本交给前端
# （plugin_open 的注入通道就是 {text, command}），因此只允许声明 text 一个键。
NONE_RUNTIME_KEYS = ("text",)


def check_params(d, label, params, runtime):
    """逐条校验一条 commands[].params（违规一律 error）。"""
    if not isinstance(params, dict):
        err(d, f"{label}: params 必须是对象")
        return
    for k in params:
        if k not in PARAM_TOP_KEYS:
            # oneOf / anyOf / $ref / allOf / additionalProperties 都从这里被拦住
            err(d, f"{label}: params 含不支持的顶层关键字 {k!r}（只允许 {'/'.join(PARAM_TOP_KEYS)}）")
    if params.get("type") != "object":
        err(d, f'{label}: params.type 必须是 "object"（宿主的参数声明只有对象形参这一种）')
    props = params.get("properties")
    if not isinstance(props, dict) or not props:
        err(d, f"{label}: params.properties 必须是非空对象")
        return
    if runtime == "none":
        extra = [k for k in props if k not in NONE_RUNTIME_KEYS]
        if extra:
            err(d, f"{label}: runtime=none 的插件只能声明 text（宿主只经 plugin:init 传纯文本），"
                    f"却声明了 {', '.join(extra)}")
    for name, spec in props.items():
        if not isinstance(spec, dict):
            err(d, f"{label}: 属性 {name!r} 必须是对象")
            continue
        for k in spec:
            if k not in PARAM_PROP_KEYS:
                err(d, f"{label}: 属性 {name!r} 含不支持的关键字 {k!r}"
                        f"（只允许 {'/'.join(PARAM_PROP_KEYS)}）")
        ptype = spec.get("type")
        if ptype is None:
            err(d, f"{label}: 属性 {name!r} 缺少 type")
        elif ptype not in PARAM_TYPES:
            # 嵌套 object / array 也从这里被拦住：宿主不支持，真要列表就声明 string 并在
            # description 里写清「逗号分隔」或「JSON 文本」。
            err(d, f"{label}: 属性 {name!r} 的 type 非法: {ptype!r}（只允许 {'/'.join(PARAM_TYPES)}）")
        desc = spec.get("description")
        if not isinstance(desc, str) or not desc.strip():
            err(d, f"{label}: 属性 {name!r} 缺少 description（模型只能靠它知道该传什么）")
        if "enum" in spec:
            ev = spec["enum"]
            if not isinstance(ev, list) or not ev or not all(isinstance(x, str) for x in ev):
                err(d, f"{label}: 属性 {name!r} 的 enum 必须是非空字符串数组")
        if "default" in spec and ptype in PARAM_TYPES:
            dv = spec["default"]
            ok = {
                "string": isinstance(dv, str),
                "boolean": isinstance(dv, bool),
                "integer": isinstance(dv, int) and not isinstance(dv, bool),
                "number": isinstance(dv, (int, float)) and not isinstance(dv, bool),
            }[ptype]
            if not ok:
                err(d, f"{label}: 属性 {name!r} 的 default 与 type={ptype} 不匹配: {dv!r}")
    req = params.get("required")
    if req is not None:
        if not isinstance(req, list) or not all(isinstance(x, str) for x in req):
            err(d, f"{label}: params.required 必须是字符串数组")
        else:
            for k in req:
                if k not in props:
                    err(d, f"{label}: params.required 里的 {k!r} 不在 properties 中")


def check_manifest(d, plugin_dir):
    p = os.path.join(plugin_dir, "plugin.json")
    if not os.path.isfile(p):
        err(d, "缺少 plugin.json")
        return None
    try:
        m = json.loads(read(p))
    except json.JSONDecodeError as e:
        err(d, f"plugin.json 不是合法 JSON: {e}")
        return None
    for f in ("id", "name", "version", "backend"):
        if f not in m:
            err(d, f"plugin.json 缺少必填字段 {f}")
    rt = (m.get("backend") or {}).get("runtime", "")
    if rt not in RUNTIMES:
        err(d, f"backend.runtime 非法: {rt!r}（只支持 {'/'.join(RUNTIMES)}）")
    if rt == "native" and not (m.get("backend") or {}).get("entry"):
        err(d, "native 运行时必须声明 backend.entry")
    if not os.path.isfile(os.path.join(plugin_dir, "icon.svg")):
        err(d, "缺少 icon.svg（插件管理/命令面板的唯一视觉标识）")
    if (m.get("frontend") or {}).get("enabled"):
        fe = (m.get("frontend") or {}).get("entry", "")
        if not fe:
            err(d, "frontend.enabled=true 但没有声明 frontend.entry")
        elif not os.path.isfile(os.path.join(plugin_dir, fe)):
            err(d, f"frontend.entry 指向不存在的文件: {fe}")
    for idx, c in enumerate(m.get("commands") or []):
        if not c.get("id"):
            err(d, "commands[] 存在没有 id 的项")
            continue
        label = f"commands[{idx}]（{c['id']}）"
        if "description" in c and not isinstance(c["description"], str):
            err(d, f"{label}: description 必须是字符串")
        if "params" in c:
            check_params(d, label, c["params"], rt)
    return m


def check_frontend(d, plugin_dir):
    fe_dir = os.path.join(plugin_dir, "frontend")
    index = os.path.join(fe_dir, "index.html")
    if not os.path.isfile(index):
        return
    html = read(index)

    # 1) 本地资源断链
    for ref in re.findall(r'(?:href|src)=["\']([^"\'#?]+)["\']', html):
        if ref.startswith(("http", "data:", "//", "javascript:", "mailto:")):
            continue
        # 跳过 JS 字符串里的模板片段（如 .replace(...,'<a href="$2">') 里的 $2）
        if any(ch in ref for ch in "$`{}"):
            continue
        if not os.path.exists(os.path.normpath(os.path.join(fe_dir, ref))):
            err(d, f"前端引用不存在的文件: {ref}")

    # 2) p-* 类：用了但没定义
    used = set()
    for m in re.finditer(r'class=["\']([^"\']*)["\']', html):
        for c in m.group(1).split():
            if re.fullmatch(r"p-[a-z0-9-]+", c):
                used.add(c)
    if used:
        css = "\n".join(read(p) for p in glob.glob(os.path.join(fe_dir, "*.css")))
        css += "\n".join(re.findall(r"<style[^>]*>(.*?)</style>", html, re.S))
        defined = set(re.findall(r"\.(p-[a-z0-9-]+)", css))
        for c in sorted(used - defined):
            if (d, c) in CLASS_ALLOW:
                continue
            err(d, f"前端用了未定义的样式类 .{c}")

    # 3) CSS 变量：引用了但没有任何声明（带 fallback 的 var(--x, #fff) 不算错）
    css_all = "\n".join(read(p) for p in glob.glob(os.path.join(fe_dir, "*.css")))
    css_all += "\n".join(re.findall(r"<style[^>]*>(.*?)</style>", html, re.S))
    declared = set(re.findall(r"(--[a-zA-Z0-9-]+)\s*:", css_all))
    # JS 运行时用 style.setProperty('--x', ...) 注入的变量也算已声明
    declared |= set(re.findall(r"setProperty\(\s*['\"](--[a-zA-Z0-9-]+)['\"]", html))
    for js in glob.glob(os.path.join(fe_dir, "*.js")):
        declared |= set(re.findall(r"setProperty\(\s*['\"](--[a-zA-Z0-9-]+)['\"]", read(js)))
    for m in re.finditer(r"var\(\s*(--[a-zA-Z0-9-]+)\s*([,)])", css_all):
        name, sep = m.group(1), m.group(2)
        if sep == "," or name in declared:
            continue
        err(d, f"CSS 变量未定义且无兜底值: var({name})")
        break  # 同一个插件只报一次，避免刷屏

    # 4) 内联宿主行为脚本：宿主已统一注入（BRIDGE_SCRIPT / quickdock-runtime），
    #    插件再内联一份既是重复代码，也会随宿主能力演进而漂移。
    #    2026-10-20 起全仓 126 份副本已清理（迁移脚本已退役），
    #    这里保留为回归守卫。
    for marker, why in (("__qdErrHook", "前端异常上报钩子"),
                        ("qd-theme-follow", "主题跟随脚本"),
                        ("__qdBridge", "宿主桥接脚本自身")):
        if marker in html:
            warn(d, f"内联{why}（{marker}）已由宿主统一注入，应删除内联副本")

    # 4b) 「换皮版」主题跟随：写法各不相同，但特征一样——读 opener/parent 的
    #     document，或用 prefers-color-scheme 猜主题。两者在当前沙箱下都无效：
    #     iframe sandbox 不含 allow-same-origin（opaque origin），读 parent.document
    #     必抛 SecurityError；而宿主主题为「跟随系统」时 currentThemeName() 已归一成
    #     light/dark 再下发，插件侧再兜一层反而会造成浅色闪变。
    #     只扫 <script> 块，避开注释里的说明文字。
    for js in re.findall(r"<script(?![^>]*\bsrc\s*=)[^>]*>(.*?)</script>", html, re.S):
        hits = []
        if re.search(r"(?:window|self)\.(?:opener|parent)\s*(?:!==[^;]*)?\.document", js):
            hits.append("同源 opener/parent.document 兜底")
        if "matchMedia" in js and "prefers-color-scheme" in js:
            hits.append("prefers-color-scheme 主题兜底")
        if hits:
            warn(d, f"内联主题兜底（{'、'.join(hits)}）已由宿主 BRIDGE_SCRIPT 统一注入，"
                    f"且同源分支在 opaque origin 下是死代码，应删除")
            break

    # 5) 深浅两套主题必须都定义。宿主通过 html[data-theme] 切换，插件只写
    #    :root（暗色）而不写 html[data-theme="light"] 的话，宿主切到浅色时
    #    该插件仍是深色——实测 photo-library 就是这样，77 个里独一份。
    #    色板以 plugins/builtin/common.css 为准，新插件照抄那份变量集。
    light_vars = set()
    for m in re.finditer(r'html\[data-theme=["\']light["\']\]\s*\{(.*?)\n\}', css_all, re.S):
        light_vars |= set(re.findall(r"(--[a-zA-Z0-9-]+)\s*:", m.group(1)))
    if not light_vars:
        err(d, "主题文件未定义 html[data-theme=\"light\"] 浅色档："
               "宿主切浅色时该插件仍会保持深色（照抄 plugins/builtin/common.css 的浅色变量集）")
    else:
        # 浅色档里的变量必须与基础档同名，否则说明两套变量集对不上
        base_vars = set(re.findall(r"(?m)^\s{2}(--[a-zA-Z0-9-]+)\s*:", css_all))
        orphan = sorted(light_vars - base_vars)
        if orphan:
            warn(d, f"浅色档定义了基础档没有的变量（疑似写错作用域）: {', '.join(orphan)}")


def check_stale_binaries(d, plugin_dir, manifest):
    """native 插件根目录里除 entry 之外的可执行文件：本地打包会把它塞进 zip。"""
    if not manifest:
        return
    if (manifest.get("backend") or {}).get("runtime") != "native":
        return
    entry = (manifest.get("backend") or {}).get("entry", "")
    for f in glob.glob(os.path.join(plugin_dir, "*.exe")):
        name = os.path.basename(f)
        if name.lower() == entry.lower() + ".exe" or name.lower() == entry.lower():
            continue
        warn(d, f"根目录有非 entry 的可执行文件 {name}（本地打包会带进 zip，建议删除）")


def check_process_decl(d, plugin_dir, manifest):
    """native 插件执行外部程序时必须声明 permissions.process。

    宿主对 native 插件的 permissions 只有「自证」效力（运行期拦不住插件自己
    调 exec.Command），所以清单漏声明会让授权契约与实现不符——
    用户在市场页看到的权限列表就不包含「执行程序」这一项。
    """
    if not manifest:
        return
    if (manifest.get("backend") or {}).get("runtime") != "native":
        return
    perms = manifest.get("permissions") or {}
    proc = perms.get("process")
    if proc is not None and proc is not False and not (isinstance(proc, list) and len(proc) == 0):
        return  # 已声明

    # 扫源码找 exec.Command / exec.CommandContext
    hit = False
    for f in glob.glob(os.path.join(plugin_dir, "*.go")):
        try:
            src = open(f, encoding="utf-8", errors="ignore").read()
        except OSError:
            continue
        if "exec.Command(" in src or "exec.CommandContext(" in src:
            hit = True
            break
    if hit:
        err(d, "native 插件调用了 exec.Command 但未声明 permissions.process"
                "（执行程序需显式授权，否则授权契约与实现不符）")


def check_design_gate(dirs):
    """设计系统门禁：插件不得在本地内联样式里顶层重定义共享组件类，
    且字号必须走排版刻度内的令牌值。

    单独跑 tools/check_design.py 可看完整明细与存量台账。
    """
    tier1_new, tier1_known, tier2, _ = check_design.scan([d for d, _ in dirs])
    for line in tier1_new:
        err("design", line.replace("顶层重定义", "顶层重定义共享组件类"))
    for line in tier1_known:
        warn("design", line + "  ← 迁移后可从 tools/design_baseline.json 移除")
    font_new, font_known = check_design.check_font_sizes(dirs)
    for line in font_new:
        err("design", line)
    for line in font_known:
        warn("design", line + "  ← 确认是语义尺寸后可忽略")
    emoji_new, emoji_known = check_design.check_emoji_icons(dirs)
    for line in emoji_new:
        err("design", line)
    for line in emoji_known:
        warn("design", line)


def main():
    only = sys.argv[1] if len(sys.argv) > 1 else None
    dirs = []
    for name in sorted(os.listdir(ROOT)):
        p = os.path.join(ROOT, name)
        if not os.path.isdir(p) or not os.path.isfile(os.path.join(p, "plugin.json")):
            continue
        if only and name != only:
            continue
        dirs.append((name, p))

    if not dirs:
        print(f"没有找到插件目录{f'（{only}）' if only else ''}")
        return 1

    for d, p in dirs:
        m = check_manifest(d, p)
        check_frontend(d, p)
        check_stale_binaries(d, p, m)
        check_process_decl(d, p, m)

    check_design_gate(dirs)

    for line in errors + warnings:
        print(line)
    print(f"\n检查 {len(dirs)} 个插件：{len(errors)} 错误 / {len(warnings)} 警告")
    return 1 if errors else 0


if __name__ == "__main__":
    sys.exit(main())
