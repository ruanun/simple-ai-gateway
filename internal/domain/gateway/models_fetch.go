package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
)

// knownCompatSuffixes 已知的「Anthropic 协议兼容子路径」后缀，顺序无关
// （stripCompatSuffix 自己取最长匹配，不依赖书写顺序——排序是编译器保护不了的维护契约）。
// 这类上游（DeepSeek、智谱、百炼等）把 Anthropic 兼容层挂在子路径下，
// 模型列表端点却常留在根域名上，故命中后要回根域名再试一轮。
var knownCompatSuffixes = []string{
	"/api/claudecode",
	"/apps/anthropic",
	"/api/anthropic",
	"/api/coding",
	"/claudecode",
	"/anthropic",
	"/step_plan",
	"/coding",
	"/claude",
}

// modelsErrBodyMaxRunes 错误响应体截断长度：避免几十 KB 的 HTML 404 页整页灌进错误信息。
const modelsErrBodyMaxRunes = 512

// ModelsFetchError 拉取模型列表失败的结构化错误：带上试过的候选与最后一次上游响应，
// 供管理面直接展示（Body 已脱敏 + 截断）。网络层原始错误只留在 Err 供日志，不进 Error()——
// 它含 Go 内部细节与出站地址，对管理员无用。
type ModelsFetchError struct {
	Tried  []string // 已尝试的候选 URL
	Status int      // 最后一次上游状态码；0 表示没拿到响应（网络层失败）
	Body   string   // 最后一次上游响应体，已脱敏 + 截断
	Err    error    // 网络层原始错误，仅供日志
}

func (e *ModelsFetchError) Error() string {
	tried := "已尝试 " + strings.Join(e.Tried, "、")
	if e.Status == 0 {
		return "上游不可达（" + tried + "）"
	}
	msg := fmt.Sprintf("上游返回状态码 %d（%s）", e.Status, tried)
	if e.Body != "" {
		msg += "：" + e.Body
	}
	return msg
}

func (e *ModelsFetchError) Unwrap() error { return e.Err }

// BuildModelsURLCandidates 由渠道 base_url + 协议推导「模型列表端点」候选，按尝试顺序返回。
//
// 单一拼法覆盖不了各家上游，且本项目两种协议的 base_url 语义不同——模型端点应与
// **主链路端点同级**，故优先级由协议决定：
//
//   - openai 渠道主链路拼 {base}/chat/completions → 同级是 {base}/models
//   - anthropic 渠道主链路拼 {base}/v1/messages   → 同级是 {base}/v1/models
//
// 生成规则：
//
//  1. base_url 以版本段 /v{N} 结尾（/v1、智谱 .../paas/v4）→ {base}/models 优先
//     （版本号已在路径里，再补 /v1 会拼出 .../paas/v4/v1/models 这种必然 404 的地址）；
//     版本段非 /v1 时追加 {base}/v1/models 兜底
//  2. 否则按协议定序：anthropic 先 /v1/models 后 /models，其余协议反之
//  3. base_url 命中 knownCompatSuffixes → 追加剥离后缀后的 {root}/v1/models、{root}/models
//
// 结果按首次出现顺序去重，最多 4 条。
func BuildModelsURLCandidates(baseURL, protocol string) ([]string, error) {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if trimmed == "" {
		return nil, errors.New("base_url 为空")
	}

	var candidates []string
	switch {
	case endsWithVersionSegment(trimmed):
		candidates = append(candidates, trimmed+"/models")
		if !strings.HasSuffix(trimmed, "/v1") {
			candidates = append(candidates, trimmed+"/v1/models")
		}
	case protocol == "anthropic":
		candidates = append(candidates, trimmed+"/v1/models", trimmed+"/models")
	default:
		// 协议为空按 openai 处理，与 buildChannelInfo 的 provType 回退一致。
		candidates = append(candidates, trimmed+"/models", trimmed+"/v1/models")
	}
	if root := stripCompatSuffix(trimmed); root != "" {
		candidates = append(candidates, root+"/v1/models", root+"/models")
	}

	// 候选最多 4 条，线性查找去重即可（要保首次出现顺序，slices.Compact 需先排序，会打乱优先级）。
	unique := make([]string, 0, len(candidates))
	for _, u := range candidates {
		if !slices.Contains(unique, u) {
			unique = append(unique, u)
		}
	}
	return unique, nil
}

// endsWithVersionSegment 判断 URL 是否以 OpenAI 风格版本段 /v{N} 结尾（/v1、.../paas/v4）。
func endsWithVersionSegment(url string) bool {
	last := url[strings.LastIndex(url, "/")+1:]
	digits, ok := strings.CutPrefix(last, "v")
	if !ok || digits == "" {
		return false
	}
	for _, r := range digits {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// stripCompatSuffix 若 base_url 以任一已知兼容子路径结尾，返回剥离后缀后的根；否则空串。
// 取**最长**匹配：否则 /anthropic 会提前吃掉 /apps/anthropic，剥离后剩下
// https://dashscope.aliyuncs.com/apps 这种错误的根。
func stripCompatSuffix(baseURL string) string {
	longest := ""
	for _, suffix := range knownCompatSuffixes {
		if len(suffix) > len(longest) && strings.HasSuffix(baseURL, suffix) {
			longest = suffix
		}
	}
	if longest == "" {
		return ""
	}
	root := strings.TrimRight(baseURL[:len(baseURL)-len(longest)], "/")
	// 剥完必须仍是个完整源站：base_url 形如 https://anthropic 会剥出 "https:" 这种残根。
	if !strings.Contains(root, "://") {
		return ""
	}
	return root
}

// modelsHeader 构造拉取模型列表的出站头，与主链路同一套治理（按 protocol/auth_style 分流鉴权
// + 渠道自定义头）。ProbeModels 与 FetchModels 共用，避免 HeaderSpec 的填法在两处漂移。
func modelsHeader(protocol, baseURL, key, authStyle string, customHeaders map[string]string) http.Header {
	return BuildOutboundHeaders(HeaderSpec{
		Protocol:      protocol,
		BaseURL:       baseURL,
		UpstreamKey:   key,
		AuthStyle:     authStyle,
		CustomHeaders: customHeaders,
	})
}

// getModels 对单个候选端点发一次 GET，返回状态码与响应体。
// 头每次克隆一份：http.Client 会在请求生命周期内持有 req.Header，多个候选共享同一个 map 不安全。
func getModels(cli *http.Client, url string, hdr http.Header) (int, []byte, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header = hdr.Clone()
	resp, err := cli.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body, nil
}

// FetchModels 按候选顺序拉取上游模型列表，返回模型名。
//
// 404/405 表示「这个端点不对」→ 换下一个候选；其它非 2xx 表示「端点对了但请求有问题」
// （鉴权失败、上游故障）→ 立即返回，不浪费剩余候选，也避免把 401 误报成「端点不存在」。
// 出站头走 BuildOutboundHeaders 统一治理，与主链路一致。
func FetchModels(cli *http.Client, baseURL, key, protocol, authStyle string, customHeaders map[string]string) ([]string, error) {
	candidates, err := BuildModelsURLCandidates(baseURL, protocol)
	if err != nil {
		return nil, err
	}
	// 上游错误体常回显凭证，且 key 未必是 sk- 前缀（正则兜不住）——把实际值收进脱敏清单。
	secrets := make([]string, 0, len(customHeaders)+1)
	secrets = append(secrets, key)
	for _, v := range customHeaders {
		secrets = append(secrets, v)
	}
	hdr := modelsHeader(protocol, baseURL, key, authStyle, customHeaders)

	// 失败正文只暂存不脱敏：候选逐个 404 时每次都跑一遍脱敏+截断，而只有最后一次会被用到；
	// 上游是几十 KB 的 HTML 404 页时这笔浪费按候选数翻倍。统一在循环外做一次。
	var lastStatus, lastIdx int
	var lastBody []byte
	for i, url := range candidates {
		// candidates 构造后不再改写，前缀切片天然是「已试候选」的快照，无需拷贝。
		status, body, err := getModels(cli, url, hdr)
		if err != nil {
			return nil, &ModelsFetchError{Tried: candidates[:i+1], Err: err}
		}
		if status >= 200 && status < 300 {
			return parseModelList(body)
		}
		lastStatus, lastBody, lastIdx = status, body, i
		if status == http.StatusNotFound || status == http.StatusMethodNotAllowed {
			continue
		}
		break
	}
	return nil, &ModelsFetchError{
		Tried:  candidates[:lastIdx+1],
		Status: lastStatus,
		Body:   redactUpstreamBody(lastBody, secrets),
	}
}

// parseModelList 解析 OpenAI/Anthropic 通用的 {"data":[{"id":...}]} 模型列表响应。
// data 缺失或为空数组算成功（返回空列表）——上游确实可能一个模型都没开；
// 但整体不是合法 JSON，说明这个端点根本不是模型列表接口，必须报错而非静默当空。
func parseModelList(body []byte) ([]string, error) {
	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, errors.New("上游 /models 响应格式不合法")
	}
	names := make([]string, 0, len(result.Data))
	for _, m := range result.Data {
		if m.ID != "" {
			names = append(names, m.ID)
		}
	}
	return names, nil
}

// redactUpstreamBody 把上游响应体处理成可直接展示给管理员的形式：脱敏（已知凭证精确替换 +
// 通用凭证正则）后去空白截断。
func redactUpstreamBody(body []byte, secrets []string) string {
	return truncateRunes(strings.TrimSpace(string(scrubSecrets(body, secrets...))), modelsErrBodyMaxRunes)
}

// truncateRunes 按字符（非字节）截断，避免截断多字节字符产生乱码。
// 先按字节数短路：字节数 ≤ max 时字符数必 ≤ max，常见的小响应体零分配——
// 无条件 []rune(s) 会为一个 200KB 的 HTML 404 页分配 800KB 切片，只为取前 512 个字符。
func truncateRunes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	n := 0
	for i := range s {
		if n == max {
			return s[:i] + "…"
		}
		n++
	}
	return s
}
