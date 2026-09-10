package worldstate

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"pentagi/pkg/database"

	_ "github.com/lib/pq"
)

type storeFixture struct {
	db     *sql.DB
	q      *database.Queries
	flowID int64
	userID int64
}

func TestStoreJournalsConcurrentMutations(t *testing.T) {
	f := newStoreFixture(t)
	ctx := context.Background()
	var hints atomic.Int32
	store := NewStore(f.q, func(_ context.Context, flowID int64) error {
		if flowID != f.flowID {
			t.Errorf("hint flow = %d, want %d", flowID, f.flowID)
		}
		hints.Add(1)
		return errors.New("optional hint unavailable")
	})

	t.Run("create merge no-op and rollback", func(t *testing.T) {
		key := uniqueKey("host:journal")
		props := map[string]any{"host": "journal.example.invalid", "port": 443, "nested": map[string]string{"api_key": "sensitive-value"}}
		entity, err := store.Observe(ctx, f.flowID, EntityTypeHost, key, AgentExecutor, props, map[string]any{"Authorization": "Bearer sensitive-value"})
		if err != nil {
			t.Fatal(err)
		}
		if entity.State != database.WorldStateLifecycleDiscovered || hints.Load() != 1 {
			t.Fatalf("state=%s hints=%d", entity.State, hints.Load())
		}
		if _, err := store.Observe(ctx, f.flowID, EntityTypeHost, key, AgentExecutor, props, nil); err != nil {
			t.Fatal(err)
		}
		if hints.Load() != 1 || eventCount(t, f.db, f.flowID, key) != 2 {
			t.Fatalf("idempotent observe produced noise: hints=%d events=%d", hints.Load(), eventCount(t, f.db, f.flowID, key))
		}
		entity, err = store.Observe(ctx, f.flowID, EntityTypeHost, key, AgentExecutor, map[string]any{"service": "https"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		assertJSONFields(t, entity.Properties, "host", "port", "service", "nested")
		if hints.Load() != 2 || eventCount(t, f.db, f.flowID, key) != 3 {
			t.Fatalf("merge coverage: hints=%d events=%d", hints.Load(), eventCount(t, f.db, f.flowID, key))
		}
		assertNoStoredSecret(t, f.db, f.flowID, entity.ID, "sensitive-value")

		rollbackKey := uniqueKey("host:rollback")
		if _, err := store.Observe(ctx, f.flowID, EntityTypeHost, rollbackKey, " ", map[string]any{"host": "rollback.invalid"}, nil); err == nil {
			t.Fatal("expected journal constraint failure")
		}
		assertEntityMissing(t, f.q, f.flowID, rollbackKey)
		if hints.Load() != 2 {
			t.Fatalf("rolled back write emitted hint: %d", hints.Load())
		}

		beforeTransitions := transitionCount(t, f.db, entity.ID)
		beforeEvents := eventCount(t, f.db, f.flowID, key)
		if _, err := store.Transition(ctx, entity.ID, StateScanning, " ", json.RawMessage(`{"token":"sensitive-value"}`)); err == nil {
			t.Fatal("expected transition journal constraint failure")
		}
		entity, err = f.q.GetWorldStateEntityByID(ctx, entity.ID)
		if err != nil || entity.State != database.WorldStateLifecycleDiscovered {
			t.Fatalf("transition rollback state=%s err=%v", entity.State, err)
		}
		if transitionCount(t, f.db, entity.ID) != beforeTransitions || eventCount(t, f.db, f.flowID, key) != beforeEvents {
			t.Fatal("transition rollback left audit or journal state")
		}
	})

	t.Run("journal projects bounded properties without changing domain properties", func(t *testing.T) {
		key := uniqueKey("host:minimal-facts")
		rawOutput := strings.Repeat("arbitrary raw tool output ", 100)
		properties := map[string]any{
			"host":       "minimal-facts.example.invalid",
			"port":       443,
			"output":     rawOutput,
			"transcript": "short arbitrary transcript",
			"nested":     map[string]any{"host": "nested.example.invalid"},
			"title":      strings.Repeat("x", maxJournalPropertyStringBytes+1),
		}
		entity, err := store.Observe(ctx, f.flowID, EntityTypeHost, key, AgentExecutor, properties, nil)
		if err != nil {
			t.Fatal(err)
		}
		assertJSONFields(t, entity.Properties, "host", "port", "output", "transcript", "nested", "title")

		var facts json.RawMessage
		if err := f.db.QueryRow(`SELECT facts FROM world_state_events WHERE flow_id=$1 AND kind='entity_upserted' AND facts->>'entity_key'=$2 ORDER BY revision LIMIT 1`, f.flowID, key).Scan(&facts); err != nil {
			t.Fatal(err)
		}
		var event map[string]any
		if err := json.Unmarshal(facts, &event); err != nil {
			t.Fatal(err)
		}
		projected, ok := event["properties"].(map[string]any)
		if !ok {
			t.Fatalf("journal properties = %T, want object", event["properties"])
		}
		if projected["host"] != properties["host"] || projected["port"] != float64(443) {
			t.Fatalf("journal omitted safe minimal properties: %v", projected)
		}
		for _, excluded := range []string{"output", "transcript", "nested", "title"} {
			if _, ok := projected[excluded]; ok {
				t.Fatalf("journal contains excluded property %q: %v", excluded, projected)
			}
		}
		encoded, err := json.Marshal(projected)
		if err != nil {
			t.Fatal(err)
		}
		if len(encoded) > maxJournalPropertiesBytes || strings.Contains(string(facts), rawOutput) {
			t.Fatalf("journal properties exceed safety boundary: %d bytes", len(encoded))
		}
	})

	t.Run("interleaved entity writers preserve properties", func(t *testing.T) {
		key := uniqueKey("host:concurrent")
		const writers = 8
		var wg sync.WaitGroup
		errs := make(chan error, writers)
		for i := 0; i < writers; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				_, err := store.Observe(ctx, f.flowID, EntityTypeHost, key, AgentResearcher, map[string]any{fmt.Sprintf("field_%d", i): i}, nil)
				errs <- err
			}(i)
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		entity, err := f.q.GetWorldStateEntityByKey(ctx, database.GetWorldStateEntityByKeyParams{FlowID: f.flowID, EntityKey: key})
		if err != nil {
			t.Fatal(err)
		}
		fields := make([]string, writers)
		for i := range fields {
			fields[i] = fmt.Sprintf("field_%d", i)
		}
		assertJSONFields(t, entity.Properties, fields...)
		if got := eventCount(t, f.db, f.flowID, key); got != writers+1 {
			t.Fatalf("events=%d want=%d", got, writers+1)
		}
	})

	t.Run("concurrent transitions are revalidated", func(t *testing.T) {
		entity, err := store.Observe(ctx, f.flowID, EntityTypeHost, uniqueKey("host:transition"), AgentSystem, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		results := make(chan error, 2)
		for range 2 {
			go func() {
				_, err := store.Transition(ctx, entity.ID, StateScanning, AgentExecutor, json.RawMessage(`{"password":"sensitive-value"}`))
				results <- err
			}()
		}
		successes, invalid := 0, 0
		for range 2 {
			err := <-results
			switch {
			case err == nil:
				successes++
			case errors.Is(err, ErrInvalidTransition):
				invalid++
			default:
				t.Fatal(err)
			}
		}
		if successes != 1 || invalid != 1 || transitionCount(t, f.db, entity.ID) != 2 {
			t.Fatalf("successes=%d invalid=%d transitions=%d", successes, invalid, transitionCount(t, f.db, entity.ID))
		}
		assertNoStoredSecret(t, f.db, f.flowID, entity.ID, "sensitive-value")
	})

	t.Run("link create update no-op and concurrent merge", func(t *testing.T) {
		source, err := store.Observe(ctx, f.flowID, EntityTypeEndpoint, uniqueKey("endpoint:source"), AgentSystem, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		target, err := store.Observe(ctx, f.flowID, EntityTypeHost, uniqueKey("host:target"), AgentSystem, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		link, err := store.Link(ctx, f.flowID, source.ID, target.ID, "found_on", map[string]any{
			"tool": "nmap", "auth": "sensitive-link-value",
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Link(ctx, f.flowID, source.ID, target.ID, "found_on", map[string]any{
			"tool": "nmap", "auth": "sensitive-link-value",
		}); err != nil {
			t.Fatal(err)
		}
		const writers = 6
		var wg sync.WaitGroup
		for i := 0; i < writers; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				if _, err := store.Link(ctx, f.flowID, source.ID, target.ID, "found_on", map[string]any{fmt.Sprintf("link_%d", i): i}); err != nil {
					t.Errorf("link writer: %v", err)
				}
			}(i)
		}
		wg.Wait()
		var count int
		if err := f.db.QueryRow(`SELECT count(*) FROM world_state_links WHERE flow_id=$1 AND source_id=$2 AND target_id=$3 AND type='found_on'`, f.flowID, source.ID, target.ID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("logical links=%d", count)
		}
		link, err = f.q.LockWorldStateLink(ctx, database.LockWorldStateLinkParams{FlowID: f.flowID, SourceID: source.ID, TargetID: target.ID, Type: "found_on"})
		if err != nil {
			t.Fatal(err)
		}
		fields := []string{"tool"}
		for i := 0; i < writers; i++ {
			fields = append(fields, fmt.Sprintf("link_%d", i))
		}
		assertJSONFields(t, link.Properties, fields...)
		if strings.Contains(string(link.Properties), "sensitive-link-value") {
			t.Fatal("link properties contain credential material")
		}
		if got := linkEventCount(t, f.db, f.flowID, link.ID); got != writers+1 {
			t.Fatalf("link events=%d want=%d", got, writers+1)
		}
	})

	t.Run("link journal failure rolls back domain and hint", func(t *testing.T) {
		source, err := store.Observe(ctx, f.flowID, EntityTypeEndpoint, uniqueKey("endpoint:rollback"), AgentSystem, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		target, err := store.Observe(ctx, f.flowID, EntityTypeHost, uniqueKey("host:rollback-target"), AgentSystem, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		beforeHints := hints.Load()
		installLinkEventFailure(t, f.db, source.EntityKey)
		if _, err := store.Link(ctx, f.flowID, source.ID, target.ID, "rollback_link", nil); err == nil {
			t.Fatal("expected link journal failure")
		}
		var count int
		if err := f.db.QueryRow(`SELECT count(*) FROM world_state_links WHERE flow_id=$1 AND source_id=$2 AND target_id=$3 AND type='rollback_link'`, f.flowID, source.ID, target.ID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 || hints.Load() != beforeHints {
			t.Fatalf("rolled back link count=%d hints=%d want=%d", count, hints.Load(), beforeHints)
		}
	})

	t.Run("automatic discovery journals committed outcome", func(t *testing.T) {
		IngestToolResult(ctx, f.q, f.flowID, "terminal", "nmap -sV scan-journal.example.invalid")
		entity, err := f.q.GetWorldStateEntityByKey(ctx, database.GetWorldStateEntityByKeyParams{FlowID: f.flowID, EntityKey: "host:scan-journal.example.invalid"})
		if err != nil {
			t.Fatal(err)
		}
		if entity.State != database.WorldStateLifecycleScanning || eventCount(t, f.db, f.flowID, entity.EntityKey) != 3 {
			t.Fatalf("automatic state=%s events=%d", entity.State, eventCount(t, f.db, f.flowID, entity.EntityKey))
		}
	})
}

func newStoreFixture(t *testing.T) storeFixture {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		t.Skipf("database unavailable: %v", err)
	}
	var f storeFixture
	f.db, f.q = db, database.New(db)
	mail := fmt.Sprintf("worldstate-store-%d@example.invalid", time.Now().UnixNano())
	if err := db.QueryRow(`INSERT INTO users(mail,name) VALUES($1,'test') RETURNING id`, mail).Scan(&f.userID); err != nil {
		db.Close()
		t.Skipf("store schema unavailable: %v", err)
	}
	if err := db.QueryRow(`INSERT INTO flows(model,model_provider_name,model_provider_type,language,tool_call_id_template,user_id,world_state_schema_version) VALUES('m','p','openai','en','',$1,1) RETURNING id`, f.userID).Scan(&f.flowID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM users WHERE id=$1`, f.userID)
		_ = db.Close()
	})
	return f
}

func uniqueKey(prefix string) string { return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano()) }

func eventCount(t *testing.T, db *sql.DB, flowID int64, key string) int {
	t.Helper()
	var count int
	err := db.QueryRow(`SELECT count(*) FROM world_state_events WHERE flow_id=$1 AND facts->>'entity_key'=$2`, flowID, key).Scan(&count)
	if err != nil {
		t.Fatal(err)
	}
	return count
}

func linkEventCount(t *testing.T, db *sql.DB, flowID, linkID int64) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM world_state_events WHERE flow_id=$1 AND facts->>'link_id'=$2`, flowID, fmt.Sprint(linkID)).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func transitionCount(t *testing.T, db *sql.DB, entityID int64) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM world_state_transitions WHERE entity_id=$1`, entityID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func assertEntityMissing(t *testing.T, q *database.Queries, flowID int64, key string) {
	t.Helper()
	_, err := q.GetWorldStateEntityByKey(context.Background(), database.GetWorldStateEntityByKeyParams{FlowID: flowID, EntityKey: key})
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("entity should be absent, err=%v", err)
	}
}

func assertJSONFields(t *testing.T, raw json.RawMessage, fields ...string) {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	for _, field := range fields {
		if _, ok := object[field]; !ok {
			t.Fatalf("missing %q in %s", field, raw)
		}
	}
}

func assertNoStoredSecret(t *testing.T, db *sql.DB, flowID, entityID int64, secret string) {
	t.Helper()
	var properties, journal, audits string
	if err := db.QueryRow(`SELECT properties::text FROM world_state_entities WHERE id=$1`, entityID).Scan(&properties); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT coalesce(string_agg(facts::text,''),'') FROM world_state_events WHERE flow_id=$1`, flowID).Scan(&journal); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT coalesce(string_agg(evidence::text,''),'') FROM world_state_transitions WHERE entity_id=$1`, entityID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(properties, secret) || strings.Contains(journal, secret) || strings.Contains(audits, secret) {
		t.Fatal("journal or lifecycle audit contains credential material")
	}
}

func installLinkEventFailure(t *testing.T, db *sql.DB, sourceKey string) {
	t.Helper()
	suffix := time.Now().UnixNano()
	functionName := fmt.Sprintf("worldstate_fail_link_%d", suffix)
	triggerName := fmt.Sprintf("worldstate_fail_link_trigger_%d", suffix)
	literal := "'" + strings.ReplaceAll(sourceKey, "'", "''") + "'"
	statement := fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.facts->>'source_key' = %s THEN RAISE EXCEPTION 'test journal failure'; END IF;
			RETURN NEW;
		END $$;
		CREATE TRIGGER %s BEFORE INSERT ON world_state_events
		FOR EACH ROW EXECUTE FUNCTION %s();`, functionName, literal, triggerName, functionName)
	if _, err := db.Exec(statement); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON world_state_events; DROP FUNCTION IF EXISTS %s()`, triggerName, functionName))
	})
}
