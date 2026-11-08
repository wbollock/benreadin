package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientIP(t *testing.T) {
	tests := []struct {
		name    string
		remote  string
		headers map[string][]string
		want    string
	}{
		{"direct, no headers", "203.0.113.9:5555", nil, "203.0.113.9:5555"},
		{"direct peer forging X-Real-IP is ignored", "203.0.113.9:5555",
			map[string][]string{"X-Real-Ip": {"1.2.3.4"}}, "203.0.113.9:5555"},
		{"direct peer forging XFF is ignored", "203.0.113.9:5555",
			map[string][]string{"X-Forwarded-For": {"1.2.3.4"}}, "203.0.113.9:5555"},
		{"direct peer forging True-Client-IP is ignored", "203.0.113.9:5555",
			map[string][]string{"True-Client-Ip": {"1.2.3.4"}}, "203.0.113.9:5555"},
		{"proxy X-Real-IP", "172.29.0.1:40000",
			map[string][]string{"X-Real-Ip": {"198.51.100.7"}}, "198.51.100.7:0"},
		{"proxy X-Real-IP last value wins", "172.29.0.1:40000",
			map[string][]string{"X-Real-Ip": {"1.2.3.4", "198.51.100.7"}}, "198.51.100.7:0"},
		{"proxy XFF rightmost (client-forged left entry ignored)", "127.0.0.1:40000",
			map[string][]string{"X-Forwarded-For": {"1.2.3.4, 198.51.100.7"}}, "198.51.100.7:0"},
		{"proxy garbage X-Real-IP fails closed", "10.0.0.5:1",
			map[string][]string{"X-Real-Ip": {"nope"}, "X-Forwarded-For": {"1.2.3.4"}}, "10.0.0.5:1"},
		{"proxy v4-mapped IPv6 folds", "[::ffff:172.29.0.1]:40000",
			map[string][]string{"X-Real-Ip": {"::ffff:198.51.100.7"}}, "198.51.100.7:0"},
		{"proxy IPv6 client", "[::1]:40000",
			map[string][]string{"X-Real-Ip": {"2001:db8::1"}}, "[2001:db8::1]:0"},
	}
	mw := ClientIP(DefaultTrustedProxies)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got string
			h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = r.RemoteAddr }))
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tt.remote
			for k, vs := range tt.headers {
				for _, v := range vs {
					req.Header.Add(k, v)
				}
			}
			h.ServeHTTP(httptest.NewRecorder(), req)
			if got != tt.want {
				t.Errorf("RemoteAddr = %q, want %q", got, tt.want)
			}
		})
	}
}
