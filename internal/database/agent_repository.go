package database

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

func (db *DB) RegisterAgent(userID int64, input AgentInput) (*Agent, error) {
	agentID := strings.TrimSpace(input.AgentID)
	if agentID == "" {
		return nil, fmt.Errorf("agent_id required")
	}
	var preferred any
	if input.PreferredDeviceUID != nil {
		if value := NormalizeDeviceUID(*input.PreferredDeviceUID); value != "" {
			preferred = value
		}
	}
	row := db.QueryRow(`
		INSERT INTO agents(user_id,agent_id,host_name,platform,app_version,preferred_device_uid,
			lan_base_url,public_key,key_id,key_algorithm,capabilities,last_seen_at,created_at,updated_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::jsonb,NOW(),NOW(),NOW())
		ON CONFLICT(user_id,agent_id) DO UPDATE SET
			host_name=CASE WHEN EXCLUDED.host_name<>'' THEN EXCLUDED.host_name ELSE agents.host_name END,
			platform=CASE WHEN EXCLUDED.platform<>'' THEN EXCLUDED.platform ELSE agents.platform END,
			app_version=CASE WHEN EXCLUDED.app_version<>'' THEN EXCLUDED.app_version ELSE agents.app_version END,
			preferred_device_uid=COALESCE(EXCLUDED.preferred_device_uid,agents.preferred_device_uid),
			lan_base_url=CASE WHEN EXCLUDED.lan_base_url<>'' THEN EXCLUDED.lan_base_url ELSE agents.lan_base_url END,
			public_key=CASE WHEN EXCLUDED.public_key<>'' THEN EXCLUDED.public_key ELSE agents.public_key END,
			key_id=CASE WHEN EXCLUDED.key_id<>'' THEN EXCLUDED.key_id ELSE agents.key_id END,
			key_algorithm=CASE WHEN EXCLUDED.key_algorithm<>'' THEN EXCLUDED.key_algorithm ELSE agents.key_algorithm END,
			capabilities=CASE WHEN EXCLUDED.capabilities<>'{}'::jsonb THEN EXCLUDED.capabilities ELSE agents.capabilities END,
			last_seen_at=NOW(),updated_at=NOW()
		RETURNING id,user_id,agent_id,host_name,platform,app_version,preferred_device_uid,last_seen_at,
			lan_base_url,public_key,key_id,key_algorithm,capabilities,created_at,updated_at
	`, userID, agentID, strings.TrimSpace(input.HostName), strings.TrimSpace(input.Platform),
		strings.TrimSpace(input.AppVersion), preferred, strings.TrimSpace(input.LanBaseURL),
		strings.TrimSpace(input.PublicKey), strings.TrimSpace(input.KeyID), strings.TrimSpace(input.KeyAlgorithm),
		string(JSONOrEmpty(input.Capabilities)))
	return scanAgent(row)
}

func (db *DB) ListAgents(userID int64) ([]Agent, error) {
	rows, err := db.Query(`
		SELECT id,user_id,agent_id,host_name,platform,app_version,preferred_device_uid,last_seen_at,
			lan_base_url,public_key,key_id,key_algorithm,capabilities,created_at,updated_at
		FROM agents WHERE user_id=$1 ORDER BY updated_at DESC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Agent, 0)
	for rows.Next() {
		item, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, *item)
	}
	return items, rows.Err()
}

func (db *DB) AgentForUser(userID int64, agentID string) (*Agent, error) {
	row := db.QueryRow(`
		SELECT id,user_id,agent_id,host_name,platform,app_version,preferred_device_uid,last_seen_at,
			lan_base_url,public_key,key_id,key_algorithm,capabilities,created_at,updated_at
		FROM agents WHERE user_id=$1 AND agent_id=$2
	`, userID, strings.TrimSpace(agentID))
	item, err := scanAgent(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return item, err
}

func scanAgent(row interface{ Scan(...any) error }) (*Agent, error) {
	var item Agent
	var preferred sql.NullString
	err := row.Scan(&item.ID, &item.UserID, &item.AgentID, &item.HostName, &item.Platform, &item.AppVersion,
		&preferred, &item.LastSeenAt, &item.LanBaseURL, &item.PublicKey, &item.KeyID, &item.KeyAlgorithm,
		&item.Capabilities, &item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if preferred.Valid {
		item.PreferredDeviceUID = &preferred.String
	}
	item.Online = item.LastSeenAt != nil && time.Since(*item.LastSeenAt) <= AgentOnlineWindow
	return &item, nil
}

func (db *DB) SetPreferredDevice(userID int64, agentID, uid string) error {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		agentID = "default"
	}
	uid = NormalizeDeviceUID(uid)
	var exists bool
	if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM devices WHERE user_id=$1 AND device_uid=$2 AND bind_state='active')`, userID, uid).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return sql.ErrNoRows
	}
	result, err := db.Exec(`UPDATE agents SET preferred_device_uid=$3,updated_at=NOW() WHERE user_id=$1 AND agent_id=$2`, userID, agentID, uid)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (db *DB) PreferredDeviceUID(userID int64, agentID string) (string, error) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		agentID = "default"
	}
	var uid sql.NullString
	err := db.QueryRow(`SELECT preferred_device_uid FROM agents WHERE user_id=$1 AND agent_id=$2`, userID, agentID).Scan(&uid)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if uid.Valid {
		return uid.String, nil
	}
	return "", nil
}

func (db *DB) EnqueueHapiConnection(userID int64, agentID, requestID string, payload json.RawMessage) (*HapiCommand, error) {
	row := db.QueryRow(`
		INSERT INTO hapi_connection_requests(user_id,agent_id,request_id,encrypted_data,status,created_at,expires_at)
		VALUES($1,$2,$3,$4::jsonb,'pending',NOW(),NOW()+INTERVAL '2 minutes')
		ON CONFLICT(user_id,agent_id,request_id) DO UPDATE SET
			encrypted_data=EXCLUDED.encrypted_data,status='pending',result='{}'::jsonb,
			created_at=NOW(),expires_at=NOW()+INTERVAL '2 minutes',claimed_at=NULL,completed_at=NULL
		RETURNING id,user_id,agent_id,'hapi_connection',encrypted_data,status,result,created_at,expires_at,claimed_at,completed_at
	`, userID, strings.TrimSpace(agentID), strings.TrimSpace(requestID), string(JSONOrEmpty(payload)))
	return scanCommand(row)
}

func (db *DB) ClaimHapiConnections(ctx context.Context, userID int64, agentID string, limit int) ([]HapiCommand, error) {
	if limit < 1 || limit > 50 {
		limit = 20
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`UPDATE hapi_connection_requests SET status='expired',completed_at=NOW() WHERE status IN ('pending','claimed') AND expires_at<=NOW()`); err != nil {
		return nil, err
	}
	rows, err := tx.Query(`
		WITH candidates AS (
			SELECT id FROM hapi_connection_requests
			WHERE user_id=$1 AND agent_id=$2 AND status='pending' AND expires_at>NOW()
			ORDER BY id FOR UPDATE SKIP LOCKED LIMIT $3
		)
		UPDATE hapi_connection_requests r SET status='claimed',claimed_at=NOW()
		FROM candidates c WHERE r.id=c.id
		RETURNING r.id,r.user_id,r.agent_id,'hapi_connection',r.encrypted_data,r.status,r.result,
			r.created_at,r.expires_at,r.claimed_at,r.completed_at
	`, userID, strings.TrimSpace(agentID), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]HapiCommand, 0)
	for rows.Next() {
		item, err := scanCommand(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, *item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return items, nil
}

func (db *DB) CompleteHapiConnection(userID, commandID int64, status string, result json.RawMessage) (*HapiCommand, error) {
	row := db.QueryRow(`
		UPDATE hapi_connection_requests SET status=$3,result=$4::jsonb,completed_at=NOW()
		WHERE user_id=$1 AND id=$2 AND status IN ('pending','claimed') AND expires_at>NOW()
		RETURNING id,user_id,agent_id,'hapi_connection',encrypted_data,status,result,created_at,expires_at,claimed_at,completed_at
	`, userID, commandID, status, string(JSONOrEmpty(result)))
	item, err := scanCommand(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return item, err
}

func scanCommand(row interface{ Scan(...any) error }) (*HapiCommand, error) {
	var item HapiCommand
	err := row.Scan(&item.ID, &item.UserID, &item.AgentID, &item.Kind, &item.Payload, &item.Status, &item.Result,
		&item.CreatedAt, &item.ExpiresAt, &item.ClaimedAt, &item.CompletedAt)
	return &item, err
}

func (db *DB) SaveHapiResponse(userID int64, agentID, requestID string, payload json.RawMessage) (*HapiResponse, error) {
	row := db.QueryRow(`
		INSERT INTO hapi_connection_responses(user_id,agent_id,request_id,encrypted_payload,created_at,expires_at)
		VALUES($1,$2,$3,$4::jsonb,NOW(),NOW()+INTERVAL '5 minutes')
		ON CONFLICT(user_id,agent_id,request_id) DO UPDATE SET
			encrypted_payload=EXCLUDED.encrypted_payload,created_at=NOW(),expires_at=NOW()+INTERVAL '5 minutes'
		RETURNING request_id,encrypted_payload,created_at,expires_at
	`, userID, strings.TrimSpace(agentID), strings.TrimSpace(requestID), string(payload))
	var item HapiResponse
	if err := row.Scan(&item.RequestID, &item.EncryptedPayload, &item.CreatedAt, &item.ExpiresAt); err != nil {
		return nil, err
	}
	return &item, nil
}

func (db *DB) HapiResponse(userID int64, agentID, requestID string) (*HapiResponse, error) {
	row := db.QueryRow(`
		SELECT request_id,encrypted_payload,created_at,expires_at FROM hapi_connection_responses
		WHERE user_id=$1 AND agent_id=$2 AND request_id=$3 AND expires_at>NOW()
	`, userID, strings.TrimSpace(agentID), strings.TrimSpace(requestID))
	var item HapiResponse
	err := row.Scan(&item.RequestID, &item.EncryptedPayload, &item.CreatedAt, &item.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &item, err
}

func (db *DB) GetSettings(userID int64) (*Settings, error) {
	_, err := db.Exec(`INSERT INTO user_settings(user_id) VALUES($1) ON CONFLICT(user_id) DO NOTHING`, userID)
	if err != nil {
		return nil, err
	}
	return db.scanSettings(userID)
}

func (db *DB) PutSettings(userID int64, input SettingsInput) (*Settings, error) {
	var notify, locale, stt, extra any
	if input.NotifyEnabled != nil {
		notify = *input.NotifyEnabled
	}
	if input.Locale != nil {
		locale = strings.TrimSpace(*input.Locale)
	}
	if input.STTPreference != nil {
		stt = strings.TrimSpace(*input.STTPreference)
	}
	if len(input.ExtraJSON) > 0 {
		extra = string(JSONOrEmpty(input.ExtraJSON))
	}
	if _, err := db.Exec(`INSERT INTO user_settings(user_id) VALUES($1) ON CONFLICT(user_id) DO NOTHING`, userID); err != nil {
		return nil, err
	}
	_, err := db.Exec(`
		UPDATE user_settings SET notify_enabled=COALESCE($2,notify_enabled),locale=COALESCE($3,locale),
			stt_preference=COALESCE($4,stt_preference),extra_json=COALESCE($5::jsonb,extra_json),updated_at=NOW()
		WHERE user_id=$1
	`, userID, notify, locale, stt, extra)
	if err != nil {
		return nil, err
	}
	return db.scanSettings(userID)
}

func (db *DB) scanSettings(userID int64) (*Settings, error) {
	var item Settings
	err := db.QueryRow(`SELECT user_id,notify_enabled,locale,stt_preference,extra_json,created_at,updated_at FROM user_settings WHERE user_id=$1`, userID).
		Scan(&item.UserID, &item.NotifyEnabled, &item.Locale, &item.STTPreference, &item.ExtraJSON, &item.CreatedAt, &item.UpdatedAt)
	return &item, err
}

func (db *DB) RotatePair(userID int64, uid, publicID, secret string) (*Device, string, error) {
	uid = NormalizeDeviceUID(uid)
	if uid == "" {
		return nil, "", fmt.Errorf("device_uid required")
	}
	if strings.TrimSpace(publicID) == "" {
		value, err := RandomToken(12)
		if err != nil {
			return nil, "", err
		}
		publicID = value
	}
	if strings.TrimSpace(secret) == "" {
		value, err := RandomToken(32)
		if err != nil {
			return nil, "", err
		}
		secret = value
	}
	row := db.QueryRow(`
		UPDATE devices SET pair_public_id=$3,pair_secret_hash=$4,updated_at=NOW()
		WHERE user_id=$1 AND device_uid=$2 AND bind_state='active'
		RETURNING id,user_id,device_uid,display_name,hostname,fw_version,host(last_ip)::text,
			last_seen_at,bind_state,pair_public_id,capabilities,lan_hint,created_at,updated_at,revoked_at
	`, userID, uid, strings.TrimSpace(publicID), HashSecret(strings.TrimSpace(secret)))
	item, err := nullableDevice(row)
	return item, secret, err
}

func (db *DB) CreatePairingClaim(userID int64, uid string, ttlSeconds int) (*PairingClaim, error) {
	uid = NormalizeDeviceUID(uid)
	if uid == "" {
		return nil, fmt.Errorf("device_uid required")
	}
	var exists bool
	if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM devices WHERE user_id=$1 AND device_uid=$2 AND bind_state='active')`, userID, uid).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, sql.ErrNoRows
	}
	if ttlSeconds < 30 || ttlSeconds > 600 {
		ttlSeconds = 120
	}
	for attempt := 0; attempt < 5; attempt++ {
		n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
		if err != nil {
			return nil, err
		}
		code := fmt.Sprintf("%06d", n.Int64())
		var item PairingClaim
		err = db.QueryRow(`
			INSERT INTO pairing_claims(user_id,device_uid,claim_code,expires_at)
			VALUES($1,$2,$3,NOW()+($4*INTERVAL '1 second'))
			RETURNING claim_code,device_uid,expires_at,created_at
		`, userID, uid, code, ttlSeconds).Scan(&item.ClaimCode, &item.DeviceUID, &item.ExpiresAt, &item.CreatedAt)
		if err == nil {
			return &item, nil
		}
	}
	return nil, fmt.Errorf("unable to allocate claim code")
}

func (db *DB) ConfirmPairingClaim(ctx context.Context, userID int64, code, agentID string) (*Device, *PairingClaim, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	var claim PairingClaim
	err = tx.QueryRow(`
		UPDATE pairing_claims SET consumed_at=NOW(),consumed_by_agent_id=$3
		WHERE user_id=$1 AND claim_code=$2 AND consumed_at IS NULL AND expires_at>NOW()
		RETURNING claim_code,device_uid,expires_at,created_at,consumed_by_agent_id
	`, userID, strings.TrimSpace(code), strings.TrimSpace(agentID)).Scan(
		&claim.ClaimCode, &claim.DeviceUID, &claim.ExpiresAt, &claim.CreatedAt, &claim.ConsumedByAgentID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	row := tx.QueryRow(`
		SELECT id,user_id,device_uid,display_name,hostname,fw_version,host(last_ip)::text,
			last_seen_at,bind_state,pair_public_id,capabilities,lan_hint,created_at,updated_at,revoked_at
		FROM devices WHERE user_id=$1 AND device_uid=$2 AND bind_state='active'
	`, userID, claim.DeviceUID)
	device, err := scanDevice(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	return device, &claim, nil
}
