// 遵循project_guide.md
package pdf

import (
	"html"
	"strings"

	"balanciz/internal/models"
)

// format.go — string formatters used by the renderer when expanding
// FieldRefs into HTML. The adapter is responsible for producing
// already-formatted values for money/date (it knows the doc currency +
// locale); these helpers handle the HTML-rendering layer (escaping +
// multi-line address rendering + image src wrapping).

// formatValue applies render-time formatting hints to a resolved string.
// `format` is the FieldRef.Format override; falls back to the registry
// type's default behavior. Always returns HTML-safe output.
func formatValue(value, format string, fieldType FieldType) string {
	if value == "" {
		return ""
	}
	effective := format
	if effective == "" {
		switch fieldType {
		case FieldTypeMoney:
			effective = "money"
		case FieldTypeDate:
			effective = "date"
		case FieldTypeAddress:
			effective = "address"
		case FieldTypeImage:
			effective = "image"
		case FieldTypeRichText:
			effective = "rich"
		default:
			effective = "raw"
		}
	}

	switch effective {
	case "address":
		// Multi-line: split on \n, escape each line, rejoin with <br>.
		parts := strings.Split(value, "\n")
		for i, p := range parts {
			parts[i] = html.EscapeString(p)
		}
		return strings.Join(parts, "<br>")
	case "rich":
		// Trusted: produced by services.SanitizeMemoHTML at save time.
		// Renderer must NOT escape — bluemonday already removed unsafe markup.
		return value
	case "image":
		// value is either a data URL (data:image/png;base64,...) or an http URL.
		// Renderer wraps in <img>; this returns the escaped src attribute.
		return html.EscapeString(value)
	default:
		// raw / money / date: adapter pre-formatted the string; just escape.
		return html.EscapeString(value)
	}
}

// resolveFieldRef returns the formatted HTML value for a single FieldRef in
// a doc-level context (header / two_col / text / totals labels). isImage is
// true when the ref's resolved value should be rendered inside an <img> tag.
func resolveFieldRef(ref FieldRef, docType models.PDFDocumentType, values DocumentValues) (text string, isImage bool) {
	switch ref.Type {
	case "literal":
		return html.EscapeString(ref.Value), false
	case "image":
		raw := values.Get(ref.Field)
		if raw == "" {
			return "", true
		}
		return html.EscapeString(raw), true
	case "field", "":
		raw := values.Get(ref.Field)
		field, _ := FieldByKey(docType, ref.Field)
		return formatValue(raw, ref.Format, field.Type), false
	default:
		return "", false
	}
}

// fieldRefLabelHTML renders the optional FieldRef.Label prefix (e.g.
// "Date: "). Returns empty string when the label is empty.
func fieldRefLabelHTML(ref FieldRef) string {
	if ref.Label == "" {
		return ""
	}
	return `<span class="gb-pdf-muted">` + html.EscapeString(ref.Label) + `</span>&nbsp;`
}

// emphasisOpen / emphasisClose wrap a value with bold / large-heading tags
// per FieldRef.EmphasisLevel.
func emphasisOpen(level int) string {
	switch level {
	case 1:
		return `<strong>`
	case 2:
		return `<span class="gb-pdf-doc-title">`
	default:
		return ""
	}
}

func emphasisClose(level int) string {
	switch level {
	case 1:
		return `</strong>`
	case 2:
		return `</span>`
	default:
		return ""
	}
}
