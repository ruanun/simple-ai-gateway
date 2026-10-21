package ipgeo

import (
	"encoding/json"
	"fmt"
	"net/url"
)

// ipApiProvider ip-api.com（免费、HTTP、无需 key）。
func ipApiProvider() provider {
	return provider{
		name: "ip-api.com",
		build: func(ip string) string {
			return "http://ip-api.com/json/" + url.PathEscape(ip) +
				"?fields=status,message,country,countryCode,regionName,city,lat,lon,timezone,isp,org,as,proxy,query"
		},
		parse: parseIPApi,
	}
}

func parseIPApi(body []byte) (*Result, error) {
	var r struct {
		Status      string  `json:"status"`
		Message     string  `json:"message"`
		Country     string  `json:"country"`
		CountryCode string  `json:"countryCode"`
		RegionName  string  `json:"regionName"`
		City        string  `json:"city"`
		Lat         float64 `json:"lat"`
		Lon         float64 `json:"lon"`
		Timezone    string  `json:"timezone"`
		ISP         string  `json:"isp"`
		Org         string  `json:"org"`
		AS          string  `json:"as"`
		Proxy       bool    `json:"proxy"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	if r.Status != "success" {
		return nil, fmt.Errorf("ip-api: %s", r.Message)
	}
	return &Result{
		Country: r.Country, CountryCode: r.CountryCode, Region: r.RegionName, City: r.City,
		Lat: r.Lat, Lon: r.Lon, ISP: r.ISP, Org: r.Org, ASN: r.AS, Timezone: r.Timezone, Proxy: r.Proxy,
	}, nil
}

// ipwhoisProvider ipwho.is（免费、HTTPS、无需 key）。
func ipwhoisProvider() provider {
	return provider{
		name: "ipwho.is",
		build: func(ip string) string {
			return "https://ipwho.is/" + url.PathEscape(ip)
		},
		parse: parseIpwhois,
	}
}

func parseIpwhois(body []byte) (*Result, error) {
	var r struct {
		Success     bool    `json:"success"`
		Message     string  `json:"message"`
		Country     string  `json:"country"`
		CountryCode string  `json:"country_code"`
		Region      string  `json:"region"`
		City        string  `json:"city"`
		Latitude    float64 `json:"latitude"`
		Longitude   float64 `json:"longitude"`
		Connection  struct {
			ASN int    `json:"asn"`
			Org string `json:"org"`
			ISP string `json:"isp"`
		} `json:"connection"`
		Timezone struct {
			ID string `json:"id"`
		} `json:"timezone"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	if !r.Success {
		return nil, fmt.Errorf("ipwho.is: %s", r.Message)
	}
	asn := ""
	if r.Connection.ASN != 0 {
		asn = fmt.Sprintf("AS%d", r.Connection.ASN)
	}
	return &Result{
		Country: r.Country, CountryCode: r.CountryCode, Region: r.Region, City: r.City,
		Lat: r.Latitude, Lon: r.Longitude, ISP: r.Connection.ISP, Org: r.Connection.Org,
		ASN: asn, Timezone: r.Timezone.ID,
	}, nil
}
