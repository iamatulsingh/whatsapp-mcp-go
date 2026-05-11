package helpers

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var (
	apiKey         = readApiKeyEnv()
	jwtToken       string
	tokenMutex     sync.Mutex
	tokenExpiresAt time.Time
)

func readApiKeyEnv() string {
	if v := ReadEnv("WHATSAPP_API_KEY", ""); v != "" {
		return v
	}
	if v := ReadEnv("WHATSAPP_API_SECRET", ""); v != "" {
		slog.Warn("env var is deprecated, use the new name",
			"deprecated", "WHATSAPP_API_SECRET",
			"use_instead", "WHATSAPP_API_KEY")
		return v
	}
	return ""
}

// GetOrRefreshJwtToken returns a valid JWT or fetches a new one
func GetOrRefreshJwtToken() (string, error) {
	tokenMutex.Lock()
	defer tokenMutex.Unlock()

	// Reuse token if still valid
	if jwtToken != "" && time.Now().Before(tokenExpiresAt.Add(-30*time.Second)) {
		return jwtToken, nil
	}

	// Request new token
	req, err := http.NewRequest("POST", fmt.Sprintf("%s/%s", strings.TrimSuffix(apiBaseURL, "/api"), "auth/login"), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", apiKey))

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("auth request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("auth failed %d: %s", resp.StatusCode, string(body))
	}

	var data struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", fmt.Errorf("failed to decode token response: %w", err)
	}

	if data.Token == "" {
		return "", fmt.Errorf("no token returned from /auth/login")
	}

	// Parse WITHOUT verifying signature
	parsed, _, err := jwt.NewParser().ParseUnverified(data.Token, jwt.MapClaims{})
	if err == nil {
		if claims, ok := parsed.Claims.(jwt.MapClaims); ok {
			if exp, ok := claims["exp"].(float64); ok {
				tokenExpiresAt = time.Unix(int64(exp), 0)
			}
		}
	}

	jwtToken = data.Token
	slog.Info("Fetched new JWT token", "expires", tokenExpiresAt)

	return jwtToken, nil
}
