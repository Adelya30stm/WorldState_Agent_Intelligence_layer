package worldstate

import (
	"errors"
	"testing"
)

func TestValidateTransition_HappyPath(t *testing.T) {
	// The straight-line progression must be valid end to end.
	chain := []State{
		StateUnknown,
		StateDiscovered,
		StateScanning,
		StateAssessed,
		StateVulnerable,
		StateExploited,
		StateRemediated,
	}
	for i := 0; i < len(chain)-1; i++ {
		if err := ValidateTransition(chain[i], chain[i+1]); err != nil {
			t.Errorf("expected %q → %q to be valid, got %v", chain[i], chain[i+1], err)
		}
	}
}

func TestValidateTransition_RejectsSkipAhead(t *testing.T) {
	// Skipping forward is the most common bug shape we need to reject.
	cases := []struct{ from, to State }{
		{StateUnknown, StateScanning},  // recon not done
		{StateUnknown, StateExploited}, // no chain
		{StateDiscovered, StateExploited},
		{StateScanning, StateExploited},
		{StateAssessed, StateExploited}, // must pass through vulnerable
	}
	for _, c := range cases {
		err := ValidateTransition(c.from, c.to)
		if !errors.Is(err, ErrInvalidTransition) {
			t.Errorf("expected %q → %q to be ErrInvalidTransition, got %v", c.from, c.to, err)
		}
	}
}

func TestValidateTransition_RejectsBackwards(t *testing.T) {
	cases := []struct{ from, to State }{
		{StateDiscovered, StateUnknown},
		{StateExploited, StateAssessed},
		{StateRemediated, StateVulnerable},
	}
	for _, c := range cases {
		err := ValidateTransition(c.from, c.to)
		if !errors.Is(err, ErrInvalidTransition) {
			t.Errorf("expected %q → %q to be rejected, got %v", c.from, c.to, err)
		}
	}
}

func TestValidateTransition_RejectsSameState(t *testing.T) {
	// Same-state transitions are not allowed: they would produce a
	// misleading row in world_state_transitions.
	if err := ValidateTransition(StateScanning, StateScanning); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("expected same-state transition to be rejected, got %v", err)
	}
}

func TestValidateTransition_AllowedShortcuts(t *testing.T) {
	// These edges exist intentionally — see allowedTransitions for the why.
	allowed := []struct{ from, to State }{
		{StateDiscovered, StateAssessed},   // passive recon was enough
		{StateAssessed, StateRemediated},   // clean target, nothing to exploit
		{StateVulnerable, StateRemediated}, // patched before exploit
	}
	for _, c := range allowed {
		if err := ValidateTransition(c.from, c.to); err != nil {
			t.Errorf("expected %q → %q to be valid, got %v", c.from, c.to, err)
		}
	}
}

func TestValidateTransition_UnknownState(t *testing.T) {
	cases := []struct{ from, to State }{
		{State("bogus"), StateDiscovered},
		{StateScanning, State("bogus")},
	}
	for _, c := range cases {
		err := ValidateTransition(c.from, c.to)
		if !errors.Is(err, ErrUnknownState) {
			t.Errorf("expected ErrUnknownState for %q → %q, got %v", c.from, c.to, err)
		}
	}
}

func TestState_IsTerminal(t *testing.T) {
	if !StateRemediated.IsTerminal() {
		t.Error("remediated must be terminal")
	}
	if StateExploited.IsTerminal() {
		t.Error("exploited must not be terminal — can still transition to remediated")
	}
}

func TestAllowedNext(t *testing.T) {
	got := AllowedNext(StateAssessed)
	if len(got) != 2 {
		t.Fatalf("expected 2 next states from assessed, got %d (%v)", len(got), got)
	}
	wantSet := map[State]bool{StateVulnerable: true, StateRemediated: true}
	for _, s := range got {
		if !wantSet[s] {
			t.Errorf("unexpected next state %q from assessed", s)
		}
	}
	if AllowedNext(StateRemediated) != nil {
		t.Error("expected no next states from terminal remediated")
	}
}
