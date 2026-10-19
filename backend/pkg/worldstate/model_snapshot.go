package worldstate

import (
	"context"
	"database/sql"
	"fmt"

	"pentagi/pkg/database"
)

// ReadSnapshot uses one PostgreSQL snapshot for both data and journal revision.
// It does not advance delivery cursors and never summarizes primary observations.
func (s *ModelStore) ReadSnapshot(ctx context.Context, flowID int64) (ModelSnapshot, error) {
	if flowID <= 0 {
		return ModelSnapshot{}, fmt.Errorf("worldstate: invalid flow id")
	}
	db, ok := s.db.(transactionalQuerier)
	if !ok {
		return ModelSnapshot{}, fmt.Errorf("worldstate: database does not support transactions")
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return ModelSnapshot{}, err
	}
	defer tx.Rollback()
	result, err := readModelSnapshot(ctx, db.WithTx(tx), flowID)
	if err != nil {
		return ModelSnapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return ModelSnapshot{}, err
	}
	return result, nil
}

func readModelSnapshot(ctx context.Context, q *database.Queries, flowID int64) (ModelSnapshot, error) {
	result := ModelSnapshot{FlowID: flowID, SchemaVersion: 2, Objects: []Object{}}
	version, err := q.GetWorldStateModelVersion(ctx, flowID)
	if err != nil {
		return ModelSnapshot{}, err
	}
	if version != 2 {
		return ModelSnapshot{}, ErrModelVersion
	}
	result.Revision, err = q.GetWorldStateEventHead(ctx, flowID)
	if err != nil {
		return ModelSnapshot{}, err
	}
	objects, err := q.ListWorldStateModelObjects(ctx, flowID)
	if err != nil {
		return ModelSnapshot{}, err
	}
	checks, err := q.ListWorldStateModelChecks(ctx, flowID)
	if err != nil {
		return ModelSnapshot{}, err
	}
	runs, err := q.ListWorldStateModelRuns(ctx, flowID)
	if err != nil {
		return ModelSnapshot{}, err
	}
	result.ObjectCount, result.CheckCount, result.RunCount = int64(len(objects)), int64(len(checks)), int64(len(runs))
	runsByCheck := make(map[int64][]CheckRun)
	for _, row := range runs {
		runsByCheck[row.CheckID] = append(runsByCheck[row.CheckID], modelRun(row))
	}
	checksByObject := make(map[int64][]Check)
	for _, row := range checks {
		check := modelCheck(row)
		if found := runsByCheck[row.ID]; found != nil {
			check.Runs = found
		}
		check.RunCount = int64(len(check.Runs))
		check.ComputedStatus = ComputedCheckStatus(check.Runs)
		checksByObject[row.ObjectID] = append(checksByObject[row.ObjectID], check)
	}
	for _, row := range objects {
		object := modelObject(row)
		if found := checksByObject[row.ID]; found != nil {
			object.Checks = found
		}
		object.CheckCount = int64(len(object.Checks))
		for _, check := range object.Checks {
			object.RunCount += check.RunCount
		}
		object.ComputedStatus = computedObjectStatus(object.Checks)
		result.Objects = append(result.Objects, object)
	}
	return result, nil
}
