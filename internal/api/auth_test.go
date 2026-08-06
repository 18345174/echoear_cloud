package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gin-gonic/gin"

	"github.com/18345174/echoear_cloud/internal/config"
	"github.com/18345174/echoear_cloud/internal/database"
)

func TestNormalizeRegistration(t *testing.T) {
	input, message := normalizeRegistration(registerRequest{
		Username: " alice ", Password: "secret123", Email: " alice@example.com ",
	})
	if message != "" {
		t.Fatal(message)
	}
	if input.Username != "alice" || input.Password != "secret123" || input.Email != "alice@example.com" || input.Role != database.RoleUser {
		t.Fatalf("unexpected normalized registration: %#v", input)
	}

	admin, message := normalizeRegistration(registerRequest{Username: "admin2", Password: "secret123", Role: " ADMIN "})
	if message != "" || admin.Role != database.RoleAdmin {
		t.Fatalf("unexpected admin registration: %#v, %q", admin, message)
	}

	invalid := []registerRequest{
		{Username: "ab", Password: "secret123"},
		{Username: "alice", Password: "short"},
		{Username: "alice", Password: "secret123", Role: "owner"},
	}
	for _, request := range invalid {
		if _, message := normalizeRegistration(request); message == "" {
			t.Fatalf("invalid request accepted: %#v", request)
		}
	}
}

func TestRequireAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name   string
		role   string
		status int
	}{
		{name: "missing session", status: http.StatusUnauthorized},
		{name: "ordinary user", role: database.RoleUser, status: http.StatusForbidden},
		{name: "administrator", role: database.RoleAdmin, status: http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			if test.role != "" {
				context.Set("session", &database.Session{Role: test.role})
			}
			(&Server{}).requireAdmin()(context)
			if recorder.Code != test.status {
				t.Fatalf("expected status %d, got %d", test.status, recorder.Code)
			}
		})
	}
}

func TestFeatureCodes(t *testing.T) {
	if got := featureCodes(database.RoleUser); !reflect.DeepEqual(got, []string{"echoear.use"}) {
		t.Fatalf("unexpected user features: %#v", got)
	}
	if got := featureCodes(database.RoleAdmin); !reflect.DeepEqual(got, []string{"echoear.use", "echoear.account.register"}) {
		t.Fatalf("unexpected admin features: %#v", got)
	}
}

func TestRegisterRouteRequiresAdministrator(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name        string
		sessionRole string
		status      int
	}{
		{name: "administrator", sessionRole: database.RoleAdmin, status: http.StatusOK},
		{name: "ordinary user", sessionRole: database.RoleUser, status: http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			db := &database.DB{DB: raw}
			now := time.Now().UTC()

			mock.ExpectQuery("SELECT s.user_id, u.username, u.role").
				WithArgs(database.HashSecret("test-session")).
				WillReturnRows(sqlmock.NewRows([]string{"user_id", "username", "role", "last_seen_at", "expires_at"}).
					AddRow(1, "operator", test.sessionRole, now, now.Add(time.Hour)))
			if test.sessionRole == database.RoleAdmin {
				mock.ExpectBegin()
				mock.ExpectQuery("INSERT INTO users").
					WithArgs("new-user", sqlmock.AnyArg(), "new@example.com", database.RoleUser).
					WillReturnRows(sqlmock.NewRows([]string{
						"id", "username", "email", "role", "password_changed", "created_at", "updated_at",
					}).AddRow(2, "new-user", "new@example.com", database.RoleUser, false, now, now))
				mock.ExpectExec("INSERT INTO user_settings").
					WithArgs(int64(2)).
					WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectCommit()
			}

			server := NewServer(db, configForAuthTest())
			request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", strings.NewReader(`{
				"username":"new-user","password":"secret123","email":"new@example.com"
			}`))
			request.Header.Set("Authorization", "Session test-session")
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			server.Handler().ServeHTTP(recorder, request)

			if recorder.Code != test.status {
				t.Fatalf("expected status %d, got %d: %s", test.status, recorder.Code, recorder.Body.String())
			}
			if test.sessionRole == database.RoleAdmin {
				var response struct {
					Data struct {
						Username string `json:"username"`
						Role     string `json:"role"`
					} `json:"data"`
				}
				if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				if response.Data.Username != "new-user" || response.Data.Role != database.RoleUser {
					t.Fatalf("unexpected response: %s", recorder.Body.String())
				}
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func configForAuthTest() config.Config {
	return config.Config{AllowedOrigins: []string{"*"}, SessionTTL: time.Hour}
}
