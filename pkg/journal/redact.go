package journal

import (
	"strings"
)

// sensitiveHeaders are masked (value replaced with "[redacted]") wherever headers
// are logged. Names are compared case-insensitively.
var sensitiveHeaders = map[string]bool{
	"authorization":       true,
	"proxy-authorization": true,
	"cookie":              true,
	"set-cookie":          true,
	"x-api-key":           true,
	"x-auth-token":        true,
}

const redactedMarker = "[redacted]"

// RedactHeaders returns a copy of h with sensitive header values masked.
func RedactHeaders(h map[string]string) map[string]string {
	if len(h) == 0 {
		return h
	}
	out := make(map[string]string, len(h))
	for k, v := range h {
		if sensitiveHeaders[strings.ToLower(k)] {
			out[k] = redactedMarker
		} else {
			out[k] = v
		}
	}
	return out
}

// IsSensitiveHeader reports whether a header name should be masked.
func IsSensitiveHeader(name string) bool {
	return sensitiveHeaders[strings.ToLower(name)]
}

// binaryContentTypes are content types whose bodies are never logged verbatim.
var binaryContentTypes = []string{
	"application/octet-stream",
	"multipart/form-data",
	"image/",
	"video/",
	"audio/",
	"application/pdf",
	"application/zip",
}

// CapBody truncates body to max bytes (appending a marker when truncated) and skips
// binary content entirely. max <= 0 disables capture and returns "[omitted]".
func CapBody(body []byte, contentType string, max int) string {
	if max <= 0 {
		return "[omitted]"
	}
	ct := strings.ToLower(contentType)
	for _, b := range binaryContentTypes {
		if strings.Contains(ct, b) {
			return "[binary omitted]"
		}
	}
	if len(body) > max {
		return string(body[:max]) + "...[truncated]"
	}
	return string(body)
}
