// Database - 数据库客户端（native 插件，JSON-RPC 2.0 over stdin/stdout）
//
// 命令：
//   conn.list      列出已保存连接（密码脱敏）
//   conn.save      新建 / 更新连接（密码 DPAPI 加密后落库）
//   conn.delete    删除连接
//   conn.test      用传入配置测试连通性（无需先保存）
//   query.run      执行 SQL（MySQL/SQLite）或 Redis 命令，返回列 + 行 + 可编辑元数据
//   tree.list      库表浏览器根节点（SQL：库；Redis：键树）
//   tree.objects   按需展开某个库下的 表/视图/字段
//   table.list     列出某库全部表/视图（引擎、行数、注释等元信息）
//   table.structure 建表信息：字段 + 索引 + 外键 + 建表语句
//   table.rows     表数据分页读取（支持排序，附总行数与主键）
//   row.update     以主键定位单行提交 UPDATE（MySQL/SQLite）
//   row.insert     整行插入（MySQL/SQLite，仅写入非空字段）
//   row.delete     以主键定位单行删除（MySQL/SQLite）
//   row.deleteMany 按主键列表批量删除（分片 IN + 单事务，供「删除所选/全选」）
//   row.pkList     按数据页当前筛选/排序收集全部匹配行的主键值（供「全选匹配行」）
//   kv.get/kv.set/kv.list  插件私有 KV（存设置、各连接上次打开的库等前端持久化数据）
//
// 库上下文（database 参数）：query.run / table.* / row.update 都接受可选的 database，
// 非空时把会话限定在该库内 —— 因此 SQL 里只写表名即可，无需「库名.表名」。
//
// Redis 可视化管理（dbType=redis 时可用；dbIndex 可选，覆盖连接里配置的库索引）：
//   redis.keyList      扁平列出键 + 类型
//   redis.keyGet       读取单 key 详情（类型/TTL/值，按类型序列化）
//   redis.keySet       新建或整体覆盖 key（按类型写入，ttl<0 永久）
//   redis.keyDelete    批量删除 key
//   redis.keyRename    重命名 key
//   redis.keyExpire    设置 TTL（seconds<0 即 PERSIST 永不过期）
//   redis.hashFieldSet / redis.hashFieldDel   hash 字段级增删
//   redis.listPush / redis.listElemDel        list 头尾追加 / 按值删除元素
//   redis.setMemberAdd / redis.setMemberDel    set 成员增删
//   redis.zsetMemberSet / redis.zsetMemberDel  zset 成员 score 设置 / 删除
//
// 生命周期：initialize → plugin.execute（多次）→ shutdown。
// shutdown 是宿主在「禁用 / 卸载 / 更新插件 / 退出主程序」时发来的通知（无 id、不回包），
// 收到后必须立刻退出并释放文件句柄，否则宿主替换不了被占用的 database.exe，
// 安装 / 更新会一直停在「安装中」（宿主此时是 treeKill=false，不会强杀）。
//
// 存储：插件自带 SQLite（modernc.org/sqlite，纯 Go），DB 文件放在宿主数据根下：
//   <用户主目录>/.quickdock/plugin-data/io.github.parieses.database\（可用 QUICKDOCK_PLUGIN_DATA 覆盖）
// 之所以不放插件安装目录：宿主更新插件时会把整个 <插件目录> 重命名成 <id>.bak.<旧版本>，
// 里面的 data/ 被一并卷走、新版本解压进去是空库 —— 连接就「看不到了」。
// 之所以不放 %APPDATA%：它常被「已知文件夹移动」重定向到 OneDrive / 网盘，
// SQLite（WAL）在重定向目录下会锁失效甚至损坏；且密码是 DPAPI 机器绑定的，不该漫游。
// 首次启动会自动把历史数据（含被卷进 .bak.* 备份里的库）合并到新位置。
// 外库驱动：go-sql-driver/mysql、modernc.org/sqlite、redis/go-redis/v9。

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// ---- JSON-RPC ----

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type executeParams struct {
	Command string                 `json:"command"`
	Input   map[string]interface{} `json:"input"`
}

func strFrom(input map[string]interface{}, key string) string {
	if v, ok := input[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

// rawStrFrom 不做 TrimSpace（SQL / 密码等需保留原文）
func rawStrFrom(input map[string]interface{}, key string) string {
	if v, ok := input[key].(string); ok {
		return v
	}
	return ""
}

func intFrom(input map[string]interface{}, key string, def int) int {
	switch v := input[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			return n
		}
	}
	return def
}

func strSliceFrom(input map[string]interface{}, key string) []string {
	raw, ok := input[key]
	if !ok || raw == nil {
		return nil
	}
	arr, ok := raw.([]interface{})
	if !ok {
		if s, ok := raw.(string); ok && s != "" {
			return []string{s}
		}
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, v := range arr {
		if s, ok := v.(string); ok {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

func strMapFrom(input map[string]interface{}, key string) map[string]string {
	raw, ok := input[key].(map[string]interface{})
	if !ok {
		return nil
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		switch t := v.(type) {
		case string:
			out[k] = t
		case nil:
			out[k] = ""
		default:
			out[k] = fmt.Sprintf("%v", t)
		}
	}
	return out
}

var outMu sync.Mutex

func respond(id int64, result interface{}) {
	defer qdFinishRequest(id, 0, "")
	outMu.Lock()
	defer outMu.Unlock()
	out, _ := json.Marshal(map[string]interface{}{"jsonrpc": "2.0", "id": id, "result": result})
	fmt.Println(string(out))
}

func respondError(id int64, code int, msg string) {
	defer qdFinishRequest(id, code, msg)
	outMu.Lock()
	defer outMu.Unlock()
	out, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0", "id": id,
		"error": map[string]interface{}{"code": code, "message": msg},
	})
	fmt.Println(string(out))
}

// ---- dispatch ----

func dispatch(raw string) {
	var req rpcRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		respondError(0, -32700, "parse error: "+err.Error())
		return
	}
	switch req.Method {
	case "initialize":
		respond(req.ID, map[string]interface{}{"status": "ready", "name": "QuickDock Database"})
	case "host.ping":
		respond(req.ID, map[string]interface{}{"pong": true})
	case "plugin.execute":
		var params executeParams
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &params); err != nil {
				respondError(req.ID, -32602, "invalid params: "+err.Error())
				return
			}
		}
		handleCommand(req.ID, params)
	// 生命周期：宿主「卸载 / 禁用 / 更新插件 / 退出主程序」时会发 shutdown 通知
	// （{"jsonrpc":"2.0","method":"shutdown","params":null}，无 id、无需响应）。
	// 必须立刻退出：宿主以 treeKill=false 停插件时不会强杀，若进程赖着不走，
	// 它就无法替换被占用的 database.exe，安装/更新会一直卡在「安装中」。
	case "shutdown", "terminate", "dispose", "exit":
		shutdown()
	default:
		// 通知类消息（无 id）不需要响应；此前会对 shutdown 回一个 id=0 的错误包，
		// 既不符合 JSON-RPC 约定，也会干扰宿主的停止判定。
		if req.ID == 0 {
			return
		}
		respondError(req.ID, -32601, "unknown method: "+req.Method)
	}
}

// shutdown 释放资源并立即退出（宿主 stopPlugin / 卸载 / 更新前触发的通知）。
func shutdown() {
	// 先关掉缓存的外库连接：SQLite 句柄不释放会锁住 .db 文件，影响宿主更新 / 用户操作
	closeAllConnCache()
	storeMu.Lock()
	if store != nil {
		_ = store.Close()
		store = nil
	}
	storeMu.Unlock()
	os.Exit(0)
}

func handleCommand(id int64, p executeParams) {
	// handler 内 panic 不应拖垮整个插件进程（否则宿主健康检查会重启，用户丢失状态）
	defer func() {
		if r := recover(); r != nil {
			respondError(id, -32000, fmt.Sprintf("internal error: %v", r))
		}
	}()
	cmd := strings.ToLower(strings.TrimSpace(p.Command))
	switch cmd {
	// 连接管理
	case "conn.list", "list":
		handleConnList(id, p.Input)
	case "conn.save", "save":
		handleConnSave(id, p.Input)
	case "conn.delete", "delete":
		handleConnDelete(id, p.Input)
	case "conn.test", "test":
		handleConnTest(id, p.Input)
	// 查询
	case "query.run", "query":
		handleQueryRun(id, p.Input)
	// 库表树
	case "tree.list", "tree":
		handleTreeList(id, p.Input)
	case "tree.objects":
		handleTreeObjects(id, p.Input)
	// 对象浏览（Navicat 式：库 → 表列表 → 结构 / 数据）
	case "table.list":
		handleTableList(id, p.Input)
	case "table.columns":
		handleTableColumns(id, p.Input)
	case "table.structure":
		handleTableStructure(id, p.Input)
	case "table.rows":
		handleTableRows(id, p.Input)
	case "redis.keylist":
		handleRedisKeyList(id, p.Input)
	// Redis 可视化管理
	case "redis.keyGet":
		redisKeyGet(id, p.Input)
	case "redis.keySet":
		redisKeySet(id, p.Input)
	case "redis.keyDelete":
		redisKeyDelete(id, p.Input)
	case "redis.keyRename":
		redisKeyRename(id, p.Input)
	case "redis.keyExpire":
		redisKeyExpire(id, p.Input)
	case "redis.hashFieldSet":
		redisHashFieldSet(id, p.Input)
	case "redis.hashFieldDel":
		redisHashFieldDel(id, p.Input)
	case "redis.listPush":
		redisListPush(id, p.Input)
	case "redis.listElemDel":
		redisListElemDel(id, p.Input)
	case "redis.setMemberAdd":
		redisSetMemberAdd(id, p.Input)
	case "redis.setMemberDel":
		redisSetMemberDel(id, p.Input)
	case "redis.zsetMemberSet":
		redisZSetMemberSet(id, p.Input)
	case "redis.zsetMemberDel":
		redisZSetMemberDel(id, p.Input)
	// 行编辑
	case "row.update":
		handleRowUpdate(id, p.Input)
	case "row.insert":
		handleRowInsert(id, p.Input)
	case "row.delete":
		handleRowDelete(id, p.Input)
	case "row.deleteMany":
		handleRowDeleteMany(id, p.Input)
	case "row.pkList":
		handleRowPkList(id, p.Input)
	// 通用 KV（设置 / 上次打开的库 等前端持久化数据）
	case "kv.get":
		handleKVGet(id, p.Input)
	case "kv.set":
		handleKVSet(id, p.Input)
	case "kv.list":
		handleKVList(id, p.Input)
	default:
		respondError(id, -32601, "unknown command: "+p.Command)
	}
}

// ---- KV 命令 ----

func handleKVGet(id int64, input map[string]interface{}) {
	key := strFrom(input, "key")
	if key == "" {
		respondError(id, -32602, "缺少 key")
		return
	}
	val, err := kvGet(key)
	if err != nil {
		respondError(id, -32000, err.Error())
		return
	}
	respond(id, map[string]interface{}{"value": val})
}

func handleKVSet(id int64, input map[string]interface{}) {
	key := strFrom(input, "key")
	if key == "" {
		respondError(id, -32602, "缺少 key")
		return
	}
	value, ok := input["value"]
	if !ok {
		respondError(id, -32602, "缺少 value")
		return
	}
	if err := kvSet(key, value); err != nil {
		respondError(id, -32000, err.Error())
		return
	}
	respond(id, map[string]interface{}{"ok": true})
}

func handleKVList(id int64, input map[string]interface{}) {
	keys, err := kvList()
	if err != nil {
		respondError(id, -32000, err.Error())
		return
	}
	respond(id, map[string]interface{}{"keys": keys})
}

func main() {
	qdLogStart()
	defer qdLogExit()
	// 存储初始化失败不阻塞进程启动：后续命令会各自返回错误，host.ping 仍可用
	initStore()

	reader := bufio.NewReader(os.Stdin)
	var wg sync.WaitGroup
	for {
		line, err := reader.ReadString('\n')
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
		// 每个请求独立 goroutine：任何 handler 都不阻塞 stdin 读循环，
		// host.ping 永远秒回，杜绝"同步 handler 卡住 → 健康检查超时 → 被误杀"。
		wg.Add(1)
		go func(raw string) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					// 外层兜底：handler 层 recover 已把大多数 panic 转成错误响应；
					// 这里只兜底 dispatch 自身未捕获的崩溃，记日志即可，避免重复回包。
					qdLogPanic("database dispatch panic", r)
				}
			}()
			dispatch(raw)
		}(data)
	}
	// stdin 关闭 = 宿主退出 / 停止插件。给在途请求最多 1.5s 收尾，然后强制退出：
	// 绝不能因为某个 handler 卡在网络 IO（比如一个连不上的 MySQL）上而让进程不退出——
	// 进程活着就锁着 exe，宿主的安装 / 更新会一直卡在「安装中」。
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(1500 * time.Millisecond):
		fmt.Fprintln(os.Stderr, "database: 仍有在途请求，stdin 已关闭，强制退出")
	}
	shutdown()
}
