package services

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"pentagi/pkg/database"
	"pentagi/pkg/worldstate"

	"github.com/gin-gonic/gin"
	"github.com/jinzhu/gorm"
	_ "github.com/jinzhu/gorm/dialects/postgres"
	_ "github.com/jinzhu/gorm/dialects/sqlite"
	"github.com/stretchr/testify/require"
)

func TestWorldStateModelV2Response(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL is required for the PostgreSQL model endpoint test")
	}
	db, err := gorm.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	sqlDB := db.DB()
	ctx := t.Context()
	var userID, flowID int64
	require.NoError(t, sqlDB.QueryRowContext(ctx,
		`INSERT INTO users(mail,name) VALUES($1,'model API test') RETURNING id`,
		fmt.Sprintf("model-api-%d@example.invalid", time.Now().UnixNano()),
	).Scan(&userID))
	t.Cleanup(func() {
		_, err := sqlDB.Exec(`DELETE FROM users WHERE id=$1`, userID)
		require.NoError(t, err)
	})
	require.NoError(t, sqlDB.QueryRowContext(ctx, `INSERT INTO flows
		(model,model_provider_name,model_provider_type,language,tool_call_id_template,user_id,world_state_schema_version)
		VALUES('test','test','openai','en','',$1,2) RETURNING id`, userID).Scan(&flowID))

	store := worldstate.NewModelStore(database.New(sqlDB))
	object, err := store.ObserveObject(ctx, flowID, worldstate.AgentSystem, worldstate.ObserveObjectInput{
		Type: "parameter", Locator: map[string]any{
			"url": "https://model-api.example.invalid/search", "method": "GET", "location": "query", "name": "q",
		},
	})
	require.NoError(t, err)
	check, err := store.EnsureCheck(ctx, flowID, object.ID, "xss", worldstate.AgentSystem)
	require.NoError(t, err)
	runIDs := make([]int64, 0, 2)
	for index, verdict := range []string{"vulnerable", "not_reproduced"} {
		run, err := store.StartRun(ctx, flowID, worldstate.AgentSystem, worldstate.StartRunInput{
			CheckID: check.ID, RequestKey: fmt.Sprintf("api-run-%d", index),
			Parameters: map[string]any{"payload": "<probe>", "auth_role": "anonymous"},
		})
		require.NoError(t, err)
		_, err = store.UpdateRun(ctx, flowID, run.ID, worldstate.AgentSystem, worldstate.UpdateRunInput{
			ExecutionStatus: "completed", Verdict: &verdict,
			Evidence: map[string]any{"observation": verdict, "response_status": 200},
		})
		require.NoError(t, err)
		runIDs = append(runIDs, run.ID)
	}
	require.NotEqual(t, runIDs[0], runIDs[1])

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/flows/:flowID/worldstate/model", func(c *gin.Context) {
		c.Set("uid", uint64(userID))
		c.Set("prm", []string{"flows.view"})
		NewWorldStateService(db, nil).GetModel(c)
	})
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/flows/%d/worldstate/model", flowID), nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var body struct {
		Data worldstate.ModelSnapshot `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Equal(t, int32(2), body.Data.SchemaVersion)
	require.Equal(t, flowID, body.Data.FlowID)
	require.Positive(t, body.Data.Revision)
	require.Equal(t, int64(1), body.Data.ObjectCount)
	require.Equal(t, int64(1), body.Data.CheckCount)
	require.Equal(t, int64(2), body.Data.RunCount)
	require.Len(t, body.Data.Objects, 1)
	gotObject := body.Data.Objects[0]
	require.Equal(t, object.ID, gotObject.ID)
	require.Equal(t, "vulnerable", gotObject.ComputedStatus)
	require.Len(t, gotObject.Checks, 1)
	gotCheck := gotObject.Checks[0]
	require.Equal(t, check.ID, gotCheck.ID)
	require.Equal(t, "xss", gotCheck.Kind)
	require.Equal(t, "vulnerable", gotCheck.ComputedStatus)
	require.Len(t, gotCheck.Runs, 2)
	gotIDs, verdicts := make([]int64, 0, 2), make([]string, 0, 2)
	for _, run := range gotCheck.Runs {
		gotIDs = append(gotIDs, run.ID)
		require.Equal(t, "completed", run.ExecutionStatus)
		require.NotNil(t, run.Verdict)
		verdicts = append(verdicts, *run.Verdict)
		require.NotNil(t, run.StartedAt)
		require.NotNil(t, run.FinishedAt)
		require.JSONEq(t, `{"payload":"<probe>","auth_role":"anonymous"}`, string(run.Parameters))
		var evidence map[string]any
		require.NoError(t, json.Unmarshal(run.Evidence, &evidence))
		require.Equal(t, *run.Verdict, evidence["observation"])
	}
	require.ElementsMatch(t, runIDs, gotIDs)
	require.ElementsMatch(t, []string{"vulnerable", "not_reproduced"}, verdicts)
}

func TestWorldStateModelAccessAndLegacyVersion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open("sqlite3", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	db.DB().SetMaxOpenConns(1)
	require.NoError(t, db.Exec(`CREATE TABLE flows (
		id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL,
		world_state_schema_version INTEGER NOT NULL, deleted_at DATETIME
	)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO flows (id, user_id, world_state_schema_version)
		VALUES (1, 7, 1), (2, 9, 2), (3, 9, 1)`).Error)
	svc := NewWorldStateService(db, nil)

	for _, tc := range []struct {
		name        string
		flowID      string
		permissions []string
		status      int
	}{
		{name: "missing permission", flowID: "1", status: http.StatusForbidden},
		{name: "foreign flow is hidden", flowID: "2", permissions: []string{"flows.view"}, status: http.StatusNotFound},
		{name: "missing flow", flowID: "4", permissions: []string{"flows.admin"}, status: http.StatusNotFound},
		{name: "invalid flow", flowID: "abc", permissions: []string{"flows.view"}, status: http.StatusBadRequest},
		{name: "zero flow", flowID: "0", permissions: []string{"flows.view"}, status: http.StatusBadRequest},
		{name: "overflow flow", flowID: "9223372036854775808", permissions: []string{"flows.view"}, status: http.StatusBadRequest},
		{name: "owner gets legacy version", flowID: "1", permissions: []string{"flows.view"}, status: http.StatusOK},
		{name: "admin gets legacy version", flowID: "1", permissions: []string{"flows.admin"}, status: http.StatusOK},
		{name: "admin gets another user's flow", flowID: "3", permissions: []string{"flows.admin"}, status: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodGet, "/flows/"+tc.flowID+"/worldstate/model", nil)
			c.Params = gin.Params{{Key: "flowID", Value: tc.flowID}}
			c.Set("uid", uint64(7))
			c.Set("prm", tc.permissions)
			svc.GetModel(c)
			require.Equal(t, tc.status, w.Code, w.Body.String())
			if tc.status == http.StatusOK {
				var body struct {
					Data worldstate.ModelSnapshot `json:"data"`
				}
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
				require.Equal(t, int32(1), body.Data.SchemaVersion)
				require.Positive(t, body.Data.FlowID)
				require.NotNil(t, body.Data.Objects)
				require.Empty(t, body.Data.Objects)
			}
		})
	}
}
