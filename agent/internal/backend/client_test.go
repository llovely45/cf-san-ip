package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewClientRequiresHTTPSAndToken(t *testing.T) {
	for _, tc := range []struct{ url, token string }{
		{"http://worker.example", "token"},
		{"https://worker.example?token=leak", "token"},
		{"https://worker.example", ""},
	} {
		if _, err := NewClient(tc.url, tc.token); err == nil {
			t.Fatalf("NewClient(%q, token) error = nil, want error", tc.url)
		}
	}
}

func TestPlanUsesBearerAuthAndRejectsASNOrScopeMismatch(t *testing.T) {
	var gotPath string
	var gotAuthorization string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path + "?" + r.URL.RawQuery
		gotAuthorization = r.Header.Get("Authorization")
		fmt.Fprint(w, `{"asn":64512,"blocks":[{"sourcePrefix":"8.8.8.0/24","blockCidr":"8.8.8.0/24","status":"active","latestIp":null}]}`)
	}))
	defer server.Close()
	client, err := newClient(server.URL, "secret-agent-token", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	blocks, err := client.Plan(context.Background(), 64512)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/agent/v1/plan?asn=64512" {
		t.Fatalf("request path = %q", gotPath)
	}
	if gotAuthorization != "Bearer secret-agent-token" {
		t.Fatalf("Authorization = %q", gotAuthorization)
	}
	if len(blocks) != 1 || blocks[0].ASN != 64512 {
		t.Fatalf("Plan() = %#v", blocks)
	}
}

func TestPlanDoesNotLeakTokenInBackendErrorsAndValidatesScope(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "asn mismatch", body: `{"asn":64513,"blocks":[]}`},
		{name: "block outside source", body: `{"asn":64512,"blocks":[{"sourcePrefix":"8.8.8.0/24","blockCidr":"1.1.1.0/24","status":"active"}]}`},
		{name: "server error", body: `not-json`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			status := http.StatusOK
			if tc.name == "server error" {
				status = http.StatusUnauthorized
			}
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			client, err := newClient(server.URL, "secret-agent-token", server.Client())
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Plan(context.Background(), 64512)
			if err == nil || strings.Contains(err.Error(), "secret-agent-token") {
				t.Fatalf("Plan() error = %v; want safe non-nil error", err)
			}
		})
	}
}

func TestReportSendsCandidateJSONToAuthenticatedEndpoint(t *testing.T) {
	var received Report
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/agent/v1/report" || r.Method != http.MethodPost {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Errorf("missing bearer auth")
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode report: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client, err := newClient(server.URL, "token", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	candidate := &Candidate{IP: "8.8.8.8", Port: 443, ASN: 64512, Region: "US"}
	err = client.Report(context.Background(), Report{
		ASN: 64512, SourcePrefix: "8.8.8.0/24", BlockCIDR: "8.8.8.0/24", Status: "active", Candidate: candidate,
	})
	if err != nil {
		t.Fatal(err)
	}
	if received.Candidate == nil || received.Candidate.IP != candidate.IP || received.Status != "active" {
		t.Fatalf("received report = %#v", received)
	}
}
