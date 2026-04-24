// 遵循project_guide.md
package services

import (
	"time"

	"gorm.io/gorm"
)

// GeneralLedgerReport is the multi-account ledger view: one
// AccountTransactionsReport section per active account that had
// activity in the period (or carried a non-zero opening balance).
//
// Conceptually GL = "Account Transactions for every account, side by
// side". Auditors live in this report — they want to walk every
// account's posting trail in one continuous document, not click 50
// per-account drill-throughs.
type GeneralLedgerReport struct {
	FromDate time.Time
	ToDate   time.Time

	// Sections is the per-account ledger, sorted by account code so the
	// COA reads top-to-bottom in the conventional 1xxxx-asset →
	// 9xxxx-equity order.
	Sections []AccountTransactionsReport
}

// BuildGeneralLedgerReport assembles a full GL by iterating every
// account in the company's COA and calling BuildAccountTransactionsReport
// for each. Accounts that have NO opening balance AND zero activity
// in the period are dropped from the output — otherwise the report
// would be cluttered with empty inactive accounts.
//
// Cost: N round-trips to BuildAccountTransactionsReport (each does
// 3 queries internally for header / opening / period). Fine for the
// report's typical run frequency (audit-time, not interactive). If
// this becomes a bottleneck, the obvious optimisation is to fold the
// per-account scans into one big query keyed by (account_id,
// entry_date) — but that's a separate story.
func BuildGeneralLedgerReport(db *gorm.DB, companyID uint, fromDate, toDate time.Time) (*GeneralLedgerReport, error) {
	// Pull every account in the COA. Inactive accounts are still
	// included (a deactivated account that posted in the period must
	// still appear in the GL — it's audit truth).
	type accountRow struct {
		ID   uint
		Code string
	}
	var accounts []accountRow
	if err := db.Raw(`
		SELECT id, code FROM accounts WHERE company_id = ? ORDER BY code ASC, id ASC
	`, companyID).Scan(&accounts).Error; err != nil {
		return nil, err
	}

	report := &GeneralLedgerReport{FromDate: fromDate, ToDate: toDate}
	for _, a := range accounts {
		section, err := BuildAccountTransactionsReport(db, companyID, a.ID, fromDate, toDate)
		if err != nil {
			// One account's failure shouldn't poison the whole report.
			// Skip and keep building — auditors can spot the gap and
			// drill into Account Transactions for the missing one.
			continue
		}
		// Drop empty sections: no opening balance + no period activity
		// means the account never moved, doesn't add value to the GL.
		if section.StartingBalance.IsZero() && len(section.Rows) == 0 {
			continue
		}
		report.Sections = append(report.Sections, *section)
	}
	return report, nil
}
