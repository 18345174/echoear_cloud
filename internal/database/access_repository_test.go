package database

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
)

func TestCreateAndAcceptSharePostgresIntegration(t *testing.T) {
	databaseURL := os.Getenv("ECHOEAR_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("ECHOEAR_TEST_DATABASE_URL is not set")
	}
	db, err := Open(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	const ownerName = "share-integration-owner"
	const granteeName = "share-integration-grantee"
	_, _ = db.Exec(`DELETE FROM users WHERE username IN ($1,$2)`, ownerName, granteeName)
	defer db.Exec(`DELETE FROM users WHERE username IN ($1,$2)`, ownerName, granteeName)

	var ownerID, granteeID int64
	if err := db.QueryRow(`INSERT INTO users(username,password_hash,email,role) VALUES($1,'test','owner@example.com','admin') RETURNING id`, ownerName).Scan(&ownerID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`INSERT INTO users(username,password_hash,email,role) VALUES($1,'test','grantee@example.com','user') RETURNING id`, granteeName).Scan(&granteeID); err != nil {
		t.Fatal(err)
	}
	var publicID string
	if err := db.QueryRow(`INSERT INTO agents(user_id,agent_id,host_name) VALUES($1,'integration-agent','integration-host') RETURNING public_id::text`, ownerID).Scan(&publicID); err != nil {
		t.Fatal(err)
	}

	policy := json.RawMessage(`{"allowed_flavors":["codex"]}`)
	share, err := db.CreateShare(context.Background(), ownerID, publicID, granteeName, time.Now().UTC(), nil, policy)
	if err != nil {
		t.Fatal(err)
	}
	if share == nil || share.Status != ShareStatusPending || share.GranteeUserID != granteeID {
		t.Fatalf("unexpected share: %#v", share)
	}
	accepted, err := db.TransitionShare(context.Background(), granteeID, share.ID, "accept", "", nil, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if accepted == nil || accepted.Status != ShareStatusActive {
		t.Fatalf("unexpected accepted share: %#v", accepted)
	}
}

func TestCreateShareCleansOrphansAndClassifiesOpenConflict(t *testing.T) {
	raw, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	db := &DB{DB: raw}
	now := time.Now().UTC()

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id FROM agents").
		WithArgs(int64(1), "agent-public").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(10))
	mock.ExpectQuery("SELECT id FROM users").
		WithArgs("pengbangle").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(4))
	mock.ExpectExec("DELETE FROM agent_shares").
		WithArgs(int64(10), int64(4)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("INSERT INTO agent_shares").
		WithArgs(int64(10), int64(1), int64(4), now, nil, "{}").
		WillReturnError(&pq.Error{Code: "23505", Constraint: "agent_shares_open_unique"})
	mock.ExpectRollback()

	_, err = db.CreateShare(context.Background(), 1, "agent-public", "pengbangle", now, nil, json.RawMessage(`{}`))
	if !errors.Is(err, ErrOpenShareExists) {
		t.Fatalf("expected open share conflict, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCreateShareReportsDatabaseStageAndSQLState(t *testing.T) {
	raw, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	db := &DB{DB: raw}
	now := time.Now().UTC()

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id FROM agents").
		WithArgs(int64(1), "agent-public").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(10))
	mock.ExpectQuery("SELECT id FROM users").
		WithArgs("pengbangle").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(4))
	mock.ExpectExec("DELETE FROM agent_shares").
		WithArgs(int64(10), int64(4)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("INSERT INTO agent_shares").
		WithArgs(int64(10), int64(1), int64(4), now, nil, "{}").
		WillReturnError(&pq.Error{Code: "23502", Constraint: "agent_shares_policy_check"})
	mock.ExpectRollback()

	_, err = db.CreateShare(context.Background(), 1, "agent-public", "pengbangle", now, nil, json.RawMessage(`{}`))
	stage, sqlState, constraint := ShareCreateFailureInfo(err)
	if stage != "insert" || sqlState != "23502" || constraint != "agent_shares_policy_check" {
		t.Fatalf("unexpected failure info: stage=%q state=%q constraint=%q err=%v", stage, sqlState, constraint, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRenewAccessRequestExtendsActiveLease(t *testing.T) {
	raw, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	db := &DB{DB: raw}
	access := &AgentAccess{Agent: Agent{ID: 12}, PolicyVersion: 3}

	mock.ExpectQuery("WITH candidate AS").
		WithArgs("request-12345678", int64(9), int64(12), 3, int64(24*60*60), int64(12*60*60)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))

	if !db.RenewAccessRequest(9, access, " request-12345678 ", 24*time.Hour) {
		t.Fatal("expected active request lease to be renewed")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRecordShareUsageIsIdempotent(t *testing.T) {
	raw, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	db := &DB{DB: raw}
	shareID := "11111111-1111-1111-1111-111111111111"

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT s.id::text,s.policy FROM agent_access_leases").
		WithArgs("request-12345678", int64(9), "agent-1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "policy"}).AddRow(shareID, []byte(`{"max_tasks_per_day":2}`)))
	mock.ExpectQuery("SELECT EXISTS\\(SELECT 1 FROM agent_share_usage_events").
		WithArgs(shareID, "event-12345678").WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery("SELECT tasks_created FROM agent_share_usage_daily").
		WithArgs(shareID, sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"tasks_created"}))
	mock.ExpectExec("INSERT INTO agent_share_usage_events").
		WithArgs(shareID, "event-12345678", sqlmock.AnyArg(), 1, int64(12), int64(34)).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT INTO agent_share_usage_daily").
		WithArgs(shareID, sqlmock.AnyArg(), 1, int64(12), int64(34)).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery("SELECT tasks_created,bytes_uploaded,bytes_downloaded FROM agent_share_usage_daily").
		WithArgs(shareID, sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"tasks_created", "bytes_uploaded", "bytes_downloaded"}).AddRow(1, 12, 34))
	mock.ExpectCommit()

	usage, err := db.RecordShareUsage(context.Background(), 9, "agent-1", "request-12345678", "event-12345678", 1, 12, 34)
	if err != nil {
		t.Fatal(err)
	}
	if usage.TasksCreated != 1 || usage.BytesUploaded != 12 || usage.BytesDownloaded != 34 {
		t.Fatalf("unexpected usage: %#v", usage)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRecordShareUsageRejectsDailyLimit(t *testing.T) {
	raw, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	db := &DB{DB: raw}
	shareID := "11111111-1111-1111-1111-111111111111"

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT s.id::text,s.policy FROM agent_access_leases").
		WithArgs("request-12345678", int64(9), "agent-1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "policy"}).AddRow(shareID, []byte(`{"max_tasks_per_day":1}`)))
	mock.ExpectQuery("SELECT EXISTS\\(SELECT 1 FROM agent_share_usage_events").
		WithArgs(shareID, "event-12345678").WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery("SELECT tasks_created FROM agent_share_usage_daily").
		WithArgs(shareID, sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"tasks_created"}).AddRow(1))
	mock.ExpectRollback()

	_, err = db.RecordShareUsage(context.Background(), 9, "agent-1", "request-12345678", "event-12345678", 1, 0, 0)
	if !errors.Is(err, ErrShareDailyLimit) {
		t.Fatalf("expected daily limit error, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
