package main

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisKeyDetail 单个 key 的详情（类型感知）。
// TTL: 剩余秒数（<0 表示永不过期或不存在，前端按 -1 显示「永久」）。
// 各类型把元素放在 Items：string 用 Value，list/set 为 []string，
// hash 为 [{field,value}]，zset 为 [{member,score}]。
type RedisKeyDetail struct {
	Key    string      `json:"key"`
	Type   string      `json:"type"`
	TTL    int64       `json:"ttl"`
	Size   int64       `json:"size"`
	Value  string      `json:"value,omitempty"`
	Items  interface{} `json:"items,omitempty"`
}

func redisCtx() context.Context {
	return context.Background()
}

func ttlToDur(seconds int) time.Duration {
	if seconds < 0 {
		return 0 // redis Set expiration=0 == 永不过期
	}
	return time.Duration(seconds) * time.Second
}

func toIfaceMap(m map[string]string) map[string]interface{} {
	out := make(map[string]interface{}, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func toIfaceSlice(s []string) []interface{} {
	out := make([]interface{}, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}

type zItem struct {
	member string
	score  float64
}

func zsetItemsFrom(input map[string]interface{}, key string) []zItem {
	raw, ok := input[key]
	if !ok {
		return nil
	}
	arr, ok := raw.([]interface{})
	if !ok {
		return nil
	}
	var out []zItem
	for _, v := range arr {
		m, ok := v.(map[string]interface{})
		if !ok {
			continue
		}
		mem, _ := m["member"].(string)
		var sc float64
		switch s := m["score"].(type) {
		case float64:
			sc = s
		case string:
			sc, _ = strconv.ParseFloat(s, 64)
		}
		out = append(out, zItem{member: mem, score: sc})
	}
	return out
}

// redisKeyGet 读取单个 key 的类型、TTL 与值（按类型序列化）。
func redisKeyGet(id int64, input map[string]interface{}) {
	cid := strFrom(input, "id")
	key := rawStrFrom(input, "key")
	if cid == "" || key == "" {
		respondError(id, -32602, "缺少 id 或 key")
		return
	}
	_, conn, err := openSaved(cid)
	if err != nil {
		respondError(id, -32000, err.Error())
		return
	}
	defer conn.close()
	if conn.redis == nil {
		respondError(id, -32000, "非 Redis 连接")
		return
	}
	ctx := redisCtx()
	kt, err := conn.redis.Type(ctx, key).Result()
	if err != nil {
		respondError(id, -32000, err.Error())
		return
	}
	if kt == "none" {
		respondError(id, -32000, "key 不存在")
		return
	}
	ttl, _ := conn.redis.TTL(ctx, key).Result()
	detail := RedisKeyDetail{Key: key, Type: kt, TTL: int64(ttl.Seconds())}
	switch kt {
	case "string":
		if v, e := conn.redis.Get(ctx, key).Result(); e == nil {
			detail.Value = v
			detail.Size = int64(len(v))
		}
	case "list":
		if items, e := conn.redis.LRange(ctx, key, 0, -1).Result(); e == nil {
			detail.Items = items
			detail.Size = int64(len(items))
		}
	case "set":
		if items, e := conn.redis.SMembers(ctx, key).Result(); e == nil {
			detail.Items = items
			detail.Size = int64(len(items))
		}
	case "hash":
		if m, e := conn.redis.HGetAll(ctx, key).Result(); e == nil {
			pairs := make([]map[string]string, 0, len(m))
			for f, v := range m {
				pairs = append(pairs, map[string]string{"field": f, "value": v})
			}
			detail.Items = pairs
			detail.Size = int64(len(pairs))
		}
	case "zset":
		if zs, e := conn.redis.ZRangeWithScores(ctx, key, 0, -1).Result(); e == nil {
			pairs := make([]map[string]interface{}, 0, len(zs))
			for _, z := range zs {
				pairs = append(pairs, map[string]interface{}{"member": z.Member, "score": z.Score})
			}
			detail.Items = pairs
			detail.Size = int64(len(zs))
		}
	}
	respond(id, detail)
}

// redisKeySet 新建或整体覆盖一个 key（按类型写入）。ttl<0 表示永不过期。
func redisKeySet(id int64, input map[string]interface{}) {
	cid := strFrom(input, "id")
	key := rawStrFrom(input, "key")
	rtype := strings.ToLower(strFrom(input, "rtype"))
	if cid == "" || key == "" || rtype == "" {
		respondError(id, -32602, "缺少 id / key / rtype")
		return
	}
	_, conn, err := openSaved(cid)
	if err != nil {
		respondError(id, -32000, err.Error())
		return
	}
	defer conn.close()
	if conn.redis == nil {
		respondError(id, -32000, "非 Redis 连接")
		return
	}
	ctx := redisCtx()
	ttl := intFrom(input, "ttl", -1)
	var setErr error
	switch rtype {
	case "string":
		setErr = conn.redis.Set(ctx, key, rawStrFrom(input, "value"), ttlToDur(ttl)).Err()
	case "hash":
		fields := strMapFrom(input, "fields")
		if len(fields) == 0 {
			respondError(id, -32602, "hash 至少需要一个 field")
			return
		}
		setErr = conn.redis.HSet(ctx, key, toIfaceMap(fields)).Err()
		if setErr == nil && ttl >= 0 {
			conn.redis.Expire(ctx, key, ttlToDur(ttl))
		}
	case "list":
		items := strSliceFrom(input, "items")
		if len(items) == 0 {
			respondError(id, -32602, "list 至少需要一个元素")
			return
		}
		for _, it := range items {
			if e := conn.redis.RPush(ctx, key, it).Err(); e != nil {
				respondError(id, -32000, e.Error())
				return
			}
		}
		if ttl >= 0 {
			conn.redis.Expire(ctx, key, ttlToDur(ttl))
		}
	case "set":
		members := strSliceFrom(input, "items")
		if len(members) == 0 {
			respondError(id, -32602, "set 至少需要一个成员")
			return
		}
		setErr = conn.redis.SAdd(ctx, key, toIfaceSlice(members)...).Err()
		if setErr == nil && ttl >= 0 {
			conn.redis.Expire(ctx, key, ttlToDur(ttl))
		}
	case "zset":
		items := zsetItemsFrom(input, "items")
		if len(items) == 0 {
			respondError(id, -32602, "zset 至少需要一个成员")
			return
		}
		zs := make([]redis.Z, 0, len(items))
		for _, it := range items {
			zs = append(zs, redis.Z{Score: it.score, Member: it.member})
		}
		setErr = conn.redis.ZAdd(ctx, key, zs...).Err()
		if setErr == nil && ttl >= 0 {
			conn.redis.Expire(ctx, key, ttlToDur(ttl))
		}
	default:
		respondError(id, -32602, "不支持的类型: "+rtype)
		return
	}
	if setErr != nil {
		respondError(id, -32000, setErr.Error())
		return
	}
	respond(id, map[string]interface{}{"ok": true, "key": key})
}

// redisKeyDelete 批量删除 key（keys 为字符串数组）。
func redisKeyDelete(id int64, input map[string]interface{}) {
	cid := strFrom(input, "id")
	keys := strSliceFrom(input, "keys")
	if cid == "" || len(keys) == 0 {
		respondError(id, -32602, "缺少 id 或 keys")
		return
	}
	_, conn, err := openSaved(cid)
	if err != nil {
		respondError(id, -32000, err.Error())
		return
	}
	defer conn.close()
	if conn.redis == nil {
		respondError(id, -32000, "非 Redis 连接")
		return
	}
	n, e := conn.redis.Del(redisCtx(), keys...).Result()
	if e != nil {
		respondError(id, -32000, e.Error())
		return
	}
	respond(id, map[string]interface{}{"deleted": n})
}

// redisKeyRename 重命名 key。
func redisKeyRename(id int64, input map[string]interface{}) {
	cid := strFrom(input, "id")
	key := rawStrFrom(input, "key")
	newKey := rawStrFrom(input, "newKey")
	if cid == "" || key == "" || newKey == "" {
		respondError(id, -32602, "缺少 id / key / newKey")
		return
	}
	_, conn, err := openSaved(cid)
	if err != nil {
		respondError(id, -32000, err.Error())
		return
	}
	defer conn.close()
	if conn.redis == nil {
		respondError(id, -32000, "非 Redis 连接")
		return
	}
	if e := conn.redis.Rename(redisCtx(), key, newKey).Err(); e != nil {
		respondError(id, -32000, e.Error())
		return
	}
	respond(id, map[string]interface{}{"ok": true, "key": newKey})
}

// redisKeyExpire 设置 TTL。seconds<0 表示 PERSIST（永不过期）。
func redisKeyExpire(id int64, input map[string]interface{}) {
	cid := strFrom(input, "id")
	key := rawStrFrom(input, "key")
	seconds := intFrom(input, "seconds", -1)
	_, conn, err := openSaved(cid)
	if err != nil {
		respondError(id, -32000, err.Error())
		return
	}
	defer conn.close()
	if conn.redis == nil {
		respondError(id, -32000, "非 Redis 连接")
		return
	}
	ctx := redisCtx()
	var e error
	if seconds < 0 {
		e = conn.redis.Persist(ctx, key).Err()
	} else {
		e = conn.redis.Expire(ctx, key, ttlToDur(seconds)).Err()
	}
	if e != nil {
		respondError(id, -32000, e.Error())
		return
	}
	respond(id, map[string]interface{}{"ok": true, "ttl": seconds})
}

// redisHashFieldSet 设置单个 hash 字段（不重置 key 过期时间）。
func redisHashFieldSet(id int64, input map[string]interface{}) {
	cid := strFrom(input, "id")
	key := rawStrFrom(input, "key")
	field := rawStrFrom(input, "field")
	value := rawStrFrom(input, "value")
	if cid == "" || key == "" || field == "" {
		respondError(id, -32602, "缺少 id / key / field")
		return
	}
	_, conn, err := openSaved(cid)
	if err != nil {
		respondError(id, -32000, err.Error())
		return
	}
	defer conn.close()
	if conn.redis == nil {
		respondError(id, -32000, "非 Redis 连接")
		return
	}
	if e := conn.redis.HSet(redisCtx(), key, field, value).Err(); e != nil {
		respondError(id, -32000, e.Error())
		return
	}
	respond(id, map[string]interface{}{"ok": true})
}

// redisHashFieldDel 删除单个 hash 字段。
func redisHashFieldDel(id int64, input map[string]interface{}) {
	cid := strFrom(input, "id")
	key := rawStrFrom(input, "key")
	field := rawStrFrom(input, "field")
	if cid == "" || key == "" || field == "" {
		respondError(id, -32602, "缺少 id / key / field")
		return
	}
	_, conn, err := openSaved(cid)
	if err != nil {
		respondError(id, -32000, err.Error())
		return
	}
	defer conn.close()
	if conn.redis == nil {
		respondError(id, -32000, "非 Redis 连接")
		return
	}
	n, e := conn.redis.HDel(redisCtx(), key, field).Result()
	if e != nil {
		respondError(id, -32000, e.Error())
		return
	}
	respond(id, map[string]interface{}{"ok": true, "deleted": n})
}

// redisListPush 往 list 头/尾追加一个元素。
func redisListPush(id int64, input map[string]interface{}) {
	cid := strFrom(input, "id")
	key := rawStrFrom(input, "key")
	value := rawStrFrom(input, "value")
	where := strings.ToLower(strFrom(input, "where"))
	if cid == "" || key == "" || value == "" {
		respondError(id, -32602, "缺少 id / key / value")
		return
	}
	_, conn, err := openSaved(cid)
	if err != nil {
		respondError(id, -32000, err.Error())
		return
	}
	defer conn.close()
	if conn.redis == nil {
		respondError(id, -32000, "非 Redis 连接")
		return
	}
	ctx := redisCtx()
	var e error
	if where == "head" {
		e = conn.redis.LPush(ctx, key, value).Err()
	} else {
		e = conn.redis.RPush(ctx, key, value).Err()
	}
	if e != nil {
		respondError(id, -32000, e.Error())
		return
	}
	respond(id, map[string]interface{}{"ok": true})
}

// redisListElemDel 删除 list 中所有等于 value 的元素（LREM count=0）。
func redisListElemDel(id int64, input map[string]interface{}) {
	cid := strFrom(input, "id")
	key := rawStrFrom(input, "key")
	value := rawStrFrom(input, "value")
	if cid == "" || key == "" || value == "" {
		respondError(id, -32602, "缺少 id / key / value")
		return
	}
	_, conn, err := openSaved(cid)
	if err != nil {
		respondError(id, -32000, err.Error())
		return
	}
	defer conn.close()
	if conn.redis == nil {
		respondError(id, -32000, "非 Redis 连接")
		return
	}
	n, e := conn.redis.LRem(redisCtx(), key, 0, value).Result()
	if e != nil {
		respondError(id, -32000, e.Error())
		return
	}
	respond(id, map[string]interface{}{"ok": true, "deleted": n})
}

// redisSetMemberAdd 往 set 添加一个成员。
func redisSetMemberAdd(id int64, input map[string]interface{}) {
	cid := strFrom(input, "id")
	key := rawStrFrom(input, "key")
	member := rawStrFrom(input, "member")
	if cid == "" || key == "" || member == "" {
		respondError(id, -32602, "缺少 id / key / member")
		return
	}
	_, conn, err := openSaved(cid)
	if err != nil {
		respondError(id, -32000, err.Error())
		return
	}
	defer conn.close()
	if conn.redis == nil {
		respondError(id, -32000, "非 Redis 连接")
		return
	}
	n, e := conn.redis.SAdd(redisCtx(), key, member).Result()
	if e != nil {
		respondError(id, -32000, e.Error())
		return
	}
	respond(id, map[string]interface{}{"ok": true, "added": n})
}

// redisSetMemberDel 从 set 删除一个成员。
func redisSetMemberDel(id int64, input map[string]interface{}) {
	cid := strFrom(input, "id")
	key := rawStrFrom(input, "key")
	member := rawStrFrom(input, "member")
	if cid == "" || key == "" || member == "" {
		respondError(id, -32602, "缺少 id / key / member")
		return
	}
	_, conn, err := openSaved(cid)
	if err != nil {
		respondError(id, -32000, err.Error())
		return
	}
	defer conn.close()
	if conn.redis == nil {
		respondError(id, -32000, "非 Redis 连接")
		return
	}
	n, e := conn.redis.SRem(redisCtx(), key, member).Result()
	if e != nil {
		respondError(id, -32000, e.Error())
		return
	}
	respond(id, map[string]interface{}{"ok": true, "deleted": n})
}

// redisZSetMemberSet 设置/更新 zset 一个成员的 score。
func redisZSetMemberSet(id int64, input map[string]interface{}) {
	cid := strFrom(input, "id")
	key := rawStrFrom(input, "key")
	member := rawStrFrom(input, "member")
	if cid == "" || key == "" || member == "" {
		respondError(id, -32602, "缺少 id / key / member")
		return
	}
	score, _ := strconv.ParseFloat(strFrom(input, "score"), 64)
	_, conn, err := openSaved(cid)
	if err != nil {
		respondError(id, -32000, err.Error())
		return
	}
	defer conn.close()
	if conn.redis == nil {
		respondError(id, -32000, "非 Redis 连接")
		return
	}
	if e := conn.redis.ZAdd(redisCtx(), key, redis.Z{Score: score, Member: member}).Err(); e != nil {
		respondError(id, -32000, e.Error())
		return
	}
	respond(id, map[string]interface{}{"ok": true})
}

// redisZSetMemberDel 从 zset 删除一个成员。
func redisZSetMemberDel(id int64, input map[string]interface{}) {
	cid := strFrom(input, "id")
	key := rawStrFrom(input, "key")
	member := rawStrFrom(input, "member")
	if cid == "" || key == "" || member == "" {
		respondError(id, -32602, "缺少 id / key / member")
		return
	}
	_, conn, err := openSaved(cid)
	if err != nil {
		respondError(id, -32000, err.Error())
		return
	}
	defer conn.close()
	if conn.redis == nil {
		respondError(id, -32000, "非 Redis 连接")
		return
	}
	n, e := conn.redis.ZRem(redisCtx(), key, member).Result()
	if e != nil {
		respondError(id, -32000, e.Error())
		return
	}
	respond(id, map[string]interface{}{"ok": true, "deleted": n})
}
