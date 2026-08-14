package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"

	"github.com/18345174/echoear_cloud/internal/config"
	"github.com/18345174/echoear_cloud/internal/database"
)

func TestWebLoginUsesHttpOnlyCookieWithoutReturningSessionToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	raw, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	db := &database.DB{DB: raw}
	hash, err := bcrypt.GenerateFromPassword([]byte("secret123"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}

	mock.ExpectQuery("SELECT id,username,password_hash,role,status,password_changed").
		WithArgs("alice").
		WillReturnRows(sqlmock.NewRows([]string{"id", "username", "password_hash", "role", "status", "password_changed"}).
			AddRow(1, "alice", string(hash), database.RoleUser, database.UserStatusActive, true))
	mock.ExpectExec("INSERT INTO user_sessions").
		WithArgs(int64(1), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("UPDATE users SET last_login_at").WithArgs(int64(1)).WillReturnResult(sqlmock.NewResult(0, 1))

	cfg := configForAuthTest()
	cfg.PublicBaseURL = "https://cloud.example"
	server := NewServer(db, cfg)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/web-login", strings.NewReader(`{"username":"alice","password":"secret123"}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	cookie := recorder.Header().Get("Set-Cookie")
	for _, wanted := range []string{webSessionCookie + "=", "Path=/", "HttpOnly", "Secure", "SameSite=Lax"} {
		if !strings.Contains(cookie, wanted) {
			t.Fatalf("cookie missing %q: %s", wanted, cookie)
		}
	}
	if strings.Contains(recorder.Body.String(), "session_id") {
		t.Fatalf("web login leaked session token: %s", recorder.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRequireSessionAcceptsWebCookie(t *testing.T) {
	gin.SetMode(gin.TestMode)
	raw, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	db := &database.DB{DB: raw}
	now := time.Now().UTC()
	mock.ExpectQuery("SELECT s.user_id, u.username, u.role").
		WithArgs(database.HashSecret("cookie-session")).
		WillReturnRows(sqlmock.NewRows([]string{"user_id", "username", "role", "status", "last_seen_at", "expires_at"}).
			AddRow(1, "alice", database.RoleUser, database.UserStatusActive, now, now.Add(time.Hour)))

	server := &Server{db: db}
	router := gin.New()
	router.GET("/protected", server.requireSession(), func(c *gin.Context) { c.Status(http.StatusNoContent) })
	request := httptest.NewRequest(http.MethodGet, "/protected", nil)
	request.AddCookie(&http.Cookie{Name: webSessionCookie, Value: "cookie-session"})
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestHapiWebServesAssetsAndSpaFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<main>HAPI</main>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "assets", "app.js"), []byte("export {}"), 0o644); err != nil {
		t.Fatal(err)
	}

	raw, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	cfg := config.Config{AllowedOrigins: []string{"*"}, HapiWebDir: root}
	server := NewServer(&database.DB{DB: raw}, cfg)

	for _, test := range []struct {
		path, body, cache string
	}{
		{path: "/hapi/sessions/session-1", body: "<main>HAPI</main>", cache: "no-cache"},
		{path: "/hapi/assets/app.js", body: "export {}", cache: "immutable"},
	} {
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, test.path, nil))
		if recorder.Code != http.StatusOK || recorder.Body.String() != test.body {
			t.Fatalf("%s returned %d %q", test.path, recorder.Code, recorder.Body.String())
		}
		if !strings.Contains(recorder.Header().Get("Cache-Control"), test.cache) {
			t.Fatalf("%s cache header = %q", test.path, recorder.Header().Get("Cache-Control"))
		}
	}
}
