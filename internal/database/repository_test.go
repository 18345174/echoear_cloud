package database

import (
	"encoding/json"
	"testing"
)

func TestNormalizeDeviceUID(t *testing.T) {
	if got := NormalizeDeviceUID(" 02:00:00:00:00:01 "); got != "02:00:00:00:00:01" {
		t.Fatalf("unexpected uid: %q", got)
	}
}

func TestNullableIP(t *testing.T) {
	if _, err := NullableIP("10.0.130.5"); err != nil {
		t.Fatalf("valid IP rejected: %v", err)
	}
	if _, err := NullableIP("not-an-ip"); err == nil {
		t.Fatal("invalid IP accepted")
	}
}

func TestJSONOrEmptyRejectsNonObject(t *testing.T) {
	if got := string(JSONOrEmpty(json.RawMessage(`[1,2]`))); got != "{}" {
		t.Fatalf("expected empty object, got %s", got)
	}
}

func TestRandomTokenAndHash(t *testing.T) {
	first, err := RandomToken(32)
	if err != nil {
		t.Fatal(err)
	}
	second, err := RandomToken(32)
	if err != nil {
		t.Fatal(err)
	}
	if first == second || len(first) < 40 {
		t.Fatal("tokens are not sufficiently distinct")
	}
	if HashSecret(first) == first || HashSecret(first) != HashSecret(first) {
		t.Fatal("hash behavior is invalid")
	}
}
