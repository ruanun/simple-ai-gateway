package proxydial

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/ruanun/simple-ai-gateway/internal/model"
	"github.com/stretchr/testify/require"
)

// buildProxyFromURL 解析 httptest server 的 URL，构造一个 http 协议的 *model.Proxy。
func buildProxyFromURL(t *testing.T, rawURL string) *model.Proxy {
	t.Helper()
	u, err := url.Parse(rawURL)
	require.NoError(t, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)
	return &model.Proxy{Protocol: "http", Host: u.Hostname(), Port: port}
}

func TestBuildClientNilDirect(t *testing.T) {
	cli, err := BuildClient(nil, 30, false)
	require.NoError(t, err)
	require.NotNil(t, cli)
	require.Same(t, directTransport, cli.Transport, "直连须用带地址校验的共享 transport")
	require.NotNil(t, cli.CheckRedirect, "出站须禁止跟随重定向")
}

// 出站地址策略：元数据/链路本地等始终拒绝；回环/私网仅在开关开启时拒绝；公网放行。
func TestCheckIP(t *testing.T) {
	defer SetBlockPrivate(false)
	always := []string{"169.254.169.254", "169.254.0.1", "fe80::1", "0.0.0.0", "::", "224.0.0.1",
		"100.100.100.200", "fd00:ec2::254", "::ffff:169.254.169.254"}
	private := []string{"127.0.0.1", "::1", "10.0.0.5", "172.16.3.4", "192.168.1.1", "fc00::1", "100.64.0.1"}
	public := []string{"8.8.8.8", "2001:4860:4860::8888", "104.18.0.1"}

	for _, on := range []bool{false, true} {
		SetBlockPrivate(on)
		for _, ip := range always {
			require.ErrorIs(t, checkIP(net.ParseIP(ip)), ErrBlockedAddr, "始终拒绝 %s (blockPrivate=%v)", ip, on)
		}
		for _, ip := range private {
			if on {
				require.ErrorIs(t, checkIP(net.ParseIP(ip)), ErrBlockedAddr, "开关开启须拒绝 %s", ip)
			} else {
				require.NoError(t, checkIP(net.ParseIP(ip)), "开关关闭须放行 %s", ip)
			}
		}
		for _, ip := range public {
			require.NoError(t, checkIP(net.ParseIP(ip)), "公网须放行 %s", ip)
		}
	}
}

// 拨号层拦截在建连前生效：直连 client 请求元数据地址立即失败（不真正发包）；开关开启后本机 httptest 也被拒。
func TestDirectClientBlocks(t *testing.T) {
	defer SetBlockPrivate(false)
	_, err := DirectClient(5 * time.Second).Get("http://169.254.169.254/latest/meta-data/")
	require.ErrorIs(t, err, ErrBlockedAddr)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer srv.Close()
	resp, err := DirectClient(5 * time.Second).Get(srv.URL)
	require.NoError(t, err, "缺省放行本机")
	resp.Body.Close()

	SetBlockPrivate(true)
	directTransport.CloseIdleConnections() // 丢掉缓存连接，确保重新拨号走校验
	_, err = DirectClient(5 * time.Second).Get(srv.URL)
	require.ErrorIs(t, err, ErrBlockedAddr, "开启后须拒绝本机")
}

// 出站不跟随重定向：上游返回 30x 时原样拿到 30x，不会被带去 Location 指向的地址。
func TestNoRedirect(t *testing.T) {
	hit := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hit = true }))
	defer target.Close()
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redir.Close()
	cli, err := BuildClient(nil, 5, false)
	require.NoError(t, err)
	resp, err := cli.Get(redir.URL)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusFound, resp.StatusCode)
	require.False(t, hit, "不得跟随重定向访问目标地址")
}

func TestBuildClientHTTPProxy(t *testing.T) {
	// 起一个收到任意请求即记录的 server，充当正向代理。
	var gotProxy bool
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotProxy = true
		w.WriteHeader(200)
		_, _ = w.Write([]byte("via-proxy"))
	}))
	defer proxySrv.Close()

	cli, err := BuildClient(buildProxyFromURL(t, proxySrv.URL), 30, false)
	require.NoError(t, err)
	require.NotNil(t, cli.Transport)
	// 经代理发一个普通 http 请求（目标随意），代理 server 应收到。
	_, _ = cli.Get("http://example.invalid/")
	require.True(t, gotProxy)
}

func TestBuildClientStreamNoTimeout(t *testing.T) {
	cli, err := BuildClient(nil, 30, true)
	require.NoError(t, err)
	require.Zero(t, cli.Timeout) // 流式不设整体超时
}

func TestBuildClientSocks5(t *testing.T) {
	// socks5 不起真 server，仅验证 client 构造成功且带 transport。
	p := &model.Proxy{Protocol: "socks5", Host: "127.0.0.1", Port: 1080, Username: "u", Password: "pw"}
	cli, err := BuildClient(p, 30, false)
	require.NoError(t, err)
	require.NotNil(t, cli)
	require.NotNil(t, cli.Transport)
}

func TestBuildClientUnsupported(t *testing.T) {
	p := &model.Proxy{Protocol: "ftp", Host: "127.0.0.1", Port: 1080}
	_, err := BuildClient(p, 30, false)
	require.Error(t, err)
}

func proxyWith(id uint64, port int) *model.Proxy {
	return &model.Proxy{
		BaseModel: model.BaseModel{ID: id},
		Protocol:  "http", Host: "127.0.0.1", Port: port,
	}
}

// TestBuildClientTransportReuse 同一代理两次构建应复用同一 Transport（连接池生效）。
func TestBuildClientTransportReuse(t *testing.T) {
	p := proxyWith(101, 18080)
	c1, err := BuildClient(p, 30, false)
	require.NoError(t, err)
	c2, err := BuildClient(p, 30, true) // 流式与否只影响 Client.Timeout，不影响 Transport
	require.NoError(t, err)
	require.Equal(t, fmt.Sprintf("%p", c1.Transport), fmt.Sprintf("%p", c2.Transport))
	require.Equal(t, int64(0), int64(c2.Timeout)) // 流式不设整体超时（原语义保留）
}

// TestBuildClientTransportRebuildOnChange 配置变更（端口改了）应重建 Transport。
func TestBuildClientTransportRebuildOnChange(t *testing.T) {
	c1, err := BuildClient(proxyWith(102, 18080), 30, false)
	require.NoError(t, err)
	c2, err := BuildClient(proxyWith(102, 18081), 30, false)
	require.NoError(t, err)
	require.NotEqual(t, fmt.Sprintf("%p", c1.Transport), fmt.Sprintf("%p", c2.Transport))
}

// TestBuildClientDirectAndZeroID 直连复用同一个带地址校验的共享 Transport（连接池）；ID=0 不缓存。
func TestBuildClientDirectAndZeroID(t *testing.T) {
	c, err := BuildClient(nil, 30, false)
	require.NoError(t, err)
	c2d, _ := BuildClient(nil, 60, true)
	require.Same(t, directTransport, c.Transport)
	require.Same(t, c.Transport, c2d.Transport, "直连各 client 共享连接池")

	p := proxyWith(0, 18080)
	c1, _ := BuildClient(p, 30, false)
	c2, _ := BuildClient(p, 30, false)
	require.NotEqual(t, fmt.Sprintf("%p", c1.Transport), fmt.Sprintf("%p", c2.Transport))
}
