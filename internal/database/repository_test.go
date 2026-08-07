package database

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
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

func TestCreateUserCreatesDefaultSettingsAtomically(t *testing.T) {
	raw, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	db := &DB{DB: raw}
	now := time.Now().UTC()

	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO users").
		WithArgs("alice", "bcrypt-hash", "alice@example.com", RoleUser).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "username", "email", "role", "status", "password_changed", "created_at", "updated_at",
		}).AddRow(7, "alice", "alice@example.com", RoleUser, UserStatusActive, false, now, now))
	mock.ExpectExec("INSERT INTO user_settings").
		WithArgs(int64(7)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	user, err := db.CreateUser(context.Background(), CreateUserInput{
		Username: " alice ", PasswordHash: "bcrypt-hash", Email: " alice@example.com ", Role: RoleUser,
	})
	if err != nil {
		t.Fatal(err)
	}
	if user.ID != 7 || user.Username != "alice" || user.Role != RoleUser || user.PasswordChanged {
		t.Fatalf("unexpected user: %#v", user)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCreateUserMapsDuplicateUsername(t *testing.T) {
	raw, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	db := &DB{DB: raw}

	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO users").
		WithArgs("alice", "bcrypt-hash", "", RoleUser).
		WillReturnError(&pq.Error{Code: "23505", Constraint: "users_username_lower_unique"})
	mock.ExpectRollback()

	user, err := db.CreateUser(context.Background(), CreateUserInput{
		Username: "alice", PasswordHash: "bcrypt-hash", Role: RoleUser,
	})
	if user != nil || !errors.Is(err, ErrUsernameExists) {
		t.Fatalf("expected duplicate username error, got user=%#v err=%v", user, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
