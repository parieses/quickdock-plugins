# 服务器自查

> 对你自己的服务器做一次只读暴露面体检：解析预检 → 端口扫描 → 服务指纹 → 敏感路径 → 未授权校验 → 风险分级。

每天都在被别人扫的机器，需要的不是再装一个扫描器，而是**定期确认自己的暴露面有没有变多**。
本插件把这条链路串成一次点击，跑完直接给结论和处置建议。

- 运行时：`native`（Go，单文件可执行，无外部依赖）
- 分类：`安全工具`
- 权限：`network`
- 窗口：900 × 600

## 做什么

| 阶段 | 动作 | 只读保障 |
| --- | --- | --- |
| 解析预检 | 域名 → A 记录，记录开始时间；可选 HTTP 查询 IP 归属（不查 whois） | 拒绝回环 / 内网 / 链路本地 / 组播 / 保留地址，防误扫他人内网 |
| 端口扫描 | TCP connect 两阶段：内置常用端口表（约 1100 个）→ 可选 1-65535 全量；输出开放/关闭/过滤三态 | 不用 ICMP、不用 raw socket、不做 SYN 半开（Windows 防火墙与杀软会拦） |
| 服务指纹 | TLS 优先（协议版本 / 套件 / ALPN + 证书 CN/SAN/颁发者/有效期/签名算法/密钥长度），失败回退 banner 抓取；再发 HTTP HEAD 与单次协议探测，归类 ssh/ftp/http/https/mysql/redis/mongodb/memcached/docker/es 等 | 探测指令只有 `HEAD`、`PING`、`version`、`isMaster`，不改目标状态 |
| 敏感路径 | 36 条内置路径只读 GET，随机路径基线 + 关键词佐证过滤 404 与软 404 兜底页 | 单次、不递归、不跟随跳转、正文最多读 1KB 作证据，并对口令字段打码 |
| 未授权校验 | Redis PING、Memcached version、Docker `/version`、ES `GET /`、FTP 匿名登录 | 每项**只做一次**，不分发字典、不并发认证、不重试 |
| 风险分级 | critical / high / medium / low / info，每条附处置建议，可导出纯文本报告 | — |

## 明确不做

- 漏洞利用、上传 / 写入 / 删除任何内容、反弹 shell
- 多口令轮询同一账号（爆破）、字典喷洒、并发认证
- DoS / 压力测试 / 大量重复请求
- 下载或爬取目标业务数据、绕过认证、SQL 注入探测
- raw socket / SYN 扫描 / 伪造源 IP

## 命令

| 命令 | 说明 |
| --- | --- |
| `audit.start` | 发起巡检，返回 `sessionId`；参数 `host` 必填，另有 `mode`（common/full）、`ports`、`timeout`、`concurrency`、`checkPaths`、`checkUnauth`、`geo` |
| `audit.poll` | 取阶段、进度、端口/指纹/路径/未授权/风险结果；`running=false` 表示结束 |
| `audit.stop` | 中止会话（已扫到的结果保留） |
| `audit.report` | 导出纯文本报告 |
| `ip.lookup` | 查 IP 国家/地区、运营商与 ASN |

默认参数：并发 500（硬上限 1000）、单端口超时 800ms、只扫公网目标。

## 调参取舍

- **并发 500 是默认，不是建议上限**：再高在笔记本上容易被本地 NAT / 光猫会话表压住，表现为大量 `filtered` 误判。上限硬编码 1000，改代码才能突破。
- **超时 800ms** 是「国内公网跨运营商」的经验值。丢包严重的线路可调到 1500，代价是全量扫描时间线性变长。
- **全端口**：65535 个端口在 500 并发下约 1~2 分钟；只扫常用表通常 3~8 秒。

## 开发

```bash
python build.py server-audit      # 打包（按 plugin.json 的 platforms）
go test ./...                     # 单元测试：预检拒绝、端口表、本机全链路
```

本机若无 Chrome，`gen_icons.py --gen` 会失败（它靠 Chrome 量图形包围盒）。
图标目前是手写的规范形态，改字形请同步 `glyphs.py` 的 `GLYPH["server-audit"]`，
有 Chrome 的机器上跑 `python gen_icons.py --gen server-audit` 会重算包围盒与缩放。
