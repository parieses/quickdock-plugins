# 脚手架生成引擎包（plugin-scaffold 的「单一生成逻辑来源」）
#
# CLI（tools/newplugin.py）与 QuickDock 插件（plugin-scaffold）都调用这里的
# generate_plugin()，保证两种形态产出的文件 100% 一致。
