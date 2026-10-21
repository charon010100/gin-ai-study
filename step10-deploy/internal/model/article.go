// Package model 定义数据表对应的结构体。
//
// 本步只需要一张表：它存在的价值不是业务功能，而是让「迁移」这件事有真实对象可讲。
package model

import "time"

// Article 是一篇文章。
//
// 字段刻意少，但保留了三个部署阶段最容易被忽略的要素：
//   - 主键用自增整数而不是字符串 UUID：InnoDB 的聚簇索引按主键物理排序，
//     随机字符串主键会导致写入时频繁页分裂，数据量大后插入性能明显下降；
//   - CreatedAt 带索引：列表页按时间倒序是最常见查询；
//   - SoftDeletedAt 用 *time.Time 而不是 gorm.DeletedAt：
//     本步不打算用 GORM 的软删除机制，NULL 表示未删除已经够用，
//     引入 DeletedAt 会让「删除」变成语义模糊的操作。
type Article struct {
	ID        uint64     `gorm:"primaryKey;autoIncrement" json:"id"`
	Title     string     `gorm:"type:varchar(200);not null" json:"title"`
	Slug      string     `gorm:"type:varchar(200);not null;uniqueIndex" json:"slug"`
	CreatedAt time.Time  `gorm:"not null;index" json:"created_at"`
	UpdatedAt time.Time  `gorm:"not null" json:"updated_at"`
	DeletedAt *time.Time `json:"-"`
}

// TableName 显式指定表名。
//
// 如果不写，GORM 会按结构体名复数化得到 articles；这里显式写出来是为了
// 让「模型」和「迁移 SQL」的表名在代码里就能对得上。
// 部署阶段最典型的故障之一就是「迁移建的是 article、GORM 查的是 articles」，
// 表现为表不存在，而两边的代码看起来都没错。
func (Article) TableName() string { return "articles" }
