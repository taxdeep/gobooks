// 遵循project_guide.md
package pages

// fieldClass returns the standard Tailwind class string for form inputs, selects,
// and textareas. It encodes the dark-mode safe set once so that page templates do
// not re-declare it. Use the error variant when the field has a server-side error.
//
// All controls that use fieldClass automatically get:
//   - bg-surface       — dark-mode: no white boxes
//   - text-text        — dark-mode: correct text colour
//   - placeholder:text-text-muted — placeholder contrast
//   - focus ring       — visible keyboard-navigation cue
//
// Note: date inputs additionally require style="color-scheme:dark" on the element
// itself so the browser-native picker UI follows the dark theme. This cannot be
// encoded in a class string.
func fieldClass(hasErr bool) string {
	if hasErr {
		return "mt-2 block w-full rounded-md border border-danger bg-surface px-3 py-2 text-body text-text placeholder:text-text-muted outline-none focus:ring-2 focus:ring-danger-focus disabled:opacity-60 disabled:cursor-not-allowed"
	}
	return "mt-2 block w-full rounded-md border border-border-input bg-surface px-3 py-2 text-body text-text placeholder:text-text-muted outline-none focus:ring-2 focus:ring-primary-focus disabled:opacity-60 disabled:cursor-not-allowed"
}

// tableInputClass returns the compact variant for inline inputs inside tables
// (e.g. the Payment column in Pay Bills). Uses a right-aligned mono style
// suitable for numeric entry.
func tableInputClass(hasErr bool) string {
	if hasErr {
		return "w-28 rounded-md border border-danger bg-surface px-2 py-1 text-right text-body tabular-nums text-text outline-none focus:ring-1 focus:ring-danger-focus"
	}
	return "w-28 rounded-md border border-border-input bg-surface px-2 py-1 text-right text-body tabular-nums text-text outline-none focus:ring-1 focus:ring-primary-focus"
}

// errText returns a standard inline error message div.
// Use after a field control when the error string is non-empty.
func errText(msg string) string {
	// Returned as a string for use with templ's text nodes; callers render it
	// with the conventional:  if msg != "" { <div class="...">{ msg }</div> }
	// This function exists only to document the canonical class string.
	_ = msg
	return "mt-1 text-small text-danger"
}
