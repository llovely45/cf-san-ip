package probe

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os/exec"
	"strings"
)

type TraceResponse struct {
	RawResponse string
	Body        string
}

const maxTraceBodyBytes = 64 * 1024
const maxTraceResponseBytes = 80 * 1024

// FetchCFTrace invokes curl for the selected public IP. Curl performs the TLS
// handshake and the HTTP request together, so no separate TLS scanner is needed.
func FetchCFTrace(ctx context.Context, ip, curlPath string) (TraceResponse, error) {
	address, err := netip.ParseAddr(ip)
	if err != nil || !address.Is4() {
		return TraceResponse{}, errors.New("trace target must be an IPv4 address")
	}
	if curlPath == "" {
		curlPath = "curl"
	}
	target := fmt.Sprintf("https://%s:443/cdn-cgi/trace", address.String())
	command := exec.CommandContext(ctx, curlPath,
		"--silent",
		"--show-error",
		"--insecure",
		"--include",
		"--connect-timeout", "1",
		"--max-time", "1",
		"--max-filesize", fmt.Sprint(maxTraceBodyBytes),
		target,
	)
	command.Stderr = io.Discard
	stdout, err := command.StdoutPipe()
	if err != nil {
		return TraceResponse{}, errors.New("cannot capture curl response")
	}
	if err := command.Start(); err != nil {
		return TraceResponse{}, errors.New("cannot start curl; verify it is installed")
	}
	var response limitedResponseBuffer
	response.limit = maxTraceResponseBytes
	_, copyErr := io.Copy(&response, stdout)
	waitErr := command.Wait()
	if ctx.Err() != nil {
		return TraceResponse{}, ctx.Err()
	}
	if copyErr != nil {
		return TraceResponse{}, errors.New("cannot read curl response")
	}
	if response.exceeded {
		return TraceResponse{}, errors.New("curl response exceeds size limit")
	}
	if waitErr != nil {
		return TraceResponse{}, errors.New("curl trace request failed")
	}
	raw := response.buffer.String()
	body, ok := responseBody(raw)
	if !ok {
		return TraceResponse{}, errors.New("curl response has no HTTP headers")
	}
	if len(body) > maxTraceBodyBytes {
		return TraceResponse{}, errors.New("Cloudflare trace response exceeds size limit")
	}
	return TraceResponse{RawResponse: raw, Body: body}, nil
}

type limitedResponseBuffer struct {
	buffer   bytes.Buffer
	limit    int
	exceeded bool
}

func (b *limitedResponseBuffer) Write(data []byte) (int, error) {
	remaining := b.limit - b.buffer.Len()
	if remaining > 0 {
		keep := len(data)
		if keep > remaining {
			keep = remaining
		}
		_, _ = b.buffer.Write(data[:keep])
	}
	if len(data) > remaining {
		b.exceeded = true
	}
	// Continue draining stdout so curl cannot block on a full pipe; storage is
	// bounded independently of the remote response size.
	return len(data), nil
}

func responseBody(raw string) (string, bool) {
	separator := strings.Index(raw, "\r\n\r\n")
	separatorLength := 4
	if separator < 0 {
		separator = strings.Index(raw, "\n\n")
		separatorLength = 2
	}
	if separator < 0 {
		return "", false
	}
	return raw[separator+separatorLength:], true
}
