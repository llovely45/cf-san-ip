package backend

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"cf-san/internal/prefix"
)

const maxResponseBytes = 5 * 1024 * 1024

type Candidate struct {
	IP     string `json:"ip"`
	Port   uint16 `json:"port"`
	ASN    uint32 `json:"asn"`
	Region string `json:"region"`
}

type Report struct {
	ASN          uint32     `json:"asn"`
	SourcePrefix string     `json:"sourcePrefix"`
	BlockCIDR    string     `json:"blockCidr"`
	Status       string     `json:"status"`
	LatestIP     string     `json:"latestIp,omitempty"`
	AgentID      string     `json:"agentId,omitempty"`
	LeaseID      string     `json:"leaseId,omitempty"`
	Candidate    *Candidate `json:"candidate,omitempty"`
}

type leaseRequest struct {
	ASN          uint32 `json:"asn"`
	SourcePrefix string `json:"sourcePrefix"`
	BlockCIDR    string `json:"blockCidr"`
	AgentID      string `json:"agentId"`
	LeaseID      string `json:"leaseId,omitempty"`
}

type claimCandidate struct {
	SourcePrefix string `json:"sourcePrefix"`
	BlockCIDR    string `json:"blockCidr"`
}

type claimNextRequest struct {
	ASN       uint32           `json:"asn"`
	AgentID   string           `json:"agentId"`
	RequestID string           `json:"requestId"`
	MaxBlocks uint32           `json:"maxBlocks"`
	Blocks    []claimCandidate `json:"blocks"`
}

type BlockClaim struct {
	Block prefix.Block
	State string
}

type claimOutcome struct {
	SourcePrefix string `json:"sourcePrefix"`
	BlockCIDR    string `json:"blockCidr"`
	State        string `json:"state"`
	LeaseID      string `json:"leaseId,omitempty"`
}

type leaseResponse struct {
	Results  []claimOutcome `json:"results"`
	Examined int            `json:"examined"`
}

type planResponse struct {
	ASN              uint32              `json:"asn"`
	Prefixes         []string            `json:"prefixes"`
	Checkpoints      []prefix.Checkpoint `json:"checkpoints"`
	BlockCheckpoints []prefix.Checkpoint `json:"blockCheckpoints"`
	Blocks           []prefix.Block      `json:"blocks"`
}

type Client struct {
	baseURL    string
	token      string
	agentID    string
	httpClient *http.Client
}

func NewClient(baseURL, token string, agentIDs ...string) (*Client, error) {
	agentID := ""
	if len(agentIDs) > 0 {
		agentID = agentIDs[0]
	}
	return newClientWithAgentID(baseURL, token, agentID, &http.Client{Timeout: 15 * time.Second})
}

func newClient(baseURL, token string, httpClient *http.Client) (*Client, error) {
	return newClientWithAgentID(baseURL, token, "", httpClient)
}

func newClientWithAgentID(baseURL, token, agentID string, httpClient *http.Client) (*Client, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("backend URL must be HTTPS without credentials, query, or fragment")
	}
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("API token is required")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	} else {
		copy := *httpClient
		if copy.Timeout == 0 {
			copy.Timeout = 15 * time.Second
		}
		httpClient = &copy
	}
	httpClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	base := strings.TrimRight(parsed.String(), "/")
	return &Client{baseURL: base, token: token, agentID: strings.TrimSpace(agentID), httpClient: httpClient}, nil
}

func (c *Client) Plan(ctx context.Context, asn uint32) (*prefix.Plan, error) {
	if asn == 0 {
		return nil, errors.New("ASN must be positive")
	}
	url := c.baseURL + "/api/agent/v1/plan?asn=" + strconv.FormatUint(uint64(asn), 10)
	body, err := c.request(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	var response planResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, errors.New("backend returned an invalid scan plan")
	}
	if response.ASN != asn {
		return nil, errors.New("backend scan plan ASN does not match the requested ASN")
	}
	if len(response.Prefixes) > 0 {
		plan, err := prefix.NewPlanWithBlockProgress(
			asn, response.Prefixes, response.Checkpoints, response.BlockCheckpoints,
		)
		if err != nil {
			return nil, fmt.Errorf("backend scan plan is invalid: %w", err)
		}
		return plan, nil
	}
	if response.Blocks != nil {
		plan, err := prefix.NewPlanFromBlocks(asn, response.Blocks)
		if err != nil {
			return nil, fmt.Errorf("backend scan plan is invalid: %w", err)
		}
		return plan, nil
	}
	return nil, errors.New("backend returned an invalid scan plan")
}

func (c *Client) Report(ctx context.Context, report Report) error {
	if report.AgentID == "" {
		report.AgentID = c.agentID
	}
	body, err := json.Marshal(report)
	if err != nil {
		return errors.New("cannot encode scan report")
	}
	_, err = c.request(ctx, http.MethodPost, c.baseURL+"/api/agent/v1/report", body)
	return err
}

func (c *Client) ClaimBlocks(
	ctx context.Context,
	asn uint32,
	candidates []prefix.Block,
	maxBlocks uint32,
) ([]BlockClaim, int, error) {
	if c.agentID == "" {
		return nil, 0, errors.New("agent ID is required to claim scan blocks")
	}
	if asn == 0 || len(candidates) == 0 || len(candidates) > 64 || maxBlocks == 0 || maxBlocks > 4 {
		return nil, 0, errors.New("scan block claim must contain 1-64 candidates and request 1-4 leases")
	}
	claimCandidates := make([]claimCandidate, len(candidates))
	for index, candidate := range candidates {
		if candidate.ASN != 0 && candidate.ASN != asn {
			return nil, 0, errors.New("scan block candidate belongs to a different ASN")
		}
		claimCandidates[index] = claimCandidate{SourcePrefix: candidate.SourcePrefix, BlockCIDR: candidate.BlockCIDR}
	}
	requestID, err := newClaimRequestID()
	if err != nil {
		return nil, 0, errors.New("cannot create scan block claim request ID")
	}
	body, err := json.Marshal(claimNextRequest{
		ASN: asn, AgentID: c.agentID, RequestID: requestID, MaxBlocks: maxBlocks, Blocks: claimCandidates,
	})
	if err != nil {
		return nil, 0, errors.New("cannot encode scan block claim")
	}
	var response []byte
	for attempt := 0; attempt < 2; attempt++ {
		response, err = c.request(ctx, http.MethodPost, c.baseURL+"/api/agent/v1/claim", body)
		if err == nil || !isRetryableClaimError(err) || attempt == 1 {
			break
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, 0, ctx.Err()
		case <-timer.C:
		}
	}
	if err != nil {
		return nil, 0, err
	}
	var result leaseResponse
	if err := json.Unmarshal(response, &result); err != nil {
		return nil, 0, errors.New("backend returned an invalid scan block claim")
	}
	if result.Examined != len(result.Results) || result.Examined > len(candidates) || result.Examined < 0 {
		return nil, 0, errors.New("backend returned an inconsistent scan block claim")
	}
	claimedCount := uint32(0)
	claims := make([]BlockClaim, len(result.Results))
	for index, outcome := range result.Results {
		candidate := candidates[index]
		if outcome.SourcePrefix != candidate.SourcePrefix || outcome.BlockCIDR != candidate.BlockCIDR {
			return nil, 0, errors.New("backend returned scan block results out of order")
		}
		if outcome.State != "claimed" && outcome.State != "leased" && outcome.State != "completed" {
			return nil, 0, errors.New("backend returned an invalid scan block state")
		}
		block := prefix.Block{
			ASN: asn, SourcePrefix: outcome.SourcePrefix, BlockCIDR: outcome.BlockCIDR,
			Status: "active", LeaseID: outcome.LeaseID,
		}
		if outcome.State == "claimed" {
			if strings.TrimSpace(outcome.LeaseID) == "" {
				return nil, 0, errors.New("backend claimed a scan block without a lease ID")
			}
			claimedCount++
		}
		claims[index] = BlockClaim{Block: block, State: outcome.State}
	}
	if claimedCount > maxBlocks {
		return nil, 0, errors.New("backend claimed more blocks than requested")
	}
	return claims, result.Examined, nil
}

func newClaimRequestID() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes[:]), nil
}

func isRetryableClaimError(err error) bool {
	message := err.Error()
	return strings.HasPrefix(message, "backend request failed:") ||
		message == "cannot read backend response" ||
		strings.HasPrefix(message, "backend returned HTTP 5")
}

func (c *Client) ReleaseBlock(ctx context.Context, block prefix.Block, leaseID string) error {
	if c.agentID == "" || strings.TrimSpace(leaseID) == "" {
		return errors.New("agent ID and lease ID are required to release a scan block")
	}
	body, err := json.Marshal(leaseRequest{
		ASN: block.ASN, SourcePrefix: block.SourcePrefix, BlockCIDR: block.BlockCIDR,
		AgentID: c.agentID, LeaseID: leaseID,
	})
	if err != nil {
		return errors.New("cannot encode scan block release")
	}
	_, err = c.request(ctx, http.MethodPost, c.baseURL+"/api/agent/v1/release", body)
	return err
}

func (c *Client) request(ctx context.Context, method, endpoint string, body []byte) ([]byte, error) {
	var requestBody io.Reader
	if body != nil {
		requestBody = strings.NewReader(string(body))
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, requestBody)
	if err != nil {
		return nil, errors.New("cannot create backend request")
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "cf-san-agent/1")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("backend request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("backend returned HTTP %d", response.StatusCode)
	}
	limited := io.LimitReader(response.Body, maxResponseBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, errors.New("cannot read backend response")
	}
	if len(data) > maxResponseBytes {
		return nil, errors.New("backend response exceeds size limit")
	}
	return data, nil
}
