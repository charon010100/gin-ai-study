package service

import (
	"context"
	"strings"
	"time"

	"ginlesson/step09/internal/model"
	"ginlesson/step09/internal/repository"
)

// PostService 承载文章相关的业务规则。
//
// 这个类型是 Step 9 的核心示范：所有「规则」集中在这里，
// 于是 handler 只管 HTTP，repository 只管 SQL。
type PostService struct {
	posts repository.PostRepo
	cache CachePort
	// maxPageSize 是业务策略，不是 HTTP 参数。
	// 它由配置注入而不是写死，因为这个值会随部署环境变化（测试想放大页容量）。
	maxPageSize int
}

// CreatePostInput 是创建文章的入参。
//
// 为什么用独立的 Input 结构而不是直接收 *model.Post：
// 后者会让调用方有能力设置 ID、CreatedAt、DeletedAt 这些
// **不该由外部决定**的字段。用 Input 就把可设置的字段收敛成一个白名单。
type CreatePostInput struct {
	Title   string
	Slug    string
	Summary string
	Content string
	// Status 允许为空（默认 published），但不允许任意字符串。
	Status string
}

// NewPostService 构造文章服务。
func NewPostService(posts repository.PostRepo, cache CachePort, maxPageSize int) *PostService {
	if maxPageSize <= 0 {
		maxPageSize = 50
	}
	return &PostService{posts: posts, cache: cache, maxPageSize: maxPageSize}
}

// ListPublic 返回前台可见的文章列表。
//
// 关键点：状态被**强制**为 published，调用方无法覆盖。
//
// 这就是「规则应该放在哪一层」的答案。如果这个强制写在 handler 里，
// 那么将来加一个后台接口（需要看草稿）时，你会在 handler 里再加个参数，
// 然后某天发现参数拼错、草稿泄露。规则留在 service，入口只有两个：
// ListPublic（永远 published）和 ListForAdmin（可以指定）。没法拼错。
func (s *PostService) ListPublic(ctx context.Context, page, pageSize int, keyword string) (*PostListResult, error) {
	return s.list(ctx, page, pageSize, keyword, model.PostPublished)
}

// ListForAdmin 返回后台列表，允许指定状态。
//
// 注意它仍然做校验：status 为空时看全部，非法值直接拒绝，
// 而不是「非法就当 published」——静默纠错会让调用方以为参数生效了。
func (s *PostService) ListForAdmin(ctx context.Context, page, pageSize int, keyword, status string) (*PostListResult, error) {
	var st model.PostStatus
	if status != "" {
		st = model.PostStatus(status)
		if !st.Valid() {
			return nil, ErrInvalidStatus()
		}
	}
	return s.list(ctx, page, pageSize, keyword, st)
}

func (s *PostService) list(ctx context.Context, page, pageSize int, keyword string, status model.PostStatus) (*PostListResult, error) {
	page, pageSize, err := s.normalizePaging(page, pageSize)
	if err != nil {
		return nil, err
	}

	rows, total, err := s.posts.ListPosts(ctx, repository.ListQuery{
		Page: page, PageSize: pageSize,
		Keyword: strings.TrimSpace(keyword),
		Status:  status,
	})
	if err != nil {
		return nil, FromRepoError(err)
	}

	// 在这里就把 repository 的结果转成 service 自己的 DTO。
	//
	// 为什么不在 handler 里转：
	// 那样 handler 就必须 import repository，于是 HTTP 层知道
	// 「数据层有个叫 PostWithCount 的结构」——分层就漏了。
	// 让 service 做翻译，handler 只认 service 暴露的类型。
	items := make([]PostItem, 0, len(rows))
	for _, r := range rows {
		items = append(items, PostItem{
			ID:           r.ID,
			Title:        r.Title,
			Slug:         r.Slug,
			Summary:      r.Summary,
			Status:       string(r.Status),
			Views:        r.Views,
			CommentCount: r.CommentCount,
			CreatedAt:    r.CreatedAt,
		})
	}

	return &PostListResult{
		Items: items, Total: total, Page: page, PageSize: pageSize,
	}, nil
}

// PostItem 是列表项的输出结构。
//
// 它属于 service 层的「对外契约」：既不暴露数据库字段（DeletedAt），
// 也不包含 HTTP 细节（json tag 由 handler 那一层的 DTO 负责）。
type PostItem struct {
	ID           uint64
	Title        string
	Slug         string
	Summary      string
	Status       string
	Views        int64
	CommentCount int64
	CreatedAt    time.Time
}

// PostListResult 是列表结果。
//
// 为什么要有它，而不是直接返回 (rows, total, error)：
// 因为分页参数经过归一化（page/pageSize 可能被改写），
// 调用方需要知道**最终生效的值**，否则前端算出的总页数会和实际不符。
type PostListResult struct {
	Items    []PostItem
	Total    int64
	Page     int
	PageSize int
}

// normalizePaging 校验并归一化分页参数。
//
// 上限用「拒绝」而不是「静默夹取」：
//   - 静默夹取会让调用方以为拿到了 1000 条，实际只有 50，
//     分页计算直接错位（这比报错更难排查）
//   - 明确报 400 让调用方立刻知道参数不对
//
// 这跟 Step 7 的做法不同，是有意的：那里可选、这里显式。
// 真实项目应该统一成一种，并在 API 文档里写清楚。
func (s *PostService) normalizePaging(page, pageSize int) (int, int, error) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 10
	}
	if pageSize > s.maxPageSize {
		return 0, 0, ErrInvalidPageSize()
	}
	return page, pageSize, nil
}

// GetBySlug 按 slug 查文章。
//
// 这里没做「只允许 published」的限制，因为详情接口要供后台预览草稿用。
// 限制放在 handler 层？不 —— 那又回到「规则散落」了。
// 正确做法是分两个方法：GetPublicBySlug 与 GetForAdminBySlug。
// 本步为了控制代码量只保留一个，并在 NOTES 里标为待改进项。
func (s *PostService) GetBySlug(ctx context.Context, slug string) (*model.Post, error) {
	if strings.TrimSpace(slug) == "" {
		return nil, ErrPostNotFound()
	}
	p, err := s.posts.GetPostBySlug(ctx, slug)
	if err != nil {
		return nil, FromRepoError(err)
	}
	return p, nil
}

// Create 创建文章。
func (s *PostService) Create(ctx context.Context, in CreatePostInput) (*model.Post, error) {
	// 参数在业务层的校验。
	//
	// 注意与 handler 里 binding tag 的区别：binding 校验的是
	// 「格式」（非空、长度），这里校验的是「业务含义」。
	// 两者都需要，不能互相替代 —— binding 挡不住「status=deleted」这种
	// 格式合法但业务非法的输入。
	if strings.TrimSpace(in.Title) == "" {
		return nil, NewError(CodeInvalidArgument, "标题不能为空", nil)
	}
	if strings.TrimSpace(in.Slug) == "" {
		return nil, NewError(CodeInvalidArgument, "slug 不能为空", nil)
	}

	status := model.PostStatus(in.Status)
	if status == "" {
		status = model.PostPublished
	}
	if !status.Valid() {
		return nil, ErrInvalidStatus()
	}

	p := &model.Post{
		Title:   strings.TrimSpace(in.Title),
		Slug:    strings.TrimSpace(in.Slug),
		Summary: in.Summary,
		Content: in.Content,
		Status:  status,
	}

	if err := s.posts.CreatePost(ctx, p); err != nil {
		return nil, FromRepoError(err)
	}
	// 新文章不需要删自己的详情缓存（还没人读过），
	// 但列表缓存会失效 —— 这里留了一个刻意的简化，
	// 见 service/cache.go 里 CachePort 的说明。
	s.cache.DelByPrefix(ctx, "postlist")
	return p, nil
}

// Delete 删除文章（软删除）。
func (s *PostService) Delete(ctx context.Context, slug string) error {
	if err := s.posts.DeletePost(ctx, slug); err != nil {
		return FromRepoError(err)
	}
	s.cache.Del(ctx, "post:"+slug)
	s.cache.DelByPrefix(ctx, "postlist")
	return nil
}
