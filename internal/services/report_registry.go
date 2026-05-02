package services

// ReportCategory groups related reports on the Reports hub. Order matters;
// slices iterate in display order so registry order is the on-screen layout.
type ReportCategory string

const (
	ReportCategoryFinancials      ReportCategory = "financials"
	ReportCategorySales           ReportCategory = "sales"
	ReportCategoryExpenses        ReportCategory = "expenses"
	ReportCategoryWhoOwesYou      ReportCategory = "who_owes_you"
	ReportCategoryWhatYouOwe      ReportCategory = "what_you_owe"
	ReportCategorySalesTax        ReportCategory = "sales_tax"
	ReportCategoryAccountantTools ReportCategory = "accountant_tools"
)

func ReportCategoryLabel(c ReportCategory) string {
	switch c {
	case ReportCategoryFinancials:
		return "Financial Statements"
	case ReportCategorySales:
		return "Sales"
	case ReportCategoryExpenses:
		return "Expenses"
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
	case ReportCategorySales:
		return "Where your revenue comes from - sales activity broken down by customer."
	case ReportCategoryExpenses:
		return "Where your money goes - expense activity broken down by vendor."
	case ReportCategoryWhoOwesYou:
		return "Money your customers owe you - outstanding receivables and aging."
	case ReportCategoryWhatYouOwe:
		return "Money you owe your vendors - outstanding payables and aging."
	case ReportCategorySalesTax:
		return "Taxes collected and paid, ready for your sales tax filing."
	case ReportCategoryAccountantTools:
		return "Detailed activity and audit-trail reports your accountant will ask for."
	}
	return ""
}

// ReportEntry is one report listed on the hub. Key is stable because it is
// persisted by report_favourites.
type ReportEntry struct {
	Key         string
	Title       string
	Desc        string
	Href        string
	Category    ReportCategory
	Core        bool
	Mode        string
	CSVHref     string
	Interactive bool
	DrillDown   bool
}

// AllReports is the canonical, ordered registry of every report surfaced in
// the Reports hub.
func AllReports() []ReportEntry {
	return []ReportEntry{
		{
			Key:         "income-statement",
			Title:       "Profit & Loss (Income Statement)",
			Desc:        "Net profit for a period - revenues minus cost of sales minus expenses.",
			Href:        "/reports/income-statement",
			Category:    ReportCategoryFinancials,
			Core:        true,
			Mode:        "Period",
			CSVHref:     "/reports/income-statement/export.csv",
			Interactive: true,
			DrillDown:   true,
		},
		{
			Key:         "balance-sheet",
			Title:       "Balance Sheet",
			Desc:        "Snapshot of finances on a given day - assets, liabilities, and equity.",
			Href:        "/reports/balance-sheet",
			Category:    ReportCategoryFinancials,
			Core:        true,
			Mode:        "As-of",
			CSVHref:     "/reports/balance-sheet/export.csv",
			Interactive: true,
			DrillDown:   true,
		},
		{
			Key:         "trial-balance",
			Title:       "Trial Balance",
			Desc:        "Sum of debits and credits for every account in a period. Helps catch posting errors.",
			Href:        "/reports/trial-balance",
			Category:    ReportCategoryFinancials,
			Core:        true,
			Mode:        "Period",
			CSVHref:     "/reports/trial-balance/export.csv",
			Interactive: true,
			DrillDown:   true,
		},
		{
			Key:         "cash-flow",
			Title:       "Cash Flow Summary",
			Desc:        "Where your cash actually moved this period - opening, inflows, outflows, closing - grouped by source.",
			Href:        "/reports/cash-flow",
			Category:    ReportCategoryFinancials,
			Core:        true,
			Mode:        "Period",
			Interactive: true,
			DrillDown:   true,
		},
		{
			Key:       "sales-by-customer",
			Title:     "Sales by Customer",
			Desc:      "Posted invoices grouped by customer, sorted by total revenue. Click a customer to drill into their full activity.",
			Href:      "/reports/sales-by-customer",
			Category:  ReportCategorySales,
			Mode:      "Period",
			DrillDown: true,
		},
		{
			Key:       "expense-by-vendor",
			Title:     "Expense by Vendor",
			Desc:      "Posted bills + expenses grouped by vendor, sorted by total spend. Click a vendor to drill into their full activity.",
			Href:      "/reports/expense-by-vendor",
			Category:  ReportCategoryExpenses,
			Mode:      "Period",
			DrillDown: true,
		},
		{
			Key:      "ar-aging",
			Title:    "A/R Aging",
			Desc:     "Outstanding receivables grouped by how long each balance has been due.",
			Href:     "/reports/ar-aging",
			Category: ReportCategoryWhoOwesYou,
			Mode:     "As-of",
			CSVHref:  "/reports/ar-aging/export.csv",
		},
		{
			Key:      "ap-aging",
			Title:    "A/P Aging",
			Desc:     "Open bill balances grouped by how long each amount has been outstanding to vendors.",
			Href:     "/ap-aging",
			Category: ReportCategoryWhatYouOwe,
			Mode:     "As-of",
		},
		{
			Key:       "sales-tax",
			Title:     "Sales Tax Report",
			Desc:      "A breakdown of taxes collected from sales and paid on purchases. Use to prepare your sales tax returns.",
			Href:      "/reports/sales-tax",
			Category:  ReportCategorySalesTax,
			Mode:      "Period",
			DrillDown: true,
		},
		{
			Key:         "general-ledger",
			Title:       "General Ledger",
			Desc:        "Every account's posting trail in one document - opening balance, period activity, ending balance.",
			Href:        "/reports/general-ledger",
			Category:    ReportCategoryAccountantTools,
			Core:        true,
			Mode:        "Period",
			Interactive: true,
			DrillDown:   true,
		},
		{
			Key:         "journal-entries",
			Title:       "Journal Entries",
			Desc:        "Every posted journal entry with reverse + drill-through to source documents.",
			Href:        "/reports/journal-entries",
			Category:    ReportCategoryAccountantTools,
			Core:        true,
			Mode:        "Period",
			Interactive: true,
			DrillDown:   true,
		},
	}
}

// ReportByKey returns the registry entry for a Key, or nil if unknown.
func ReportByKey(key string) *ReportEntry {
	for _, r := range AllReports() {
		if r.Key == key {
			return &r
		}
	}
	return nil
}

// Categories returns the ordered list of categories used on the hub.
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

// ReportsByCategory returns the entries in one category, in registry order.
func ReportsByCategory(c ReportCategory) []ReportEntry {
	out := []ReportEntry{}
	for _, r := range AllReports() {
		if r.Category == c {
			out = append(out, r)
		}
	}
	return out
}

// CoreReports returns the primary report package in registry order.
func CoreReports() []ReportEntry {
	out := []ReportEntry{}
	for _, r := range AllReports() {
		if r.Core {
			out = append(out, r)
		}
	}
	return out
}
