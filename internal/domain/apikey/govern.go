package apikey

import (
	"encoding/json"

	"github.com/ruanun/simple-ai-gateway/internal/model"
)

// MapModel 按 key 的 model_mappings 重写模型名;未命中或空映射返回原值。
func MapModel(k model.APIKey, modelName string) string {
	if k.ModelMappings == "" {
		return modelName
	}
	var m map[string]string
	if json.Unmarshal([]byte(k.ModelMappings), &m) != nil {
		return modelName
	}
	if to, ok := m[modelName]; ok && to != "" {
		return to
	}
	return modelName
}

// ParseMappings 解析 key 的 model_mappings JSON 字段为映射表；解析失败返回 nil。
func ParseMappings(raw string) map[string]string {
	var m map[string]string
	if json.Unmarshal([]byte(raw), &m) != nil {
		return nil
	}
	return m
}

// AllowsModel 校验 model 是否在 key 白名单内;空白名单=不限。
func AllowsModel(k model.APIKey, modelName string) bool {
	return model.WhitelistAllows(k.ModelWhitelist, modelName)
}
