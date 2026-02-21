package helpers

import (
	"encoding/json"
	"os"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var apiBaseURL = ReadEnv("API_BASE_URL", "http://192.168.178.119:30015/api")

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

// ReadEnv read return value for an env
func ReadEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}
