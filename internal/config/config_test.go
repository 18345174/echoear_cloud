package config

import (
	"net/url"
	"testing"
)

func TestBuildPostgresURLEscapesCredentials(t *testing.T) {
	t.Setenv("POSTGRES_USER", "echo@ear")
	t.Setenv("POSTGRES_PASSWORD", "p@ss#word")
	t.Setenv("POSTGRES_HOST", "2001:db8::1")
	t.Setenv("POSTGRES_PORT", "5432")
	t.Setenv("POSTGRES_DB", "echoear")
	t.Setenv("POSTGRES_SSLMODE", "require")

	parsed, err := url.Parse(buildPostgresURL())
	if err != nil {
		t.Fatal(err)
	}
	if parsed.User.Username() != "echo@ear" {
		t.Fatalf("username was not preserved: %q", parsed.User.Username())
	}
	password, _ := parsed.User.Password()
	if password != "p@ss#word" {
		t.Fatalf("password was not preserved: %q", password)
	}
	if parsed.Hostname() != "2001:db8::1" || parsed.Query().Get("sslmode") != "require" {
		t.Fatalf("unexpected postgres URL: %s", parsed.Redacted())
	}
}

func TestNormalizePublicBaseURL(t *testing.T) {
	got, err := normalizePublicBaseURL(" https://echoear.example.com:8443/ ")
	if err != nil || got != "https://echoear.example.com:8443" {
		t.Fatalf("unexpected normalized URL %q: %v", got, err)
	}
	for _, value := range []string{"", "echoear.example.com", "ftp://echoear.example.com", "https://echoear.example.com/api", "https://user@example.com", "https://echoear.example.com?q=1"} {
		if _, err := normalizePublicBaseURL(value); err == nil {
			t.Fatalf("invalid public base URL accepted: %q", value)
		}
	}
}
