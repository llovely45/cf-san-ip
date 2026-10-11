package probe

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

const maxTraceBodyBytes = 64 * 1024
const targetRequestTimeout = time.Second

type TraceResponse struct {
	RawResponse string
	Body        string
}

func FetchCFTrace(ctx context.Context, ip string, client *http.Client) (TraceResponse, error) {
	address, err := netip.ParseAddr(ip)
	if err != nil || !address.Is4() {
		return TraceResponse{}, errors.New("trace target must be an IPv4 address")
	}
	if client == nil {
		client = newTargetHTTPClient()
	}
	safeClient := *client
	safeClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}

	target := &url.URL{
		Scheme: "https",
		Host:   net.JoinHostPort(address.String(), "443"),
		Path:   "/cdn-cgi/trace",
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return TraceResponse{}, errors.New("cannot create Cloudflare trace request")
	}
	request.Header.Set("User-Agent", "cf-san-agent/1")
	response, err := safeClient.Do(request)
	if err != nil {
		return TraceResponse{}, errors.New("Cloudflare trace request failed")
	}
	defer response.Body.Close()

	body, err := io.ReadAll(io.LimitReader(response.Body, maxTraceBodyBytes+1))
	if err != nil {
		return TraceResponse{}, errors.New("cannot read Cloudflare trace response")
	}
	if len(body) > maxTraceBodyBytes {
		return TraceResponse{}, errors.New("Cloudflare trace response exceeds size limit")
	}

	status := response.Status
	if status == "" {
		status = fmt.Sprintf("%d %s", response.StatusCode, http.StatusText(response.StatusCode))
	}
	var raw strings.Builder
	raw.WriteString(response.Proto)
	raw.WriteByte(' ')
	raw.WriteString(status)
	raw.WriteString("\r\n")
	if err := response.Header.Write(&raw); err != nil {
		return TraceResponse{}, errors.New("cannot serialize Cloudflare trace response")
	}
	raw.WriteString("\r\n")
	raw.Write(body)
	return TraceResponse{RawResponse: raw.String(), Body: string(body)}, nil
}

func newTargetHTTPClient() *http.Client {
	transport := &http.Transport{
		Proxy: nil,
		DialContext: (&net.Dialer{
			Timeout: targetRequestTimeout,
		}).DialContext,
		TLSHandshakeTimeout: targetRequestTimeout,
		TLSClientConfig: &tls.Config{
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: true, // Only used for the explicitly selected public IP target.
		},
		ResponseHeaderTimeout: targetRequestTimeout,
		DisableKeepAlives:     true,
	}
	return &http.Client{Transport: transport, Timeout: targetRequestTimeout}
}
