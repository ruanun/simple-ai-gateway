package keytag

import (
	"testing"

	"github.com/ruanun/simple-ai-gateway/internal/dbtest"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/stretchr/testify/require"
)

func TestTagBindAndList(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.KeyTag{}, &model.KeyTagRelation{}))
	svc := NewService(NewRepo(g))

	tag, err := svc.Create("prod")
	require.NoError(t, err)
	require.NoError(t, svc.Bind(100, tag.ID)) // key 100 绑 prod
	require.NoError(t, svc.Bind(101, tag.ID))

	keyIDs, err := svc.KeyIDsByTag(tag.ID)
	require.NoError(t, err)
	require.ElementsMatch(t, []uint64{100, 101}, keyIDs)

	tags, err := svc.TagsByKey(100)
	require.NoError(t, err)
	require.Len(t, tags, 1)
	require.Equal(t, "prod", tags[0].Name)

	require.NoError(t, svc.Unbind(100, tag.ID))
	keyIDs, _ = svc.KeyIDsByTag(tag.ID)
	require.ElementsMatch(t, []uint64{101}, keyIDs)
}

// TestTagsByKeys 验证批量取标签：一次查询按 key 分组，避免列表 N+1(E2)。
func TestTagsByKeys(t *testing.T) {
	g := dbtest.OpenInMemory(t)
	require.NoError(t, g.AutoMigrate(&model.KeyTag{}, &model.KeyTagRelation{}))
	svc := NewService(NewRepo(g))

	t1, _ := svc.Create("t1")
	t2, _ := svc.Create("t2")
	require.NoError(t, svc.Bind(1, t1.ID)) // key1 → t1,t2
	require.NoError(t, svc.Bind(1, t2.ID))
	require.NoError(t, svc.Bind(2, t1.ID)) // key2 → t1

	m, err := svc.TagsByKeys([]uint64{1, 2, 3})
	require.NoError(t, err)
	require.Len(t, m[1], 2)
	require.Len(t, m[2], 1)
	require.Empty(t, m[3], "无标签的 key 不出现或为空")

	// 空输入不查库
	m2, err := svc.TagsByKeys(nil)
	require.NoError(t, err)
	require.Empty(t, m2)
}
