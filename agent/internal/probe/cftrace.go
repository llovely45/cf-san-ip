package probe

import "strings"

func ValidateCFResolveResponse(response string) bool {
	separator := strings.Index(response, "\r\n\r\n")
	if separator < 0 {
		separator = strings.Index(response, "\n\n")
	}
	if separator < 0 {
		return false
	}
	headers := response[:separator]
	body := response[separator:]
	serverIsCloudflare := false
	for _, line := range strings.Split(headers, "\n") {
		line = strings.TrimSuffix(line, "\r")
		name, value, found := strings.Cut(line, ":")
		if found && strings.EqualFold(strings.TrimSpace(name), "server") &&
			strings.EqualFold(strings.TrimSpace(value), "cloudflare") {
			serverIsCloudflare = true
		}
	}
	if !serverIsCloudflare {
		return false
	}
	return !strings.Contains(strings.ToLower(body), "error code: 1034")
}

func ParseTraceRegion(body string) (string, bool) {
	region := ""
	found := false
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if !strings.HasPrefix(line, "loc=") {
			continue
		}
		if found {
			return "", false
		}
		found = true
		region = strings.TrimSpace(strings.TrimPrefix(line, "loc="))
	}
	if !found || region == "" || strings.ContainsAny(region, " \t\r\n") {
		return "", false
	}
	return region, true
}
