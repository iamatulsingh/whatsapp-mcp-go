package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
)

type dbConfig struct {
	User       string
	Pass       string
	Host       string
	Port       string
	IsPostgres bool
}

type Config struct {
	DB            dbConfig
	JWTSecret     []byte
	APIKey        string
	WebhookUrl    string
	Host          string
	Port          int
	AuthLoginRate string
}

func LoadConfig() (*Config, error) {
	isPostgres := os.Getenv("IS_POSTGRES") == "true"

	var user, pass, host, port string
	if isPostgres {
		var ok bool
		user, ok = os.LookupEnv("POSTGRES_USER")
		if !ok {
			return nil, fmt.Errorf("missing POSTGRES_USER")
		}
		pass, ok = os.LookupEnv("POSTGRES_PASS")
		if !ok {
			return nil, fmt.Errorf("missing POSTGRES_PASS")
		}
		host, ok = os.LookupEnv("POSTGRES_HOST")
		if !ok {
			return nil, fmt.Errorf("missing POSTGRES_HOST")
		}
		port, ok = os.LookupEnv("POSTGRES_PORT")
		if !ok {
			return nil, fmt.Errorf("missing POSTGRES_PORT")
		}
	}

	jwtSecret, ok := lookupEither("WHATSAPP_JWT_SECRET", "JWT_SECRET")
	if !ok {
		return nil, fmt.Errorf("missing WHATSAPP_JWT_SECRET")
	}
	apiKey, ok := lookupEither("WHATSAPP_API_KEY", "API_KEY")
	if !ok {
		return nil, fmt.Errorf("missing WHATSAPP_API_KEY")
	}
	if err := validateSecret("WHATSAPP_JWT_SECRET (or deprecated alias JWT_SECRET)", jwtSecret); err != nil {
		return nil, err
	}
	if err := validateSecret("WHATSAPP_API_KEY (or deprecated alias API_KEY)", apiKey); err != nil {
		return nil, err
	}
	webhookUrl := os.Getenv("WEBHOOK_URL")

	serverHost := os.Getenv("HOST")
	serverPort := 8080
	if v, ok := os.LookupEnv("PORT"); ok {
		p, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("invalid PORT %q: %w", v, err)
		}
		serverPort = p
	}

	authLoginRate := os.Getenv("AUTH_LOGIN_RATE") // parsed in auth package; empty -> default

	return &Config{
		DB: dbConfig{
			User:       user,
			Pass:       pass,
			Host:       host,
			Port:       port,
			IsPostgres: isPostgres,
		},
		JWTSecret:     []byte(jwtSecret),
		APIKey:        apiKey,
		WebhookUrl:    webhookUrl,
		Host:          serverHost,
		Port:          serverPort,
		AuthLoginRate: authLoginRate,
	}, nil
}

const minSecretLen = 32

// knownPlaceholders are the example values shipped in docker-compose.yaml.
// Operators who forget to override them must see startup fail loudly.
var knownPlaceholders = []string{
	"c3VwZXItbG9uZy1yYW5kb20tc3RyaW5nLW1pbmltdW0tb2YtNjQtY2hhcmFjdGVycy15b3UtbmVlZC10by1wYXN0ZS1oZXJl",
	"YW5vdGhlci1zdXBlci1sb25nLXJhbmRvbS1zdHJpbmctbWluaW11bS1vZi02NC1jaGFyYWN0ZXJzLXlvdS1uZWVkLXRvLXBhc3RlLWhlcmU=",
}

func validateSecret(name, value string) error {
	for _, ph := range knownPlaceholders {
		if value == ph {
			return fmt.Errorf("%s is set to a placeholder value; generate a real one with `openssl rand -base64 48`", name)
		}
	}
	if len(value) < minSecretLen {
		return fmt.Errorf("%s is too short (%d chars, need ≥%d)", name, len(value), minSecretLen)
	}
	return nil
}

// envWarnFn is replaced in tests; in production it logs via slog.
var envWarnFn = func(msg string, args ...any) {
	slog.Warn(msg, args...)
}

// lookupEither returns the value for `primary` if set, otherwise falls back
// to `deprecated` and emits a deprecation warning. Returns ("", false) only
// when neither is set.
func lookupEither(primary, deprecated string) (string, bool) {
	if v, ok := os.LookupEnv(primary); ok {
		return v, true
	}
	if v, ok := os.LookupEnv(deprecated); ok {
		envWarnFn("env var is deprecated, use the new name",
			"deprecated", deprecated,
			"use_instead", primary)
		return v, true
	}
	return "", false
}
