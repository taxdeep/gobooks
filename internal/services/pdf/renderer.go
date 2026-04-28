// 遵循project_guide.md
package pdf

import (
	"encoding/json"
	"fmt"
	"html"
	"strings"

	"balanciz/internal/models"
)

// renderer.go — Schema → HTML.
//
// RenderHTML is the single public entry. It walks Schema.Blocks, dispatches
// to the per-block renderer based on Block.Type, and wraps the body in a
// minimal HTML document with the theme CSS and @page rule for the chromedp
// engine (G3) to convert to PDF.
//
// Design notes:
//   • The renderer is pure: no DB access, no I/O. All values come in via
//     RenderInput, all output is a string. Easy to unit-test with golden
//     HTML fixtures.
//   • Each block returns a complete HTML fragment (no shared state). The
//     header writer concats them with vertical-spacing wrappers.
//   • Unknown block types are silently skipped (forward compat for future
//     block types added by saved templates after the renderer downgrades).
//   • Visible=false skips rendering. The block stays in schema_json so the
//     editor's "show" toggle round-trips losslessly.

// RenderHTML produces a complete <!doctype html>...</html> string suitable
// for chromedp PDF conversion. Returns an error only on JSON-shape problems
// inside Block.Config (which a well-formed editor never emits).
func RenderHTML(in RenderInput) (string, error) {
	var sb strings.Builder
	sb.Grow(8192)

	docType := models.PDFDocumentType(in.DocumentType)

	sb.WriteString(documentChrome(in.Schema))

	for _, b := range in.Schema.Blocks {
		if !b.Visible {
			continue
		}
		body, err := renderBlock(docType, b, in.Values, in.Lines)
		if err != nil {
			return "", fmt.Errorf("pdf renderer: block %s/%s: %w", b.ID, b.Type, err)
		}
		if body == "" {
			continue
		}
		sb.WriteString(`<section class="gb-pdf-block gb-pdf-block-`)
		sb.WriteString(html.EscapeString(b.Type))
		sb.WriteString(`">`)
		sb.WriteString(body)
		sb.WriteString(`</section>`)
	}

	sb.WriteString(`</main></body></html>`)
	return sb.String(), nil
}

func renderBlock(docType models.PDFDocumentType, b Block, values DocumentValues, lines []LineValues) (string, error) {
	switch b.Type {
	case BlockTypeHeader:
		var cfg HeaderConfig
		if err := unmarshalConfig(b.Config, &cfg); err != nil {
			return "", err
		}
		return renderHeader(docType, cfg, values), nil
	case BlockTypeTwoCol:
		var cfg TwoColConfig
		if err := unmarshalConfig(b.Config, &cfg); err != nil {
			return "", err
		}
		return renderTwoCol(docType, cfg, values), nil
	case BlockTypeLinesTable:
		var cfg LinesTableConfig
		if err := unmarshalConfig(b.Config, &cfg); err != nil {
			return "", err
		}
		return renderLinesTable(docType, cfg, lines), nil
	case BlockTypeTotals:
		var cfg TotalsConfig
		if err := unmarshalConfig(b.Config, &cfg); err != nil {
			return "", err
		}
		return renderTotals(docType, cfg, values), nil
	case BlockTypeText:
		var cfg TextConfig
		if err := unmarshalConfig(b.Config, &cfg); err != nil {
			return "", err
		}
		return renderText(docType, cfg, values), nil
	case BlockTypeSpacer:
		var cfg SpacerConfig
		if err := unmarshalConfig(b.Config, &cfg); err != nil {
			return "", err
		}
		return renderSpacer(cfg), nil
	default:
		return "", nil
	}
}

func unmarshalConfig(raw json.RawMessage, dst any) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, dst)
}

// ── Block renderers ─────────────────────────────────────────────────────────

func renderHeader(docType models.PDFDocumentType, cfg HeaderConfig, values DocumentValues) string {
	var sb strings.Builder
	sb.WriteString(`<div class="gb-pdf-header">`)
	sb.WriteString(`<div class="gb-pdf-header-left">`)
	for _, ref := range cfg.Left {
		sb.WriteString(renderHeaderItem(docType, ref, values))
	}
	sb.WriteString(`</div>`)
	sb.WriteString(`<div class="gb-pdf-header-right">`)
	for _, ref := range cfg.Right {
		sb.WriteString(renderHeaderItem(docType, ref, values))
	}
	sb.WriteString(`</div>`)
	sb.WriteString(`</div>`)
	return sb.String()
}

func renderHeaderItem(docType models.PDFDocumentType, ref FieldRef, values DocumentValues) string {
	val, isImage := resolveFieldRef(ref, docType, values)
	if val == "" && ref.HideWhenEmpty {
		return ""
	}
	if isImage {
		if val == "" {
			return ""
		}
		return `<div class="gb-pdf-header-img"><img src="` + val + `" alt=""></div>`
	}
	return `<div class="gb-pdf-header-line">` +
		fieldRefLabelHTML(ref) +
		emphasisOpen(ref.EmphasisLevel) + val + emphasisClose(ref.EmphasisLevel) +
		`</div>`
}

func renderTwoCol(docType models.PDFDocumentType, cfg TwoColConfig, values DocumentValues) string {
	var sb strings.Builder
	sb.WriteString(`<div class="gb-pdf-two-col">`)
	sb.WriteString(`<div class="gb-pdf-two-col-cell">`)
	if cfg.LeftTitle != "" {
		sb.WriteString(`<div class="gb-pdf-two-col-title">` + html.EscapeString(cfg.LeftTitle) + `</div>`)
	}
	for _, ref := range cfg.Left {
		sb.WriteString(renderTwoColItem(docType, ref, values))
	}
	sb.WriteString(`</div>`)
	sb.WriteString(`<div class="gb-pdf-two-col-cell">`)
	if cfg.RightTitle != "" {
		sb.WriteString(`<div class="gb-pdf-two-col-title">` + html.EscapeString(cfg.RightTitle) + `</div>`)
	}
	for _, ref := range cfg.Right {
		sb.WriteString(renderTwoColItem(docType, ref, values))
	}
	sb.WriteString(`</div>`)
	sb.WriteString(`</div>`)
	return sb.String()
}

func renderTwoColItem(docType models.PDFDocumentType, ref FieldRef, values DocumentValues) string {
	val, isImage := resolveFieldRef(ref, docType, values)
	if val == "" && ref.HideWhenEmpty {
		return ""
	}
	if isImage {
		if val == "" {
			return ""
		}
		return `<div class="gb-pdf-two-col-img"><img src="` + val + `" alt=""></div>`
	}
	return `<div class="gb-pdf-two-col-line">` +
		fieldRefLabelHTML(ref) +
		emphasisOpen(ref.EmphasisLevel) + val + emphasisClose(ref.EmphasisLevel) +
		`</div>`
}

func renderLinesTable(docType models.PDFDocumentType, cfg LinesTableConfig, lines []LineValues) string {
	if len(cfg.Columns) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(`<table class="gb-pdf-lines"><thead><tr>`)
	for _, col := range cfg.Columns {
		field, _ := FieldByKey(docType, col.Field)
		label := col.LabelOverride
		if label == "" {
			label = field.Label
		}
		alignClass := alignClassFor(col.Align, field.Type)
		widthAttr := ""
		if col.WidthPct > 0 {
			widthAttr = fmt.Sprintf(` style="width:%d%%"`, col.WidthPct)
		}
		sb.WriteString(`<th class="` + alignClass + `"` + widthAttr + `>` + html.EscapeString(label) + `</th>`)
	}
	sb.WriteString(`</tr></thead><tbody>`)
	rowsRendered := 0
	for i, line := range lines {
		sb.WriteString(`<tr>`)
		for _, col := range cfg.Columns {
			field, _ := FieldByKey(docType, col.Field)
			alignClass := alignClassFor(col.Align, field.Type)
			val := lineCellValue(line, col.Field, i, cfg.ShowProductSku, field.Type)
			sb.WriteString(`<td class="` + alignClass + `">` + val + `</td>`)
		}
		sb.WriteString(`</tr>`)
		rowsRendered++
	}
	for rowsRendered < cfg.EmptyRowsHint {
		sb.WriteString(`<tr class="gb-pdf-lines-empty">`)
		for range cfg.Columns {
			sb.WriteString(`<td>&nbsp;</td>`)
		}
		sb.WriteString(`</tr>`)
		rowsRendered++
	}
	sb.WriteString(`</tbody></table>`)
	return sb.String()
}

func lineCellValue(line LineValues, fieldKey string, rowIdx int, showSKU bool, ftype FieldType) string {
	if fieldKey == "lines.row_number" {
		return fmt.Sprintf("%d", rowIdx+1)
	}
	raw := line.Get(fieldKey)
	if showSKU && fieldKey == "lines.product_name" {
		if sku := line.Get("lines.product_sku"); sku != "" {
			raw = sku + " — " + raw
		}
	}
	return formatValue(raw, "", ftype)
}

func alignClassFor(explicit string, ftype FieldType) string {
	if explicit == "left" {
		return "gb-pdf-cell-l"
	}
	if explicit == "right" {
		return "gb-pdf-cell-r"
	}
	if explicit == "center" {
		return "gb-pdf-cell-c"
	}
	switch ftype {
	case FieldTypeMoney, FieldTypeNumber:
		return "gb-pdf-cell-r"
	default:
		return "gb-pdf-cell-l"
	}
}

func renderTotals(docType models.PDFDocumentType, cfg TotalsConfig, values DocumentValues) string {
	if len(cfg.Rows) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(`<table class="gb-pdf-totals">`)
	for i, row := range cfg.Rows {
		field, _ := FieldByKey(docType, row.Field)
		label := row.LabelOverride
		if label == "" {
			label = field.Label
		}
		val := formatValue(values.Get(row.Field), "", field.Type)
		emphasis := i == len(cfg.Rows)-1 && cfg.ShowGrandTotalEmphasis
		rowClass := "gb-pdf-totals-row"
		if emphasis {
			rowClass += " gb-pdf-totals-grand"
		}
		sb.WriteString(`<tr class="` + rowClass + `">`)
		sb.WriteString(`<td class="gb-pdf-totals-label">` + html.EscapeString(label) + `</td>`)
		sb.WriteString(`<td class="gb-pdf-totals-value">` + val + `</td>`)
		sb.WriteString(`</tr>`)
	}
	sb.WriteString(`</table>`)
	return sb.String()
}

func renderText(docType models.PDFDocumentType, cfg TextConfig, values DocumentValues) string {
	body := expandTemplateVars(cfg.Body, docType, values)
	if cfg.Title == "" && strings.TrimSpace(body) == "" {
		return ""
	}
	classes := []string{"gb-pdf-text"}
	switch cfg.Align {
	case "center":
		classes = append(classes, "gb-pdf-text-center")
	case "right":
		classes = append(classes, "gb-pdf-text-right")
	}
	if cfg.Bold {
		classes = append(classes, "gb-pdf-text-bold")
	}
	if cfg.Italic {
		classes = append(classes, "gb-pdf-text-italic")
	}
	var sb strings.Builder
	sb.WriteString(`<div class="` + strings.Join(classes, " ") + `">`)
	if cfg.Title != "" {
		sb.WriteString(`<div class="gb-pdf-text-title">` + html.EscapeString(cfg.Title) + `</div>`)
	}
	if body != "" {
		// Body is rich-text (sanitised at save time per services.SanitizeMemoHTML).
		// Newlines convert to <br> for plain-text-friendly authoring.
		sb.WriteString(`<div class="gb-pdf-text-body">` + strings.ReplaceAll(body, "\n", "<br>") + `</div>`)
	}
	sb.WriteString(`</div>`)
	return sb.String()
}

// expandTemplateVars replaces {{field.key}} occurrences in body with the
// resolved+formatted field value. Unknown keys collapse to empty so a stale
// template doesn't print "{{missing}}" verbatim.
func expandTemplateVars(body string, docType models.PDFDocumentType, values DocumentValues) string {
	if !strings.Contains(body, "{{") {
		return body
	}
	out := body
	for {
		start := strings.Index(out, "{{")
		if start < 0 {
			break
		}
		end := strings.Index(out[start:], "}}")
		if end < 0 {
			break
		}
		end += start
		key := strings.TrimSpace(out[start+2 : end])
		field, _ := FieldByKey(docType, key)
		val := formatValue(values.Get(key), "", field.Type)
		out = out[:start] + val + out[end+2:]
	}
	return out
}

func renderSpacer(cfg SpacerConfig) string {
	h := cfg.HeightPt
	if h <= 0 {
		h = 16
	}
	return fmt.Sprintf(`<div class="gb-pdf-spacer" style="height:%dpt"></div>`, h)
}

// ── Document chrome (CSS + html shell) ──────────────────────────────────────

func documentChrome(s Schema) string {
	pageRule := pageCSSRule(s.Page)
	themeVars := themeCSSVars(s.Theme)
	return `<!doctype html><html lang="en"><head>
<meta charset="utf-8">
<style>
` + pageRule + `
:root { ` + themeVars + ` }
* { box-sizing: border-box; }
html, body { margin: 0; padding: 0; }
body {
  font-family: var(--gb-pdf-font);
  font-size: var(--gb-pdf-font-size);
  line-height: var(--gb-pdf-line-height);
  color: var(--gb-pdf-text);
}
.gb-pdf-block { margin-bottom: 18pt; }
.gb-pdf-block:last-child { margin-bottom: 0; }
.gb-pdf-muted { color: var(--gb-pdf-muted); font-size: 0.92em; }

.gb-pdf-header { display: flex; justify-content: space-between; gap: 24pt; align-items: flex-start; }
.gb-pdf-header-left, .gb-pdf-header-right { display: flex; flex-direction: column; gap: 4pt; }
.gb-pdf-header-right { text-align: right; }
.gb-pdf-header-line { line-height: 1.3; }
.gb-pdf-header-img img { max-height: 72pt; max-width: 200pt; object-fit: contain; }
.gb-pdf-doc-title { font-size: 1.7em; font-weight: 700; color: var(--gb-pdf-accent); letter-spacing: 0.04em; text-transform: uppercase; }

.gb-pdf-two-col { display: flex; gap: 32pt; }
.gb-pdf-two-col-cell { flex: 1 1 0; }
.gb-pdf-two-col-title { font-size: 0.78em; text-transform: uppercase; letter-spacing: 0.08em; color: var(--gb-pdf-muted); margin-bottom: 4pt; }
.gb-pdf-two-col-line { line-height: 1.4; }

.gb-pdf-lines { width: 100%; border-collapse: collapse; }
.gb-pdf-lines thead th { text-align: left; padding: 6pt 8pt; border-bottom: 1.2pt solid var(--gb-pdf-accent); color: var(--gb-pdf-accent); font-size: 0.82em; text-transform: uppercase; letter-spacing: 0.06em; }
.gb-pdf-lines tbody td { padding: 6pt 8pt; border-bottom: 0.4pt solid var(--gb-pdf-muted); vertical-align: top; }
.gb-pdf-lines tbody tr:last-child td { border-bottom: none; }
.gb-pdf-cell-l { text-align: left; }
.gb-pdf-cell-r { text-align: right; font-variant-numeric: tabular-nums; }
.gb-pdf-cell-c { text-align: center; }
.gb-pdf-lines-empty td { color: transparent; border-bottom: 0.4pt solid var(--gb-pdf-muted); }

.gb-pdf-totals { margin-left: auto; min-width: 230pt; border-collapse: collapse; }
.gb-pdf-totals-row td { padding: 4pt 8pt; }
.gb-pdf-totals-label { color: var(--gb-pdf-muted); }
.gb-pdf-totals-value { text-align: right; font-variant-numeric: tabular-nums; }
.gb-pdf-totals-grand td { border-top: 1pt solid var(--gb-pdf-text); padding-top: 6pt; font-weight: 700; font-size: 1.08em; }

.gb-pdf-text { white-space: normal; }
.gb-pdf-text-center { text-align: center; }
.gb-pdf-text-right { text-align: right; }
.gb-pdf-text-bold { font-weight: 600; }
.gb-pdf-text-italic { font-style: italic; }
.gb-pdf-text-title { font-weight: 600; margin-bottom: 4pt; color: var(--gb-pdf-accent); }
.gb-pdf-text-body { line-height: 1.4; }
</style>
</head><body><main>`
}

func pageCSSRule(p Page) string {
	size := p.Size
	if size == "" {
		size = "Letter"
	}
	orientation := p.Orientation
	if orientation == "" {
		orientation = "portrait"
	}
	t, r, b, l := p.Margins[0], p.Margins[1], p.Margins[2], p.Margins[3]
	if t == 0 && r == 0 && b == 0 && l == 0 {
		t, r, b, l = 40, 40, 40, 40
	}
	return fmt.Sprintf(`@page { size: %s %s; margin: %dpt %dpt %dpt %dpt; }`,
		size, orientation, t, r, b, l)
}

func themeCSSVars(t Theme) string {
	font := t.FontFamily
	if font == "" {
		font = "Inter, system-ui, sans-serif"
	}
	if !strings.Contains(font, ",") {
		font = font + ", system-ui, sans-serif"
	}
	return fmt.Sprintf(
		"--gb-pdf-accent:%s; --gb-pdf-text:%s; --gb-pdf-muted:%s; --gb-pdf-font:%s; --gb-pdf-font-size:%dpt; --gb-pdf-line-height:%s;",
		t.AccentColor, t.TextColor, t.MutedColor, font, t.FontSizePt, t.LineHeight,
	)
}
