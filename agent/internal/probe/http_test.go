package probe

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (fn roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestFetchCFTraceUsesOnlyIPTargetAndConventionalPath(t *testing.T) {
	client := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Scheme != "https" || request.URL.Host != "8.8.8.8:443" || request.URL.Path != "/cdn-cgi/trace" {
			t.Fatalf("target request = %s %s", request.Method, request.URL)
		}
		if request.Header.Get("Authorization") != "" {
			t.Fatal("backend credentials must never be sent to the target")
		}
		return &http.Response{
			StatusCode: 404,
			Status:     "404 Not Found",
			Proto:      "HTTP/1.1",
			Header:     http.Header{"Server": []string{"cloudflare"}},
			Body:       io.NopCloser(strings.NewReader("fl=abc\nloc=US\n")),
			Request:    request,
		}, nil
	})}
	result, err := FetchCFTrace(context.Background(), "8.8.8.8", client)
	if err != nil {
		t.Fatal(err)
	}
	if !ValidateCFResolveResponse(result.RawResponse) {
		t.Fatalf("raw response was rejected: %q", result.RawResponse)
	}
	if result.Body != "fl=abc\nloc=US\n" {
		t.Fatalf("body = %q", result.Body)
	}
}

func TestFetchCFTraceRejectsInvalidIP(t *testing.T) {
	if _, err := FetchCFTrace(context.Background(), "not-an-ip", nil); err == nil {
		t.Fatal("FetchCFTrace() error = nil for invalid address")
	}
}
