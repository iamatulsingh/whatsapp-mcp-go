package auth

import (
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"whatsapp-bridge/config"

	"github.com/golang-jwt/jwt/v5"
)

type Claims struct {
	Service string `json:"service"`
	jwt.RegisteredClaims
}

func LoginHandler(cfg *config.Config) http.HandlerFunc {
	limiter, err := newLoginLimiter(cfg.AuthLoginRate)
	if err != nil {
		// Surface fatal config error at startup by returning a handler that always 500s.
		// In practice main() should call newLoginLimiter directly and exit, but keeping
		// the existing LoginHandler signature stable avoids a wider refactor in this PR.
		slog.Error("invalid AUTH_LOGIN_RATE", "err", err)
		return func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "server misconfigured", http.StatusInternalServerError)
		}
	}
	return func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r)
		lim := limiter.get(ip)
		if !lim.Allow() {
			retry := retryAfterSeconds(lim)
			w.Header().Set("Retry-After", strconv.Itoa(retry))
			slog.Warn("login rate-limited", "remote", ip)
			http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
			return
		}
		expected := []byte("Bearer " + cfg.APIKey)
		got := []byte(r.Header.Get("Authorization"))

		if subtle.ConstantTimeCompare(expected, got) != 1 {
			slog.Warn("login rejected: bad api key", "remote", r.RemoteAddr)
			http.Error(w, "Invalid credentials", http.StatusUnauthorized)
			return
		}

		claims := Claims{
			Service: "mcp-server",
			RegisteredClaims: jwt.RegisteredClaims{
				Issuer:    "whatsapp-bridge",
				Audience:  []string{"whatsapp-mcp-server"},
				ExpiresAt: jwt.NewNumericDate(time.Now().Add(45 * time.Minute)),
				IssuedAt:  jwt.NewNumericDate(time.Now()),
			},
		}

		token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
		signed, err := token.SignedString(cfg.JWTSecret)
		if err != nil {
			slog.Error("failed to sign token", "err", err)
			http.Error(w, "Failed to generate token", http.StatusInternalServerError)
			return
		}

		json.NewEncoder(w).Encode(map[string]string{"token": signed})
	}
}

// JwtAuthMiddleware Protect normal API endpoints
func JwtAuthMiddleware(cfg *config.Config, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			http.Error(w, "Missing or invalid Authorization header", http.StatusUnauthorized)
			return
		}

		tokenStr := strings.TrimPrefix(auth, "Bearer ")

		token, err := jwt.ParseWithClaims(
			tokenStr,
			&Claims{},
			func(token *jwt.Token) (interface{}, error) {
				return cfg.JWTSecret, nil
			},
			jwt.WithIssuer("whatsapp-bridge"),
			jwt.WithAudience("whatsapp-mcp-server"),
			jwt.WithValidMethods([]string{"HS256"}),
		)

		if err != nil {
			slog.Warn("jwt parse error", "err", err, "remote", r.RemoteAddr)
			http.Error(w, "Invalid or expired token", http.StatusUnauthorized)
			return
		}

		if !token.Valid {
			slog.Warn("jwt invalid", "remote", r.RemoteAddr)
			http.Error(w, "Invalid or expired token", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	})
}
