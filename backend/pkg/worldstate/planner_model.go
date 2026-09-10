package worldstate

import (
	"bytes"
	"encoding/json"
)

// Preserve the planner's existing fact-reference API, with computed statuses
// and scalar locator/check facts. Full typed observations are also available in
// Model. No lifecycle progression or per-object revision is fabricated.
func modelPlannerEvidence(snapshot ModelSnapshot, limits PlannerEvidenceLimits) (PlannerEvidence, error) {
	view, err := NewModelContext(snapshot, limits.MaxBytes)
	if err != nil {
		return PlannerEvidence{}, err
	}
	projection := PlannerEvidenceProjection{Summary: PlannerEvidenceSummary{
		Entities: int(snapshot.ObjectCount), ByState: map[string]int{}, ByType: map[string]int{},
	}}
	for _, object := range snapshot.Objects {
		projection.Summary.ByState[object.ComputedStatus]++
		projection.Summary.ByType[object.Type]++
		if len(projection.Entities) >= limits.MaxEntities {
			continue
		}
		var locator map[string]any
		decoder := json.NewDecoder(bytes.NewReader(object.Locator))
		decoder.UseNumber()
		if err := decoder.Decode(&locator); err != nil {
			return PlannerEvidence{}, err
		}
		properties := projectJournalProperties(locator)
		if properties == nil {
			properties = map[string]any{}
		}
		properties["check_count"] = object.CheckCount
		properties["run_count"] = object.RunCount
		for _, check := range object.Checks {
			properties["check_"+check.Kind+"_status"] = check.ComputedStatus
			properties["check_"+check.Kind+"_run_count"] = check.RunCount
		}
		projection.Entities = append(projection.Entities, PlannerEvidenceEntity{
			ID: object.ID, Key: object.ObjectKey, Type: object.Type, State: object.ComputedStatus, Properties: properties,
		})
	}
	projection.updateOmitted()
	return fitPlannerEvidence(PlannerEvidence{SchemaVersion: 2, FlowID: snapshot.FlowID, CapturedHead: snapshot.Revision,
		Model: &view, Projection: projection, Journal: PlannerEvidenceJournal{AfterRevision: snapshot.Revision, ThroughRevision: snapshot.Revision},
	}, limits.MaxBytes)
}
