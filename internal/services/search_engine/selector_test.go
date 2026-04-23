// 遵循project_guide.md
package search_engine

import (
	"context"
	"errors"
	"testing"
)

// Phase 5.0 contract:
//   - empty string defaults to ent (DefaultMode)
//   - explicit "ent" / "legacy" / "dual" parse to their Mode (case + space tolerant)
//   - unknown values fail fast with ErrUnknownMode wrapping the input
func TestParseMode_KnownValues(t *testing.T) {
	cases := []struct {
		in   string
		want Mode
	}{
		{"", DefaultMode},        // Phase 5: empty → ent
		{"ent", ModeEnt},         // explicit ent
		{"ENT", ModeEnt},         // case insensitive
		{"  ent ", ModeEnt},      // whitespace tolerant
		{"legacy", ModeLegacy},   // explicit legacy still allowed
		{"LEGACY", ModeLegacy},
		{"dual", ModeDual},
		{"  dual ", ModeDual},
	}
	for _, tc := range cases {
		got, err := ParseMode(tc.in)
		if err != nil {
			t.Errorf("ParseMode(%q) unexpected err: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseMode(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Required by Phase 5.0: empty config → ent (the new default).
func TestParseMode_EmptyConfigDefaultsToEnt(t *testing.T) {
	got, err := ParseMode("")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != ModeEnt {
		t.Errorf("ParseMode(\"\") = %q, want ent (DefaultMode)", got)
	}
	// Sanity: DefaultMode constant tracks ent so future changes show up here.
	if DefaultMode != ModeEnt {
		t.Errorf("DefaultMode = %q, want ent — change is intentional?", DefaultMode)
	}
}

// Required by Phase 5.0: unknown SEARCH_ENGINE value fails fast.
// No silent fallback to legacy / ent / anything else.
func TestParseMode_UnknownFailsFast(t *testing.T) {
	bogusInputs := []string{"bogus", "elasticsearch", "redis", "leg-acy", "0", "true", "smartpicker"}
	for _, in := range bogusInputs {
		got, err := ParseMode(in)
		if err == nil {
			t.Errorf("ParseMode(%q) = %q, want ErrUnknownMode", in, got)
			continue
		}
		if !errors.Is(err, ErrUnknownMode) {
			t.Errorf("ParseMode(%q) error %v does not wrap ErrUnknownMode", in, err)
		}
		if got != "" {
			t.Errorf("ParseMode(%q) returned non-empty mode %q on error", in, got)
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
func (s *stubEngine) SearchAdvanced(_ context.Context, _ AdvancedRequest) (*AdvancedResponse, error) {
	if s.hit != nil {
		*s.hit = true
	}
	return &AdvancedResponse{}, nil
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

// Required by Phase 5.0: legacy mode returns empty results, NEVER 500s.
// HTTP handler relies on this — empty SearchResponse is the contract.
func TestLegacyEngine_ReturnsEmptyNotError(t *testing.T) {
	e := NewLegacyEngine()
	resp, err := e.Search(context.Background(), SearchRequest{
		CompanyID: 42,
		Query:     "anything",
		Limit:     20,
	})
	if err != nil {
		t.Fatalf("LegacyEngine.Search must NEVER return an error in Phase 5+; got %v", err)
	}
	if resp == nil {
		t.Fatal("LegacyEngine.Search must return a non-nil response")
	}
	if len(resp.Candidates) != 0 {
		t.Errorf("LegacyEngine.Search must return empty Candidates, got %d", len(resp.Candidates))
	}
	if resp.Source != "legacy_empty" {
		t.Errorf("Source = %q, want %q so callers can identify the fallback", resp.Source, "legacy_empty")
	}
}

// Required by Phase 5.0 (composite): SEARCH_ENGINE=legacy must not 500.
// This exercises the full Selector→LegacyEngine path the way the HTTP
// handler does, asserting the empty-response contract end-to-end.
func TestSelector_LegacyModeReturnsEmptyResponse(t *testing.T) {
	s := NewSelector(ModeLegacy, NewLegacyEngine(), nil, nil)
	resp, err := s.Search(context.Background(), SearchRequest{
		CompanyID: 1, Query: "does not matter",
	})
	if err != nil {
		t.Fatalf("legacy mode must not error; got %v", err)
	}
	if len(resp.Candidates) != 0 {
		t.Errorf("legacy mode candidates len = %d, want 0", len(resp.Candidates))
	}
}

// DualEngine is still a stub — keep an eye on it. When 5.5 implements
// real diff logic, this test should be replaced.
func TestDualEngine_StillStubbed(t *testing.T) {
	e := NewDualEngine(NewLegacyEngine(), nil)
	_, err := e.Search(context.Background(), SearchRequest{})
	if err == nil {
		t.Error("DualEngine should still return stub error until Phase 5.5 lands")
	}
}
