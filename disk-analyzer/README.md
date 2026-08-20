# disk-analyzer 插件

## 目录结构
```
plugins/external/disk-analyzer/
├── plugin.json           # 插件清单（声明 platforms: [windows, darwin, linux]）
├── disk-analyzer.go      # Go 后端（JSON-RPC 2.0 主逻辑）
├── disk_stats_windows.go # Windows 磁盘统计（GetDiskFreeSpaceExW）
├── disk_stats_unix.go    # macOS/Linux 磁盘统计（syscall.Statfs）
├── icon.svg              # 插件图标
├── go.mod                # Go module
└── frontend/
    └── index.html        # 前端页面（层级树图）
```

## 构建
```bash
cd plugins/external/disk-analyzer
go build -o disk-analyzer.exe .
```

## 功能
- `disk.scan` - 递归扫描目录，返回树形结构（支持 depth/limit 参数）
- `disk.list` - 列出根目录顶级子目录及大小
- `disk.info` - 获取磁盘总空间/已用/可用/使用率
- `ping` - 健康检查

## 集成到 QuickDock
编译后把 `disk-analyzer.exe` 复制到 QuickDock 的插件目录（`~/.quickdock/plugins/disk-analyzer/`），启动时自动加载。
