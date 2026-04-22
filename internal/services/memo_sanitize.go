// 遵循project_guide.md
package services

import (
	"strings"
	"sync"

	"github.com/microcosm-cc/bluemonday"
)

// memoPolicy is the shared bluemonday policy for memo / internal-note fields.
// Built once (sync.Once) because constructing a Policy is O(pattern-count).
//
// Allowed tags: <b>, <strong>, <i>, <em>. Everything else (attributes, URLs,
// scripts, iframes, style, etc.) is stripped. This matches the Phase 2 spec:
// operators can emphasise text inside notes but cannot inject markup that
// would render as scripts, links, images, or layout.
var (
	memoPolicyOnce sync.Once
	memoPolicy     *bluemonday.Policy
)

func buildMemoPolicy() *bluemonday.Policy {
	p := bluemonday.NewPolicy()
	p.AllowElements("b", "strong", "i", "em")
	return p
}

// SanitizeMemoHTML strips any HTML that is not on the memo allow-list (bold /
// italic only). Preserves inner text content and whitespace. Used by the
// Invoice / Bill / SO / Expense save handlers before persisting Memo fields.
//
// Trimming whitespace after sanitisation is safe because the policy never
// introduces new whitespace (it only removes tags).
func SanitizeMemoHTML(raw string) string {
	memoPolicyOnce.Do(func() { memoPolicy = buildMemoPolicy() })
	return strings.TrimSpace(memoPolicy.Sanitize(raw))
}
