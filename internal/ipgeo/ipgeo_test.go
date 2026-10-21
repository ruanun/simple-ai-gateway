package ipgeo

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestIsPrivateIP(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
	}{
		{"10.0.0.1", true},
		{"192.168.1.1", true},
		{"172.16.5.5", true},
		{"127.0.0.1", true},
		{"169.254.1.1", true},
		{"100.64.0.1", true},
		{"::1", true},
		{"8.8.8.8", false},
		{"1.1.1.1", false},
		{"114.114.114.114", false},
	}
	for _, c := range cases {
		t.Run(c.ip, func(t *testing.T) {
			if got := IsPrivateIP(c.ip); got != c.want {
				t.Errorf("IsPrivateIP(%q) = %v, want %v", c.ip, got, c.want)
			}
		})
	}
}

func TestParseIPApi(t *testing.T) {
	ok := []byte(`{
		"status": "success",
		"country": "United States",
		"countryCode": "US",
		"regionName": "California",
		"city": "Mountain View",
		"lat": 37.4056,
		"lon": -122.0775,
		"timezone": "America/Los_Angeles",
		"isp": "Google LLC",
		"org": "Google LLC",
		"as": "AS15169 Google LLC",
		"proxy": false,
		"query": "8.8.8.8"
	}`)
	r, err := parseIPApi(ok)
	if err != nil {
		t.Fatalf("parseIPApi() error = %v", err)
	}
	if r.Country != "United States" || r.CountryCode != "US" || r.Region != "California" || r.City != "Mountain View" {
		t.Errorf("unexpected location fields: %+v", r)
	}
	if r.Lat != 37.4056 || r.Lon != -122.0775 {
		t.Errorf("unexpected coordinates: %+v", r)
	}
	if r.ISP != "Google LLC" || r.Org != "Google LLC" || r.ASN != "AS15169 Google LLC" || r.Timezone != "America/Los_Angeles" {
		t.Errorf("unexpected isp/org/asn/timezone: %+v", r)
	}
	if r.Proxy {
		t.Errorf("expected proxy=false, got true")
	}

	fail := []byte(`{"status":"fail","message":"private range"}`)
	if _, err := parseIPApi(fail); err == nil {
		t.Errorf("parseIPApi() on fail status: expected error, got nil")
	}
}

func TestParseIpwhois(t *testing.T) {
	ok := []byte(`{
		"success": true,
		"country": "United States",
		"country_code": "US",
		"region": "California",
		"city": "Mountain View",
		"latitude": 37.4056,
		"longitude": -122.0775,
		"connection": {
			"asn": 15169,
			"org": "Google LLC",
			"isp": "Google LLC"
		},
		"timezone": {
			"id": "America/Los_Angeles"
		}
	}`)
	r, err := parseIpwhois(ok)
	if err != nil {
		t.Fatalf("parseIpwhois() error = %v", err)
	}
	if r.ASN != "AS15169" {
		t.Errorf("ASN = %q, want AS15169", r.ASN)
	}
	if r.Country != "United States" || r.CountryCode != "US" || r.Region != "California" || r.City != "Mountain View" {
		t.Errorf("unexpected location fields: %+v", r)
	}
	if r.Org != "Google LLC" || r.ISP != "Google LLC" || r.Timezone != "America/Los_Angeles" {
		t.Errorf("unexpected org/isp/timezone: %+v", r)
	}

	fail := []byte(`{"success":false,"message":"invalid ip"}`)
	if _, err := parseIpwhois(fail); err == nil {
		t.Errorf("parseIpwhois() on success=false: expected error, got nil")
	}
}

func TestLookupPrivateIPSkips(t *testing.T) {
	called := false
	fake := provider{
		name: "should-not-be-called",
		build: func(ip string) string {
			called = true
			return "http://127.0.0.1:1" // 不可达地址，若真的被访问会失败
		},
		parse: func(body []byte) (*Result, error) {
			called = true
			t.Fatal("parse 不应被调用：私有 IP 应在触达 provider 前短路返回")
			return nil, nil
		},
	}
	c := newClient([]provider{fake}, time.Hour)

	r, err := c.Lookup(context.Background(), "127.0.0.1")
	if err != nil {
		t.Fatalf("Lookup() error = %v", err)
	}
	if !r.Private {
		t.Errorf("Private = false, want true")
	}
	if r.Source != "local" {
		t.Errorf("Source = %q, want local", r.Source)
	}
	if called {
		t.Errorf("provider 被调用，私有 IP 应直接短路")
	}
}

func TestLookupFailover(t *testing.T) {
	badTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer badTS.Close()

	goodTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"success": true,
			"country": "United States",
			"country_code": "US",
			"region": "California",
			"city": "Mountain View",
			"connection": {"asn": 15169, "org": "Google LLC", "isp": "Google LLC"},
			"timezone": {"id": "America/Los_Angeles"}
		}`))
	}))
	defer goodTS.Close()

	providers := []provider{
		{
			name:  "primary-down",
			build: func(ip string) string { return badTS.URL },
			parse: parseIPApi,
		},
		{
			name:  "backup-ipwhois",
			build: func(ip string) string { return goodTS.URL },
			parse: parseIpwhois,
		},
	}
	c := newClient(providers, time.Hour)

	r, err := c.Lookup(context.Background(), "8.8.8.8")
	if err != nil {
		t.Fatalf("Lookup() error = %v", err)
	}
	if r.Source != "backup-ipwhois" {
		t.Errorf("Source = %q, want backup-ipwhois", r.Source)
	}
	if r.IP != "8.8.8.8" {
		t.Errorf("IP = %q, want 8.8.8.8", r.IP)
	}
	if r.Country != "United States" {
		t.Errorf("Country = %q, want United States", r.Country)
	}
}

func TestLookupCache(t *testing.T) {
	var hits int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"success": true,
			"country": "United States",
			"country_code": "US",
			"region": "California",
			"city": "Mountain View",
			"connection": {"asn": 15169, "org": "Google LLC", "isp": "Google LLC"},
			"timezone": {"id": "America/Los_Angeles"}
		}`))
	}))
	defer ts.Close()

	p := provider{
		name:  "counted",
		build: func(ip string) string { return ts.URL },
		parse: parseIpwhois,
	}
	c := newClient([]provider{p}, time.Hour)

	if _, err := c.Lookup(context.Background(), "8.8.8.8"); err != nil {
		t.Fatalf("Lookup() #1 error = %v", err)
	}
	if _, err := c.Lookup(context.Background(), "8.8.8.8"); err != nil {
		t.Fatalf("Lookup() #2 error = %v", err)
	}
	if hits != 1 {
		t.Errorf("外部请求次数 = %d，想要 1（第二次应命中缓存）", hits)
	}
}

// TestCacheBoundedBySize 验证缓存条目数受 maxEntries 上界约束，不会无限增长(#33)。
func TestCacheBoundedBySize(t *testing.T) {
	c := newClient(nil, time.Hour, 3)
	for i := 0; i < 20; i++ {
		c.setCache(fmt.Sprintf("ip%d", i), &Result{})
	}
	c.mu.Lock()
	n := len(c.cache)
	c.mu.Unlock()
	if n > 3 {
		t.Errorf("缓存条目 = %d，应 <= maxEntries(3)", n)
	}
}

// TestCacheEvictsExpired 验证清理时回收过期条目、保留未过期条目(#33)。
func TestCacheEvictsExpired(t *testing.T) {
	c := newClient(nil, time.Hour, 100)
	c.cache["expired"] = cacheEntry{res: &Result{}, exp: time.Now().Add(-time.Hour)}
	c.cache["fresh"] = cacheEntry{res: &Result{}, exp: time.Now().Add(time.Hour)}
	c.mu.Lock()
	c.evictLocked()
	c.mu.Unlock()
	if _, ok := c.cache["expired"]; ok {
		t.Error("过期条目应被回收")
	}
	if _, ok := c.cache["fresh"]; !ok {
		t.Error("未过期条目应保留")
	}
}
