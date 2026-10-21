// mock 上游：返回带 usage 的 OpenAI chat completion，供网关端到端测试。author: ruan
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
)

func main() {
	http.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		stream := strings.Contains(string(body), "\"stream\":true") || strings.Contains(string(body), "\"stream\": true")
		log.Printf("MOCK chat (auth=%s stream=%v): %s", r.Header.Get("Authorization"), stream, string(body))
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			fl, _ := w.(http.Flusher)
			chunks := []string{
				`{"id":"c1","object":"chat.completion.chunk","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","content":"你好"}}]}`,
				`{"id":"c1","object":"chat.completion.chunk","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"，流式来自 mock"}}]}`,
				`{"id":"c1","object":"chat.completion.chunk","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1200,"completion_tokens":800,"total_tokens":2000}}`,
			}
			for _, c := range chunks {
				fmt.Fprintf(w, "data: %s\n\n", c)
				if fl != nil {
					fl.Flush()
				}
			}
			fmt.Fprint(w, "data: [DONE]\n\n")
			if fl != nil {
				fl.Flush()
			}
			return
		}
		resp := map[string]any{
			"id":      "chatcmpl-mock1",
			"object":  "chat.completion",
			"created": 1700000000,
			"model":   "gpt-4o",
			"choices": []any{map[string]any{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": "你好！这是来自 mock 上游的回复。"},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 1200, "completion_tokens": 800, "total_tokens": 2000},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
	log.Println("mock upstream listening on 127.0.0.1:9911")
	log.Fatal(http.ListenAndServe("127.0.0.1:9911", nil))
}
