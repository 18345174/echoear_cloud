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
