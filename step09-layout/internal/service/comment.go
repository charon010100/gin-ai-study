package service

import (
	"context"
	"strings"

	"ginlesson/step09/internal/model"
	"ginlesson/step09/internal/repository"
)

// CommentService 承载评论相关的业务规则。
type CommentService struct {
	comments repository.CommentRepo
	// posts 依赖它有两个原因：
	//   1. 提交评论前要确认文章存在、且状态允许评论（业务规则）
	//   2. 审核通过时要维护文章上的冗余评论计数（需要跨表写）
	posts repository.PostRepo
	// tx 提供事务能力。
	//
	// 注意 service 拿到的是「能执行事务」这个抽象，
	// 而不是 *gorm.DB。它不知道事务怎么实现，只知道
	// 「把这几步包起来，出错就整体回滚」。
	tx repository.TxManager
}

// CreateCommentInput 是提交评论的入参。
//
// 注意：**没有 Status 字段**。
//
// 这是有意的防呆设计。如果允许外部传 status，
// 那么「先审后发」就只靠 service 里一行赋值来保证，
// 而任何人（包括半年后的你）都可能顺手把它改掉。
// 让这个字段根本不存在，是成本最低、最不可能被破坏的防线。
type CreateCommentInput struct {
	Nickname string
	Content  string
}

// NewCommentService 构造评论服务。
func NewCommentService(comments repository.CommentRepo, posts repository.PostRepo, tx repository.TxManager) *CommentService {
	return &CommentService{comments: comments, posts: posts, tx: tx}
}

// ListApproved 返回某篇文章已通过审核的评论（前台接口）。
func (s *CommentService) ListApproved(ctx context.Context, slug string) ([]model.Comment, error) {
	p, err := s.posts.GetPostBySlug(ctx, slug)
	if err != nil {
		return nil, FromRepoError(err)
	}
	// 状态在这里写死为 approved，不接受调用方指定。
	// 否则 `?status=pending` 就能看到待审内容。
	return s.comments.ListComments(ctx, p.ID, model.CommentApproved)
}

// Submit 提交一条评论。
func (s *CommentService) Submit(ctx context.Context, slug string, in CreateCommentInput) (*model.Comment, error) {
	if strings.TrimSpace(in.Nickname) == "" {
		return nil, NewError(CodeInvalidArgument, "昵称不能为空", nil)
	}
	if len([]rune(strings.TrimSpace(in.Content))) < 2 {
		return nil, NewError(CodeInvalidArgument, "评论内容太短", nil)
	}

	p, err := s.posts.GetPostBySlug(ctx, slug)
	if err != nil {
		return nil, FromRepoError(err)
	}

	// 业务规则：草稿文章的评论功能视为关闭。
	//
	// 这条规则依赖「文章状态」和「评论」两个概念，
	// 放在 handler 里会变成「先查文章再判断」，而那又是两次库操作、
	// 且没有任何地方能阻止别的入口绕过它。
	// 放在 service 是它唯一的归属。
	if p.Status != model.PostPublished {
		return nil, ErrCommentClosed()
	}

	c := &model.Comment{
		PostID:   p.ID,
		Nickname: strings.TrimSpace(in.Nickname),
		Content:  in.Content,
		// 强制 pending。这一行就是「先审后发」的落点。
		Status: model.CommentPending,
	}

	if err := s.comments.CreateComment(ctx, c); err != nil {
		return nil, FromRepoError(err)
	}
	return c, nil
}

// Approve 审核通过一条评论。
//
// 这个方法是本步最重要的示例：**事务边界在 service 层**。
//
// 为什么必须在一个事务里：
//
//	① 把评论状态从 pending 改成 approved
//	② 把文章的 comment_count 加 1
//
// 如果①成功②失败，就会出现「评论已通过，但文章上显示评论数 0」。
// 这种不一致不会报错，也没有日志，只会让读者觉得"你这站坏了吧"。
// 而且它无法自愈 —— 除非有人手动改数据库。
//
// 为什么不放在 repository：
//
//	repository 只应该回答「怎么存取一条评论」，
//	它不知道「通过审核还要维护计数」这个业务规则。
//	如果让它知道，那么「计数规则变了」（比如算上 spam）
//	就得改数据层，而那是最不该频繁变的地方。
//
// 为什么不放在 handler：
//
//	handler 是 HTTP 适配层，它连数据库长什么样都不知道。
//	而且「通过审核」这个动作将来可能来自命令行、定时任务、
//	甚至消息队列，它们都没有 gin.Context。
func (s *CommentService) Approve(ctx context.Context, id uint64) error {
	// WithTx 的语义：fn 返回 error 则整体回滚，返回 nil 则提交。
	// 注意传给 fn 的 ctx 携带了事务，repository 的方法会自动用它。
	return s.tx.WithTx(ctx, func(ctx context.Context) error {
		// ① 先读出评论，目的是拿到 postID 和确认当前状态。
		c, err := s.comments.GetCommentByID(ctx, id)
		if err != nil {
			return FromRepoError(err)
		}

		// ② 状态流转必须从 pending 出发。
		// 已通过 / 已拒绝 的评论再次审核应当报错，而不是静默成功。
		if c.Status != model.CommentPending {
			return ErrCommentNotFound()
		}

		// ③ 带条件的更新。第二个返回值表示「真的改到了行」。
		//
		// 为什么不用 c.Status = ... 然后 Save(c)：
		// Save 会写回整行。如果两个管理员同时点「通过」，
		// 后写的那个会覆盖前一个的所有字段（包括它可能刚改过的内容）。
		// 带条件的 UPDATE 只动 status 一列，且第二次会更新 0 行 —— 天然幂等。
		updated, err := s.comments.UpdateCommentStatus(ctx, id, model.CommentPending, model.CommentApproved)
		if err != nil {
			return FromRepoError(err)
		}
		if !updated {
			// 到了这里说明：在我们读出来之后、更新之前，
			// 有另一个请求把状态改掉了。并发下这是正常情况。
			return ErrCommentNotFound()
		}

		// ④ 维护冗余计数。
		//
		// 这一步失败会返回 error，于是 WithTx 会回滚 ③。
		// 结果就是「要么评论通过了且计数加上了，要么两者都没发生」。
		//
		// 这就是事务要解决的全部问题：让多个写操作的**可见性**保持原子，
		// 而不是"尽量都成功"。
		if err := s.posts.IncrCommentCount(ctx, c.PostID, 1); err != nil {
			return FromRepoError(err)
		}

		return nil
	})
}

// ListPending 返回待审队列（后台接口）。
func (s *CommentService) ListPending(ctx context.Context, limit int) ([]model.Comment, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	return s.comments.ListCommentsByStatus(ctx, model.CommentPending, limit)
}
