// 遵循project_guide.md
package search_engine

import (
	"context"
	"errors"
	"testing"
)

func TestParseMode(t *testing.T) {
	cases := map[string]Mode{
		"":        ModeLegacy,
		"legacy":  ModeLegacy,
		"LEGACY":  ModeLegacy,
		"  dual ": ModeDual,
		"ent":     ModeEnt,
		"bogus":   ModeLegacy, // unknown → safe default
	}
	for in, want := range cases {
		if got := ParseMode(in); got != want {
			t.Errorf("ParseMode(%q) = %q, want %q", in, got, want)
		}
	}
}

// stubEngine records which engine the selector dispatched to.
type stubEngine struct {
	mode Mode
	hit  *bool
}

func (s *stubEngine) Mode() Mode { return s.mode }
func (s *stubEngine) Search(_ context.Context, _ SearchRequest) (*SearchResponse, error) {
	if s.hit != nil {
		*s.hit = true
	}
	return &SearchResponse{Source: string(s.mode)}, nil
}

func TestSelector_DispatchesToConfiguredMode(t *testing.T) {
	var legacyHit, dualHit, entHit bool
	legacy := &stubEngine{mode: ModeLegacy, hit: &legacyHit}
	dual := &stubEngine{mode: ModeDual, hit: &dualHit}
	ent := &stubEngine{mode: ModeEnt, hit: &entHit}

	cases := []struct {
		mode   Mode
		want   Mode
		legacy *bool
		dual   *bool
		ent    *bool
	}{
		{ModeLegacy, ModeLegacy, &legacyHit, nil, nil},
		{ModeDual, ModeDual, nil, &dualHit, nil},
		{ModeEnt, ModeEnt, nil, nil, &entHit},
	}
	for _, tc := range cases {
		legacyHit, dualHit, entHit = false, false, false
		s := NewSelector(tc.mode, legacy, dual, ent)
		resp, err := s.Search(context.Background(), SearchRequest{CompanyID: 1})
		if err != nil {
			t.Fatalf("mode=%q: %v", tc.mode, err)
		}
		if resp.Source != string(tc.want) {
			t.Errorf("mode=%q: dispatched to %q, want %q", tc.mode, resp.Source, tc.want)
		}
	}
}

func TestSelector_FallsBackToLegacy_WhenSecondaryEngineNil(t *testing.T) {
	legacy := &stubEngine{mode: ModeLegacy}
	// dual + ent both nil — selector should fall through to legacy
	s := NewSelector(ModeDual, legacy, nil, nil)
	resp, err := s.Search(context.Background(), SearchRequest{CompanyID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Source != string(ModeLegacy) {
		t.Errorf("expected fallback to legacy, got %q", resp.Source)
	}

	s = NewSelector(ModeEnt, legacy, nil, nil)
	resp, err = s.Search(context.Background(), SearchRequest{CompanyID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Source != string(ModeLegacy) {
		t.Errorf("expected fallback to legacy, got %q", resp.Source)
	}
}

func TestSelector_NilLegacyIsAnError(t *testing.T) {
	var s *Selector
	if _, err := s.Search(context.Background(), SearchRequest{}); err == nil {
		t.Error("expected error from nil selector")
	}

	s = NewSelector(ModeLegacy, nil, nil, nil)
	if _, err := s.Search(context.Background(), SearchRequest{}); err == nil {
		t.Error("expected error when legacy engine is nil")
	}
}

// TestRemainingStubs_ReturnNotImplementedErrors keeps an eye on the engines
// that haven't graduated from stub to real implementation yet. Phase 4
// fully implemented EntEngine (see ent_test.go) but LegacyEngine and
// DualEngine still return placeholder errors — when either of those
// grows real logic this test will break loudly, prompting an update.
func TestRemainingStubs_ReturnNotImplementedErrors(t *testing.T) {
	engines := []Engine{
		NewLegacyEngine(),
		NewDualEngine(NewLegacyEngine(), nil),
	}
	for _, e := range engines {
		_, err := e.Search(context.Background(), SearchRequest{})
		if err == nil {
			t.Errorf("engine %q returned nil error from stub", e.Mode())
			continue
		}
		if errors.Unwrap(err) == nil && err.Error() == "" {
			t.Errorf("engine %q returned empty error", e.Mode())
		}
	}
}
