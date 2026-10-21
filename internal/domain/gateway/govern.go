package gateway

import (
	"time"

	"github.com/ruanun/simple-ai-gateway/internal/domain/apikey"
	"github.com/ruanun/simple-ai-gateway/internal/model"
)

// governResult 治理前置检查结果。Status==0 通过；非0 应拒绝(403白名单/402日成本)。
type governResult struct {
	Model  string // 映射后的模型(贯穿路由/计费/日志)
	AppID  uint64
	Status int
	Msg    string
}

// govern 请求前治理：模型映射→白名单403→日成本402。
// key 为 APIKeyAuth 已载入的完整行（零值=未识别，按无限制通过），不再回表重查；
// AppID 复用给调用方，避免重复查 key。
func govern(keys *apikey.Service, key model.APIKey, reqModel string, now time.Time) governResult {
	mapped := apikey.MapModel(key, reqModel)
	if !apikey.AllowsModel(key, mapped) {
		return governResult{Model: mapped, AppID: key.AppID, Status: 403, Msg: "model not allowed for this key"}
	}
	// 复用已载入的 key 行判日成本，不回表重查同一行。
	if key.ID != 0 && keys != nil && !keys.CheckDailyCostOf(&key, now) {
		return governResult{Model: mapped, AppID: key.AppID, Status: 402, Msg: "daily cost limit exceeded"}
	}
	return governResult{Model: mapped, AppID: key.AppID, Status: 0}
}
