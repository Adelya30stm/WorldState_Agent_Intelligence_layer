package tools

import (
	"context"
	"encoding/json"
	"testing"

	"pentagi/pkg/config"
	"pentagi/pkg/database"
)

type readerToolDB struct{ database.Querier }

func (*readerToolDB) GetFlowPrimaryContainer(context.Context, int64) (database.Container, error) {
	return database.Container{ID: 1}, nil
}

func TestWorldStateSnapshotReadersCanQueryWithoutWriting(t *testing.T) {
	fte := &flowToolsExecutor{flowID: 1, db: &readerToolDB{}, cfg: &config.Config{}, functions: &Functions{}}
	handler := func(context.Context, string, json.RawMessage) (string, error) { return "ok", nil }
	readers := map[string]func() (ContextToolsExecutor, error){
		"assistant": func() (ContextToolsExecutor, error) {
			return fte.GetAssistantExecutor(AssistantExecutorConfig{Adviser: handler, Coder: handler, Installer: handler, Memorist: handler, Pentester: handler, Searcher: handler})
		},
		"installer": func() (ContextToolsExecutor, error) {
			return fte.GetInstallerExecutor(InstallerExecutorConfig{Adviser: handler, Memorist: handler, Searcher: handler, MaintenanceResult: handler})
		},
		"coder": func() (ContextToolsExecutor, error) {
			return fte.GetCoderExecutor(CoderExecutorConfig{Adviser: handler, Installer: handler, Memorist: handler, Searcher: handler, CodeResult: handler})
		},
		"searcher": func() (ContextToolsExecutor, error) {
			return fte.GetSearcherExecutor(SearcherExecutorConfig{Memorist: handler, SearchResult: handler})
		},
		"generator": func() (ContextToolsExecutor, error) {
			return fte.GetGeneratorExecutor(GeneratorExecutorConfig{TaskID: 1, Memorist: handler, Searcher: handler, SubtaskList: handler})
		},
		"refiner": func() (ContextToolsExecutor, error) {
			return fte.GetRefinerExecutor(RefinerExecutorConfig{TaskID: 1, Memorist: handler, Searcher: handler, SubtaskPatch: handler})
		},
		"memorist": func() (ContextToolsExecutor, error) {
			return fte.GetMemoristExecutor(MemoristExecutorConfig{SearchResult: handler})
		},
		"enricher": func() (ContextToolsExecutor, error) {
			return fte.GetEnricherExecutor(EnricherExecutorConfig{EnricherResult: handler})
		},
		"reporter": func() (ContextToolsExecutor, error) {
			return fte.GetReporterExecutor(ReporterExecutorConfig{ReportResult: handler})
		},
		"web_reporter": func() (ContextToolsExecutor, error) {
			return fte.GetWebReporterExecutor(WebReporterExecutorConfig{ReportResult: handler})
		},
	}
	for name, makeExecutor := range readers {
		t.Run(name, func(t *testing.T) {
			executor, err := makeExecutor()
			if err != nil {
				t.Fatal(err)
			}
			if !executor.IsFunctionExists(WorldStateQueryToolName) || executor.IsFunctionExists(WorldStateUpdateToolName) {
				t.Fatal("snapshot reader must have query access without write access")
			}
			if _, err := executor.GetToolSchema(WorldStateQueryToolName); err != nil {
				t.Fatalf("query handler must also have a callable schema: %v", err)
			}
		})
	}
}
