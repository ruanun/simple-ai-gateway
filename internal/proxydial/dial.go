// Package proxydial 按代理配置构造出站 http.Client（直连/http-https/socks5）。
// Transport 按代理 ID 缓存复用（连接池），配置变更时按签名失配重建。
package proxydial

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/ruanun/simple-ai-gateway/internal/model"
	xproxy "golang.org/x/net/proxy"
)

// ---- 出站地址策略（SSRF 防护）----
//
// 渠道 base_url 由管理员（含 ops 角色）配置，非流式响应原样回显给调用方，等于一个可读回响应的出站代理。
// 在拨号层（DNS 解析之后、建连之前）按真实 IP 校验，DNS rebinding（先解析到公网过校验、建连时换内网）
// 也绕不过：
//   - 链路本地（含 169.254.169.254 等云元数据）、未指定地址、组播、已知云元数据地址：始终拒绝；
//   - 回环 / 私网 / CGNAT：仅在 SetBlockPrivate(true)（security.upstream_block_private）时拒绝——
//     缺省放行，因为把本机/内网的中转服务（如订阅转 API 的上游）注册成渠道是常见部署。
// 走代理时校验的是到代理本身的连接；目标地址由代理解析，不在本进程控制范围内。

// ErrBlockedAddr 出站目标地址被策略拒绝。
var ErrBlockedAddr = errors.New("出站目标地址被安全策略拒绝")

var blockPrivate atomic.Bool

// SetBlockPrivate 设置是否同时拒绝回环/私网/CGNAT 地址（装配点按配置调用一次）。
func SetBlockPrivate(b bool) { blockPrivate.Store(b) }

// metadataIPs 不在链路本地段内的已知云元数据地址。
var metadataIPs = []net.IP{
	net.ParseIP("100.100.100.200"), // 阿里云
	net.ParseIP("fd00:ec2::254"),   // AWS IMDS IPv6
}

var cgnat = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

// checkIP 按策略判定出站 IP 是否允许。
func checkIP(ip net.IP) error {
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() {
		return fmt.Errorf("%w: %s（链路本地/元数据/未指定地址）", ErrBlockedAddr, ip)
	}
	for _, m := range metadataIPs {
		if ip.Equal(m) {
			return fmt.Errorf("%w: %s（云元数据地址）", ErrBlockedAddr, ip)
		}
	}
	if blockPrivate.Load() && (ip.IsLoopback() || ip.IsPrivate() || cgnat.Contains(ip)) {
		return fmt.Errorf("%w: %s（回环/私网地址，security.upstream_block_private 已开启）", ErrBlockedAddr, ip)
	}
	return nil
}

// dialControl 在 socket 建连前校验已解析的目标 IP。
func dialControl(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("%w: 无法解析的地址 %q", ErrBlockedAddr, host)
	}
	return checkIP(ip)
}

// guardedDialer 带地址校验的拨号器；超时参数与 http.DefaultTransport 一致。
var guardedDialer = &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second, Control: dialControl}

// directTransport 直连共享 Transport：克隆默认 Transport（保留连接池与环境代理行为），换上校验拨号器。
var directTransport = func() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DialContext = guardedDialer.DialContext
	return t
}()

// noRedirect 出站一律不跟随重定向：否则上游（或被篡改的 base_url）可用 30x 把请求导向内网/元数据地址，
// 且重定向会把请求带到 base_url 之外的地方。LLM API 正常不会对业务请求返回重定向。
func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// DirectClient 直连出站 client（地址校验 + 不跟随重定向），供拉取上游模型等非派发路径使用。
func DirectClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: directTransport, CheckRedirect: noRedirect}
}

// transportCache 按代理 ID 缓存共享 Transport；sig 记录构建时的连接参数，
// 代理配置被管理端修改后签名失配 → 关闭旧池、重建。代理数量有限，不设逐出。
var transportCache = struct {
	mu sync.Mutex
	m  map[uint64]*transportEntry
}{m: map[uint64]*transportEntry{}}

type transportEntry struct {
	sig string
	tr  *http.Transport
}

func proxySig(p *model.Proxy) string {
	return fmt.Sprintf("%s|%s|%d|%s|%s", p.Protocol, p.Host, p.Port, p.Username, p.Password)
}

// BuildClient 按代理构造 http.Client；proxy==nil 返回直连 client（默认 transport 自带连接池）。
// stream=true 时不设整体超时（与 Dial 一致，避免长连接被超时切断）。
// 同一代理(按 ID)的 Transport 缓存复用，避免每请求重建导致 TCP+TLS 反复握手。
func BuildClient(p *model.Proxy, timeoutSec int, stream bool) (*http.Client, error) {
	timeout := time.Duration(timeoutSec) * time.Second
	if stream {
		timeout = 0
	}
	if p == nil {
		return DirectClient(timeout), nil
	}
	tr, err := transportFor(p)
	if err != nil {
		return nil, err
	}
	return &http.Client{Timeout: timeout, Transport: tr, CheckRedirect: noRedirect}, nil
}

// transportFor 取/建代理对应的共享 Transport。p.ID==0（未落库对象，测试/兜底）不缓存。
func transportFor(p *model.Proxy) (*http.Transport, error) {
	if p.ID == 0 {
		return buildTransport(p)
	}
	sig := proxySig(p)
	transportCache.mu.Lock()
	defer transportCache.mu.Unlock()
	if e, ok := transportCache.m[p.ID]; ok && e.sig == sig {
		return e.tr, nil
	}
	tr, err := buildTransport(p)
	if err != nil {
		return nil, err
	}
	if old, ok := transportCache.m[p.ID]; ok {
		old.tr.CloseIdleConnections() // 配置已变，释放旧池空闲连接
	}
	transportCache.m[p.ID] = &transportEntry{sig: sig, tr: tr}
	return tr, nil
}

// buildTransport 按协议构建 Transport；连接池参数取保守值（网关到少数上游域名，不需要大池）。
func buildTransport(p *model.Proxy) (*http.Transport, error) {
	base := func() *http.Transport {
		return &http.Transport{
			DialContext:         guardedDialer.DialContext, // 到代理本身的连接同样过地址校验
			MaxIdleConnsPerHost: 8,
			IdleConnTimeout:     90 * time.Second,
		}
	}
	switch p.Protocol {
	case "http", "https", "":
		u := &url.URL{
			Scheme: ifEmpty(p.Protocol, "http"),
			Host:   fmt.Sprintf("%s:%d", p.Host, p.Port),
		}
		if p.Username != "" {
			u.User = url.UserPassword(p.Username, p.Password)
		}
		tr := base()
		tr.Proxy = http.ProxyURL(u)
		return tr, nil
	case "socks5":
		var auth *xproxy.Auth
		if p.Username != "" {
			auth = &xproxy.Auth{User: p.Username, Password: p.Password}
		}
		dialer, err := xproxy.SOCKS5("tcp", fmt.Sprintf("%s:%d", p.Host, p.Port), auth, guardedDialer)
		if err != nil {
			return nil, err
		}
		tr := base()
		tr.Dial = dialer.Dial //nolint:staticcheck // xproxy 兼容 Dial 签名，行为与改造前一致
		return tr, nil
	default:
		return nil, fmt.Errorf("unsupported proxy protocol: %s", p.Protocol)
	}
}

func ifEmpty(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
