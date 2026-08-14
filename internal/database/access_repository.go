package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"
)

const (
	ShareStatusPending  = "pending"
	ShareStatusActive   = "active"
	ShareStatusDeclined = "declined"
	ShareStatusRevoked  = "revoked"
	ShareStatusExpired  = "expired"
)

var ErrShareDailyLimit = errors.New("share daily task limit reached")
var ErrOpenShareExists = errors.New("open share already exists")

type ShareCreateFailure struct {
	Stage      string
	SQLState   string
	Constraint string
	Err        error
}

func (e *ShareCreateFailure) Error() string {
	return fmt.Sprintf("create share %s: %v", e.Stage, e.Err)
}

func (e *ShareCreateFailure) Unwrap() error { return e.Err }

func wrapShareCreateFailure(stage string, err error) error {
	if err == nil {
		return nil
	}
	failure := &ShareCreateFailure{Stage: stage, Err: err}
	var postgresError *pq.Error
	if errors.As(err, &postgresError) {
		failure.SQLState = string(postgresError.Code)
		failure.Constraint = postgresError.Constraint
	}
	return failure
}

func ShareCreateFailureInfo(err error) (stage, sqlState, constraint string) {
	var failure *ShareCreateFailure
	if errors.As(err, &failure) {
		return failure.Stage, failure.SQLState, failure.Constraint
	}
	return "unknown", "", ""
}

type ShareUsage struct {
	ShareID         string `json:"share_id"`
	UsageDate       string `json:"usage_date"`
	TasksCreated    int    `json:"tasks_created"`
	BytesUploaded   int64  `json:"bytes_uploaded"`
	BytesDownloaded int64  `json:"bytes_downloaded"`
}

type AgentShare struct {
	ID              string          `json:"id"`
	AgentDBID       int64           `json:"-"`
	AgentPublicID   string          `json:"agent_public_id"`
	AgentID         string          `json:"agent_id"`
	AgentName       string          `json:"agent_name"`
	OwnerUserID     int64           `json:"owner_user_id"`
	OwnerUsername   string          `json:"owner_username"`
	GranteeUserID   int64           `json:"grantee_user_id"`
	GranteeUsername string          `json:"grantee_username"`
	Status          string          `json:"status"`
	ValidFrom       time.Time       `json:"valid_from"`
	ValidUntil      *time.Time      `json:"valid_until,omitempty"`
	Policy          json.RawMessage `json:"policy"`
	PolicyVersion   int             `json:"policy_version"`
	AcceptedAt      *time.Time      `json:"accepted_at,omitempty"`
	DeclinedAt      *time.Time      `json:"declined_at,omitempty"`
	RevokedAt       *time.Time      `json:"revoked_at,omitempty"`
	RevokeReason    string          `json:"revoke_reason"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

type AgentAccess struct {
	Agent
	AccessID      string          `json:"access_id"`
	AccessType    string          `json:"access_type"`
	OwnerUsername string          `json:"owner_username"`
	ShareID       string          `json:"share_id,omitempty"`
	Policy        json.RawMessage `json:"policy,omitempty"`
	PolicyVersion int             `json:"policy_version,omitempty"`
	ValidFrom     *time.Time      `json:"valid_from,omitempty"`
	ValidUntil    *time.Time      `json:"valid_until,omitempty"`
}

type AuditEntry struct {
	ID            int64           `json:"id"`
	ActorUserID   *int64          `json:"actor_user_id,omitempty"`
	ActorUsername string          `json:"actor_username"`
	Action        string          `json:"action"`
	TargetType    string          `json:"target_type"`
	TargetID      string          `json:"target_id"`
	Outcome       string          `json:"outcome"`
	Reason        string          `json:"reason"`
	Details       json.RawMessage `json:"details"`
	CreatedAt     time.Time       `json:"created_at"`
}

type AuditInput struct {
	ActorUserID   *int64
	Action        string
	TargetType    string
	TargetID      string
	OwnerUserID   *int64
	GranteeUserID *int64
	AgentID       *int64
	ShareID       *string
	Outcome       string
	Reason        string
	Details       json.RawMessage
	IPAddress     string
	UserAgent     string
}

func (db *DB) ListUsers(includeDeleted bool) ([]User, error) {
	query := `SELECT id,username,email,role,status,password_changed,created_at,updated_at,last_login_at,
		disabled_at,disabled_by,disabled_reason,deleted_at,deleted_by,deleted_reason FROM users`
	if !includeDeleted {
		query += ` WHERE status <> 'deleted'`
	}
	query += ` ORDER BY status, LOWER(username)`
	rows, err := db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]User, 0)
	for rows.Next() {
		var item User
		var lastLogin, disabledAt, deletedAt sql.NullTime
		var disabledBy, deletedBy sql.NullInt64
		if err := rows.Scan(&item.ID, &item.Username, &item.Email, &item.Role, &item.Status,
			&item.PasswordChanged, &item.CreatedAt, &item.UpdatedAt, &lastLogin, &disabledAt,
			&disabledBy, &item.DisabledReason, &deletedAt, &deletedBy, &item.DeletedReason); err != nil {
			return nil, err
		}
		if lastLogin.Valid {
			item.LastLoginAt = &lastLogin.Time
		}
		if disabledAt.Valid {
			item.DisabledAt = &disabledAt.Time
		}
		if disabledBy.Valid {
			value := disabledBy.Int64
			item.DisabledBy = &value
		}
		if deletedAt.Valid {
			item.DeletedAt = &deletedAt.Time
		}
		if deletedBy.Valid {
			value := deletedBy.Int64
			item.DeletedBy = &value
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (db *DB) UpdateUserLifecycle(ctx context.Context, actorID, targetID int64, status, role, reason string) (*User, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var currentRole, currentStatus string
	if err := tx.QueryRowContext(ctx, `SELECT role,status FROM users WHERE id=$1 FOR UPDATE`, targetID).Scan(&currentRole, &currentStatus); err != nil {
		return nil, err
	}
	if status == "" {
		status = currentStatus
	}
	if role == "" {
		role = currentRole
	}
	if status != UserStatusActive && status != UserStatusDisabled && status != UserStatusDeleted {
		return nil, fmt.Errorf("invalid status")
	}
	if role != RoleAdmin && role != RoleUser {
		return nil, fmt.Errorf("invalid role")
	}
	if actorID == targetID && status != UserStatusActive {
		return nil, ErrSelfManagement
	}
	if currentRole == RoleAdmin && currentStatus == UserStatusActive && (role != RoleAdmin || status != UserStatusActive) {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE role='admin' AND status='active'`).Scan(&count); err != nil {
			return nil, err
		}
		if count <= 1 {
			return nil, ErrLastActiveAdmin
		}
	}
	reason = strings.TrimSpace(reason)
	_, err = tx.ExecContext(ctx, `UPDATE users SET role=$2,status=$3,updated_at=NOW(),
		disabled_at=CASE WHEN $3='disabled' THEN NOW() WHEN $3='active' THEN NULL ELSE disabled_at END,
		disabled_by=CASE WHEN $3='disabled' THEN $4 WHEN $3='active' THEN NULL ELSE disabled_by END,
		disabled_reason=CASE WHEN $3='disabled' THEN $5 WHEN $3='active' THEN '' ELSE disabled_reason END,
		deleted_at=CASE WHEN $3='deleted' THEN NOW() ELSE deleted_at END,
		deleted_by=CASE WHEN $3='deleted' THEN $4 ELSE deleted_by END,
		deleted_reason=CASE WHEN $3='deleted' THEN $5 ELSE deleted_reason END
		WHERE id=$1`, targetID, role, status, actorID, reason)
	if err != nil {
		return nil, err
	}
	if status != UserStatusActive {
		if _, err = tx.ExecContext(ctx, `UPDATE user_sessions SET status='revoked',revoked_at=NOW(),updated_at=NOW() WHERE user_id=$1 AND status='active'`, targetID); err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE agent_access_leases SET revoked_at=NOW() WHERE revoked_at IS NULL AND (subject_user_id=$1 OR owner_user_id=$1)`, targetID); err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE agent_shares SET status='revoked',revoked_at=NOW(),revoked_by=$2,revoke_reason=$3,updated_at=NOW() WHERE status IN ('pending','active') AND (owner_user_id=$1 OR grantee_user_id=$1)`, targetID, actorID, "account "+status); err != nil {
			return nil, err
		}
	}
	details, _ := json.Marshal(map[string]any{"previous_status": currentStatus, "status": status, "previous_role": currentRole, "role": role})
	if _, err = tx.ExecContext(ctx, `INSERT INTO access_audit_log(actor_user_id,action,target_type,target_id,outcome,reason,details) VALUES($1,'user.update','user',$2,'success',$3,$4::jsonb)`, actorID, fmt.Sprint(targetID), reason, string(details)); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	items, err := db.ListUsers(true)
	if err != nil {
		return nil, err
	}
	for i := range items {
		if items[i].ID == targetID {
			return &items[i], nil
		}
	}
	return nil, sql.ErrNoRows
}

func (db *DB) AddAudit(input AuditInput) error {
	outcome := input.Outcome
	if outcome == "" {
		outcome = "success"
	}
	var share any
	if input.ShareID != nil && *input.ShareID != "" {
		share = *input.ShareID
	}
	_, err := db.Exec(`INSERT INTO access_audit_log(actor_user_id,action,target_type,target_id,owner_user_id,
		grantee_user_id,agent_id,share_id,outcome,reason,details,ip_address,user_agent)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8::uuid,$9,$10,$11::jsonb,$12,$13)`, input.ActorUserID,
		input.Action, input.TargetType, input.TargetID, input.OwnerUserID, input.GranteeUserID, input.AgentID,
		share, outcome, strings.TrimSpace(input.Reason), string(JSONOrEmpty(input.Details)), input.IPAddress, input.UserAgent)
	return err
}

func (db *DB) ListAudit(limit int) ([]AuditEntry, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	rows, err := db.Query(`SELECT a.id,a.actor_user_id,COALESCE(u.username,''),a.action,a.target_type,
		a.target_id,a.outcome,a.reason,a.details,a.created_at FROM access_audit_log a
		LEFT JOIN users u ON u.id=a.actor_user_id ORDER BY a.id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]AuditEntry, 0)
	for rows.Next() {
		var item AuditEntry
		var actor sql.NullInt64
		if err := rows.Scan(&item.ID, &actor, &item.ActorUsername, &item.Action, &item.TargetType, &item.TargetID, &item.Outcome, &item.Reason, &item.Details, &item.CreatedAt); err != nil {
			return nil, err
		}
		if actor.Valid {
			value := actor.Int64
			item.ActorUserID = &value
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func scanShare(row interface{ Scan(...any) error }) (*AgentShare, error) {
	var item AgentShare
	var validUntil, acceptedAt, declinedAt, revokedAt sql.NullTime
	err := row.Scan(&item.ID, &item.AgentDBID, &item.AgentPublicID, &item.AgentID, &item.AgentName,
		&item.OwnerUserID, &item.OwnerUsername, &item.GranteeUserID, &item.GranteeUsername, &item.Status,
		&item.ValidFrom, &validUntil, &item.Policy, &item.PolicyVersion, &acceptedAt, &declinedAt, &revokedAt,
		&item.RevokeReason, &item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if validUntil.Valid {
		item.ValidUntil = &validUntil.Time
	}
	if acceptedAt.Valid {
		item.AcceptedAt = &acceptedAt.Time
	}
	if declinedAt.Valid {
		item.DeclinedAt = &declinedAt.Time
	}
	if revokedAt.Valid {
		item.RevokedAt = &revokedAt.Time
	}
	return &item, nil
}

const shareSelect = `SELECT s.id::text,s.agent_id,a.public_id::text,a.agent_id,a.host_name,
	s.owner_user_id,ou.username,s.grantee_user_id,gu.username,s.status,s.valid_from,s.valid_until,
	s.policy,s.policy_version,s.accepted_at,s.declined_at,s.revoked_at,s.revoke_reason,s.created_at,s.updated_at
	FROM agent_shares s JOIN agents a ON a.id=s.agent_id JOIN users ou ON ou.id=s.owner_user_id
	JOIN users gu ON gu.id=s.grantee_user_id`

func (db *DB) CreateShare(ctx context.Context, ownerID int64, agentPublicID, granteeUsername string, validFrom time.Time, validUntil *time.Time, policy json.RawMessage) (*AgentShare, error) {
	if validFrom.IsZero() {
		validFrom = time.Now().UTC()
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, wrapShareCreateFailure("begin", err)
	}
	defer tx.Rollback()
	var agentID int64
	if err = tx.QueryRowContext(ctx, `SELECT id FROM agents WHERE user_id=$1 AND public_id::text=$2`, ownerID, strings.TrimSpace(agentPublicID)).Scan(&agentID); err != nil {
		return nil, wrapShareCreateFailure("resolve_agent", err)
	}
	var granteeID int64
	if err = tx.QueryRowContext(ctx, `SELECT id FROM users WHERE LOWER(username)=LOWER($1) AND status='active'`, strings.TrimSpace(granteeUsername)).Scan(&granteeID); err != nil {
		return nil, wrapShareCreateFailure("resolve_grantee", err)
	}
	if granteeID == ownerID {
		return nil, wrapShareCreateFailure("validate_grantee", fmt.Errorf("cannot share with owner"))
	}
	// Older deployments may contain an open row whose owner no longer exists.
	// Such a row is hidden by ListShares' joins but still occupies the open-share
	// unique index, so remove it before attempting the new invitation.
	if _, err = tx.ExecContext(ctx, `DELETE FROM agent_shares s
		WHERE s.agent_id=$1 AND s.grantee_user_id=$2 AND s.status IN ('pending','active')
		AND (NOT EXISTS (SELECT 1 FROM agents a WHERE a.id=s.agent_id)
			OR NOT EXISTS (SELECT 1 FROM users u WHERE u.id=s.owner_user_id)
			OR NOT EXISTS (SELECT 1 FROM users u WHERE u.id=s.grantee_user_id))`, agentID, granteeID); err != nil {
		return nil, wrapShareCreateFailure("cleanup_orphans", err)
	}
	var shareID string
	err = tx.QueryRowContext(ctx, `INSERT INTO agent_shares(agent_id,owner_user_id,grantee_user_id,valid_from,valid_until,policy,created_by)
		VALUES($1,$2,$3,$4,$5,$6::jsonb,$2) RETURNING id::text`, agentID, ownerID, granteeID, validFrom, validUntil, string(JSONOrEmpty(policy))).Scan(&shareID)
	if err != nil {
		var postgresError *pq.Error
		if errors.As(err, &postgresError) && postgresError.Code == "23505" && postgresError.Constraint == "agent_shares_open_unique" {
			return nil, fmt.Errorf("%w: %v", ErrOpenShareExists, err)
		}
		return nil, wrapShareCreateFailure("insert", err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO access_audit_log(actor_user_id,action,target_type,target_id,owner_user_id,grantee_user_id,agent_id,share_id,details)
		VALUES($1,'share.create','share',$2::text,$1,$3,$4,$2::uuid,$5::jsonb)`, ownerID, shareID, granteeID, agentID, string(JSONOrEmpty(policy))); err != nil {
		return nil, wrapShareCreateFailure("audit", err)
	}
	if err = tx.Commit(); err != nil {
		return nil, wrapShareCreateFailure("commit", err)
	}
	item, err := db.ShareByID(shareID)
	if err != nil {
		return nil, wrapShareCreateFailure("hydrate", err)
	}
	return item, nil
}

func (db *DB) ShareByID(id string) (*AgentShare, error) {
	item, err := scanShare(db.QueryRow(shareSelect+` WHERE s.id::text=$1`, strings.TrimSpace(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return item, err
}

func (db *DB) ListShares(userID int64, admin bool) ([]AgentShare, error) {
	_, _ = db.Exec(`UPDATE agent_shares SET status='expired',updated_at=NOW() WHERE status IN ('pending','active') AND valid_until IS NOT NULL AND valid_until<=NOW()`)
	query := shareSelect
	args := []any{}
	if !admin {
		query += ` WHERE s.owner_user_id=$1 OR s.grantee_user_id=$1`
		args = append(args, userID)
	}
	query += ` ORDER BY s.updated_at DESC`
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]AgentShare, 0)
	for rows.Next() {
		item, err := scanShare(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, *item)
	}
	return items, rows.Err()
}

func (db *DB) TransitionShare(ctx context.Context, actorID int64, shareID, action, reason string, policy json.RawMessage, validFrom *time.Time, validUntil *time.Time, admin bool) (*AgentShare, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var ownerID, granteeID, agentID int64
	var status string
	if err = tx.QueryRowContext(ctx, `SELECT owner_user_id,grantee_user_id,agent_id,status FROM agent_shares WHERE id::text=$1 FOR UPDATE`, shareID).Scan(&ownerID, &granteeID, &agentID, &status); err != nil {
		return nil, err
	}
	allowed := false
	next := status
	switch action {
	case "accept":
		allowed = actorID == granteeID && status == ShareStatusPending
		next = ShareStatusActive
	case "decline":
		allowed = actorID == granteeID && status == ShareStatusPending
		next = ShareStatusDeclined
	case "revoke":
		allowed = (actorID == ownerID || admin) && (status == ShareStatusPending || status == ShareStatusActive)
		next = ShareStatusRevoked
	case "update":
		allowed = actorID == ownerID && (status == ShareStatusPending || status == ShareStatusActive)
	}
	if !allowed {
		return nil, sql.ErrNoRows
	}
	if action == "update" {
		if len(policy) == 0 {
			policy = json.RawMessage(`{}`)
		}
		_, err = tx.ExecContext(ctx, `UPDATE agent_shares SET policy=$2::jsonb,valid_from=COALESCE($3,valid_from),valid_until=$4,
			policy_version=policy_version+1,updated_at=NOW() WHERE id::text=$1`, shareID, string(JSONOrEmpty(policy)), validFrom, validUntil)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE agent_shares SET status=$2,updated_at=NOW(),
			accepted_at=CASE WHEN $2='active' THEN NOW() ELSE accepted_at END,accepted_by=CASE WHEN $2='active' THEN $3 ELSE accepted_by END,
			declined_at=CASE WHEN $2='declined' THEN NOW() ELSE declined_at END,
			revoked_at=CASE WHEN $2='revoked' THEN NOW() ELSE revoked_at END,revoked_by=CASE WHEN $2='revoked' THEN $3 ELSE revoked_by END,
			revoke_reason=CASE WHEN $2='revoked' THEN $4 ELSE revoke_reason END WHERE id::text=$1`, shareID, next, actorID, strings.TrimSpace(reason))
	}
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE agent_access_leases SET revoked_at=NOW() WHERE share_id::text=$1 AND revoked_at IS NULL`, shareID); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO access_audit_log(actor_user_id,action,target_type,target_id,owner_user_id,grantee_user_id,agent_id,share_id,reason)
		VALUES($1,$2,'share',$3::text,$4,$5,$6,$3::uuid,$7)`, actorID, "share."+action, shareID, ownerID, granteeID, agentID, strings.TrimSpace(reason)); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.ShareByID(shareID)
}

func (db *DB) ListAccessibleAgents(userID int64) ([]AgentAccess, error) {
	_, _ = db.Exec(`UPDATE agent_shares SET status='expired',updated_at=NOW() WHERE status='active' AND valid_until IS NOT NULL AND valid_until<=NOW()`)
	rows, err := db.Query(`SELECT a.id,a.user_id,a.agent_id,a.host_name,a.platform,a.app_version,a.preferred_device_uid,a.last_seen_at,
		a.lan_base_url,a.public_key,a.key_id,a.key_algorithm,a.capabilities,a.created_at,a.updated_at,a.public_id::text,
		CASE WHEN a.user_id=$1 THEN a.agent_id ELSE a.public_id::text END,
		CASE WHEN a.user_id=$1 THEN 'owner' ELSE 'shared' END,ou.username,COALESCE(s.id::text,''),COALESCE(s.policy,'{}'::jsonb),
		COALESCE(s.policy_version,0),s.valid_from,s.valid_until
		FROM agents a JOIN users ou ON ou.id=a.user_id
		LEFT JOIN agent_shares s ON s.agent_id=a.id AND s.grantee_user_id=$1 AND s.status='active'
			AND s.valid_from<=NOW() AND (s.valid_until IS NULL OR s.valid_until>NOW())
		WHERE ou.status='active' AND (a.user_id=$1 OR s.id IS NOT NULL) ORDER BY a.updated_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]AgentAccess, 0)
	for rows.Next() {
		var item AgentAccess
		var preferred sql.NullString
		var validFrom, validUntil sql.NullTime
		if err := rows.Scan(&item.ID, &item.UserID, &item.AgentID, &item.HostName, &item.Platform, &item.AppVersion, &preferred, &item.LastSeenAt,
			&item.LanBaseURL, &item.PublicKey, &item.KeyID, &item.KeyAlgorithm, &item.Capabilities, &item.CreatedAt, &item.UpdatedAt, &item.PublicID,
			&item.AccessID, &item.AccessType, &item.OwnerUsername, &item.ShareID, &item.Policy, &item.PolicyVersion, &validFrom, &validUntil); err != nil {
			return nil, err
		}
		if preferred.Valid {
			item.PreferredDeviceUID = &preferred.String
		}
		if validFrom.Valid {
			item.ValidFrom = &validFrom.Time
		}
		if validUntil.Valid {
			item.ValidUntil = &validUntil.Time
		}
		item.Online = item.LastSeenAt != nil && time.Since(*item.LastSeenAt) <= AgentOnlineWindow
		items = append(items, item)
	}
	return items, rows.Err()
}

func (db *DB) ResolveAgentAccess(userID int64, accessID string) (*AgentAccess, error) {
	items, err := db.ListAccessibleAgents(userID)
	if err != nil {
		return nil, err
	}
	for i := range items {
		if items[i].AccessID == strings.TrimSpace(accessID) || (items[i].AccessType == "owner" && items[i].PublicID == strings.TrimSpace(accessID)) {
			return &items[i], nil
		}
	}
	return nil, nil
}

func (db *DB) CreateAccessLease(userID int64, access *AgentAccess, ttl time.Duration, requestID string) (ticketID, namespace string, expiresAt time.Time, err error) {
	if ttl <= 0 || ttl > 10*time.Minute {
		ttl = 5 * time.Minute
	}
	expiresAt = time.Now().UTC().Add(ttl)
	shareID := any(nil)
	namespace = "owner"
	if access.AccessType == "shared" {
		shareID = access.ShareID
		namespace = "share-" + access.ShareID
		var policy struct {
			MaxTasksPerDay int `json:"max_tasks_per_day"`
		}
		_ = json.Unmarshal(access.Policy, &policy)
		if policy.MaxTasksPerDay > 0 {
			var used int
			_ = db.QueryRow(`SELECT tasks_created FROM agent_share_usage_daily WHERE share_id::text=$1 AND usage_date=(NOW() AT TIME ZONE 'UTC')::date`, access.ShareID).Scan(&used)
			if used >= policy.MaxTasksPerDay {
				err = fmt.Errorf("daily limit reached")
				return
			}
		}
	}
	err = db.QueryRow(`INSERT INTO agent_access_leases(agent_id,subject_user_id,owner_user_id,share_id,policy_version,namespace,expires_at,request_id)
		VALUES($1,$2,$3,$4::uuid,$5,$6,$7,$8) RETURNING ticket_id::text`, access.ID, userID, access.UserID, shareID, access.PolicyVersion, namespace, expiresAt, strings.TrimSpace(requestID)).Scan(&ticketID)
	return
}

func (db *DB) RevokeAccessLease(ticketID string) error {
	_, err := db.Exec(`UPDATE agent_access_leases SET revoked_at=NOW() WHERE ticket_id::text=$1 AND revoked_at IS NULL`, strings.TrimSpace(ticketID))
	return err
}

func (db *DB) AccessLeaseValid(ticketID string, userID int64, access *AgentAccess) bool {
	var ok bool
	err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM agent_access_leases l WHERE l.ticket_id::text=$1 AND l.subject_user_id=$2 AND l.agent_id=$3
		AND l.policy_version=$4 AND l.revoked_at IS NULL AND l.expires_at>NOW())`, ticketID, userID, access.ID, access.PolicyVersion).Scan(&ok)
	return err == nil && ok
}

func (db *DB) RenewAccessRequest(userID int64, access *AgentAccess, requestID string, ttl time.Duration) bool {
	if ttl <= 0 || ttl > 7*24*time.Hour {
		ttl = 24 * time.Hour
	}
	ttlSeconds := int64(ttl.Seconds())
	refreshSeconds := int64((ttl / 2).Seconds())
	var ok bool
	err := db.QueryRow(`WITH candidate AS (
		SELECT ticket_id FROM agent_access_leases l WHERE l.request_id=$1 AND l.subject_user_id=$2 AND l.agent_id=$3
			AND l.policy_version=$4 AND l.revoked_at IS NULL AND l.expires_at>NOW()
	), renewed AS (
		UPDATE agent_access_leases SET expires_at=NOW()+($5 * INTERVAL '1 second')
		WHERE ticket_id IN (SELECT ticket_id FROM candidate)
			AND expires_at<NOW()+($6 * INTERVAL '1 second')
		RETURNING ticket_id
	)
	SELECT EXISTS(SELECT 1 FROM candidate)`, strings.TrimSpace(requestID), userID, access.ID, access.PolicyVersion, ttlSeconds, refreshSeconds).Scan(&ok)
	return err == nil && ok
}

func (db *DB) RecordShareUsage(ctx context.Context, ownerID int64, agentID, requestID, eventID string, tasksCreated int, bytesUploaded, bytesDownloaded int64) (*ShareUsage, error) {
	requestID = strings.TrimSpace(requestID)
	eventID = strings.TrimSpace(eventID)
	if requestID == "" || eventID == "" || len(eventID) > 128 || tasksCreated < 0 || tasksCreated > 1 || bytesUploaded < 0 || bytesDownloaded < 0 {
		return nil, fmt.Errorf("invalid share usage")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var shareID string
	var policyRaw json.RawMessage
	err = tx.QueryRowContext(ctx, `SELECT s.id::text,s.policy FROM agent_access_leases l
		JOIN agents a ON a.id=l.agent_id JOIN agent_shares s ON s.id=l.share_id
		WHERE l.request_id=$1 AND l.owner_user_id=$2 AND a.agent_id=$3
			AND l.share_id IS NOT NULL AND l.issued_at>NOW()-INTERVAL '24 hours'
		ORDER BY l.issued_at DESC LIMIT 1 FOR UPDATE OF s`, requestID, ownerID, strings.TrimSpace(agentID)).Scan(&shareID, &policyRaw)
	if err != nil {
		return nil, err
	}
	usageDate := time.Now().UTC().Format("2006-01-02")
	var duplicate bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_share_usage_events WHERE share_id::text=$1 AND event_id=$2)`, shareID, eventID).Scan(&duplicate); err != nil {
		return nil, err
	}
	if !duplicate && tasksCreated > 0 {
		var policy struct {
			MaxTasksPerDay int `json:"max_tasks_per_day"`
		}
		_ = json.Unmarshal(policyRaw, &policy)
		if policy.MaxTasksPerDay > 0 {
			var used int
			err = tx.QueryRowContext(ctx, `SELECT tasks_created FROM agent_share_usage_daily WHERE share_id::text=$1 AND usage_date=$2`, shareID, usageDate).Scan(&used)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
			if used+tasksCreated > policy.MaxTasksPerDay {
				return nil, ErrShareDailyLimit
			}
		}
	}
	if !duplicate {
		if _, err = tx.ExecContext(ctx, `INSERT INTO agent_share_usage_events(share_id,event_id,usage_date,tasks_created,bytes_uploaded,bytes_downloaded)
			VALUES($1::uuid,$2,$3,$4,$5,$6)`, shareID, eventID, usageDate, tasksCreated, bytesUploaded, bytesDownloaded); err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO agent_share_usage_daily(share_id,usage_date,tasks_created,bytes_uploaded,bytes_downloaded)
			VALUES($1::uuid,$2,$3,$4,$5) ON CONFLICT(share_id,usage_date) DO UPDATE SET
			tasks_created=agent_share_usage_daily.tasks_created+EXCLUDED.tasks_created,
			bytes_uploaded=agent_share_usage_daily.bytes_uploaded+EXCLUDED.bytes_uploaded,
			bytes_downloaded=agent_share_usage_daily.bytes_downloaded+EXCLUDED.bytes_downloaded,updated_at=NOW()`,
			shareID, usageDate, tasksCreated, bytesUploaded, bytesDownloaded); err != nil {
			return nil, err
		}
	}
	var usage ShareUsage
	usage.ShareID, usage.UsageDate = shareID, usageDate
	if err = tx.QueryRowContext(ctx, `SELECT tasks_created,bytes_uploaded,bytes_downloaded FROM agent_share_usage_daily
		WHERE share_id::text=$1 AND usage_date=$2`, shareID, usageDate).Scan(&usage.TasksCreated, &usage.BytesUploaded, &usage.BytesDownloaded); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &usage, nil
}
