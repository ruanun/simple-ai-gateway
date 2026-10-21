package middleware

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	idempotencyTTL          = 24 * time.Hour
	idempotencyMaxKeyLen    = 128
	idempotencyMaxBodyBytes = 1 << 20 // 1 MiB：大响应（图片 base64 等）不缓存，重试重新执行
)

// Idempotency 为非流式 /v1 POST 提供 Idempotency-Key 去重：
//   - 首次请求登记 processing，响应后落状态码+响应体；
//   - 同 key 同 payload 重放直接返回缓存响应；
//   - 同 key 不同 payload → 409；processing 中 → 409；
//   - 流式请求不支持（返回 400），避免把 SSE 缓存成一段 JSON。
//
// 放在 APIKeyAuth 之后、RequestID/RateLimit 之前：重放不消耗 RPM，也不需要请求 ID。
func Idempotency(g *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		if g == nil || c.Request.Method != http.MethodPost {
			c.Next()
			return
		}
		key := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
		if key == "" {
			c.Next()
			return
		}
		if len(key) > idempotencyMaxKeyLen || !printableASCII(key) {
			abortV1Error(c, http.StatusBadRequest, "idempotency key is invalid")
			return
		}
		rec := APIKeyFromCtx(c)
		if rec == nil || rec.ID == 0 {
			c.Next()
			return
		}
		raw, err := io.ReadAll(c.Request.Body)
		if err != nil {
			abortV1Error(c, http.StatusBadRequest, "failed to read request body")
			return
		}
		c.Request.Body = io.NopCloser(bytes.NewReader(raw))
		var probe struct {
			Stream bool `json:"stream"`
		}
		_ = json.Unmarshal(raw, &probe)
		if probe.Stream {
			abortV1Error(c, http.StatusBadRequest, "idempotency key is not supported for streaming requests")
			return
		}

		scope := fmt.Sprintf("key:%d:%s", rec.ID, c.Request.URL.Path)
		keyHash := sha256Hex(key)
		reqHash := sha256Hex(fmt.Sprintf("%s\n%s\n%d\n%s", c.Request.Method, c.Request.URL.Path, rec.ID, raw))
		now := time.Now()

		// 先清掉同 scope+key 的过期记录，再尝试登记 processing。
		var existing model.IdempotencyRecord
		err = g.Where("scope = ? AND key_hash = ?", scope, keyHash).First(&existing).Error
		if err == nil && existing.ExpiresAt.Before(now) {
			_ = g.Delete(&existing).Error
			err = gorm.ErrRecordNotFound
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			row := model.IdempotencyRecord{
				Scope: scope, KeyHash: keyHash, RequestHash: reqHash,
				Status: "processing", ExpiresAt: now.Add(idempotencyTTL),
			}
			res := g.Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
			if res.Error != nil {
				abortV1Error(c, http.StatusInternalServerError, "idempotency store failed")
				return
			}
			if res.RowsAffected == 1 {
				// 查到刚插入行的主键（OnConflict 下部分驱动不回填 ID）。
				var inserted model.IdempotencyRecord
				if qerr := g.Where("scope = ? AND key_hash = ?", scope, keyHash).First(&inserted).Error; qerr == nil {
					row.ID = inserted.ID
				}
				cw := &idempotencyWriter{ResponseWriter: c.Writer, status: http.StatusOK}
				c.Writer = cw
				defer func() {
					if r := recover(); r != nil {
						_ = g.Where("scope = ? AND key_hash = ?", scope, keyHash).Delete(&model.IdempotencyRecord{}).Error
						panic(r)
					}
				}()
				c.Next()
				status := cw.Status()
				body := cw.body.Bytes()
				if status >= 500 || len(body) > idempotencyMaxBodyBytes {
					// 5xx 可能瞬时失败、大响应不适合入库：删除登记，允许客户端重试重新执行。
					_ = g.Where("scope = ? AND key_hash = ?", scope, keyHash).Delete(&model.IdempotencyRecord{}).Error
					return
				}
				_ = g.Model(&model.IdempotencyRecord{}).Where("scope = ? AND key_hash = ?", scope, keyHash).
					Updates(map[string]any{"status": "succeeded", "response_status": status, "response_body": body}).Error
				return
			}
		} else if err != nil {
			abortV1Error(c, http.StatusInternalServerError, "idempotency store failed")
			return
		}

		// 已存在：校验 payload 与状态。
		if err := g.Where("scope = ? AND key_hash = ?", scope, keyHash).First(&existing).Error; err != nil {
			abortV1Error(c, http.StatusInternalServerError, "idempotency store failed")
			return
		}
		if existing.RequestHash != reqHash {
			abortV1Error(c, http.StatusConflict, "idempotency key reused with a different request body")
			return
		}
		if existing.Status != "succeeded" {
			c.Header("Retry-After", "1")
			abortV1Error(c, http.StatusConflict, "idempotent request is still processing")
			return
		}
		c.Header("Idempotent-Replayed", "true")
		c.Data(existing.ResponseStatus, "application/json", existing.ResponseBody)
		c.Abort()
	}
}

type idempotencyWriter struct {
	gin.ResponseWriter
	body        bytes.Buffer
	status      int
	wroteHeader bool
}

func (w *idempotencyWriter) WriteHeader(code int) {
	if !w.wroteHeader {
		w.status = code
		w.wroteHeader = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *idempotencyWriter) WriteHeaderNow() {
	if !w.wroteHeader {
		w.status = w.ResponseWriter.Status()
		w.wroteHeader = true
	}
	w.ResponseWriter.WriteHeaderNow()
}

func (w *idempotencyWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	_, _ = w.body.Write(b)
	return w.ResponseWriter.Write(b)
}

func (w *idempotencyWriter) WriteString(s string) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	_, _ = w.body.WriteString(s)
	return w.ResponseWriter.WriteString(s)
}

func (w *idempotencyWriter) Status() int {
	if w.wroteHeader {
		return w.status
	}
	return w.ResponseWriter.Status()
}

func (w *idempotencyWriter) Size() int { return w.body.Len() }

func (w *idempotencyWriter) Written() bool { return w.wroteHeader || w.ResponseWriter.Written() }

func printableASCII(s string) bool {
	for _, r := range s {
		if r < 33 || r > 126 {
			return false
		}
	}
	return true
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
