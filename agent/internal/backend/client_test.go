package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cf-san/internal/prefix"
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
	plan, err := client.Plan(context.Background(), 64512)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/agent/v1/plan?asn=64512" {
		t.Fatalf("request path = %q", gotPath)
	}
	if gotAuthorization != "Bearer secret-agent-token" {
		t.Fatalf("Authorization = %q", gotAuthorization)
	}
	if plan.PrefixCount() != 1 {
		t.Fatalf("Plan() prefix count = %d, want 1", plan.PrefixCount())
	}
}

func TestPlanMapsLegacyCheckpointToNewBlockAndRescansIt(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"asn":64512,"prefixes":["8.8.8.0/23"],"checkpoints":[{"sourcePrefix":"8.8.8.0/23","blockCidr":"8.8.8.0/24","status":"completed","latestIp":"8.8.8.200"}]}`)
	}))
	defer server.Close()
	client, err := newClient(server.URL, "token", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	plan, err := client.Plan(context.Background(), 64512)
	if err != nil {
		t.Fatal(err)
	}
	var blocks []prefix.Block
	if err := plan.ForEachPending(func(block prefix.Block) error {
		blocks = append(blocks, block)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 || blocks[0].BlockCIDR != "8.8.8.0/23" || blocks[0].Status != "active" {
		t.Fatalf("Plan() pending blocks = %#v, want the legacy partial /24 mapped to its /23 and rescanned", blocks)
	}
}

func TestPlanPrefersExactCurrentCheckpointOverLegacyCheckpointForSameSource(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"asn":64512,"prefixes":["8.8.0.0/14"],"checkpoints":[{"sourcePrefix":"8.8.0.0/14","blockCidr":"8.8.0.0/24","status":"completed","latestIp":"8.8.0.8"},{"sourcePrefix":"8.8.0.0/14","blockCidr":"8.8.0.0/16","status":"completed","latestIp":"8.8.0.9"}]}`)
	}))
	defer server.Close()
	client, err := newClient(server.URL, "token", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	plan, err := client.Plan(context.Background(), 64512)
	if err != nil {
		t.Fatal(err)
	}
	var blocks []prefix.Block
	if err := plan.ForEachPending(func(block prefix.Block) error {
		blocks = append(blocks, block)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{"8.9.0.0/16", "8.10.0.0/16", "8.11.0.0/16"}
	if len(blocks) != len(want) {
		t.Fatalf("Plan() pending blocks = %#v, want %#v", blocks, want)
	}
	for index, block := range blocks {
		if block.BlockCIDR != want[index] {
			t.Errorf("block %d = %s, want %s", index, block.BlockCIDR, want[index])
		}
	}
}

func TestPlanResumesEachConcurrentBlockWithoutSkippingEarlierActiveWork(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"asn":64512,"prefixes":["8.8.0.0/14"],"checkpoints":[],"blockCheckpoints":[{"sourcePrefix":"8.8.0.0/14","blockCidr":"8.8.0.0/16","status":"active"},{"sourcePrefix":"8.8.0.0/14","blockCidr":"8.9.0.0/16","status":"completed"}]}`)
	}))
	defer server.Close()
	client, err := newClient(server.URL, "token", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	plan, err := client.Plan(context.Background(), 64512)
	if err != nil {
		t.Fatal(err)
	}
	var got []prefix.Block
	if err := plan.ForEachPending(func(block prefix.Block) error {
		got = append(got, block)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{"8.8.0.0/16", "8.10.0.0/16", "8.11.0.0/16"}
	if len(got) != len(want) {
		t.Fatalf("pending blocks = %#v, want active and unscanned blocks only", got)
	}
	for index, cidr := range want {
		if got[index].BlockCIDR != cidr {
			t.Errorf("pending block %d = %s, want %s", index, got[index].BlockCIDR, cidr)
		}
	}
}

func TestPlanAcceptsTensOfThousandsOfPrefixesAndCheckpoints(t *testing.T) {
	prefixes := make([]string, 19_152)
	checkpoints := make([]prefix.Checkpoint, len(prefixes))
	for index := range prefixes {
		cidr := fmt.Sprintf("11.%d.%d.0/24", index/256, index%256)
		prefixes[index] = cidr
		checkpoints[index] = prefix.Checkpoint{
			SourcePrefix: cidr,
			BlockCIDR:    cidr,
			Status:       "completed",
		}
	}
	body, err := json.Marshal(map[string]any{
		"asn":              64512,
		"prefixes":         prefixes,
		"checkpoints":      checkpoints,
		"blockCheckpoints": checkpoints,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(body) >= maxResponseBytes {
		t.Fatalf("synthetic compact plan is %d bytes; want under %d", len(body), maxResponseBytes)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer server.Close()
	client, err := newClient(server.URL, "token", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	plan, err := client.Plan(context.Background(), 64512)
	if err != nil {
		t.Fatal(err)
	}
	if plan.PrefixCount() != len(prefixes) {
		t.Fatalf("Plan() prefix count = %d, want %d", plan.PrefixCount(), len(prefixes))
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

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestPlanPreservesBackendTransportErrorForLogs(t *testing.T) {
	want := errors.New("synthetic connection timeout")
	client, err := newClient("https://worker.example", "token", &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, want
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Plan(context.Background(), 64512)
	if !errors.Is(err, want) {
		t.Fatalf("Plan() error = %v, want wrapped transport error", err)
	}
	if !strings.Contains(err.Error(), "backend request failed") || !strings.Contains(err.Error(), want.Error()) {
		t.Fatalf("Plan() error = %v, want operation and transport details", err)
	}
}
