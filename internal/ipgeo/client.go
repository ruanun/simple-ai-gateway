package ipgeo

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// defaultMaxCacheEntries IP 归属缓存的默认容量上限，防止无界增长(#33)。
const defaultMaxCacheEntries = 10000

// Result IP 归属查询结果（归一各 provider 字段）。
type Result struct {
	IP          string  `json:"ip"`
	Private     bool    `json:"private"`
	Country     string  `json:"country"`
	CountryCode string  `json:"country_code"`
	Region      string  `json:"region"`
	City        string  `json:"city"`
	Lat         float64 `json:"lat"`
	Lon         float64 `json:"lon"`
	ISP         string  `json:"isp"`
	Org         string  `json:"org"`
	ASN         string  `json:"asn"`
	Timezone    string  `json:"timezone"`
	Proxy       bool    `json:"proxy"`
	Source      string  `json:"source"` // 命中的 provider 名，或 local
}

type provider struct {
	name  string
	build func(ip string) string
	parse func(body []byte) (*Result, error)
}

type cacheEntry struct {
	res *Result
	exp time.Time
}

// Client 多 provider 故障转移 + 内存 TTL 缓存的 IP 归属客户端。
type Client struct {
	hc         *http.Client
	providers  []provider
	ttl        time.Duration
	maxEntries int // 缓存容量上限(#33)
	mu         sync.Mutex
	cache      map[string]cacheEntry
}

// NewClient 用内置公开 provider（ip-api.com 主、ipwho.is 备）构造。
func NewClient() *Client {
	return newClient([]provider{ipApiProvider(), ipwhoisProvider()}, time.Hour)
}

// newClient 构造客户端；可选 maxEntries 覆盖默认缓存上限（测试注入小值用）。
func newClient(providers []provider, ttl time.Duration, maxEntries ...int) *Client {
	m := defaultMaxCacheEntries
	if len(maxEntries) > 0 && maxEntries[0] > 0 {
		m = maxEntries[0]
	}
	return &Client{
		hc:         &http.Client{Timeout: 3 * time.Second},
		providers:  providers,
		ttl:        ttl,
		maxEntries: m,
		cache:      map[string]cacheEntry{},
	}
}

// Lookup 查询 IP 归属：私有 IP 直接标记；命中缓存返回；否则按 provider 顺序尝试，成功即缓存返回。
func (c *Client) Lookup(ctx context.Context, ip string) (*Result, error) {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return nil, fmt.Errorf("empty ip")
	}
	if IsPrivateIP(ip) {
		return &Result{IP: ip, Private: true, Source: "local"}, nil
	}
	if r := c.getCache(ip); r != nil {
		return r, nil
	}
	var lastErr error
	for _, p := range c.providers {
		r, err := c.query(ctx, p, ip)
		if err != nil {
			lastErr = err
			continue
		}
		r.IP = ip
		r.Source = p.name
		c.setCache(ip, r)
		return r, nil
	}
	return nil, fmt.Errorf("all providers failed: %w", lastErr)
}

func (c *Client) query(ctx context.Context, p provider, ip string) (*Result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.build(ip), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s status %d", p.name, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return p.parse(body)
}

func (c *Client) getCache(ip string) *Result {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.cache[ip]
	if !ok || time.Now().After(e.exp) {
		return nil
	}
	return e.res
}

func (c *Client) setCache(ip string, r *Result) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.cache) >= c.maxEntries {
		c.evictLocked()
	}
	c.cache[ip] = cacheEntry{res: r, exp: time.Now().Add(c.ttl)}
}

// evictLocked 缓存达上限时回收：先删所有过期条目；若仍达上限，再任意删至 3/4 容量。
// 缓存只为省一次外部查询，驱逐谁无关紧要，故不排序、直接借 map 的随机遍历删。
// 调用方须已持有 c.mu。
func (c *Client) evictLocked() {
	now := time.Now()
	for k, e := range c.cache {
		if now.After(e.exp) {
			delete(c.cache, k)
		}
	}
	target := c.maxEntries * 3 / 4
	for k := range c.cache {
		if len(c.cache) <= target {
			break
		}
		delete(c.cache, k)
	}
}
