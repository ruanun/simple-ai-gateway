package router

// newSeqRng 返回一个伪随机 rng：用简单线性同余产生与种子相关的序列，
// 仅用于测试让层内顺序在多轮间变化以统计权重分布。
func newSeqRng(seed int) func(n int) int {
	s := int64(seed*2654435761 + 1)
	return func(n int) int {
		if n <= 0 {
			return 0
		}
		s = (s*1103515245 + 12345) & 0x7fffffff
		return int(s) % n
	}
}
