// 遵循project_guide.md
package services

// csv_export_service.go — CSV export for clearing, settlement, and channel orders.
// All exports are read-only views of existing data. No state changes.
// All queries are company-scoped.

import (
	"encoding/csv"
	"fmt"
	"io"
	"time"

	"gobooks/internal/models"
	"gorm.io/gorm"
)

// ── Helpers ──────────────────────────────────────────────────────────────────

func csvTimestamp() string {
	return time.Now().Format("20060102-150405")
}

func ptrStr(p *uint) string {
	if p == nil {
		return ""
	}
	return fmt.Sprintf("%d", *p)
}

func timeStr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format("2006-01-02")
}

// ── Status / blocker label helpers (audit-friendly) ──────────────────────────

// SettlementStatusLabel returns a human-readable composite status.
func SettlementStatusLabel(s models.ChannelSettlement) string {
	if s.PostedReversalJEID != nil {
		return "Fee Reversed"
	}
	if s.PostedJournalEntryID != nil {
		return "Fee Posted"
	}
	return "Not Posted"
}

// SettlementPayoutLabel returns the payout status.
func SettlementPayoutLabel(s models.ChannelSettlement) string {
	if s.PayoutReversalJEID != nil {
		return "Payout Reversed"
	}
	if s.PayoutJournalEntryID != nil {
		return "Payout Recorded"
	}
	return "No Payout"
}

// OrderBlockerReason returns a short explanation of why an order is blocked.
func OrderBlockerReason(lines []models.ChannelOrderLine) string {
	for _, l := range lines {
		switch l.MappingStatus {
		case models.MappingStatusUnmapped:
			return "Unmapped SKU: " + l.ExternalSKU
		case models.MappingStatusNeedsReview:
			return "Needs review: " + l.ExternalSKU
		}
	}
	if len(lines) == 0 {
		return "No lines"
	}
	return ""
}

// ── Clearing exports ─────────────────────────────────────────────────────────

// ExportClearingSummaryCSV writes clearing summary rows to w.
func ExportClearingSummaryCSV(db *gorm.DB, companyID uint, w io.Writer) error {
	accounts, _ := ListChannelAccounts(db, companyID)
	var rows [][]string

	for _, a := range accounts {
		summary, err := GetClearingSummary(db, companyID, a.ID)
		if err != nil {
			return err
		}
		if summary == nil {
			continue
		}
		unsettled := "No"
		if !summary.CurrentBalance.IsZero() {
			unsettled = "Yes"
		}
		rows = append(rows, []string{
			fmt.Sprintf("%d", a.ID),
			string(a.ChannelType),
			a.DisplayName,
			summary.ClearingAccountCode + " " + summary.ClearingAccountName,
			summary.CurrentBalance.StringFixed(2),
			summary.SalesTotal.StringFixed(2),
			summary.FeesTotal.StringFixed(2),
			summary.ReversalsTotal.StringFixed(2),
			unsettled,
		})
	}

	cw := csv.NewWriter(w)
	defer cw.Flush()

	cw.Write([]string{
		"Channel Account", "Channel Type", "Display Name",
		"Clearing Account", "Current Balance",
		"Sales Total", "Fees Total", "Reversals Total",
		"Unsettled",
	})

	for _, row := range rows {
		cw.Write(row)
	}
	return nil
}

// ExportClearingMovementsCSV writes clearing movements for a channel to w.
func ExportClearingMovementsCSV(db *gorm.DB, companyID, channelAccountID uint, w io.Writer) error {
	movements, err := ListClearingMovements(db, companyID, channelAccountID, 10000)
	if err != nil {
		return err
	}

	cw := csv.NewWriter(w)
	defer cw.Flush()

	cw.Write([]string{
		"Date", "Source Type", "Source Label", "Source ID",
		"Journal Entry ID", "Debit", "Credit", "Running Balance",
	})

	for _, m := range movements {
		cw.Write([]string{
			m.Date, m.SourceType, m.SourceLabel,
			fmt.Sprintf("%d", m.SourceID),
			fmt.Sprintf("%d", m.JournalEntryID),
			m.Debit.StringFixed(2), m.Credit.StringFixed(2),
			m.RunningBalance.StringFixed(2),
		})
	}
	return nil
}

// ── Settlement exports ───────────────────────────────────────────────────────

// ExportSettlementsListCSV writes all settlements for a company.
func ExportSettlementsListCSV(db *gorm.DB, companyID uint, w io.Writer) error {
	settlements, _ := ListSettlements(db, companyID, 10000)

	cw := csv.NewWriter(w)
	defer cw.Flush()

	cw.Write([]string{
		"Settlement ID", "External ID", "Channel Account", "Channel Type",
		"Settlement Date", "Currency", "Gross Amount", "Fee Amount", "Net Amount",
		"Fee Status", "Fee JE", "Payout Status", "Payout JE",
	})

	for _, s := range settlements {
		cw.Write([]string{
			fmt.Sprintf("%d", s.ID),
			s.ExternalSettlementID,
			s.ChannelAccount.DisplayName,
			string(s.ChannelAccount.ChannelType),
			timeStr(s.SettlementDate),
			s.CurrencyCode,
			s.GrossAmount.StringFixed(2),
			s.FeeAmount.StringFixed(2),
			s.NetAmount.StringFixed(2),
			SettlementStatusLabel(s),
			ptrStr(s.PostedJournalEntryID),
			SettlementPayoutLabel(s),
			ptrStr(s.PayoutJournalEntryID),
		})
	}
	return nil
}

// ExportSettlementLinesCSV writes lines for a specific settlement.
func ExportSettlementLinesCSV(db *gorm.DB, companyID, settlementID uint, w io.Writer) error {
	settlement, err := GetSettlement(db, companyID, settlementID)
	if err != nil {
		return err
	}
	lines, _ := GetSettlementLines(db, companyID, settlementID)

	cw := csv.NewWriter(w)
	defer cw.Flush()

	cw.Write([]string{
		"Settlement ID", "Settlement Date", "Channel Account",
		"Line Type", "Description", "External Ref", "Amount",
		"Mapped Account", "Postable",
	})

	for _, l := range lines {
		acctLabel := ""
		if l.MappedAccount != nil {
			acctLabel = l.MappedAccount.Code + " " + l.MappedAccount.Name
		}
		postable := "No"
		if IsPostableLineType(l.LineType) {
			postable = "Yes"
		}
		cw.Write([]string{
			fmt.Sprintf("%d", settlement.ID),
			timeStr(settlement.SettlementDate),
			settlement.ChannelAccount.DisplayName,
			string(l.LineType),
			l.Description,
			l.ExternalRef,
			l.Amount.StringFixed(2),
			acctLabel,
			postable,
		})
	}
	return nil
}

// ── Channel order exports ────────────────────────────────────────────────────

// ExportChannelOrdersListCSV writes all channel orders for a company.
func ExportChannelOrdersListCSV(db *gorm.DB, companyID uint, w io.Writer) error {
	summaries, _ := ListChannelOrderSummaries(db, companyID, 10000)

	cw := csv.NewWriter(w)
	defer cw.Flush()

	cw.Write([]string{
		"Order ID", "External Order ID", "Channel Account", "Channel Type",
		"Order Date", "Workflow Status", "Line Count", "Unmapped Count",
		"Converted Invoice ID",
	})

	for _, s := range summaries {
		// Derive workflow status.
		orderLines, _ := GetChannelOrderLines(db, companyID, s.Order.ID)
		ws := DeriveOrderWorkflowStatus(s.Order, orderLines)

		cw.Write([]string{
			fmt.Sprintf("%d", s.Order.ID),
			s.Order.ExternalOrderID,
			s.Order.ChannelAccount.DisplayName,
			string(s.Order.ChannelAccount.ChannelType),
			timeStr(s.Order.OrderDate),
			string(ws),
			fmt.Sprintf("%d", s.LineCount),
			fmt.Sprintf("%d", s.UnmappedCount),
			ptrStr(s.Order.ConvertedInvoiceID),
		})
	}
	return nil
}

// ExportChannelOrderLinesCSV writes lines for a specific channel order.
func ExportChannelOrderLinesCSV(db *gorm.DB, companyID, orderID uint, w io.Writer) error {
	order, err := GetChannelOrder(db, companyID, orderID)
	if err != nil {
		return err
	}
	lines, _ := GetChannelOrderLines(db, companyID, orderID)

	cw := csv.NewWriter(w)
	defer cw.Flush()

	cw.Write([]string{
		"Order ID", "External Order ID", "External SKU",
		"Quantity", "Item Price", "Tax Amount", "Discount Amount",
		"Mapped Item", "Mapping Status", "Converted Invoice ID",
	})

	for _, l := range lines {
		mappedItem := ""
		if l.MappedItem != nil {
			mappedItem = l.MappedItem.Name
		}
		price := ""
		if l.ItemPrice != nil {
			price = l.ItemPrice.StringFixed(2)
		}
		tax := ""
		if l.TaxAmount != nil {
			tax = l.TaxAmount.StringFixed(2)
		}
		disc := ""
		if l.DiscountAmount != nil {
			disc = l.DiscountAmount.StringFixed(2)
		}
		cw.Write([]string{
			fmt.Sprintf("%d", order.ID),
			order.ExternalOrderID,
			l.ExternalSKU,
			l.Quantity.String(),
			price, tax, disc,
			mappedItem,
			string(l.MappingStatus),
			ptrStr(order.ConvertedInvoiceID),
		})
	}
	return nil
}
