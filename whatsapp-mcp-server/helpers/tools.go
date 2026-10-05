package helpers

import (
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var apiBaseURL = readApiBaseURL()

func readApiBaseURL() string {
	if v := ReadEnv("API_BASE_URL", ""); v != "" {
		return v
	}
	const fallback = "http://localhost:8080/api"
	slog.Warn("api_base_url not set, using default", "fallback", fallback)
	return fallback
}

const apiTimeout = 25 * time.Second

// OkResult return proper ok result for mcp tool
func OkResult(v any) *mcp.CallToolResult {
	b, err := json.Marshal(v)
	if err != nil {
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{
				&mcp.TextContent{Text: "Failed to encode JSON: " + err.Error()},
			},
		}
	}

	return &mcp.CallToolResult{
		IsError: false,
		Content: []mcp.Content{
			&mcp.TextContent{Text: string(b)},
		},
	}
}

// ErrResult return error result for mcp tool
func ErrResult(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{
			&mcp.TextContent{Text: msg},
		},
	}
}

// IsReadOnly reports whether sending is disabled. Read-only is the default;
// only an explicit READ_ONLY=false (or 0/no) enables the send tools.
func IsReadOnly() bool {
	switch strings.ToLower(strings.TrimSpace(ReadEnv("READ_ONLY", "true"))) {
	case "false", "0", "no":
		return false
	}
	return true
}

// ReadEnv read return value for an env
func ReadEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}
