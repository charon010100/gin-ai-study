package service

import (
	"context"
)

// CachePort 是 service 层对缓存的最小依赖。
//
// 关键设计：**接口定义在 service 包里，实现放在 cache 包里**。
//
// 这就是依赖倒置（Dependency Inversion）在 Go 里的样子：
//
//	不是 service → cache（依赖具体实现）
//	而是 service → CachePort ← cache（双方都依赖抽象）
//
// 收益是：测试 PostService 时，可以塞一个 10 行的假实现进去，
// 不启动 Redis、不需要 docker、不依赖网络。如果 service 直接依赖
// *cache.Client，那每个涉及缓存的单测都会变成集成测试。
//
// 注意接口里只有三个方法，而且都不是 Redis 专有的。
// 「最小接口」让假实现几乎零成本 —— 一个 300 行的大接口，
// 没人愿意为它写假实现，最后测试就只剩集成测试。
type CachePort interface {
	// GetJSON 读取并反序列化。第二个返回值表示是否命中。
	GetJSON(ctx context.Context, key string, dst any) (bool, error)
	// SetJSON 写入并设置 TTL。
	SetJSON(ctx context.Context, key string, val any, ttlSeconds int)
	// Del 删除指定 key。
	Del(ctx context.Context, keys ...string)
	// DelByPrefix 按前缀批量失效。
	//
	// 这个方法看起来很「便宜」，实际在生产 Redis 上用 SCAN 实现，
	// 成本很高且可能阻塞。真实项目里应该换成「版本号」方案
	// （见 NOTES 的遗留问题）。这里保留它是为了让缓存一致性
	// 有个可运行的实现，而不是假装问题不存在。
	DelByPrefix(ctx context.Context, prefix string)
}

// NoopCache 是一个空实现，用于「不要缓存」的场景与测试。
//
// 有了它，PostService 可以在两种模式下运行：
//   - 生产：传真实 Redis 实现
//   - 测试 / 本地调试：传 NoopCache
//
// 这就是「依赖接口而不是具体实现」最直接的好处 ——
// 切换成本是一个函数调用，而不是改八个文件。
type NoopCache struct{}

// GetJSON 永远未命中。
func (NoopCache) GetJSON(context.Context, string, any) (bool, error) { return false, nil }

// SetJSON 什么都不做。
func (NoopCache) SetJSON(context.Context, string, any, int) {}

// Del 什么都不做。
func (NoopCache) Del(context.Context, ...string) {}

// DelByPrefix 什么都不做。
func (NoopCache) DelByPrefix(context.Context, string) {}

// 编译期断言：NoopCache 满足接口。
var _ CachePort = NoopCache{}
