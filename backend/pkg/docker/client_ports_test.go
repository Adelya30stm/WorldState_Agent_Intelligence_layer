package docker

import (
	"slices"
	"testing"
)

func resetAllocatedPorts() {
	allocatedPortsMu.Lock()
	allocatedPorts = map[int64][]int{}
	allocatedPortsMu.Unlock()
}

func TestDefaultPrimaryContainerPorts(t *testing.T) {
	got := defaultPrimaryContainerPorts(8)
	want := []int{28016, 28017}
	if !slices.Equal(got, want) {
		t.Fatalf("defaultPrimaryContainerPorts(8) = %v, want %v", got, want)
	}
}

func TestChoosePrimaryContainerPortsSkipsOccupied(t *testing.T) {
	resetAllocatedPorts()
	t.Cleanup(resetAllocatedPorts)

	used := map[int]struct{}{
		28016: {},
		28017: {},
	}
	got := choosePrimaryContainerPorts(8, used)
	want := []int{28018, 28019}
	if !slices.Equal(got, want) {
		t.Fatalf("choosePrimaryContainerPorts(8) = %v, want %v", got, want)
	}
	if !slices.Equal(GetPrimaryContainerPorts(8), want) {
		t.Fatalf("GetPrimaryContainerPorts(8) = %v, want registered %v", GetPrimaryContainerPorts(8), want)
	}
}

func TestChoosePrimaryContainerPortsReusesRegistration(t *testing.T) {
	resetAllocatedPorts()
	t.Cleanup(resetAllocatedPorts)
	registerAllocatedPorts(8, []int{28018, 28019})

	got := choosePrimaryContainerPorts(8, map[int]struct{}{28016: {}, 28017: {}})
	want := []int{28018, 28019}
	if !slices.Equal(got, want) {
		t.Fatalf("choosePrimaryContainerPorts(8) = %v, want %v", got, want)
	}
}

func TestChoosePrimaryContainerPortsReplacesOccupiedRegistration(t *testing.T) {
	resetAllocatedPorts()
	t.Cleanup(resetAllocatedPorts)
	registerAllocatedPorts(13, []int{28026, 28027})

	got := choosePrimaryContainerPorts(13, map[int]struct{}{28026: {}, 28027: {}})
	want := []int{28028, 28029}
	if !slices.Equal(got, want) {
		t.Fatalf("choosePrimaryContainerPorts(13) = %v, want %v", got, want)
	}
	if !slices.Equal(GetPrimaryContainerPorts(13), want) {
		t.Fatalf("GetPrimaryContainerPorts(13) = %v, want registered %v", GetPrimaryContainerPorts(13), want)
	}
}

func TestPrimaryTerminalFlowID(t *testing.T) {
	id, ok := primaryTerminalFlowID("/redscope-terminal-8")
	if !ok || id != 8 {
		t.Fatalf("primaryTerminalFlowID(/redscope-terminal-8) = %d, %v", id, ok)
	}
	id, ok = primaryTerminalFlowID("/pentagi-pentagi-terminal-8")
	if !ok || id != 8 {
		t.Fatalf("primaryTerminalFlowID(/pentagi-pentagi-terminal-8) = %d, %v", id, ok)
	}
	if _, ok := primaryTerminalFlowID("/pentagi-terminal-8"); ok {
		t.Fatal("expected pentagi-terminal-8 to be ignored")
	}
}
