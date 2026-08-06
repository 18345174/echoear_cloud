package database

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/lib/pq"
)

const (
	AgentOnlineWindow = 2 * time.Minute
	CommandTTL        = 2 * time.Minute
	ResponseTTL       = 5 * time.Minute
	DeviceAccessTTL   = 5 * time.Hour
	DeviceRefreshTTL  = 100 * 365 * 24 * time.Hour
	RoleAdmin         = "admin"
	RoleUser          = "user"
)

var ErrUsernameExists = errors.New("username already exists")

type Session struct {
	UserID     int64
	Username   string
	Role       string
	SessionID  string
	LastSeenAt time.Time
	ExpiresAt  time.Time
}

type User struct {
	ID              int64     `json:"id"`
	Username        string    `json:"username"`
	Email           string    `json:"email"`
	Role            string    `json:"role"`
	PasswordChanged bool      `json:"password_changed"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type CreateUserInput struct {
	Username     string
	PasswordHash string
	Email        string
	Role         string
}

type Device struct {
	ID           int64           `json:"id"`
	UserID       int64           `json:"user_id"`
	DeviceUID    string          `json:"device_uid"`
	DisplayName  string          `json:"display_name"`
	Hostname     string          `json:"hostname"`
	FWVersion    string          `json:"fw_version"`
	LastIP       *string         `json:"last_ip"`
	LastSeenAt   *time.Time      `json:"last_seen_at"`
	BindState    string          `json:"bind_state"`
	PairPublicID string          `json:"pair_public_id"`
	Capabilities json.RawMessage `json:"capabilities"`
	LanHint      json.RawMessage `json:"lan_hint"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
	RevokedAt    *time.Time      `json:"revoked_at,omitempty"`
}

type DeviceInput struct {
	DeviceUID    string
	DisplayName  string
	Hostname     string
	FWVersion    string
	LastIP       string
	PairPublicID string
	PairSecret   string
	Capabilities json.RawMessage
	LanHint      json.RawMessage
}

type DevicePatch struct {
	DisplayName  *string
	Hostname     *string
	FWVersion    *string
	Capabilities json.RawMessage
	LanHint      json.RawMessage
}

type Heartbeat struct {
	LastIP    string
	Hostname  string
	FWVersion string
}

type DeviceTokens struct {
	RefreshToken     string    `json:"refresh_token,omitempty"`
	RefreshExpiresAt time.Time `json:"refresh_expires_at"`
	AccessToken      string    `json:"access_token"`
	AccessExpiresAt  time.Time `json:"access_expires_at"`
	AccessExpiresIn  int64     `json:"access_expires_in"`
	TokenType        string    `json:"token_type"`
}

type Agent struct {
	ID                 int64           `json:"id"`
	UserID             int64           `json:"user_id"`
	AgentID            string          `json:"agent_id"`
	HostName           string          `json:"host_name"`
	Platform           string          `json:"platform"`
	AppVersion         string          `json:"app_version"`
	PreferredDeviceUID *string         `json:"preferred_device_uid"`
	LastSeenAt         *time.Time      `json:"last_seen_at"`
	LanBaseURL         string          `json:"lan_base_url"`
	PublicKey          string          `json:"public_key"`
	KeyID              string          `json:"key_id"`
	KeyAlgorithm       string          `json:"key_algorithm"`
	Capabilities       json.RawMessage `json:"capabilities"`
	Online             bool            `json:"online"`
	CreatedAt          time.Time       `json:"created_at"`
	UpdatedAt          time.Time       `json:"updated_at"`
}

type AgentInput struct {
	AgentID            string
	HostName           string
	Platform           string
	AppVersion         string
	PreferredDeviceUID *string
	LanBaseURL         string
	PublicKey          string
	KeyID              string
	KeyAlgorithm       string
	Capabilities       json.RawMessage
}

type HapiCommand struct {
	ID          int64           `json:"id"`
	UserID      int64           `json:"user_id"`
	AgentID     string          `json:"agent_id"`
	Kind        string          `json:"kind"`
	Payload     json.RawMessage `json:"payload"`
	Status      string          `json:"status"`
	Result      json.RawMessage `json:"result"`
	CreatedAt   time.Time       `json:"created_at"`
	ExpiresAt   time.Time       `json:"expires_at"`
	ClaimedAt   *time.Time      `json:"claimed_at,omitempty"`
	CompletedAt *time.Time      `json:"completed_at,omitempty"`
}

type HapiResponse struct {
	RequestID        string          `json:"request_id"`
	EncryptedPayload json.RawMessage `json:"encrypted_payload"`
	CreatedAt        time.Time       `json:"created_at"`
	ExpiresAt        time.Time       `json:"expires_at"`
}

type Settings struct {
	UserID        int64           `json:"user_id"`
	NotifyEnabled bool            `json:"notify_enabled"`
	Locale        string          `json:"locale"`
	STTPreference string          `json:"stt_preference"`
	ExtraJSON     json.RawMessage `json:"extra_json"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

type SettingsInput struct {
	NotifyEnabled *bool
	Locale        *string
	STTPreference *string
	ExtraJSON     json.RawMessage
}

type PairingClaim struct {
	ClaimCode         string    `json:"claim_code"`
	DeviceUID         string    `json:"device_uid"`
	ExpiresAt         time.Time `json:"expires_at"`
	CreatedAt         time.Time `json:"created_at"`
	ConsumedByAgentID string    `json:"consumed_by_agent_id,omitempty"`
}

func HashSecret(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func RandomToken(bytes int) (string, error) {
	raw := make([]byte, bytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func NormalizeDeviceUID(value string) string {
	return strings.ToUpper(strings.TrimSpace(value))
}

func JSONOrEmpty(raw json.RawMessage) json.RawMessage {
	var object map[string]any
	if len(raw) == 0 || json.Unmarshal(raw, &object) != nil || object == nil {
		return json.RawMessage(`{}`)
	}
	value, _ := json.Marshal(object)
	return value
}

func NullableIP(value string) (any, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	if net.ParseIP(value) == nil {
		return nil, fmt.Errorf("invalid last_ip")
	}
	return value, nil
}

func (db *DB) CreateSession(userID int64, ip, userAgent string, ttl time.Duration) (string, time.Time, error) {
	token, err := RandomToken(32)
	if err != nil {
		return "", time.Time{}, err
	}
	expiresAt := time.Now().UTC().Add(ttl)
	_, err = db.Exec(`
		INSERT INTO user_sessions(user_id, session_hash, ip_address, user_agent, expires_at)
		VALUES ($1, $2, $3, $4, $5)
	`, userID, HashSecret(token), strings.TrimSpace(ip), strings.TrimSpace(userAgent), expiresAt)
	return token, expiresAt, err
}

func (db *DB) SessionByToken(token string) (*Session, error) {
	var item Session
	err := db.QueryRow(`
		SELECT s.user_id, u.username, u.role, s.last_seen_at, s.expires_at
		FROM user_sessions s JOIN users u ON u.id = s.user_id
		WHERE s.session_hash = $1 AND s.status = 'active' AND s.expires_at > NOW()
	`, HashSecret(strings.TrimSpace(token))).Scan(
		&item.UserID, &item.Username, &item.Role, &item.LastSeenAt, &item.ExpiresAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	item.SessionID = token
	return &item, nil
}

func (db *DB) TouchSession(token string) error {
	_, err := db.Exec(`UPDATE user_sessions SET last_seen_at = NOW(), updated_at = NOW() WHERE session_hash = $1`, HashSecret(token))
	return err
}

func (db *DB) RevokeSession(token string) error {
	_, err := db.Exec(`
		UPDATE user_sessions SET status = 'revoked', revoked_at = NOW(), updated_at = NOW()
		WHERE session_hash = $1 AND status = 'active'
	`, HashSecret(token))
	return err
}

func (db *DB) CreateUser(ctx context.Context, input CreateUserInput) (*User, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var user User
	err = tx.QueryRowContext(ctx, `
		INSERT INTO users(username, password_hash, email, role, password_changed)
		VALUES ($1, $2, $3, $4, FALSE)
		RETURNING id, username, email, role, password_changed, created_at, updated_at
	`, strings.TrimSpace(input.Username), input.PasswordHash, strings.TrimSpace(input.Email), input.Role).Scan(
		&user.ID, &user.Username, &user.Email, &user.Role, &user.PasswordChanged, &user.CreatedAt, &user.UpdatedAt,
	)
	if err != nil {
		var pqErr *pq.Error
		if errors.As(err, &pqErr) && pqErr.Code == "23505" {
			return nil, ErrUsernameExists
		}
		return nil, err
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO user_settings(user_id) VALUES ($1)
		ON CONFLICT (user_id) DO NOTHING
	`, user.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &user, nil
}

func (db *DB) BindDevice(ctx context.Context, userID int64, input DeviceInput) (*Device, bool, error) {
	uid := NormalizeDeviceUID(input.DeviceUID)
	if uid == "" {
		return nil, false, fmt.Errorf("device_uid required")
	}
	ip, err := NullableIP(input.LastIP)
	if err != nil {
		return nil, false, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	var currentUser int64
	var state string
	err = tx.QueryRow(`SELECT user_id, bind_state FROM devices WHERE device_uid = $1 FOR UPDATE`, uid).Scan(&currentUser, &state)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}
	if err == nil && currentUser != userID && state != "revoked" {
		return nil, true, nil
	}
	pairHash := any(nil)
	if strings.TrimSpace(input.PairSecret) != "" {
		pairHash = HashSecret(strings.TrimSpace(input.PairSecret))
	}
	row := tx.QueryRow(`
		INSERT INTO devices(
			user_id, device_uid, display_name, hostname, fw_version, last_ip,
			last_seen_at, bind_state, pair_public_id, pair_secret_hash,
			capabilities, lan_hint, created_at, updated_at, revoked_at
		) VALUES ($1,$2,$3,$4,$5,$6,NOW(),'active',$7,$8,$9::jsonb,$10::jsonb,NOW(),NOW(),NULL)
		ON CONFLICT (device_uid) DO UPDATE SET
			user_id = EXCLUDED.user_id,
			display_name = CASE WHEN EXCLUDED.display_name <> '' THEN EXCLUDED.display_name ELSE devices.display_name END,
			hostname = CASE WHEN EXCLUDED.hostname <> '' THEN EXCLUDED.hostname ELSE devices.hostname END,
			fw_version = CASE WHEN EXCLUDED.fw_version <> '' THEN EXCLUDED.fw_version ELSE devices.fw_version END,
			last_ip = COALESCE(EXCLUDED.last_ip, devices.last_ip),
			last_seen_at = NOW(), bind_state = 'active', revoked_at = NULL,
			pair_public_id = CASE WHEN EXCLUDED.pair_public_id <> '' THEN EXCLUDED.pair_public_id ELSE devices.pair_public_id END,
			pair_secret_hash = COALESCE(EXCLUDED.pair_secret_hash, devices.pair_secret_hash),
			capabilities = CASE WHEN EXCLUDED.capabilities <> '{}'::jsonb THEN EXCLUDED.capabilities ELSE devices.capabilities END,
			lan_hint = CASE WHEN EXCLUDED.lan_hint <> '{}'::jsonb THEN EXCLUDED.lan_hint ELSE devices.lan_hint END,
			updated_at = NOW()
		RETURNING id,user_id,device_uid,display_name,hostname,fw_version,host(last_ip)::text,
			last_seen_at,bind_state,pair_public_id,capabilities,lan_hint,created_at,updated_at,revoked_at
	`, userID, uid, strings.TrimSpace(input.DisplayName), strings.TrimSpace(input.Hostname),
		strings.TrimSpace(input.FWVersion), ip, strings.TrimSpace(input.PairPublicID), pairHash,
		string(JSONOrEmpty(input.Capabilities)), string(JSONOrEmpty(input.LanHint)))
	item, err := scanDevice(row)
	if err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return item, false, nil
}

func (db *DB) ListDevices(userID int64, bindState string, includeRevoked bool) ([]Device, error) {
	query := `
		SELECT id,user_id,device_uid,display_name,hostname,fw_version,host(last_ip)::text,
			last_seen_at,bind_state,pair_public_id,capabilities,lan_hint,created_at,updated_at,revoked_at
		FROM devices WHERE user_id = $1`
	args := []any{userID}
	if bindState = strings.TrimSpace(bindState); bindState != "" {
		query += ` AND bind_state = $2`
		args = append(args, bindState)
	} else if !includeRevoked {
		query += ` AND bind_state <> 'revoked'`
	}
	query += ` ORDER BY updated_at DESC`
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Device, 0)
	for rows.Next() {
		item, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, *item)
	}
	return items, rows.Err()
}

func (db *DB) DeviceForUser(userID int64, uid string) (*Device, error) {
	row := db.QueryRow(`
		SELECT id,user_id,device_uid,display_name,hostname,fw_version,host(last_ip)::text,
			last_seen_at,bind_state,pair_public_id,capabilities,lan_hint,created_at,updated_at,revoked_at
		FROM devices WHERE user_id = $1 AND device_uid = $2
	`, userID, NormalizeDeviceUID(uid))
	return nullableDevice(row)
}

func (db *DB) PatchDevice(userID int64, uid string, input DevicePatch) (*Device, error) {
	var display, hostname, firmware any
	if input.DisplayName != nil {
		display = strings.TrimSpace(*input.DisplayName)
	}
	if input.Hostname != nil {
		hostname = strings.TrimSpace(*input.Hostname)
	}
	if input.FWVersion != nil {
		firmware = strings.TrimSpace(*input.FWVersion)
	}
	capabilities := any(nil)
	if len(input.Capabilities) > 0 {
		capabilities = string(JSONOrEmpty(input.Capabilities))
	}
	lanHint := any(nil)
	if len(input.LanHint) > 0 {
		lanHint = string(JSONOrEmpty(input.LanHint))
	}
	row := db.QueryRow(`
		UPDATE devices SET display_name=COALESCE($3,display_name), hostname=COALESCE($4,hostname),
			fw_version=COALESCE($5,fw_version), capabilities=COALESCE($6::jsonb,capabilities),
			lan_hint=COALESCE($7::jsonb,lan_hint), updated_at=NOW()
		WHERE user_id=$1 AND device_uid=$2
		RETURNING id,user_id,device_uid,display_name,hostname,fw_version,host(last_ip)::text,
			last_seen_at,bind_state,pair_public_id,capabilities,lan_hint,created_at,updated_at,revoked_at
	`, userID, NormalizeDeviceUID(uid), display, hostname, firmware, capabilities, lanHint)
	return nullableDevice(row)
}

func (db *DB) HeartbeatDevice(userID int64, uid string, input Heartbeat) (*Device, error) {
	ip, err := NullableIP(input.LastIP)
	if err != nil {
		return nil, err
	}
	row := db.QueryRow(`
		UPDATE devices SET last_ip=COALESCE($3,last_ip),
			hostname=CASE WHEN $4 <> '' THEN $4 ELSE hostname END,
			fw_version=CASE WHEN $5 <> '' THEN $5 ELSE fw_version END,
			last_seen_at=NOW(),updated_at=NOW()
		WHERE user_id=$1 AND device_uid=$2 AND bind_state='active'
		RETURNING id,user_id,device_uid,display_name,hostname,fw_version,host(last_ip)::text,
			last_seen_at,bind_state,pair_public_id,capabilities,lan_hint,created_at,updated_at,revoked_at
	`, userID, NormalizeDeviceUID(uid), ip, strings.TrimSpace(input.Hostname), strings.TrimSpace(input.FWVersion))
	return nullableDevice(row)
}

func (db *DB) IssueDeviceTokens(userID int64, uid string) (*DeviceTokens, error) {
	refresh, err := RandomToken(32)
	if err != nil {
		return nil, err
	}
	access, err := RandomToken(32)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	bundle := &DeviceTokens{
		RefreshToken: refresh, RefreshExpiresAt: now.Add(DeviceRefreshTTL),
		AccessToken: access, AccessExpiresAt: now.Add(DeviceAccessTTL),
		AccessExpiresIn: int64(DeviceAccessTTL.Seconds()), TokenType: "Bearer",
	}
	result, err := db.Exec(`
		UPDATE devices SET device_refresh_token_hash=$3,device_refresh_expires_at=$4,
			device_access_token_hash=$5,device_access_expires_at=$6,updated_at=NOW()
		WHERE user_id=$1 AND device_uid=$2 AND bind_state='active'
	`, userID, NormalizeDeviceUID(uid), HashSecret(refresh), bundle.RefreshExpiresAt, HashSecret(access), bundle.AccessExpiresAt)
	if err != nil {
		return nil, err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return nil, nil
	}
	return bundle, nil
}

func (db *DB) ExchangeRefresh(uid, refresh string) (*DeviceTokens, error) {
	uid, refresh = NormalizeDeviceUID(uid), strings.TrimSpace(refresh)
	if uid == "" || refresh == "" {
		return nil, fmt.Errorf("device_uid and refresh_token required")
	}
	var userID int64
	var expires time.Time
	err := db.QueryRow(`
		SELECT user_id,device_refresh_expires_at FROM devices
		WHERE device_uid=$1 AND bind_state='active' AND device_refresh_token_hash=$2
	`, uid, HashSecret(refresh)).Scan(&userID, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("invalid_refresh")
	}
	if err != nil {
		return nil, err
	}
	if time.Now().After(expires) {
		return nil, fmt.Errorf("refresh_expired")
	}
	access, err := RandomToken(32)
	if err != nil {
		return nil, err
	}
	accessExpires := time.Now().UTC().Add(DeviceAccessTTL)
	_, err = db.Exec(`UPDATE devices SET device_access_token_hash=$2,device_access_expires_at=$3,updated_at=NOW() WHERE device_uid=$1`, uid, HashSecret(access), accessExpires)
	if err != nil {
		return nil, err
	}
	return &DeviceTokens{RefreshExpiresAt: expires, AccessToken: access, AccessExpiresAt: accessExpires, AccessExpiresIn: int64(DeviceAccessTTL.Seconds()), TokenType: "Bearer"}, nil
}

func (db *DB) PresenceByAccess(access string, input Heartbeat) (*Device, error) {
	ip, err := NullableIP(input.LastIP)
	if err != nil {
		return nil, err
	}
	row := db.QueryRow(`
		UPDATE devices SET last_ip=COALESCE($2,last_ip),
			hostname=CASE WHEN $3<>'' THEN $3 ELSE hostname END,
			fw_version=CASE WHEN $4<>'' THEN $4 ELSE fw_version END,
			last_seen_at=NOW(),updated_at=NOW()
		WHERE device_access_token_hash=$1 AND device_access_expires_at>NOW() AND bind_state='active'
		RETURNING id,user_id,device_uid,display_name,hostname,fw_version,host(last_ip)::text,
			last_seen_at,bind_state,pair_public_id,capabilities,lan_hint,created_at,updated_at,revoked_at
	`, HashSecret(strings.TrimSpace(access)), ip, strings.TrimSpace(input.Hostname), strings.TrimSpace(input.FWVersion))
	return nullableDevice(row)
}

func (db *DB) RevokeDevice(userID int64, uid string, pending bool) (*Device, error) {
	row := db.QueryRow(`
		UPDATE devices SET bind_state='revoked',revoked_at=NOW(),updated_at=NOW(),
			lan_hint=jsonb_set(COALESCE(lan_hint,'{}'::jsonb),'{pending_factory_reset}',to_jsonb($3::boolean),true),
			device_refresh_token_hash=NULL,device_refresh_expires_at=NULL,
			device_access_token_hash=NULL,device_access_expires_at=NULL
		WHERE user_id=$1 AND device_uid=$2 AND bind_state<>'revoked'
		RETURNING id,user_id,device_uid,display_name,hostname,fw_version,host(last_ip)::text,
			last_seen_at,bind_state,pair_public_id,capabilities,lan_hint,created_at,updated_at,revoked_at
	`, userID, NormalizeDeviceUID(uid), pending)
	return nullableDevice(row)
}

func (db *DB) ListPendingReset(userID int64) ([]Device, error) {
	rows, err := db.Query(`
		SELECT id,user_id,device_uid,display_name,hostname,fw_version,host(last_ip)::text,
			last_seen_at,bind_state,pair_public_id,capabilities,lan_hint,created_at,updated_at,revoked_at
		FROM devices WHERE user_id=$1 AND COALESCE((lan_hint->>'pending_factory_reset')::boolean,false)=true
		ORDER BY updated_at DESC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Device, 0)
	for rows.Next() {
		item, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, *item)
	}
	return items, rows.Err()
}

func (db *DB) AckPendingReset(userID int64, uid string) (*Device, error) {
	row := db.QueryRow(`
		UPDATE devices SET lan_hint=jsonb_set(COALESCE(lan_hint,'{}'::jsonb),'{pending_factory_reset}','false'::jsonb,true),updated_at=NOW()
		WHERE user_id=$1 AND device_uid=$2
		RETURNING id,user_id,device_uid,display_name,hostname,fw_version,host(last_ip)::text,
			last_seen_at,bind_state,pair_public_id,capabilities,lan_hint,created_at,updated_at,revoked_at
	`, userID, NormalizeDeviceUID(uid))
	return nullableDevice(row)
}

func scanDevice(row interface{ Scan(...any) error }) (*Device, error) {
	var item Device
	var ip sql.NullString
	err := row.Scan(&item.ID, &item.UserID, &item.DeviceUID, &item.DisplayName, &item.Hostname, &item.FWVersion, &ip,
		&item.LastSeenAt, &item.BindState, &item.PairPublicID, &item.Capabilities, &item.LanHint,
		&item.CreatedAt, &item.UpdatedAt, &item.RevokedAt)
	if err != nil {
		return nil, err
	}
	if ip.Valid {
		item.LastIP = &ip.String
	}
	return &item, nil
}

func nullableDevice(row interface{ Scan(...any) error }) (*Device, error) {
	item, err := scanDevice(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return item, err
}
