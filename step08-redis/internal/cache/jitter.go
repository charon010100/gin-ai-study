package cache

import (
	"math/rand"
	"time"
)

// Jitter 给 TTL 加 ±10% 的随机抖动。
//
// 要解决的问题叫「缓存雪崩」：
//
//	一批 key 在同一时刻写入（比如服务启动后处理的第一批请求），
//	又设置了相同的 TTL，于是它们会在同一时刻集体过期。
//	那一瞬间所有请求同时未命中，一起打到数据库上。
//	如果数据库扛不住这个瞬时峰值，整个服务就雪崩了。
//
// 加随机抖动把过期时间打散成一片，峰值被摊平。
// 这是成本最低、收益最直接的一个缓存防护措施 —— 只需一行代码。
//
// 为什么不直接用 math/rand 的全局 Shuffle 之类：
// 只需要一点随机偏移，用 Int63n 足够，且不分配内存。
func Jitter(ttl time.Duration) time.Duration {
	if ttl <= 0 {
		return ttl
	}
	// 抖动幅度取 10%。
	delta := int64(float64(ttl) * 0.1)
	if delta <= 0 {
		return ttl
	}
	// 范围 [-delta, +delta]。
	// 注意 Go 1.20+ 的 math/rand 顶层函数自带并发安全，不需要再加锁。
	offset := rand.Int63n(2*delta+1) - delta
	jittered := ttl + time.Duration(offset)
	if jittered <= 0 {
		return ttl
	}
	return jittered
}
