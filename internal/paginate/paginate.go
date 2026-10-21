// Package paginate 提供统一的分页参数钳制与响应封装，供 admin / portal 等各面复用，
// 避免各 handler 各自散装解析参数、手写 gin.H{"total","items"} 造成的行为不一致。
package paginate

import "strconv"

// Params 解析并夹逼分页参数，防止负数/超大值直传数据库（负 limit 在 GORM 里等于不限制、会拉全表；
// 负 offset 会报错）。limit：非法/非正回 def，超过 max 夹到 max；offset：非法/负数回 0。
func Params(limitStr, offsetStr string, def, max int) (limit, offset int) {
	limit, err := strconv.Atoi(limitStr)
	if err != nil || limit <= 0 {
		limit = def
	}
	if limit > max {
		limit = max
	}
	offset, err = strconv.Atoi(offsetStr)
	if err != nil || offset < 0 {
		offset = 0
	}
	return limit, offset
}

// PageResult 统一分页响应结构。total/items 键名与旧散装 gin.H 完全一致（老前端零改动即兼容），
// page/page_size/total_pages 为新增的自描述增量字段，老前端忽略、将来需要时可选消费。
type PageResult[T any] struct {
	Total      int64 `json:"total"`
	Items      []T   `json:"items"`
	Page       int   `json:"page"`        // 1 起始的当前页
	PageSize   int   `json:"page_size"`   // 每页条数（即钳制后的 limit）
	TotalPages int   `json:"total_pages"` // 总页数，向上取整，至少 1
}

// New 由 limit/offset 语义构造统一分页响应：page = offset/limit + 1，total_pages = ceil(total/limit)。
// items 为 nil 时归一为空切片，保证 JSON 输出 [] 而非 null。limit 应已由 Params 钳制为正数，
// 这里对 <=0 兜底防除零。
func New[T any](items []T, total int64, limit, offset int) PageResult[T] {
	if items == nil {
		items = []T{}
	}
	if limit <= 0 {
		limit = 1
	}
	if offset < 0 {
		offset = 0
	}
	totalPages := int((total + int64(limit) - 1) / int64(limit))
	if totalPages < 1 {
		totalPages = 1
	}
	return PageResult[T]{
		Total:      total,
		Items:      items,
		Page:       offset/limit + 1,
		PageSize:   limit,
		TotalPages: totalPages,
	}
}
