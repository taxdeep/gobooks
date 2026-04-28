// 遵循project_guide.md
package services

import (
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

// AccountTransactionRow is one line in the account ledger.
type AccountTransactionRow struct {
	Date        string
	Description string // journal entry memo or journal number
	JournalNo   string
	Debit       decimal.Decimal
	Credit      decimal.Decimal
	Balance     decimal.Decimal // running credit-normal balance after this line

	// Drill-down metadata. Populated by BuildAccountTransactionsReport
	// from journal_entries.source_type / .source_id and the JE line's
	// party_type / party_id columns. Surfaces in the report table so an
	// operator can click straight from a tax / expense ledger entry to
	// the originating Bill / Expense / Invoice / Journal Entry.
	TransactionTypeLabel string // human label: "Bill", "Expense", "Invoice", "Journal Entry", …
	DocumentNumber       string // bill_number / invoice_number / receipt_number / journal_no
	DocumentURL          string // /bills/123, /expenses/45, /journal-entry/678 — empty = no drill target
	CounterpartyName     string // resolved customer / vendor name; "" when none
}

// AccountTransactionsReport is the full result for one account ledger view.
type AccountTransactionsReport struct {
	AccountID       uint
	AccountCode     string
	AccountName     string
	AccountRootType string          // e.g. "liability"
	DetailType      string          // e.g. "sales_tax_payable"
	StartingBalance decimal.Decimal // credit-normal balance before fromDate
	Rows            []AccountTransactionRow
	TotalDebits     decimal.Decimal
	TotalCredits    decimal.Decimal
	EndingBalance   decimal.Decimal // credit-normal balance at end of toDate
}

// BuildAccountTransactionsReport loads one account's transaction history for the
// given period. Returns an error when the account does not belong to companyID
// or does not exist.
//
// Balance convention: credit-normal for liability/equity/revenue accounts;
// debit-normal for asset/expense/cost_of_sales accounts. The sign is positive
// when the balance is in the account's normal direction, negative when abnormal.
func BuildAccountTransactionsReport(
	db *gorm.DB,
	companyID, accountID uint,
	fromDate, toDate time.Time,
) (*AccountTransactionsReport, error) {
	// ── 1. Load account ───────────────────────────────────────────────────────
	type accountRow struct {
		ID                uint
		Code              string
		Name              string
		RootAccountType   string
		DetailAccountType string
	}
	var acc accountRow
	if err := db.Raw(`
		SELECT id, code, name, root_account_type, detail_account_type
		FROM accounts
		WHERE id = ? AND company_id = ?
		LIMIT 1
	`, accountID, companyID).Scan(&acc).Error; err != nil {
		return nil, err
	}
	if acc.ID == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	// ── 2. Starting balance (all posted JEs before fromDate) ──────────────────
	type sumRow struct {
		TotalDebit  decimal.Decimal
		TotalCredit decimal.Decimal
	}
	var prePeriod sumRow
	if err := db.Raw(`
		SELECT COALESCE(SUM(jl.debit),  0) AS total_debit,
		       COALESCE(SUM(jl.credit), 0) AS total_credit
		FROM journal_lines jl
		JOIN journal_entries je ON je.id = jl.journal_entry_id
		WHERE jl.account_id = ?
		  AND je.company_id = ?
		  AND `+reportableJournalEntryWhere+`
		  AND je.entry_date < ?
	`, accountID, companyID, fromDate).Scan(&prePeriod).Error; err != nil {
		return nil, err
	}
	startingBalance := signedBalance(acc.RootAccountType, prePeriod.TotalDebit, prePeriod.TotalCredit)

	// ── 3. Period lines ───────────────────────────────────────────────────────
	var lines []accountTransactionLineRow
	if err := db.Raw(`
		SELECT je.id           AS journal_entry_id,
		       je.entry_date,
		       je.journal_no,
		       je.source_type,
		       je.source_id,
		       jl.memo,
		       jl.party_type,
		       jl.party_id,
		       jl.debit,
		       jl.credit
		FROM journal_lines jl
		JOIN journal_entries je ON je.id = jl.journal_entry_id
		WHERE jl.account_id = ?
		  AND je.company_id = ?
		  AND `+reportableJournalEntryWhere+`
		  AND je.entry_date >= ?
		  AND je.entry_date <= ?
		ORDER BY je.entry_date ASC, je.id ASC, jl.id ASC
	`, accountID, companyID, fromDate, toDate).Scan(&lines).Error; err != nil {
		return nil, err
	}

	// ── 3a. Batch-load source-document numbers + party names ──────────────────
	// Avoids N+1 by collecting all (source_type, source_id) and
	// (party_type, party_id) pairs first, then doing one query per
	// type. Empty when no rows have that source/party type.
	docNumbers := loadSourceDocumentNumbers(db, companyID, lines)
	partyNames := loadPartyNames(db, companyID, lines)

	// ── 4. Build rows with running balance ────────────────────────────────────
	rows := make([]AccountTransactionRow, 0, len(lines))
	runningBalance := startingBalance
	var totalDebits, totalCredits decimal.Decimal

	for _, l := range lines {
		delta := signedBalance(acc.RootAccountType, l.Debit, l.Credit)
		runningBalance = runningBalance.Add(delta)
		totalDebits = totalDebits.Add(l.Debit)
		totalCredits = totalCredits.Add(l.Credit)

		desc := l.Memo
		if desc == "" {
			desc = l.JournalNo
		}

		// Resolve drill metadata. JE detail page is the universal
		// fallback — every posted line has a JE, even when the source
		// is empty (manual JE) or unmapped (revaluation, settlement,
		// etc.). docNumber falls back to journal_no for the same reason.
		typeLabel := transactionTypeLabel(l.SourceType)
		docKey := docKey(l.SourceType, l.SourceID)
		docNumber := docNumbers[docKey]
		if docNumber == "" {
			docNumber = l.JournalNo
		}
		docURL := documentURL(l.SourceType, l.SourceID, l.JournalEntryID)

		partyName := partyNames[partyKey(l.PartyType, l.PartyID)]

		rows = append(rows, AccountTransactionRow{
			Date:                 l.EntryDate.Format("2006-01-02"),
			Description:          desc,
			JournalNo:            l.JournalNo,
			Debit:                l.Debit,
			Credit:               l.Credit,
			Balance:              runningBalance,
			TransactionTypeLabel: typeLabel,
			DocumentNumber:       docNumber,
			DocumentURL:          docURL,
			CounterpartyName:     partyName,
		})
	}

	return &AccountTransactionsReport{
		AccountID:       acc.ID,
		AccountCode:     acc.Code,
		AccountName:     acc.Name,
		AccountRootType: acc.RootAccountType,
		DetailType:      acc.DetailAccountType,
		StartingBalance: startingBalance,
		Rows:            rows,
		TotalDebits:     totalDebits,
		TotalCredits:    totalCredits,
		EndingBalance:   runningBalance,
	}, nil
}

// accountTransactionLineRow is the raw-scan shape for one journal-line
// row in the report period. Lifted out of BuildAccountTransactionsReport
// so the batched-lookup helpers (loadSourceDocumentNumbers, etc.) can
// take a typed slice instead of going through interface{} adapters.
type accountTransactionLineRow struct {
	JournalEntryID uint
	EntryDate      time.Time // scanned as time.Time; formatted to "2006-01-02" by the caller
	JournalNo      string
	SourceType     string // from journal_entries.source_type
	SourceID       uint   // from journal_entries.source_id
	Memo           string
	PartyType      string
	PartyID        uint
	Debit          decimal.Decimal
	Credit         decimal.Decimal
}

// signedBalance converts raw debit/credit sums to a signed balance following
// the account's normal balance convention. Delegates to normalBalance in reports.go.
func signedBalance(rootType string, debit, credit decimal.Decimal) decimal.Decimal {
	return normalBalance(models.RootAccountType(rootType), debit, credit)
}

// ── Drill-down helpers ──────────────────────────────────────────────────────

// transactionTypeLabel maps a journal_entries.source_type to the display
// label shown in the report's "Type" column. Unmapped / manual JEs read
// as "Journal Entry" so the column never renders blank.
func transactionTypeLabel(sourceType string) string {
	switch models.LedgerSourceType(sourceType) {
	case models.LedgerSourceInvoice:
		return "Invoice"
	case models.LedgerSourceBill:
		return "Bill"
	case models.LedgerSourceExpense:
		return "Expense"
	case models.LedgerSourceReceipt, models.LedgerSourceCustomerReceipt:
		return "Receipt"
	case models.LedgerSourcePayment:
		return "Payment"
	case models.LedgerSourceCreditNote:
		return "Credit Memo"
	case models.LedgerSourceVendorCreditNote:
		return "Vendor Credit"
	case models.LedgerSourceARRefund:
		return "Customer Refund"
	case models.LedgerSourceVendorRefund:
		return "Vendor Refund"
	case models.LedgerSourceCustomerDeposit:
		return "Customer Deposit"
	case models.LedgerSourceVendorPrepayment:
		return "Vendor Prepayment"
	case models.LedgerSourceARReturnReceipt:
		return "Customer Return"
	case models.LedgerSourceVendorReturnShipment:
		return "Vendor Return"
	case models.LedgerSourceReversal:
		return "Reversal"
	case models.LedgerSourceOpeningBalance:
		return "Opening Balance"
	default:
		// "" (manual), revaluation, settlement, payment_gateway, etc. —
		// no business doc to drill to, so show as Journal Entry.
		return "Journal Entry"
	}
}

// documentURL returns the per-record edit/detail URL the operator can
// click. Falls back to the JE detail page when the source type doesn't
// have its own document UI (manual JEs, FX revaluations, payment-gateway
// postings, etc.).
func documentURL(sourceType string, sourceID, journalEntryID uint) string {
	if sourceID == 0 {
		return jeURL(journalEntryID)
	}
	switch models.LedgerSourceType(sourceType) {
	case models.LedgerSourceInvoice:
		return formatID("/invoices/", sourceID)
	case models.LedgerSourceBill:
		return formatID("/bills/", sourceID)
	case models.LedgerSourceExpense:
		// Expenses don't have a /expenses/:id detail route — they go
		// straight to the editor (the only single-record view).
		return formatID("/expenses/", sourceID) + "/edit"
	case models.LedgerSourceReceipt, models.LedgerSourceCustomerReceipt:
		return formatID("/receipts/", sourceID)
	case models.LedgerSourceCreditNote:
		return formatID("/credit-notes/", sourceID)
	case models.LedgerSourceVendorCreditNote:
		return formatID("/vendor-credit-notes/", sourceID)
	case models.LedgerSourceARRefund:
		return formatID("/refunds/", sourceID)
	case models.LedgerSourceVendorRefund:
		return formatID("/vendor-refunds/", sourceID)
	case models.LedgerSourceCustomerDeposit:
		return formatID("/deposits/", sourceID)
	case models.LedgerSourceVendorPrepayment:
		return formatID("/vendor-prepayments/", sourceID)
	default:
		// AR/vendor returns post via "ar_return_receipt" / "vendor_return_shipment"
		// source types — sourceID points to the receipt/shipment row, not
		// the parent return. Drilling straight to the return would need an
		// extra lookup; fall back to JE detail until that's wired.
		return jeURL(journalEntryID)
	}
}

func jeURL(journalEntryID uint) string {
	if journalEntryID == 0 {
		return ""
	}
	return formatID("/journal-entry/", journalEntryID)
}

// formatID is a tiny strconv-free uint→URL builder. Inline because the
// alternative is one strconv import for one call site.
func formatID(prefix string, id uint) string {
	if id == 0 {
		return ""
	}
	var buf [20]byte
	i := len(buf)
	for id > 0 {
		i--
		buf[i] = byte('0' + id%10)
		id /= 10
	}
	return prefix + string(buf[i:])
}

// docKey + partyKey are the map keys used to dedupe (type, id) pairs
// across the line set so each underlying record is fetched at most
// once per report run.
func docKey(sourceType string, sourceID uint) string {
	if sourceID == 0 {
		return ""
	}
	return sourceType + ":" + formatUint(sourceID)
}
func partyKey(partyType string, partyID uint) string {
	if partyID == 0 || partyType == "" {
		return ""
	}
	return partyType + ":" + formatUint(partyID)
}
func formatUint(n uint) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// loadSourceDocumentNumbers does one batched lookup per source-type
// family (bills.bill_number, invoices.invoice_number, etc.) and returns
// a map keyed by docKey() entries. Source types without a numbered
// document — manual JEs, payment-gateway postings, opening balances —
// are skipped; the caller falls back to journal_no for display.
func loadSourceDocumentNumbers(db *gorm.DB, companyID uint, lines []accountTransactionLineRow) map[string]string {
	out := map[string]string{}

	// Group source IDs by type so we can do one IN query per family.
	groups := map[string][]uint{}
	for _, l := range lines {
		if l.SourceID == 0 || l.SourceType == "" {
			continue
		}
		groups[l.SourceType] = append(groups[l.SourceType], l.SourceID)
	}

	type pair struct {
		ID     uint
		Number string
	}
	for sourceType, ids := range groups {
		table, col := sourceTable(sourceType)
		if table == "" {
			continue // no numbered table for this source type
		}
		var rows []pair
		if err := db.Table(table).
			Select("id", col+" AS number").
			Where("company_id = ? AND id IN ?", companyID, ids).
			Scan(&rows).Error; err != nil {
			continue // fail-quiet: report still renders with journal_no fallback
		}
		for _, r := range rows {
			out[docKey(sourceType, r.ID)] = r.Number
		}
	}
	return out
}

// loadPartyNames fetches customer + vendor display names in two batched
// queries (one per party type). Returns a map keyed by partyKey().
func loadPartyNames(db *gorm.DB, companyID uint, lines []accountTransactionLineRow) map[string]string {
	out := map[string]string{}

	var custIDs, vendIDs []uint
	for _, l := range lines {
		if l.PartyID == 0 {
			continue
		}
		switch models.PartyType(l.PartyType) {
		case models.PartyTypeCustomer:
			custIDs = append(custIDs, l.PartyID)
		case models.PartyTypeVendor:
			vendIDs = append(vendIDs, l.PartyID)
		}
	}

	type pair struct {
		ID   uint
		Name string
	}
	if len(custIDs) > 0 {
		var rows []pair
		if err := db.Table("customers").
			Select("id", "name").
			Where("company_id = ? AND id IN ?", companyID, custIDs).
			Scan(&rows).Error; err == nil {
			for _, r := range rows {
				out[partyKey(string(models.PartyTypeCustomer), r.ID)] = r.Name
			}
		}
	}
	if len(vendIDs) > 0 {
		var rows []pair
		if err := db.Table("vendors").
			Select("id", "name").
			Where("company_id = ? AND id IN ?", companyID, vendIDs).
			Scan(&rows).Error; err == nil {
			for _, r := range rows {
				out[partyKey(string(models.PartyTypeVendor), r.ID)] = r.Name
			}
		}
	}
	return out
}

// sourceTable maps a journal source_type to its (table_name,
// number_column_name). Returns ("", "") when the source type doesn't
// have a numbered business document the operator would recognise.
func sourceTable(sourceType string) (string, string) {
	switch models.LedgerSourceType(sourceType) {
	case models.LedgerSourceInvoice:
		return "invoices", "invoice_number"
	case models.LedgerSourceBill:
		return "bills", "bill_number"
	case models.LedgerSourceExpense:
		return "expenses", "expense_number"
	case models.LedgerSourceReceipt:
		return "receipts", "receipt_number"
	case models.LedgerSourceCustomerReceipt:
		return "customer_receipts", "receipt_number"
	case models.LedgerSourceCreditNote:
		return "credit_notes", "credit_note_number"
	case models.LedgerSourceVendorCreditNote:
		return "vendor_credit_notes", "credit_note_number"
	case models.LedgerSourceARRefund:
		return "ar_refunds", "refund_number"
	case models.LedgerSourceVendorRefund:
		return "vendor_refunds", "refund_number"
	case models.LedgerSourceCustomerDeposit:
		return "customer_deposits", "deposit_number"
	case models.LedgerSourceVendorPrepayment:
		return "vendor_prepayments", "prepayment_number"
	default:
		return "", ""
	}
}
