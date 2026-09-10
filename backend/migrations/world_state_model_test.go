package migrations

import (
	"testing"

	"github.com/pressly/goose/v3"
)

func TestWorldStateModelMigrationPreservesLegacyFlows(t *testing.T) {
	db := disposablePostgres(t)
	goose.SetBaseFS(EmbedMigrations)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpTo(db, "sql", 20260817); err != nil {
		t.Fatal(err)
	}
	old := createDeliveryFixture(t, db, "before-model")
	var legacyEntity int64
	if err := db.QueryRow(`INSERT INTO world_state_entities(flow_id,type,entity_key) VALUES($1,'host','host:legacy.example') RETURNING id`, old.flowID).Scan(&legacyEntity); err != nil {
		t.Fatal(err)
	}
	if err := goose.Up(db, "sql"); err != nil {
		t.Fatal(err)
	}
	var version int
	if err := db.QueryRow(`SELECT world_state_schema_version FROM flows WHERE id=$1`, old.flowID).Scan(&version); err != nil || version != 1 {
		t.Fatalf("existing flow version=%d err=%v", version, err)
	}
	fresh := createDeliveryFixture(t, db, "after-model")
	if err := db.QueryRow(`SELECT world_state_schema_version FROM flows WHERE id=$1`, fresh.flowID).Scan(&version); err != nil || version != 2 {
		t.Fatalf("new flow version=%d err=%v", version, err)
	}
	assertCount(t, db, `SELECT count(*) FROM world_state_entities WHERE id=$1`, 1, legacyEntity)
	for _, table := range []string{"world_state_objects", "world_state_checks", "world_state_check_runs"} {
		assertObjectExists(t, db, table, true)
	}
	if _, err := db.Exec(`INSERT INTO world_state_events(flow_id,kind,facts,actor) VALUES($1,'check_created','{"schema_version":2,"check_id":1}','test')`, fresh.flowID); err != nil {
		t.Fatal(err)
	}
	if err := goose.DownTo(db, "sql", 20260817); err != nil {
		t.Fatal(err)
	}
	assertObjectExists(t, db, "world_state_objects", false)
	assertObjectExists(t, db, "world_state_events", true)
	assertCount(t, db, `SELECT count(*) FROM world_state_entities WHERE id=$1`, 1, legacyEntity)
	if err := goose.Up(db, "sql"); err != nil {
		t.Fatal(err)
	}
	assertObjectExists(t, db, "world_state_objects", true)
	assertCount(t, db, `SELECT count(*) FROM world_state_events WHERE flow_id=$1 AND kind='check_created'`, 1, fresh.flowID)
}
