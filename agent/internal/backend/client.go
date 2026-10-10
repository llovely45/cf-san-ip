package backend

import (
	"context"
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
	Candidate    *Candidate `json:"candidate,omitempty"`
}

type planResponse struct {
	ASN    uint32         `json:"asn"`
	Blocks []prefix.Block `json:"blocks"`
}

type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

func NewClient(baseURL, token string) (*Client, error) {
	return newClient(baseURL, token, &http.Client{Timeout: 15 * time.Second})
}

func newClient(baseURL, token string, httpClient *http.Client) (*Client, error) {
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
	return &Client{baseURL: base, token: token, httpClient: httpClient}, nil
}

func (c *Client) Plan(ctx context.Context, asn uint32) ([]prefix.Block, error) {
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
	blocks, err := prefix.ValidatePlan(asn, response.Blocks)
	if err != nil {
		return nil, fmt.Errorf("backend scan plan is invalid: %w", err)
	}
	return blocks, nil
}

func (c *Client) Report(ctx context.Context, report Report) error {
	body, err := json.Marshal(report)
	if err != nil {
		return errors.New("cannot encode scan report")
	}
	_, err = c.request(ctx, http.MethodPost, c.baseURL+"/api/agent/v1/report", body)
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
		return nil, errors.New("backend request failed")
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
