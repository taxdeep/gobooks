// 遵循project_guide.md
// Package pdf is the Phase 3 block-based template system shared across all
// document types (Invoice / Quote / SO / Bill / PO / Shipment).
//
// A template is a JSONB blob with three top-level sections:
//
//	page    — paper size, orientation, margins.
//	theme   — accent colour, font family/size, line height.
//	blocks  — ordered list of typed blocks rendered top-to-bottom.
//
// Block.Config is type-specific JSON; the renderer (G2) dispatches on
// Block.Type and unmarshals into the matching config struct (HeaderConfig,
// LinesTableConfig, etc.). The split keeps the schema strongly typed in Go
// while staying flexible enough to add new block types without DB changes.
package pdf

import (
	"encoding/json"
	"fmt"
)

// Schema is the full template payload stored in pdf_templates.schema_json.
type Schema struct {
	Version int     `json:"version"`     // schema version for forward-compat (currently 1)
	Page    Page    `json:"page"`
	Theme   Theme   `json:"theme"`
	Blocks  []Block `json:"blocks"`
}

// Page describes physical paper layout. Values are in points (pt; 1pt = 1/72 inch).
type Page struct {
	// Size is "A4" or "Letter" (G1 supports these two; renderer maps to mm/in).
	Size string `json:"size"`
	// Orientation is "portrait" or "landscape".
	Orientation string `json:"orientation"`
	// Margins are top, right, bottom, left in points. Default: [40, 40, 40, 40].
	Margins [4]int `json:"margins"`
}

// Theme controls cross-block styling — colour and typography.
type Theme struct {
	// AccentColor is the hex string used for headings and table header rules.
	AccentColor string `json:"accent_color"`
	// FontFamily is a CSS font-family value. Renderer-supplied web fonts:
	// "Inter", "Roboto", "Helvetica", "Times" are guaranteed.
	FontFamily string `json:"font_family"`
	// FontSizePt is the body font size in points (10–14 typical).
	FontSizePt int `json:"font_size_pt"`
	// LineHeight is a CSS unitless line-height value (e.g. "1.4").
	LineHeight string `json:"line_height"`
	// TextColor is the hex string for body text. Defaults to "#1a1a1a".
	TextColor string `json:"text_color"`
	// MutedColor is the hex string for secondary labels / row separators.
	MutedColor string `json:"muted_color"`
}

// Block is one row in the rendered document. Config is opaque JSON in
// transit; the renderer unmarshals into the type-specific struct based on Type.
type Block struct {
	// ID is a stable per-template UUID — used by the editor for drag-reorder
	// state and by re-render diffs.
	ID string `json:"id"`
	// Type drives renderer dispatch. See BlockType* constants.
	Type string `json:"type"`
	// Visible=false suppresses the block without removing it from the schema
	// (used by the editor's "hide" toggle so layout decisions are reversible).
	Visible bool `json:"visible"`
	// Config is one of the *Config structs below; serialise via json.RawMessage
	// so unknown / future block types round-trip safely.
	Config json.RawMessage `json:"config"`
}

// Block type discriminators — keep in sync with the renderer's switch.
const (
	BlockTypeHeader     = "header"
	BlockTypeTwoCol     = "two_col"
	BlockTypeLinesTable = "lines_table"
	BlockTypeTotals     = "totals"
	BlockTypeText       = "text"
	BlockTypeSpacer     = "spacer"
)

// AllBlockTypes is the canonical block-type list shown in the editor's
// "+ Add block" picker.
var AllBlockTypes = []string{
	BlockTypeHeader,
	BlockTypeTwoCol,
	BlockTypeLinesTable,
	BlockTypeTotals,
	BlockTypeText,
	BlockTypeSpacer,
}

// HeaderConfig is the top-of-page header block. Left and right slots each
// accept an ordered list of FieldRefs that the renderer concatenates
// vertically. Typical layout: left = company logo + address; right =
// document title + number + key dates.
type HeaderConfig struct {
	Left  []FieldRef `json:"left"`
	Right []FieldRef `json:"right"`
}

// TwoColConfig is the bill-to / doc-meta two-column band that sits below
// the header. Each column holds a list of FieldRefs; renderer wraps each
// entry into a label + value pair.
type TwoColConfig struct {
	LeftTitle  string     `json:"left_title"`
	Left       []FieldRef `json:"left"`
	RightTitle string     `json:"right_title"`
	Right      []FieldRef `json:"right"`
}

// LinesTableConfig defines the line-items table. Columns lists the column
// keys (must reference the doc type's lines.* fields in the FieldRegistry)
// in display order; the renderer pulls labels + format hints from the
// registry at render time.
type LinesTableConfig struct {
	Columns []LinesTableColumn `json:"columns"`
	// EmptyRowsHint suggests how many blank rows to render when the document
	// has fewer line items than this — avoids an awkward gap below short
	// invoices. 0 = render exactly the line count.
	EmptyRowsHint int `json:"empty_rows_hint"`
	// ShowProductSku, when true, prepends the SKU to each line description.
	ShowProductSku bool `json:"show_product_sku"`
}

// LinesTableColumn is one column in the line-items table.
type LinesTableColumn struct {
	// Field is a key from the FieldRegistry, e.g. "lines.qty".
	Field string `json:"field"`
	// LabelOverride lets the template show a different header than the
	// registry default (e.g. "Hours" instead of "Qty"). Empty = use registry.
	LabelOverride string `json:"label_override"`
	// WidthPct is the column width as a percentage of the table (0 = auto).
	WidthPct int `json:"width_pct"`
	// Align is "left" / "right" / "center". Empty = field's natural align
	// (right-align for money/number, left for everything else).
	Align string `json:"align"`
}

// TotalsConfig drives the totals table at the bottom of the line-items
// section. Rows are the totals lines in display order.
type TotalsConfig struct {
	Rows []TotalsRow `json:"rows"`
	// ShowGrandTotalEmphasis bolds + underlines the last row.
	ShowGrandTotalEmphasis bool `json:"show_grand_total_emphasis"`
}

// TotalsRow is one row in the totals block. Field references a registry key
// (typically a doc-level money field like "invoice.subtotal").
type TotalsRow struct {
	Field         string `json:"field"`
	LabelOverride string `json:"label_override"`
}

// TextConfig is a free-form rich text block (notes, footer, payment
// instructions). The Body string supports {{field}} substitutions whose
// keys must exist in the doc type's FieldRegistry.
type TextConfig struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	// Align is "left" / "center" / "right".
	Align string `json:"align"`
	// Italic / Bold apply to the whole block (richer formatting can come
	// inline via <b>/<i> tags when bluemonday-sanitised upstream).
	Italic bool `json:"italic"`
	Bold   bool `json:"bold"`
}

// SpacerConfig inserts vertical whitespace. HeightPt is in points.
type SpacerConfig struct {
	HeightPt int `json:"height_pt"`
}

// FieldRef points at a doc field (or a literal string / image). Used inside
// HeaderConfig left/right slots and TwoColConfig left/right slots.
type FieldRef struct {
	// Type is "field" (lookup), "literal" (use Value), or "image" (treat
	// Field as an image URL or a registry key whose value is a base64 data URL).
	Type string `json:"type"`
	// Field is the FieldRegistry key when Type="field" or "image".
	Field string `json:"field"`
	// Value is the literal text when Type="literal".
	Value string `json:"value"`
	// Label is an optional prefix shown before the value (e.g. "Date: ").
	// Renderer outputs label-or-value when the value is empty unless
	// HideWhenEmpty is set.
	Label string `json:"label"`
	// Format overrides the registry's default format. Common values:
	// "money", "date", "datetime", "raw" (no format), "address" (multi-line).
	Format string `json:"format"`
	// HideWhenEmpty suppresses the row entirely when the resolved value is
	// blank. Useful for optional fields like Customer PO# / SO Number.
	HideWhenEmpty bool `json:"hide_when_empty"`
	// EmphasisLevel: 0=normal, 1=bold, 2=heading-large.
	EmphasisLevel int `json:"emphasis_level"`
}

// ── Helpers ─────────────────────────────────────────────────────────────────

// ParseSchema deserialises a raw JSON blob (typically pdf_templates.schema_json)
// into a Schema, returning a descriptive error when the shape is malformed.
func ParseSchema(raw []byte) (Schema, error) {
	if len(raw) == 0 {
		return DefaultSchema(), nil
	}
	var s Schema
	if err := json.Unmarshal(raw, &s); err != nil {
		return Schema{}, fmt.Errorf("pdf: invalid schema_json: %w", err)
	}
	if s.Version == 0 {
		s.Version = 1
	}
	s.applyDefaults()
	return s, nil
}

// MustMarshalSchema serialises a Schema to bytes. Panics on encoding error
// (which only happens for unsupported types — never with the structs above).
// Used by the seeder + tests.
func MustMarshalSchema(s Schema) []byte {
	b, err := json.Marshal(s)
	if err != nil {
		panic(fmt.Errorf("pdf: marshal schema: %w", err))
	}
	return b
}

// DefaultSchema returns an empty but valid schema suitable as a starting
// point for new templates. The renderer treats blocks=[] as "render nothing
// but the page chrome".
func DefaultSchema() Schema {
	return Schema{
		Version: 1,
		Page: Page{
			Size:        "Letter",
			Orientation: "portrait",
			Margins:     [4]int{40, 40, 40, 40},
		},
		Theme: Theme{
			AccentColor: "#0066cc",
			FontFamily:  "Inter",
			FontSizePt:  11,
			LineHeight:  "1.4",
			TextColor:   "#1a1a1a",
			MutedColor:  "#6b7280",
		},
		Blocks: []Block{},
	}
}

// applyDefaults backfills any zero-value fields after JSON unmarshal so the
// renderer never sees nil colours / empty page size / etc.
func (s *Schema) applyDefaults() {
	def := DefaultSchema()
	if s.Page.Size == "" {
		s.Page.Size = def.Page.Size
	}
	if s.Page.Orientation == "" {
		s.Page.Orientation = def.Page.Orientation
	}
	if s.Page.Margins == ([4]int{}) {
		s.Page.Margins = def.Page.Margins
	}
	if s.Theme.AccentColor == "" {
		s.Theme.AccentColor = def.Theme.AccentColor
	}
	if s.Theme.FontFamily == "" {
		s.Theme.FontFamily = def.Theme.FontFamily
	}
	if s.Theme.FontSizePt == 0 {
		s.Theme.FontSizePt = def.Theme.FontSizePt
	}
	if s.Theme.LineHeight == "" {
		s.Theme.LineHeight = def.Theme.LineHeight
	}
	if s.Theme.TextColor == "" {
		s.Theme.TextColor = def.Theme.TextColor
	}
	if s.Theme.MutedColor == "" {
		s.Theme.MutedColor = def.Theme.MutedColor
	}
}
