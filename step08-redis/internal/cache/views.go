// 本文件属于 cache 包，实现「浏览量 Redis 计数 + 定时批量回写 DB」。
//
// 为什么不能每次访问就 UPDATE posts SET views = views + 1：
//
//   - 每来一个访问就产生一次写事务。热门文章被爬虫或分享刷量时，
//     一个只读接口会被放大成写瓶颈。
//   - SQLite 的写锁是整库级的，这个影响会直接表现为「全站变慢」。
//   - MySQL 上同理：高频更新同一行会争同一把行锁。
//     而浏览量恰恰是最不需要精确的那个数字。
//
// 正解：Redis 的 INCR/HINCRBY 是原子的内存操作，把 10000 次访问收敛成
// 10000 次自增，再由后台任务每分钟批量回写一次 —— 数据库只承担几十次写。
package cache

import (
	"context"
	"log"
	"strconv"
	"sync"
	"time"
)

// viewsHashKey 是「待回写增量」的 hash key。
// 字段为文章 ID，值为尚未写进 DB 的增量。
func (c *Client) viewsHashKey() string {
	return c.Key("views", "pending")
}

// IncrViews 给某篇文章的浏览量 +1，返回当前待回写的增量。
//
// 返回值里的 bool 表示是否成功。Redis 不可用时返回 false，
// 调用方应该「跳过计数」而不是报错 —— 浏览量丢了不影响用户。
func (c *Client) IncrViews(ctx context.Context, postID uint64) (int64, bool) {
	n, err := c.rdb.HIncrBy(ctx, c.viewsHashKey(), strconv.FormatUint(postID, 10), 1).Result()
	if err != nil {
		logDegraded(err)
		return 0, false
	}
	return n, true
}

// flushViewsScript 做「原子地读出全部并清空」。
//
// 为什么必须用 Lua：
//
//	先 HGETALL 再 DEL —— 两次调用之间进来的 HINCRBY 会丢。
//	先 DEL 再 HGETALL —— 丢得更多。
//
// Lua 脚本在 Redis 里单线程原子执行，保证「读取并清空」不可分割。
// 于是并发进来的自增只会进入下一轮，既不丢也不重复。
const flushViewsScript = `
local data = redis.call('HGETALL', KEYS[1])
if #data == 0 then
  return {}
end
redis.call('DEL', KEYS[1])
return data
`

// ViewsFlushFn 是回写函数：把一批 ID → 增量 写进数据库。
type ViewsFlushFn func(ctx context.Context, deltas map[uint64]int64) error

// ViewsFlusher 定时把 Redis 计数刷进 DB。
type ViewsFlusher struct {
	client  *Client
	flushFn ViewsFlushFn
	every   time.Duration

	mu      sync.Mutex
	running bool
}

// NewViewsFlusher 构造后台回写器。
func NewViewsFlusher(c *Client, fn ViewsFlushFn, every time.Duration) *ViewsFlusher {
	if every <= 0 {
		every = time.Minute
	}
	return &ViewsFlusher{client: c, flushFn: fn, every: every}
}

// Start 启动后台循环，返回停止函数。
//
// 用「返回停止函数」而不是暴露 Stop 方法，是为了让调用方能 defer 它，
// 不必关心内部 goroutine 的生命周期。
func (f *ViewsFlusher) Start(ctx context.Context) (stop func()) {
	f.mu.Lock()
	if f.running {
		f.mu.Unlock()
		return func() {}
	}
	f.running = true
	f.mu.Unlock()

	// 用独立的 cancel 而不是直接复用传入的 ctx：
	// 停止函数需要能主动取消，而传入的 ctx 往往来自 main、生命周期更长。
	runCtx, cancel := context.WithCancel(ctx)

	go func() {
		ticker := time.NewTicker(f.every)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				// 退出前最后刷一次，避免丢掉最近一个周期的计数。
				// 这里用 Background 而不是已被 cancel 的 runCtx，
				// 否则最后这次回写会因为 context 已取消而必然失败。
				f.FlushOnce(context.Background())
				return
			case <-ticker.C:
				f.FlushOnce(runCtx)
			}
		}
	}()

	return func() {
		cancel()
		f.mu.Lock()
		f.running = false
		f.mu.Unlock()
	}
}

// FlushOnce 执行一次回写。
func (f *ViewsFlusher) FlushOnce(ctx context.Context) {
	res, err := f.client.rdb.Eval(ctx, flushViewsScript, []string{f.client.viewsHashKey()}).Result()
	if err != nil {
		// 脚本失败时 DEL 不会执行，计数仍在 Redis 里，下一轮会重试。
		log.Printf("[WARN] 读取浏览量增量失败: %v", err)
		return
	}

	pairs, ok := res.([]any)
	if !ok || len(pairs) == 0 {
		return
	}

	deltas := make(map[uint64]int64, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		idStr, ok1 := pairs[i].(string)
		valStr, ok2 := pairs[i+1].(string)
		if !ok1 || !ok2 {
			continue
		}
		// 用 strconv 而不是 fmt.Sscanf：循环里快一个数量级，
		// 且失败时能区分「格式错误」与「数值溢出」。
		id, err := strconv.ParseUint(idStr, 10, 64)
		if err != nil {
			continue
		}
		delta, err := strconv.ParseInt(valStr, 10, 64)
		if err != nil {
			continue
		}
		if delta > 0 {
			deltas[id] = delta
		}
	}
	if len(deltas) == 0 {
		return
	}

	if err := f.flushFn(ctx, deltas); err != nil {
		// 回写失败的处理策略必须明确，不能含糊。
		//
		// 方案 A：把增量加回 Redis（更精确，但可能重复计数）。
		// 方案 B：丢弃（浏览量是近似指标，少一分钟可以接受）。
		//
		// 这里选 B。浏览量不是钱，不该为它引入补偿逻辑，
		// 更不该让它触发写库重试从而把数据库压垮。
		// 如果这是「订单金额」，答案会完全不同 ——
		// 这就是「按数据性质决定容错策略」的具体样子。
		log.Printf("[WARN] 回写浏览量失败，本批 %d 条增量丢弃: %v", len(deltas), err)
		return
	}
	log.Printf("已回写 %d 篇文章的浏览量", len(deltas))
}
