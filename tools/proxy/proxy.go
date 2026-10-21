// 本地正向代理:支持明文 HTTP(绝对URL转发)与 HTTPS(CONNECT 隧道),每条转发打日志。
// 仅用于验证网关 IP 代理池出站确实经代理;用 -addr/-tag 起多实例演示代理池加权轮换:
//
//	go run ./tools/proxy                                   # 默认 127.0.0.1:8899,日志标记 PROXIED
//	go run ./tools/proxy -addr 127.0.0.1:8898 -tag B       # 第二实例,日志标记 PROXIED-B
//
// author: ruan
package main

import (
	"flag"
	"io"
	"log"
	"net"
	"net/http"
	"sync/atomic"
	"time"
)

var (
	count int64
	label string // 日志前缀,默认 PROXIED,-tag X 时为 PROXIED-X
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8899", "监听地址")
	tag := flag.String("tag", "", "日志标记后缀(区分多实例)")
	flag.Parse()
	label = "PROXIED"
	if *tag != "" {
		label += "-" + *tag
	}
	log.Printf("forward proxy [%s] listening on %s (http + connect)", label, *addr)
	log.Fatal((&http.Server{Addr: *addr, Handler: http.HandlerFunc(handle)}).ListenAndServe())
}

func handle(w http.ResponseWriter, r *http.Request) {
	n := atomic.AddInt64(&count, 1)
	if r.Method == http.MethodConnect {
		log.Printf("%s #%d CONNECT %s", label, n, r.Host) // HTTPS 隧道(目标 host:443)
		dest, err := net.DialTimeout("tcp", r.Host, 20*time.Second)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "hijack unsupported", http.StatusInternalServerError)
			dest.Close()
			return
		}
		client, _, err := hj.Hijack()
		if err != nil {
			dest.Close()
			return
		}
		client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
		go transfer(dest, client)
		go transfer(client, dest)
		return
	}
	// 明文 HTTP:请求行携带绝对 URL,转发到目标。
	log.Printf("%s #%d HTTP %s %s", label, n, r.Method, r.URL.String())
	outReq, err := http.NewRequest(r.Method, r.URL.String(), r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	for k, vs := range r.Header {
		for _, v := range vs {
			outReq.Header.Add(k, v)
		}
	}
	resp, err := http.DefaultTransport.RoundTrip(outReq)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

func transfer(dst, src net.Conn) {
	defer dst.Close()
	defer src.Close()
	io.Copy(dst, src)
}
