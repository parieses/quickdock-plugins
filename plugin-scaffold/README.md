# 插件脚手架

> 可视化生成 QuickDock 插件骨架：填表即产出 `plugin.json` / 前端 / 后端样板，并自动接入图标与索引流水线。

由 plugin-scaffold 生成（自身也是用本脚手架理念做的）。

- 运行时：`none`（纯前端表单，经 `host.process.exec` 调用仓库 `tools/newplugin.py` 生成引擎）
- 分类：开发者日常

## 工作原理

1. 表单收集插件元信息（名称、运行时、分类、权限、命令等）；
2. 经 `host.process.exec` 调起仓库内的 Python 生成引擎 `tools/newplugin.py`；
3. 引擎写出一套「能通过 `check_plugins.py` 门禁」的插件目录；若勾选「接入仓库」，还自动：
   - 在 `glyphs.py` 登记图标字形、在 `gen_icons.py` 登记语义色；
   - 跑 `gen_icons.py --gen` 生成规范图标；
   - 跑 `gen_tables.py` / `gen_site.py` 刷新 README 与在线市场索引。

## 开发

```bash
python build.py plugin-scaffold        # 打包
python gen_icons.py --gen plugin-scaffold
```
