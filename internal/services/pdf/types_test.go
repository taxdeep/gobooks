// 遵循project_guide.md
package pdf

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseSchemaEmptyBlobYieldsDefault(t *testing.T) {
	s, err := ParseSchema(nil)
	if err != nil {
		t.Fatalf("ParseSchema(nil) error: %v", err)
	}
	if s.Page.Size != "Letter" || s.Theme.AccentColor != "#0066cc" {
		t.Fatalf("expected DefaultSchema fallbacks, got %+v", s)
	}
}

func TestParseSchemaBackfillsMissingDefaults(t *testing.T) {
	raw := []byte(`{"version":1,"page":{"size":"A4"},"theme":{},"blocks":[]}`)
	s, err := ParseSchema(raw)
	if err != nil {
		t.Fatalf("ParseSchema error: %v", err)
	}
	if s.Page.Size != "A4" {
		t.Fatalf("expected page size to round-trip, got %q", s.Page.Size)
	}
	if s.Page.Orientation != "portrait" {
		t.Fatalf("expected default orientation backfill, got %q", s.Page.Orientation)
	}
	if s.Theme.FontFamily == "" || s.Theme.AccentColor == "" {
		t.Fatalf("expected theme defaults backfilled, got %+v", s.Theme)
	}
}

func TestParseSchemaRejectsMalformed(t *testing.T) {
	_, err := ParseSchema([]byte("not json"))
	if err == nil {
		t.Fatal("expected error on garbage input")
	}
	if !strings.Contains(err.Error(), "invalid schema_json") {
		t.Fatalf("expected wrapped error, got %v", err)
	}
}

func TestSchemaRoundTripPreservesBlockConfigs(t *testing.T) {
	hdrCfg := HeaderConfig{
		Left:  []FieldRef{{Type: "image", Field: "company.logo"}},
		Right: []FieldRef{{Type: "literal", Value: "INVOICE", EmphasisLevel: 2}},
	}
	hdrJSON, err := json.Marshal(hdrCfg)
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}

	src := Schema{
		Version: 1,
		Page:    Page{Size: "A4", Orientation: "portrait", Margins: [4]int{40, 40, 40, 40}},
		Theme:   Theme{AccentColor: "#1a1a2e", FontFamily: "Inter", FontSizePt: 11, LineHeight: "1.4", TextColor: "#1a1a1a", MutedColor: "#6b7280"},
		Blocks:  []Block{{ID: "h1", Type: BlockTypeHeader, Visible: true, Config: hdrJSON}},
	}
	raw := MustMarshalSchema(src)
	got, err := ParseSchema(raw)
	if err != nil {
		t.Fatalf("ParseSchema round-trip: %v", err)
	}
	if len(got.Blocks) != 1 || got.Blocks[0].Type != BlockTypeHeader {
		t.Fatalf("expected single header block, got %+v", got.Blocks)
	}
	var roundCfg HeaderConfig
	if err := json.Unmarshal(got.Blocks[0].Config, &roundCfg); err != nil {
		t.Fatalf("unmarshal header config: %v", err)
	}
	if len(roundCfg.Right) != 1 || roundCfg.Right[0].Value != "INVOICE" {
		t.Fatalf("right slot lost: %+v", roundCfg.Right)
	}
}
