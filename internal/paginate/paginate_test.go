package paginate

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestParams 验证分页参数夹逼：limit 落入 [1,max]、非法/非正回默认，offset 不小于 0。
func TestParams(t *testing.T) {
	// 正常值原样返回
	l, o := Params("20", "40", 20, 200)
	require.Equal(t, 20, l)
	require.Equal(t, 40, o)

	// limit 为 0 / 负数 / 非法 / 空 → 默认值
	for _, s := range []string{"0", "-5", "abc", ""} {
		l, _ = Params(s, "0", 20, 200)
		require.Equalf(t, 20, l, "limit=%q 应回默认", s)
	}

	// limit 超上限 → 夹到 max；恰等 max 不夹
	l, _ = Params("9999", "0", 20, 200)
	require.Equal(t, 200, l)
	l, _ = Params("200", "0", 20, 200)
	require.Equal(t, 200, l)

	// offset 负数 / 非法 / 空 → 0
	for _, s := range []string{"-10", "xyz", ""} {
		_, o = Params("20", s, 20, 200)
		require.Equalf(t, 0, o, "offset=%q 应回 0", s)
	}
}

// TestNew 验证统一分页响应构造：page/page_size/total_pages 的计算与 nil→[] 归一。
func TestNew(t *testing.T) {
	// 整除：45 条、每页 10、第 1 页
	r := New([]int{1, 2, 3}, 45, 10, 0)
	require.Equal(t, int64(45), r.Total)
	require.Equal(t, 10, r.PageSize)
	require.Equal(t, 1, r.Page)
	require.Equal(t, 5, r.TotalPages) // ceil(45/10)=5
	require.Equal(t, []int{1, 2, 3}, r.Items)

	// 恰好整除
	require.Equal(t, 5, New([]int{}, 50, 10, 0).TotalPages)

	// offset 非零推算当前页：offset=20、limit=10 → 第 3 页
	require.Equal(t, 3, New([]int{}, 45, 10, 20).Page)

	// total=0 → TotalPages 至少 1，Items 归一为空切片（非 nil）
	empty := New[int](nil, 0, 20, 0)
	require.Equal(t, 1, empty.TotalPages)
	require.NotNil(t, empty.Items)
	require.Equal(t, 0, len(empty.Items))

	// limit<=0 兜底防除零
	require.NotPanics(t, func() { New([]int{}, 10, 0, 0) })
}
