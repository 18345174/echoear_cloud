package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	HTTPPort       int
	DatabaseURL    string
	PublicBaseURL  string
	AllowedOrigins []string
	SessionTTL     time.Duration
}

func Load() (Config, error) {
	port, err := strconv.Atoi(env("HTTP_PORT", "8080"))
	if err != nil || port < 1 || port > 65535 {
		return Config{}, fmt.Errorf("HTTP_PORT must be between 1 and 65535")
	}
	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if databaseURL == "" {
		databaseURL = buildPostgresURL()
	}
	publicBaseURL, err := normalizePublicBaseURL(os.Getenv("PUBLIC_BASE_URL"))
	if err != nil {
		return Config{}, err
	}
	origins := splitCSV(env("CORS_ALLOWED_ORIGINS", "*"))
	return Config{
		HTTPPort:       port,
		DatabaseURL:    databaseURL,
		PublicBaseURL:  publicBaseURL,
		AllowedOrigins: origins,
		SessionTTL:     30 * 24 * time.Hour,
	}, nil
}

func buildPostgresURL() string {
	value := &url.URL{
		Scheme: "postgres",
		User: url.UserPassword(
			env("POSTGRES_USER", "echoear"),
			env("POSTGRES_PASSWORD", "echoear"),
		),
		Host: net.JoinHostPort(env("POSTGRES_HOST", "postgres"), env("POSTGRES_PORT", "5432")),
		Path: "/" + env("POSTGRES_DB", "echoear"),
	}
	query := value.Query()
	query.Set("sslmode", env("POSTGRES_SSLMODE", "disable"))
	value.RawQuery = query.Encode()
	return value.String()
}

func normalizePublicBaseURL(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", fmt.Errorf("PUBLIC_BASE_URL is required")
	}
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", fmt.Errorf("PUBLIC_BASE_URL must be an absolute http(s) origin")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return "", fmt.Errorf("PUBLIC_BASE_URL must contain only scheme, host, and optional port")
	}
	parsed.Path = ""
	parsed.RawPath = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func splitCSV(value string) []string {
	items := make([]string, 0)
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			items = append(items, item)
		}
	}
	if len(items) == 0 {
		return []string{"*"}
	}
	return items
}
