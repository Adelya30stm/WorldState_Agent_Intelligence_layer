package worldstate

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"pentagi/pkg/database"
)

type ScopeCandidate struct {
	ID         int64
	EntityKey  string
	Type       string
	Properties json.RawMessage
	Reason     string
	Status     string
	CreatedAt  time.Time
}

func asQueries(db database.Querier) *database.Queries {
	q, _ := db.(*database.Queries)
	return q
}

func LoadScope(ctx context.Context, db database.Querier, flowID int64) (Scope, error) {
	q := asQueries(db)
	if q == nil || flowID <= 0 {
		return Scope{}, nil
	}
	var domainsRaw, cidrsRaw []byte
	err := q.Raw().QueryRowContext(ctx,
		`SELECT domains, cidrs FROM world_state_scope WHERE flow_id = $1`, flowID,
	).Scan(&domainsRaw, &cidrsRaw)
	if err == sql.ErrNoRows {
		return Scope{}, nil
	}
	if err != nil {
		return Scope{}, err
	}
	s := Scope{}
	if len(domainsRaw) > 0 {
		_ = json.Unmarshal(domainsRaw, &s.Domains)
	}
	if len(cidrsRaw) > 0 {
		_ = json.Unmarshal(cidrsRaw, &s.CIDRs)
	}
	return NormalizeScope(s), nil
}

func SaveScope(ctx context.Context, db database.Querier, flowID int64, scope Scope) error {
	q := asQueries(db)
	if q == nil {
		return fmt.Errorf("worldstate: scope store requires *database.Queries")
	}
	scope = NormalizeScope(scope)
	domains, err := marshalStringList(scope.Domains)
	if err != nil {
		return err
	}
	cidrs, err := marshalStringList(scope.CIDRs)
	if err != nil {
		return err
	}
	_, err = q.Raw().ExecContext(ctx, `
		INSERT INTO world_state_scope (flow_id, domains, cidrs, updated_at)
		VALUES ($1, $2, $3, NOW())
		ON CONFLICT (flow_id) DO UPDATE
		SET domains = EXCLUDED.domains, cidrs = EXCLUDED.cidrs, updated_at = NOW()`,
		flowID, domains, cidrs)
	return err
}

func InsertCandidate(ctx context.Context, db database.Querier, flowID int64, c Candidate, reason string) error {
	q := asQueries(db)
	if q == nil || flowID <= 0 || c.Key == "" {
		return nil
	}
	props := []byte("{}")
	if c.Properties != nil {
		var err error
		props, err = json.Marshal(c.Properties)
		if err != nil {
			return err
		}
	}
	_, err := q.Raw().ExecContext(ctx, `
		INSERT INTO world_state_candidates (flow_id, entity_key, type, properties, reason, status)
		VALUES ($1, $2, $3, $4, $5, 'pending')
		ON CONFLICT (flow_id, entity_key) DO NOTHING`,
		flowID, safeEntityKey(c.Key), c.Type, props, reason)
	return err
}

func ListPendingCandidates(ctx context.Context, db database.Querier, flowID int64) ([]ScopeCandidate, error) {
	q := asQueries(db)
	if q == nil {
		return nil, fmt.Errorf("worldstate: scope store requires *database.Queries")
	}
	rows, err := q.Raw().QueryContext(ctx, `
		SELECT id, entity_key, type, properties, reason, status, created_at
		FROM world_state_candidates
		WHERE flow_id = $1 AND status = 'pending'
		ORDER BY created_at ASC`, flowID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ScopeCandidate
	for rows.Next() {
		var c ScopeCandidate
		if err := rows.Scan(&c.ID, &c.EntityKey, &c.Type, &c.Properties, &c.Reason, &c.Status, &c.CreatedAt); err != nil {
			return nil, err
		}
		if len(c.Properties) == 0 {
			c.Properties = json.RawMessage(`{}`)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func GetPendingCandidate(ctx context.Context, db database.Querier, flowID int64, entityKey string) (ScopeCandidate, error) {
	q := asQueries(db)
	if q == nil {
		return ScopeCandidate{}, fmt.Errorf("worldstate: scope store requires *database.Queries")
	}
	var c ScopeCandidate
	err := q.Raw().QueryRowContext(ctx, `
		SELECT id, entity_key, type, properties, reason, status, created_at
		FROM world_state_candidates
		WHERE flow_id = $1 AND entity_key = $2 AND status = 'pending'`,
		flowID, entityKey,
	).Scan(&c.ID, &c.EntityKey, &c.Type, &c.Properties, &c.Reason, &c.Status, &c.CreatedAt)
	if err != nil {
		return ScopeCandidate{}, err
	}
	if len(c.Properties) == 0 {
		c.Properties = json.RawMessage(`{}`)
	}
	return c, nil
}

func SetCandidateStatus(ctx context.Context, db database.Querier, flowID int64, entityKey, status string) (bool, error) {
	q := asQueries(db)
	if q == nil {
		return false, fmt.Errorf("worldstate: scope store requires *database.Queries")
	}
	res, err := q.Raw().ExecContext(ctx, `
		UPDATE world_state_candidates
		SET status = $3
		WHERE flow_id = $1 AND entity_key = $2 AND status = 'pending'`,
		flowID, entityKey, status)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func marshalStringList(items []string) ([]byte, error) {
	if items == nil {
		return []byte("[]"), nil
	}
	return json.Marshal(items)
}

func MergeScopeHosts(ctx context.Context, db database.Querier, flowID int64, hosts []string) error {
	if len(hosts) == 0 {
		return nil
	}
	scope, err := LoadScope(ctx, db, flowID)
	if err != nil {
		return err
	}
	scope.Domains = append(scope.Domains, hosts...)
	return SaveScope(ctx, db, flowID, scope)
}

func CandidateHost(c ScopeCandidate) string {
	var props map[string]any
	_ = json.Unmarshal(c.Properties, &props)
	if host, _ := props["host"].(string); host != "" {
		return canonicalScopeHost(host)
	}
	if c.Type == EntityTypeHost {
		return canonicalScopeHost(strings.TrimPrefix(c.EntityKey, "host:"))
	}
	return ""
}
