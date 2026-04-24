// 遵循project_guide.md
package services

// ReportCategory groups related reports on the Reports hub. Order
// matters — slices iterate in display order so what you see in the
// `Categories()` list IS the on-screen layout.
type ReportCategory string

const (
	ReportCategoryFinancials       ReportCategory = "financials"
	ReportCategoryWhoOwesYou       ReportCategory = "who_owes_you"
	ReportCategoryWhatYouOwe       ReportCategory = "what_you_owe"
	ReportCategorySalesTax         ReportCategory = "sales_tax"
	ReportCategoryAccountantTools  ReportCategory = "accountant_tools"
)

// ReportCategoryLabel + ReportCategoryDescription drive the section
// header on the Reports hub. Kept as functions (not constants) so a
// future i18n layer can hook in without touching every call site.
func ReportCategoryLabel(c ReportCategory) string {
	switch c {
	case ReportCategoryFinancials:
		return "Financial Statements"
	case ReportCategoryWhoOwesYou:
		return "Who owes you"
	case ReportCategoryWhatYouOwe:
		return "What you owe"
	case ReportCategorySalesTax:
		return "Sales Tax"
	case ReportCategoryAccountantTools:
		return "For my accountant"
	}
	return string(c)
}

func ReportCategoryDescription(c ReportCategory) string {
	switch c {
	case ReportCategoryFinancials:
		return "The core statements that summarise your financial position and performance."
	case ReportCategoryWhoOwesYou:
		return "Money your customers owe you — outstanding receivables and aging."
	case ReportCategoryWhatYouOwe:
		return "Money you owe your vendors — outstanding payables and aging."
	case ReportCategorySalesTax:
		return "Taxes collected and paid, ready for your sales tax filing."
	case ReportCategoryAccountantTools:
		return "Detailed activity and audit-trail reports your accountant will ask for."
	}
	return ""
}

// ReportEntry is one report listed on the hub. Key is a stable
// identifier used for favourites (must never change for an existing
// report — that's the row ID in the report_favourites join). Href
// is what the link points at.
type ReportEntry struct {
	Key      string
	Title    string
	Desc     string
	Href     string
	Category ReportCategory
}

// AllReports is the canonical, ordered registry of every report
// surfaced in the Reports hub. Add new reports here — the hub picks
// them up automatically and the favourites toggle starts working
// without any other glue.
//
// Renaming a report's Key would orphan existing favourites; if a
// rename is unavoidable, write a migration that updates the
// report_favourites.report_key column in place.
func AllReports() []ReportEntry {
	return []ReportEntry{
		// ── Financial Statements ─────────────────────────────────────
		{
			Key:      "income-statement",
			Title:    "Profit & Loss (Income Statement)",
			Desc:     "Net profit for a period — revenues minus cost of sales minus expenses.",
			Href:     "/reports/income-statement",
			Category: ReportCategoryFinancials,
		},
		{
			Key:      "balance-sheet",
			Title:    "Balance Sheet",
			Desc:     "Snapshot of finances on a given day — assets, liabilities, and equity.",
			Href:     "/reports/balance-sheet",
			Category: ReportCategoryFinancials,
		},
		{
			Key:      "trial-balance",
			Title:    "Trial Balance",
			Desc:     "Sum of debits and credits for every account on a single day. Helps catch posting errors.",
			Href:     "/reports/trial-balance",
			Category: ReportCategoryFinancials,
		},
		{
			Key:      "cash-flow",
			Title:    "Cash Flow Summary",
			Desc:     "Where your cash actually moved this period — opening, inflows, outflows, closing — grouped by source.",
			Href:     "/reports/cash-flow",
			Category: ReportCategoryFinancials,
		},

		// ── Who owes you ─────────────────────────────────────────────
		{
			Key:      "ar-aging",
			Title:    "A/R Aging",
			Desc:     "Outstanding receivables grouped by how long each balance has been due.",
			Href:     "/reports/ar-aging",
			Category: ReportCategoryWhoOwesYou,
		},

		// ── What you owe ─────────────────────────────────────────────
		{
			Key:      "ap-aging",
			Title:    "A/P Aging",
			Desc:     "Open bill balances grouped by how long each amount has been outstanding to vendors.",
			Href:     "/ap-aging",
			Category: ReportCategoryWhatYouOwe,
		},

		// ── Sales Tax ────────────────────────────────────────────────
		{
			Key:      "sales-tax",
			Title:    "Sales Tax Report",
			Desc:     "A breakdown of taxes collected from sales and paid on purchases. Use to prepare your sales tax returns.",
			Href:     "/reports/sales-tax",
			Category: ReportCategorySalesTax,
		},

		// ── For my accountant ───────────────────────────────────────
		{
			Key:      "general-ledger",
			Title:    "General Ledger",
			Desc:     "Every account's posting trail in one document — opening balance, period activity, ending balance.",
			Href:     "/reports/general-ledger",
			Category: ReportCategoryAccountantTools,
		},
		{
			Key:      "journal-entries",
			Title:    "Journal Entries",
			Desc:     "Every posted journal entry with reverse + drill-through to source documents.",
			Href:     "/reports/journal-entries",
			Category: ReportCategoryAccountantTools,
		},
	}
}

// ReportByKey returns the registry entry for a Key, or nil if unknown.
// Used by the favourites endpoint to validate the key against the
// canonical list before persisting (so a typo in the form can't
// pollute the table with garbage).
func ReportByKey(key string) *ReportEntry {
	for _, r := range AllReports() {
		if r.Key == key {
			return &r
		}
	}
	return nil
}

// Categories returns the ordered list of categories used on the hub.
// Computed from AllReports() so adding a new category there
// automatically flows here without a second source of truth.
func Categories() []ReportCategory {
	seen := map[ReportCategory]bool{}
	out := []ReportCategory{}
	for _, r := range AllReports() {
		if seen[r.Category] {
			continue
		}
		seen[r.Category] = true
		out = append(out, r.Category)
	}
	return out
}

// ReportsByCategory returns the entries in one category, in registry
// order.
func ReportsByCategory(c ReportCategory) []ReportEntry {
	out := []ReportEntry{}
	for _, r := range AllReports() {
		if r.Category == c {
			out = append(out, r)
		}
	}
	return out
}
