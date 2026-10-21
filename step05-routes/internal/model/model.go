// Package model 存放跨层共享的数据结构。
package model

// Post 是一篇文章。
type Post struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Slug     string `json:"slug"`
	Summary  string `json:"summary"`
	Views    int64  `json:"views"`
	CreateAt string `json:"created_at"`
}

// SiteMeta 是站点基础信息，供模板渲染使用。
type SiteMeta struct {
	Title    string
	Subtitle string
}
