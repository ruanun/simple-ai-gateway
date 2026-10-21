package audit

import (
	"bytes"
	"compress/gzip"
	"io"
)

// gzipBytes 正文入存储层前压缩（JSON/SSE 文本通常 5～10 倍）。写 bytes.Buffer 不会失败，故不返回 error。
func gzipBytes(b []byte) []byte {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write(b)
	_ = zw.Close()
	return buf.Bytes()
}

// gunzipBytes 读路径解压；非 gzip 数据返回 error。
func gunzipBytes(b []byte) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	return io.ReadAll(zr)
}
