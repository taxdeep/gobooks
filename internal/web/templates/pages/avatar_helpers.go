// 遵循project_guide.md
package pages

import (
	"hash/fnv"
	"strings"
	"unicode"
)

// contactInitials extracts up to 2 uppercase initials from a contact
// name. Handles the common shapes:
//
//   "AIRBEAM WIRELESS TECHNOLOGIES" → "AW"
//   "Jane Smith"                    → "JS"
//   "OpenAI"                        → "OP"  (single-word → first two chars)
//   ""                              → "??"  (sentinel so the avatar still renders)
//
// Rune-aware so multi-byte names like "李明" → "李" render correctly.
func contactInitials(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "??"
	}
	words := strings.Fields(name)
	if len(words) >= 2 {
		return string(firstRune(words[0])) + string(firstRune(words[len(words)-1]))
	}
	// Single-word fallback: take the first two runes if available.
	rs := []rune(words[0])
	if len(rs) == 0 {
		return "??"
	}
	if len(rs) == 1 {
		return strings.ToUpper(string(rs[0]))
	}
	return strings.ToUpper(string(rs[:2]))
}

// firstRune returns the first rune of a string as an uppercase rune.
// Empty strings are caller-guarded (contactInitials never hands us one).
func firstRune(s string) rune {
	for _, r := range s {
		return unicode.ToUpper(r)
	}
	return '?'
}

// contactAvatarClass returns a Tailwind class string for the small
// avatar badge. The background colour is a deterministic hash of the
// name so the same customer always renders the same colour — visual
// continuity without storing a preference.
//
// The palette is deliberately muted so avatars fit alongside the other
// list-page chrome without stealing attention.
func contactAvatarClass(name string) string {
	palette := []string{
		"bg-primary-soft text-primary",
		"bg-success-soft text-success-hover",
		"bg-warning-soft text-warning",
		"bg-danger-soft text-danger-hover",
		"bg-muted-soft text-text-muted",
	}
	if name == "" {
		return palette[0]
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(name))
	return palette[int(h.Sum32())%len(palette)]
}
