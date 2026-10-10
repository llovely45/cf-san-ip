package probe

import (
	"testing"
)

func TestValidateCFResolveResponse(t *testing.T) {
	tests := []struct {
		name     string
		response string
		want     bool
	}{
		{
			name: "cloudflare 404 is usable",
			response: "HTTP/2 404\r\n" +
				"server: cloudflare\r\n\r\n" +
				"<h1>404 Not Found</h1>",
			want: true,
		},
		{
			name: "cloudflare 403 without 1034 is usable",
			response: "HTTP/2 403\r\n" +
				"server: cloudflare\r\n\r\n" +
				"access denied",
			want: true,
		},
		{
			name: "cloudflare 1034 is unusable",
			response: "HTTP/2 403\r\n" +
				"server: cloudflare\r\n\r\n" +
				"error code: 1034",
			want: false,
		},
		{
			name: "non cloudflare is unusable",
			response: "HTTP/2 200\r\n" +
				"server: nginx\r\n\r\n" +
				"ok",
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ValidateCFResolveResponse(tt.response); got != tt.want {
				t.Fatalf("ValidateCFResolveResponse() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseTraceRegionRequiresOneUsableSpaceFreeLoc(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
		ok   bool
	}{
		{name: "valid loc", body: "fl=abc\nloc=US\nts=1\n", want: "US", ok: true},
		{name: "missing loc", body: "fl=abc\nts=1\n"},
		{name: "empty loc", body: "loc=\n"},
		{name: "whitespace loc", body: "loc=US West\n"},
		{name: "duplicate loc", body: "loc=US\nloc=CA\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParseTraceRegion(tt.body)
			if got != tt.want || ok != tt.ok {
				t.Fatalf("ParseTraceRegion() = (%q, %v), want (%q, %v)", got, ok, tt.want, tt.ok)
			}
		})
	}
}
