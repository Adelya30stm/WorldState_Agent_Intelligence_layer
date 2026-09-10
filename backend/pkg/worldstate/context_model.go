package worldstate

import (
	"encoding/json"
	"fmt"
	"sort"
)

// ModelContext is a typed, bounded view for a model request. Counts and statuses
// describe the full snapshot, even when some experiment details are omitted.
type ModelContext struct {
	Kind string `json:"kind"`
	ModelSnapshot
	Omitted ModelContextOmitted `json:"omitted"`
}

type ModelContextOmitted struct {
	Objects int64 `json:"objects"`
	Checks  int64 `json:"checks"`
	Runs    int64 `json:"runs"`
}

func NewModelContext(snapshot ModelSnapshot, maxBytes int) (ModelContext, error) {
	out := ModelContext{Kind: "world_state_snapshot", ModelSnapshot: snapshot}
	out.Objects = append([]Object{}, snapshot.Objects...)
	// Keep confirmed problems visible, then the most recently observed objects.
	sort.SliceStable(out.Objects, func(i, j int) bool {
		if (out.Objects[i].ComputedStatus == "vulnerable") != (out.Objects[j].ComputedStatus == "vulnerable") {
			return out.Objects[i].ComputedStatus == "vulnerable"
		}
		return out.Objects[i].ID > out.Objects[j].ID
	})
	if len(out.Objects) > 32 {
		out.Objects = out.Objects[:32]
	}
	for i := range out.Objects {
		obj := &out.Objects[i]
		obj.Checks = append([]Check{}, obj.Checks...)
		if len(obj.Checks) > 16 {
			obj.Checks = obj.Checks[:16]
		}
		for j := range obj.Checks {
			check := &obj.Checks[j]
			check.Runs = append([]CheckRun{}, check.Runs...)
			sort.SliceStable(check.Runs, func(i, j int) bool { return check.Runs[i].ID > check.Runs[j].ID })
			if len(check.Runs) > 8 {
				check.Runs = check.Runs[:8]
			}
		}
	}
	for {
		out.updateOmitted()
		data, err := json.Marshal(out)
		if err != nil {
			return ModelContext{}, fmt.Errorf("worldstate: marshal model context: %w", err)
		}
		if len(data) <= maxBytes {
			return out, nil
		}
		if !out.removeDetail() {
			return ModelContext{}, fmt.Errorf("%w: model context needs %d bytes, limit is %d", ErrDeliveryTooLarge, len(data), maxBytes)
		}
	}
}

func (c *ModelContext) updateOmitted() {
	var checks, runs int64
	for _, object := range c.Objects {
		checks += int64(len(object.Checks))
		for _, check := range object.Checks {
			runs += int64(len(check.Runs))
		}
	}
	c.Omitted = ModelContextOmitted{Objects: c.ObjectCount - int64(len(c.Objects)), Checks: c.CheckCount - checks, Runs: c.RunCount - runs}
}

func (c *ModelContext) removeDetail() bool {
	for i := len(c.Objects) - 1; i >= 0; i-- {
		for j := len(c.Objects[i].Checks) - 1; j >= 0; j-- {
			runs := &c.Objects[i].Checks[j].Runs
			if len(*runs) > 0 {
				*runs = (*runs)[:len(*runs)-1]
				return true
			}
		}
	}
	for i := len(c.Objects) - 1; i >= 0; i-- {
		checks := &c.Objects[i].Checks
		if len(*checks) > 0 {
			*checks = (*checks)[:len(*checks)-1]
			return true
		}
	}
	if len(c.Objects) > 0 {
		c.Objects = c.Objects[:len(c.Objects)-1]
		return true
	}
	return false
}
