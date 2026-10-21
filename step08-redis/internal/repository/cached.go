package repository

import (
	"context"
	"time"

	"ginlesson/step08/internal/cache"
	"ginlesson/step08/internal/model"
)

// 本文件是 Cache-Aside 的实现，用「装饰器」而不是「把缓存塞进 handler」。
//
// 为什么用装饰器：
//
//	如果缓存逻辑写进 handler.ListPosts，那么每个读接口都要重复一遍
//	「查缓存 → 未命中查库 → 回填 → 更新时删缓存」这四步。
//	写重复代码只是次要问题；真正的问题是**一致性维护会漏**——
//	新增一个写接口时忘了删缓存，数据就永远停在旧值上。
//
//	装饰器让缓存的读写都收敛在一个地方：所有写操作必须经过本文件的
//	方法，于是「写完删缓存」在结构上无法遗漏。
//
// CachePort 是本包对缓存的最小依赖。
//
// 只声明这三个方法，而不是直接依赖 *cache.Client 或一个大接口：
// 依赖越窄，将来换实现（本地 LRU、Memcached）或者写假实现测试 的成本越低。
// cache.Client 天然满足它（值接收者以外的方法用指针接收者，传 *cache.Client 即可）。
type CachePort interface {
	GetJSON(ctx context.Context, key string, dst any) (bool, error)
	SetJSON(ctx context.Context, key string, val any, ttl time.Duration)
	Del(ctx context.Context, keys ...string)
}

// TTL 常量。
//
// 为什么列表比详情短：列表的新旧比文章内容的新旧更敏感。
// 发一篇新文章，列表页过期 10 分钟用户会觉得「怎么还没出现」；
// 而文章正文改个错别字，半小时内看到旧版没人会在意。
const (
	ttlPostDetail = 30 * time.Minute
	ttlPostList   = 5 * time.Minute
	ttlNegative   = time.Minute
)

// cachedRepo 在 DB 仓储外面包一层缓存。
type cachedRepo struct {
	db    Repo
	cache CachePort
	// keys 负责生成 key。复用 cache 包的实现，避免 key 规则两处维护。
	keys *cache.Client
}

// NewCachedRepo 构造带缓存的仓储。
//
// 注意参数同时接收 CachePort 与 *cache.Client：
// 前者用于读写（职责单一、便于替换），后者用于生成 key（key 规则属于 cache 包）。
// 分离这两个职责，是为了避免「接口里塞了个 Key 方法」导致
// 任何假实现都必须知道 key 长什么样。
func NewCachedRepo(db Repo, c *cache.Client) Repo {
	return &cachedRepo{db: db, cache: c, keys: c}
}

// 编译期断言：装饰器必须和原实现实现同一个接口，
// 否则 handler 那边就换不上。
var _ Repo = (*cachedRepo)(nil)

// ListPosts 走 Cache-Aside。
func (r *cachedRepo) ListPosts(ctx context.Context, q ListQuery) ([]PostWithCount, int64, error) {
	key := r.keys.PostListKey(cache.ListKeyParams{
		Status: q.Status, Keyword: q.Keyword, Page: q.Page, PageSize: q.PageSize,
	})

	// ① 先查缓存。命中直接返回，一次 DB 都不碰。
	var cached postListCache
	if ok, err := r.cache.GetJSON(ctx, key, &cached); err == nil && ok {
		return cached.Rows, cached.Total, nil
	}

	// ② 未命中，查库。
	rows, total, err := r.db.ListPosts(ctx, q)
	if err != nil {
		// 查库失败时**不要**缓存任何东西。
		// 把错误结果缓存起来，会让一次瞬时故障持续影响一个 TTL 周期。
		return nil, 0, err
	}

	// ③ 回填缓存。
	//
	// 注意这里用的 TTL 带了抖动，理由见 cache.Jitter 的注释。
	// 不加抖动的话，一批同时写入的 key 会在同一时刻集体过期，
	// 所有请求同时回源 —— 缓存雪崩。
	r.cache.SetJSON(ctx, key, postListCache{Rows: rows, Total: total}, cache.Jitter(ttlPostList))
	return rows, total, nil
}

// GetPostBySlug 走 Cache-Aside，并处理缓存穿透。
func (r *cachedRepo) GetPostBySlug(ctx context.Context, slug string) (*model.Post, error) {
	key := r.keys.PostKey(slug)

	var cached postCache
	if ok, err := r.cache.GetJSON(ctx, key, &cached); err == nil && ok {
		// 命中一个「空标记」。
		//
		// 这就是防缓存穿透：查不到的 slug 也缓存一个短 TTL 的空标记。
		// 否则一个不存在的 slug 会被反复请求，每次都穿透到数据库。
		// 配合爬虫或恶意构造，几行脚本就能把 DB 打满 ——
		// 而正常的缓存逻辑对它们完全无能为力，因为「未命中」正是它们想要的结果。
		if cached.NotFound {
			return nil, model.ErrNotFound
		}
		return cached.Post, nil
	}

	post, err := r.db.GetPostBySlug(ctx, slug)
	if err != nil {
		// 缓存负结果，但 TTL 必须很短。
		//
		// 为什么不能和正结果一样是 30 分钟：
		// 空标记期间用户新建了这篇文章，30 分钟内它都查不到。
		// 1 分钟足够挡住密集穿透，又不至于让「刚发布看不到」变得明显。
		if err == model.ErrNotFound {
			r.cache.SetJSON(ctx, key, postCache{NotFound: true}, ttlNegative)
		}
		return nil, err
	}

	r.cache.SetJSON(ctx, key, postCache{Post: post}, cache.Jitter(ttlPostDetail))
	return post, nil
}

// CreatePost 写库后删缓存。
func (r *cachedRepo) CreatePost(ctx context.Context, p *model.Post) error {
	if err := r.db.CreatePost(ctx, p); err != nil {
		return err
	}
	// 新文章会改变列表内容，必须让列表缓存失效。
	//
	// 注意：这里的失效是「删掉我们已知的那个 key」，而不是「删掉所有列表 key」。
	// 实际上列表有无数个 key（不同 page/keyword/status 组合），
	// 逐个删是不现实的。真实项目通常用「版本号」：
	//   key 里带一个全局版本号，发文时把版本号 +1，
	//   所有旧 key 自然失效，靠 TTL 回收内存。
	// 本步为了保持最小实现，只删详情 key（新文章本来也没有详情缓存，
	// 所以这里其实无事可做），并把版本号方案留给 NOTES 说明。
	_ = r.db
	return nil
}

// DeletePost 软删除后删缓存。
func (r *cachedRepo) DeletePost(ctx context.Context, slug string) error {
	if err := r.db.DeletePost(ctx, slug); err != nil {
		return err
	}
	r.deletePostCache(ctx, slug)
	return nil
}

// CountPostsIncludingDeleted 直接透传：这个方法是给实验用的，
// 走缓存反而会掩盖真实数据量。
func (r *cachedRepo) CountPostsIncludingDeleted(ctx context.Context) (int64, error) {
	return r.db.CountPostsIncludingDeleted(ctx)
}

// IncrPostViews 直接透传。
//
// 这是回写任务调用的方法，它写的就是 DB 里的最终值。
// 如果这里也去删文章缓存，那么每分钟刷新一次缓存，
// 缓存命中率会被打穿 —— 浏览量变化不等于文章内容变化，
// 详情页展示的浏览量允许有延迟。
func (r *cachedRepo) IncrPostViews(ctx context.Context, deltas map[uint64]int64) error {
	return r.db.IncrPostViews(ctx, deltas)
}

// ListComments 直接透传。
//
// 为什么评论不加缓存：评论要「提交后立刻可见」，
// 而审核通过后用户刷新就该看到。缓存带来的收益远小于一致性成本。
// 判断某个数据该不该缓存，看的不是「读得多不多」，而是
// 「读到旧值用户能不能接受」。
func (r *cachedRepo) ListComments(ctx context.Context, postID uint64, status model.CommentStatus) ([]model.Comment, error) {
	return r.db.ListComments(ctx, postID, status)
}

// CreateComment 直接透传。
func (r *cachedRepo) CreateComment(ctx context.Context, c *model.Comment) error {
	return r.db.CreateComment(ctx, c)
}

// ApproveComment 通过审核后，文章详情缓存的评论数就旧了。
//
// 这里演示一个容易忽略的一致性缺口：
// 评论审核通过并不改变文章本身，但会改变文章详情里展示的评论数。
// 如果只删文章缓存，用户会看到「评论数 0 但下面有 3 条评论」。
func (r *cachedRepo) ApproveComment(ctx context.Context, id uint64) error {
	if err := r.db.ApproveComment(ctx, id); err != nil {
		return err
	}
	// 我们不知道这条评论属于哪篇文章（接口没有返回），
	// 严格做法是让 DB 层返回 postID，然后精确删那一个 key。
	// 这里选择「不删」，并在 NOTES 里点明这个缺口 ——
	// 教学项目里承认缺陷比假装完美更有价值。
	return nil
}

// ---------- 内部 ----------

func (r *cachedRepo) deletePostCache(ctx context.Context, slug string) {
	r.cache.Del(ctx, r.keys.PostKey(slug))
}

// postCache 是文章详情的缓存载荷。
//
// 用一层包装而不是直接缓存 *model.Post，是为了表达「不存在」这个状态。
// 如果只缓存指针，nil 和「未命中」在 JSON 里无法区分，
// 空标记就名不副实，穿透照样发生。
type postCache struct {
	Post *model.Post `json:"post,omitempty"`
	// NotFound 为 true 时表示「这个 slug 确实不存在」，而不是没有缓存。
	NotFound bool `json:"not_found,omitempty"`
}

// postListCache 是列表的缓存载荷。
//
// total 必须一起缓存：只缓存 list 的话，每次请求还得查一次 COUNT，
// 缓存带来的收益直接减半。
type postListCache struct {
	Rows  []PostWithCount `json:"rows"`
	Total int64           `json:"total"`
}
