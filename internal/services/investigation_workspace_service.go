// 遵循project_guide.md
package services

// investigation_workspace_service.go — Batch 24: Investigation workspace query layer.
//
// This file provides the operational visibility aggregation layer for the
// exception investigation workspace.  It is a READ-ONLY consumer of the
// existing exception truth layers — it does not own any domain truth and
// does not modify any records.
//
// ─── Layer position ───────────────────────────────────────────────────────────
//
//   ReconciliationException               ← recon exception truth (Batch 20)
//   PaymentReverseException               ← payment-reverse exception truth (Batch 23)
//   ReconciliationResolutionAttempt       ← hook execution trace (Batch 21)
//   InvestigationWorkspace (THIS)         ← organisation / operational visibility layer
//
// ─── What this layer does ─────────────────────────────────────────────────────
//
//   - Provides a unified list of workspace rows that spans both exception domains
//   - Applies operator-facing filters (domain, status, type, hooks, attempts)
//   - Computes per-row hook availability by reusing the authoritative hook policy
//   - Computes summary bucket counts for the workspace header
//   - Bulk-loads attempt counts in a single aggregate query (not N+1)
//
// ─── What this layer does NOT do ─────────────────────────────────────────────
//
//   - Does not create or modify any exception, payout, bank entry, or JE
//   - Does not own any truth — all data is derived from existing tables
//   - Does not replace the existing per-domain list / detail handlers
//
// ─── HasAvailableHooks accuracy note ─────────────────────────────────────────
//
//   The workspace row + bucket HasAvailableHooks signals reuse
//   AvailableHooksForException so the list, filters, and detail page all speak
//   the same truth. Payment-reverse rows reuse AvailablePaymentReverseHooks for
//   the same reason. This intentionally prefers correctness over avoiding
//   extra matched-state queries.
//
// ─── Pagination design (Batch 27) ────────────────────────────────────────────
//
//   Single-domain queries (Domain filter set, or HasLinkedPayout implicitly
//   excludes payment_reverse):
//     - A COUNT(*) query computes the true total matching the filter.
//     - The main query applies OFFSET + LIMIT at the DB level, so only the
//       requested page is transferred from the database.
//     - totalCount reflects the real total (correct page count in UI).
//
//   Cross-domain queries (both domains active):
//     - Each domain query is capped at maxWorkspaceRowsPerDomain rows.
//     - Rows are merged, sorted, and sliced in memory as before.
//     - totalCount reflects the merged pre-pagination count (≤ 2 × cap).
//     - This cap prevents full-table scans becoming memory bombs as volume
//       grows; true cursor pagination across two tables is deferred to Batch 28.
//
// ─── Not in Batch 24/27 ──────────────────────────────────────────────────────
//   - Assignment / SLA / notification workflows
//   - Analytics / aging reports
//   - Workflow automation for payment_reverse domain
//   - Cursor-based pagination across two domains (Batch 28+)
//   - Full-text search

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"gorm.io/gorm"

	"balanciz/internal/models"
)

// ── Cursor types ──────────────────────────────────────────────────────────────

// WorkspaceCursor is an opaque pagination cursor encoding the position of the
// last row returned by ListWorkspacePage.  It encodes the canonical sort key
// (created_at DESC, domain ASC, id DESC) as a base64 JSON token.
type WorkspaceCursor struct {
	TS     time.Time                  `json:"ts"`
	Domain OperationalExceptionDomain `json:"domain"`
	ID     uint                       `json:"id"`
}

// EncodeCursor serialises c to a URL-safe base64 JSON string.
func EncodeCursor(c WorkspaceCursor) string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

// DecodeCursor parses a cursor produced by EncodeCursor.
// Returns (zero, false) on any parse error.
func DecodeCursor(s string) (WorkspaceCursor, bool) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return WorkspaceCursor{}, false
	}
	var c WorkspaceCursor
	if err := json.Unmarshal(b, &c); err != nil {
		return WorkspaceCursor{}, false
	}
	if c.TS.IsZero() || c.ID == 0 || !isWorkspaceCursorDomain(c.Domain) {
		return WorkspaceCursor{}, false
	}
	return c, true
}

func isWorkspaceCursorDomain(d OperationalExceptionDomain) bool {
	return d == DomainReconciliation || d == DomainPaymentReverse
}

// ── Pagination constants ──────────────────────────────────────────────────────

// maxWorkspaceRowsPerDomain is the maximum number of rows fetched from each
// exception domain when both domains are queried simultaneously.  It prevents
// full-table scans from becoming memory bombs as exception volume grows.
//
// For single-domain queries the limit is applied at the SQL level using
// OFFSET+LIMIT so this cap is not needed; those paths use DB-level pagination.
const maxWorkspaceRowsPerDomain = 500

// ── Domain constants ──────────────────────────────────────────────────────────

// OperationalExceptionDomain identifies the exception truth table a workspace
// row originates from.  It is used for domain-scoped filtering and display.
type OperationalExceptionDomain string

const (
	// DomainReconciliation covers ReconciliationException rows (Batch 20).
	DomainReconciliation OperationalExceptionDomain = "reconciliation"

	// DomainPaymentReverse covers PaymentReverseException rows (Batch 23).
	DomainPaymentReverse OperationalExceptionDomain = "payment_reverse"
)

// DomainLabel returns a human-readable label for the domain.
func DomainLabel(d OperationalExceptionDomain) string {
	switch d {
	case DomainReconciliation:
		return "Reconciliation"
	case DomainPaymentReverse:
		return "Payment Reverse"
	default:
		return string(d)
	}
}

// ── Filter ────────────────────────────────────────────────────────────────────

// WorkspaceFilter holds all supported filter dimensions for the investigation
// workspace list.  All fields are zero-valued by default, meaning "no filter."
type WorkspaceFilter struct {
	// Domain restricts results to one exception domain.  "" = both domains.
	Domain OperationalExceptionDomain

	// Status restricts results to one status value.  "" = all statuses.
	// Must match the string constants used in each domain's status type.
	Status string

	// TypeStr restricts results to one exception type string.  "" = all types.
	TypeStr string

	// HasAvailableHooks, when true, restricts to rows with at least one
	// currently-available hook per the authoritative hook policy.
	HasAvailableHooks bool

	// NoAttempts, when true, restricts to rows with zero resolution attempts.
	// Each domain uses its own attempt truth table.
	NoAttempts bool

	// HasLinkedPayout, when true, restricts to rows with a linked gateway payout.
	// Payment-reverse rows are always excluded (they don't have payouts).
	HasLinkedPayout bool

	// Limit is the maximum number of rows to return.  0 means no limit.
	Limit int

	// Offset is the number of rows to skip before returning results.
	// Used for offset-based pagination.
	Offset int

	// CursorAfter, when non-nil, restricts results to rows appearing after the
	// cursor in the canonical sort order (created_at DESC, domain ASC, id DESC).
	// Applied at the SQL level in each domain query via workspaceCursorWhere.
	CursorAfter *WorkspaceCursor
}

// ── WorkspaceRow ──────────────────────────────────────────────────────────────

// WorkspaceRow is a unified, display-ready row for the investigation workspace.
//
// It is an ephemeral aggregation struct — it is NEVER persisted and does NOT
// replace or modify any exception truth.  Fields are populated from existing
// exception records and read-only derived signals.
type WorkspaceRow struct {
	// Domain and ID uniquely identify the source record.
	Domain OperationalExceptionDomain
	ID     uint

	// TypeLabel is the human-readable exception type label.
	TypeLabel string

	// TypeStr is the raw exception type string (used for template comparisons).
	TypeStr string

	// StatusStr is the raw status string.
	StatusStr string

	// Summary is the one-line exception description from the source record.
	Summary string

	// CreatedAt is the exception creation time (used for sorting).
	CreatedAt time.Time

	// Linked object presence flags — used for display and cross-linking.
	// Not all flags apply to every domain.
	HasLinkedPayout     bool // reconciliation: gateway_payout_id IS NOT NULL
	HasLinkedBankEntry  bool // reconciliation: bank_entry_id IS NOT NULL
	HasLinkedReverseTxn bool // payment_reverse: reverse_txn_id IS NOT NULL

	// HasAvailableHooks is true when the authoritative hook policy says at least
	// one hook is currently available for this exception.
	HasAvailableHooks bool

	// AttemptCount is the number of recorded execution-hook attempts.
	// Populated from the domain-specific attempt truth table.
	AttemptCount int

	// DetailURL is the full path to the domain-specific exception detail page.
	DetailURL string
}

// ── OperationalBuckets ────────────────────────────────────────────────────────

// OperationalBuckets holds summary counts across all exception domains for the
// workspace header.  Counts use the same authoritative hook availability
// criteria as WorkspaceRow.HasAvailableHooks.
type OperationalBuckets struct {
	// OpenCount is the total number of open exceptions across both domains.
	OpenCount int

	// ReviewedCount is the total number of reviewed exceptions across both domains.
	ReviewedCount int

	// ResolvedCount is the total number of resolved exceptions across both domains.
	ResolvedCount int

	// UnresolvedNoAttemptsCount is the count of open/reviewed exceptions with
	// zero resolution attempts.
	UnresolvedNoAttemptsCount int

	// UnresolvedWithHooksCount is the count of open/reviewed exceptions with at
	// least one currently-available hook.
	UnresolvedWithHooksCount int

	// DismissedCount is the total number of dismissed exceptions across both
	// domains.  Dismissed is a terminal state distinct from resolved.
	DismissedCount int
}

// ── Public API ────────────────────────────────────────────────────────────────

// ListWorkspaceRows returns workspace rows across all applicable exception
// domains, with the given filters applied.  Rows are sorted newest-first.
//
// Returns (rows, totalCount, error).  totalCount is the pre-pagination count
// matching the filter — used for pagination controls in the UI.
//
// Pagination strategy:
//   - Single-domain: DB-level OFFSET+LIMIT; totalCount from COUNT(*).
//   - Cross-domain:  in-memory merge after per-domain safety cap; totalCount
//     is the merged pre-pagination count (capped at 2×maxWorkspaceRowsPerDomain).
//
// This function is the single entry point for the workspace list and must be
// the only place that constructs WorkspaceRow values.
func ListWorkspaceRows(db *gorm.DB, companyID uint, f WorkspaceFilter) ([]WorkspaceRow, int, error) {
	if companyID == 0 {
		return nil, 0, fmt.Errorf("company_id is required")
	}

	includeRecon := f.Domain == "" || f.Domain == DomainReconciliation
	includePR := (f.Domain == "" || f.Domain == DomainPaymentReverse)

	// Filters that implicitly exclude the payment-reverse domain.
	if f.HasLinkedPayout {
		includePR = false // payment_reverse exceptions don't carry payout links
	}

	// ── Single-domain path — use DB-level pagination ──────────────────────────
	// When exactly one domain is active (explicit filter or implicit exclusion),
	// push OFFSET+LIMIT to the database and return an accurate total count.
	singleDomain := includeRecon != includePR // XOR: exactly one domain active

	if singleDomain && f.Limit > 0 {
		if includeRecon {
			total, err := countReconWorkspaceRows(db, companyID, f)
			if err != nil {
				return nil, 0, fmt.Errorf("count reconciliation workspace rows: %w", err)
			}
			rows, err := listReconWorkspaceRows(db, companyID, f, f.Limit, f.Offset)
			if err != nil {
				return nil, 0, fmt.Errorf("load reconciliation workspace rows: %w", err)
			}
			return rows, total, nil
		}
		// includePR only
		total, err := countPRWorkspaceRows(db, companyID, f)
		if err != nil {
			return nil, 0, fmt.Errorf("count payment-reverse workspace rows: %w", err)
		}
		rows, err := listPRWorkspaceRows(db, companyID, f, f.Limit, f.Offset)
		if err != nil {
			return nil, 0, fmt.Errorf("load payment-reverse workspace rows: %w", err)
		}
		return rows, total, nil
	}

	// ── Cross-domain path — in-memory merge with safety cap ───────────────────
	// Each domain query is capped at maxWorkspaceRowsPerDomain so that this
	// path never performs a full-table scan when volume is large.  The merged
	// total reflects the capped count, not the full DB count.
	var rows []WorkspaceRow

	if includeRecon {
		reconRows, err := listReconWorkspaceRows(db, companyID, f, maxWorkspaceRowsPerDomain, 0)
		if err != nil {
			return nil, 0, fmt.Errorf("load reconciliation workspace rows: %w", err)
		}
		rows = append(rows, reconRows...)
	}

	if includePR {
		prRows, err := listPRWorkspaceRows(db, companyID, f, maxWorkspaceRowsPerDomain, 0)
		if err != nil {
			return nil, 0, fmt.Errorf("load payment-reverse workspace rows: %w", err)
		}
		rows = append(rows, prRows...)
	}

	// Sort combined rows newest-first; use domain+ID as deterministic tie-breakers.
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].CreatedAt.Equal(rows[j].CreatedAt) {
			if rows[i].Domain != rows[j].Domain {
				return rows[i].Domain < rows[j].Domain
			}
			return rows[i].ID > rows[j].ID
		}
		return rows[i].CreatedAt.After(rows[j].CreatedAt)
	})

	total := len(rows)

	// Apply Go-level pagination after sort (cross-domain only).
	if f.Limit > 0 {
		rows = paginateWorkspaceRows(rows, f.Limit, f.Offset)
	}

	return rows, total, nil
}

// ── WorkspacePage ─────────────────────────────────────────────────────────────

// WorkspacePage is the result of a paginated workspace query.
// Total is always an accurate SQL COUNT — it is not capped by the per-domain
// safety cap used when fetching rows.
type WorkspacePage struct {
	// Rows is the current page of workspace rows.
	Rows []WorkspaceRow

	// Total is the accurate count of all rows matching the filter, across all
	// active domains.  Used to compute page counts in the UI.
	Total int

	// HasMore is true when there are additional rows beyond this page.
	HasMore bool

	// NextCursor is an opaque token that can be passed as CursorAfter in a
	// subsequent request to fetch the next page.  Empty when HasMore is false.
	NextCursor string
}

// ListWorkspacePage returns a paginated workspace page with accurate totals.
//
// Unlike ListWorkspaceRows, the cross-domain total is computed via SQL COUNT
// on each domain separately, so Total is never capped.  HasMore and NextCursor
// support cursor-based navigation in addition to offset-based pagination.
//
// Single-domain path: delegates to ListWorkspaceRows for DB-level pagination.
// Cross-domain path:  SQL COUNT per domain, in-memory merge for the current page.
func ListWorkspacePage(db *gorm.DB, companyID uint, f WorkspaceFilter) (WorkspacePage, error) {
	if companyID == 0 {
		return WorkspacePage{}, fmt.Errorf("company_id is required")
	}

	includeRecon := f.Domain == "" || f.Domain == DomainReconciliation
	includePR := (f.Domain == "" || f.Domain == DomainPaymentReverse)

	if f.HasLinkedPayout {
		includePR = false
	}

	singleDomain := includeRecon != includePR

	totalFilter := f
	totalFilter.CursorAfter = nil
	totalFilter.Offset = 0
	totalFilter.Limit = 0

	// ── Single-domain path — DB pagination or cursor continuation ─────────────
	if singleDomain && f.Limit > 0 {
		var (
			rows  []WorkspaceRow
			total int
			err   error
		)
		pageOffset := f.Offset
		if pageOffset < 0 {
			pageOffset = 0
		}

		limit := f.Limit
		if f.CursorAfter != nil {
			limit++
			pageOffset = 0
		}

		if includeRecon {
			total, err = countReconWorkspaceRows(db, companyID, totalFilter)
			if err != nil {
				return WorkspacePage{}, fmt.Errorf("count reconciliation workspace rows: %w", err)
			}
			rows, err = listReconWorkspaceRows(db, companyID, f, limit, pageOffset)
			if err != nil {
				return WorkspacePage{}, fmt.Errorf("load reconciliation workspace rows: %w", err)
			}
		} else {
			total, err = countPRWorkspaceRows(db, companyID, totalFilter)
			if err != nil {
				return WorkspacePage{}, fmt.Errorf("count payment-reverse workspace rows: %w", err)
			}
			rows, err = listPRWorkspaceRows(db, companyID, f, limit, pageOffset)
			if err != nil {
				return WorkspacePage{}, fmt.Errorf("load payment-reverse workspace rows: %w", err)
			}
		}

		var hasMore bool
		if f.CursorAfter != nil {
			rows, hasMore = trimWorkspaceRowsForCursor(rows, f.Limit)
		} else {
			hasMore = (pageOffset + len(rows)) < total
		}
		return WorkspacePage{Rows: rows, Total: total, HasMore: hasMore, NextCursor: nextWorkspaceCursor(rows, hasMore)}, nil
	}

	// ── Cross-domain path — accurate SQL COUNT + in-memory merge ─────────────

	// Step 1: accurate total via separate COUNT queries per domain.
	var reconTotal, prTotal int
	var err error
	if includeRecon {
		reconTotal, err = countReconWorkspaceRows(db, companyID, totalFilter)
		if err != nil {
			return WorkspacePage{}, fmt.Errorf("count reconciliation workspace rows: %w", err)
		}
	}
	if includePR {
		prTotal, err = countPRWorkspaceRows(db, companyID, totalFilter)
		if err != nil {
			return WorkspacePage{}, fmt.Errorf("count payment-reverse workspace rows: %w", err)
		}
	}
	total := reconTotal + prTotal

	// Step 2: fetch rows for the current page.
	// Cap per-domain fetch at max(offset+limit, maxWorkspaceRowsPerDomain) so
	// that worst-case interleaving is still covered while memory is bounded.
	fetchCap := maxWorkspaceRowsPerDomain
	pageOffset := f.Offset
	if pageOffset < 0 {
		pageOffset = 0
	}
	if f.Limit > 0 {
		if f.CursorAfter != nil {
			fetchCap = f.Limit + 1
			pageOffset = 0
		} else if needed := pageOffset + f.Limit; needed > fetchCap {
			fetchCap = needed
		}
	}

	var rows []WorkspaceRow
	if includeRecon {
		rr, err := listReconWorkspaceRows(db, companyID, f, fetchCap, 0)
		if err != nil {
			return WorkspacePage{}, fmt.Errorf("load reconciliation workspace rows: %w", err)
		}
		rows = append(rows, rr...)
	}
	if includePR {
		pr, err := listPRWorkspaceRows(db, companyID, f, fetchCap, 0)
		if err != nil {
			return WorkspacePage{}, fmt.Errorf("load payment-reverse workspace rows: %w", err)
		}
		rows = append(rows, pr...)
	}

	// Sort: newest-first, then domain ASC, then id DESC (deterministic).
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].CreatedAt.Equal(rows[j].CreatedAt) {
			if rows[i].Domain != rows[j].Domain {
				return rows[i].Domain < rows[j].Domain
			}
			return rows[i].ID > rows[j].ID
		}
		return rows[i].CreatedAt.After(rows[j].CreatedAt)
	})

	if f.Limit > 0 {
		if f.CursorAfter != nil {
			var hasMore bool
			rows, hasMore = trimWorkspaceRowsForCursor(rows, f.Limit)
			return WorkspacePage{Rows: rows, Total: total, HasMore: hasMore, NextCursor: nextWorkspaceCursor(rows, hasMore)}, nil
		}
		rows = paginateWorkspaceRows(rows, f.Limit, pageOffset)
	}

	hasMore := (pageOffset + len(rows)) < total
	return WorkspacePage{Rows: rows, Total: total, HasMore: hasMore, NextCursor: nextWorkspaceCursor(rows, hasMore)}, nil
}

func trimWorkspaceRowsForCursor(rows []WorkspaceRow, limit int) ([]WorkspaceRow, bool) {
	if limit <= 0 || len(rows) <= limit {
		return rows, false
	}
	return rows[:limit], true
}

func nextWorkspaceCursor(rows []WorkspaceRow, hasMore bool) string {
	if !hasMore || len(rows) == 0 {
		return ""
	}
	last := rows[len(rows)-1]
	return EncodeCursor(WorkspaceCursor{TS: last.CreatedAt, Domain: last.Domain, ID: last.ID})
}

// workspaceCursorWhere appends a WHERE clause that restricts a domain query to
// rows appearing after cursor in the canonical sort order
// (created_at DESC, domain ASC, id DESC).
//
// domainStr is the constant domain string for the queried table (not a column).
// It is passed as a SQL parameter so the DB evaluates ordering correctly.
func workspaceCursorWhere(q *gorm.DB, cursor WorkspaceCursor, domainStr string) *gorm.DB {
	curDomain := string(cursor.Domain)
	return q.Where(
		"(created_at < ?) OR"+
			" (created_at = ? AND ? > ?) OR"+
			" (created_at = ? AND ? = ? AND id < ?)",
		cursor.TS,
		cursor.TS, domainStr, curDomain,
		cursor.TS, domainStr, curDomain, cursor.ID,
	)
}

func paginateWorkspaceRows(rows []WorkspaceRow, limit, offset int) []WorkspaceRow {
	if limit <= 0 {
		return rows
	}
	if offset < 0 {
		offset = 0
	}
	total := len(rows)
	if offset >= total {
		return []WorkspaceRow{}
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return rows[offset:end]
}

// CountOperationalBuckets computes summary counts across all exception domains
// for the workspace header.  All counts are computed in this single call so
// the handler can display a self-consistent snapshot.
func CountOperationalBuckets(db *gorm.DB, companyID uint) (OperationalBuckets, error) {
	var b OperationalBuckets
	if companyID == 0 {
		return b, fmt.Errorf("company_id is required")
	}

	// ── Open count (both domains) ─────────────────────────────────────────────

	var reconOpen, prOpen int64
	if err := db.Model(&models.ReconciliationException{}).
		Where("company_id = ? AND status = ?", companyID, models.ExceptionStatusOpen).
		Count(&reconOpen).Error; err != nil {
		return b, fmt.Errorf("count open recon exceptions: %w", err)
	}
	if err := db.Model(&models.PaymentReverseException{}).
		Where("company_id = ? AND status = ?", companyID, models.PRExceptionStatusOpen).
		Count(&prOpen).Error; err != nil {
		return b, fmt.Errorf("count open payment-reverse exceptions: %w", err)
	}
	b.OpenCount = int(reconOpen + prOpen)

	// ── Reviewed count (both domains) ─────────────────────────────────────────

	var reconReviewed, prReviewed int64
	if err := db.Model(&models.ReconciliationException{}).
		Where("company_id = ? AND status = ?", companyID, models.ExceptionStatusReviewed).
		Count(&reconReviewed).Error; err != nil {
		return b, fmt.Errorf("count reviewed recon exceptions: %w", err)
	}
	if err := db.Model(&models.PaymentReverseException{}).
		Where("company_id = ? AND status = ?", companyID, models.PRExceptionStatusReviewed).
		Count(&prReviewed).Error; err != nil {
		return b, fmt.Errorf("count reviewed payment-reverse exceptions: %w", err)
	}
	b.ReviewedCount = int(reconReviewed + prReviewed)

	var reconResolved, prResolved int64
	if err := db.Model(&models.ReconciliationException{}).
		Where("company_id = ? AND status = ?", companyID, models.ExceptionStatusResolved).
		Count(&reconResolved).Error; err != nil {
		return b, fmt.Errorf("count resolved recon exceptions: %w", err)
	}
	if err := db.Model(&models.PaymentReverseException{}).
		Where("company_id = ? AND status = ?", companyID, models.PRExceptionStatusResolved).
		Count(&prResolved).Error; err != nil {
		return b, fmt.Errorf("count resolved payment-reverse exceptions: %w", err)
	}
	b.ResolvedCount = int(reconResolved + prResolved)

	// ── Dismissed count (both domains) ────────────────────────────────────────

	var reconDismissed, prDismissed int64
	if err := db.Model(&models.ReconciliationException{}).
		Where("company_id = ? AND status = ?", companyID, models.ExceptionStatusDismissed).
		Count(&reconDismissed).Error; err != nil {
		return b, fmt.Errorf("count dismissed recon exceptions: %w", err)
	}
	if err := db.Model(&models.PaymentReverseException{}).
		Where("company_id = ? AND status = ?", companyID, models.PRExceptionStatusDismissed).
		Count(&prDismissed).Error; err != nil {
		return b, fmt.Errorf("count dismissed payment-reverse exceptions: %w", err)
	}
	b.DismissedCount = int(reconDismissed + prDismissed)

	// ── Unresolved with no attempts ───────────────────────────────────────────
	//
	// Each domain uses its own attempt truth table.

	var reconNoAttempts, prNoAttempts int64
	if err := db.Model(&models.ReconciliationException{}).
		Where("company_id = ? AND status IN ?", companyID,
			[]string{
				string(models.ExceptionStatusOpen),
				string(models.ExceptionStatusReviewed),
			}).
		Where(
			"NOT EXISTS (SELECT 1 FROM reconciliation_resolution_attempts"+
				" WHERE reconciliation_exception_id = reconciliation_exceptions.id"+
				" AND company_id = ?)",
			companyID,
		).
		Count(&reconNoAttempts).Error; err != nil {
		return b, fmt.Errorf("count recon no-attempts: %w", err)
	}
	if err := db.Model(&models.PaymentReverseException{}).
		Where("company_id = ? AND status IN ?", companyID,
			[]string{
				string(models.PRExceptionStatusOpen),
				string(models.PRExceptionStatusReviewed),
			}).
		Where(
			"NOT EXISTS (SELECT 1 FROM payment_reverse_resolution_attempts"+
				" WHERE payment_reverse_exception_id = payment_reverse_exceptions.id"+
				" AND company_id = ?)",
			companyID,
		).
		Count(&prNoAttempts).Error; err != nil {
		return b, fmt.Errorf("count payment-reverse no-attempts: %w", err)
	}
	b.UnresolvedNoAttemptsCount = int(reconNoAttempts + prNoAttempts)

	// ── Unresolved with available hooks ───────────────────────────────────────
	//
	// Count rows where the authoritative hook policy says at least one hook is
	// currently available.
	var reconCandidates []models.ReconciliationException
	if err := db.
		Where(
			"company_id = ? AND status IN ? AND exception_type IN ? AND gateway_payout_id IS NOT NULL",
			companyID,
			[]string{
				string(models.ExceptionStatusOpen),
				string(models.ExceptionStatusReviewed),
			},
			[]string{
				string(models.ExceptionAmountMismatch),
				string(models.ExceptionUnknownComponentPattern),
			},
		).
		Find(&reconCandidates).Error; err != nil {
		return b, fmt.Errorf("load recon hook candidates: %w", err)
	}
	for i := range reconCandidates {
		if hasAvailableReconHooks(db, companyID, &reconCandidates[i]) {
			b.UnresolvedWithHooksCount++
		}
	}
	var prCandidates []models.PaymentReverseException
	if err := db.
		Where(
			"company_id = ? AND status IN ? AND (reverse_txn_id IS NOT NULL OR original_txn_id IS NOT NULL)",
			companyID,
			[]string{
				string(models.PRExceptionStatusOpen),
				string(models.PRExceptionStatusReviewed),
			},
		).
		Find(&prCandidates).Error; err != nil {
		return b, fmt.Errorf("load payment-reverse hook candidates: %w", err)
	}
	for i := range prCandidates {
		if hasAvailablePRHooks(db, companyID, &prCandidates[i]) {
			b.UnresolvedWithHooksCount++
		}
	}

	return b, nil
}

// ── Internal: reconciliation domain rows ─────────────────────────────────────

// applyReconWorkspaceFilters builds the filtered base query for recon exceptions.
// The returned query does not include ORDER BY, LIMIT, or OFFSET.
func applyReconWorkspaceFilters(db *gorm.DB, companyID uint, f WorkspaceFilter) *gorm.DB {
	q := db.Model(&models.ReconciliationException{}).Where("company_id = ?", companyID)
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	if f.TypeStr != "" {
		q = q.Where("exception_type = ?", f.TypeStr)
	}
	if f.HasAvailableHooks {
		q = q.Where(
			"exception_type IN ? AND status IN ? AND gateway_payout_id IS NOT NULL",
			[]string{
				string(models.ExceptionAmountMismatch),
				string(models.ExceptionUnknownComponentPattern),
			},
			[]string{
				string(models.ExceptionStatusOpen),
				string(models.ExceptionStatusReviewed),
			},
		)
	}
	if f.NoAttempts {
		q = q.Where(
			"NOT EXISTS (SELECT 1 FROM reconciliation_resolution_attempts"+
				" WHERE reconciliation_exception_id = reconciliation_exceptions.id"+
				" AND company_id = ?)",
			companyID,
		)
	}
	if f.HasLinkedPayout {
		q = q.Where("gateway_payout_id IS NOT NULL")
	}
	if f.CursorAfter != nil {
		q = workspaceCursorWhere(q, *f.CursorAfter, string(DomainReconciliation))
	}
	return q
}

// countReconWorkspaceRows returns the total number of reconciliation exceptions
// matching the filter, without pagination.  Used for single-domain pagination.
func countReconWorkspaceRows(db *gorm.DB, companyID uint, f WorkspaceFilter) (int, error) {
	var total int64
	if err := applyReconWorkspaceFilters(db, companyID, f).Count(&total).Error; err != nil {
		return 0, err
	}
	// HasAvailableHooks requires a post-query filter (hook availability is not
	// expressible purely in SQL).  Run a full load to count accurately.
	// For cross-domain path this is handled via the merged in-memory total.
	if f.HasAvailableHooks {
		rows, err := listReconWorkspaceRows(db, companyID, f, 0, 0)
		if err != nil {
			return 0, err
		}
		return len(rows), nil
	}
	return int(total), nil
}

// listReconWorkspaceRows loads reconciliation exception workspace rows.
//
//	limit  > 0: apply DB-level LIMIT (0 = fetch all, capped by caller).
//	offset > 0: apply DB-level OFFSET.
//
// When HasAvailableHooks is set, post-query hook-policy filtering is applied
// after fetching (hook availability cannot be expressed in SQL).
func listReconWorkspaceRows(db *gorm.DB, companyID uint, f WorkspaceFilter, limit, offset int) ([]WorkspaceRow, error) {
	q := applyReconWorkspaceFilters(db, companyID, f).Order("created_at DESC, id DESC")
	sqlPage := !f.HasAvailableHooks
	if sqlPage && limit > 0 {
		q = q.Limit(limit)
	}
	if sqlPage && offset > 0 {
		q = q.Offset(offset)
	}

	var exceptions []models.ReconciliationException
	if err := q.Find(&exceptions).Error; err != nil {
		return nil, err
	}
	if len(exceptions) == 0 {
		return nil, nil
	}

	// Bulk-load attempt counts in a single aggregate query.
	ids := make([]uint, len(exceptions))
	for i, ex := range exceptions {
		ids[i] = ex.ID
	}
	attemptCounts := loadReconAttemptCounts(db, companyID, ids)

	rows := make([]WorkspaceRow, len(exceptions))
	for i, ex := range exceptions {
		rows[i] = WorkspaceRow{
			Domain:             DomainReconciliation,
			ID:                 ex.ID,
			TypeLabel:          models.ReconciliationExceptionTypeLabel(ex.ExceptionType),
			TypeStr:            string(ex.ExceptionType),
			StatusStr:          string(ex.Status),
			Summary:            ex.Summary,
			CreatedAt:          ex.CreatedAt,
			HasLinkedPayout:    ex.GatewayPayoutID != nil,
			HasLinkedBankEntry: ex.BankEntryID != nil,
			HasAvailableHooks:  hasAvailableReconHooks(db, companyID, &ex),
			AttemptCount:       attemptCounts[ex.ID],
			DetailURL:          fmt.Sprintf("/settings/payment-gateways/reconciliation-exceptions/%d", ex.ID),
		}
	}

	if f.HasAvailableHooks {
		filtered := rows[:0]
		for _, row := range rows {
			if row.HasAvailableHooks {
				filtered = append(filtered, row)
			}
		}
		rows = paginateWorkspaceRows(filtered, limit, offset)
	}
	return rows, nil
}

// ── Internal: payment-reverse domain rows ────────────────────────────────────

// applyPRWorkspaceFilters builds the filtered base query for payment-reverse exceptions.
func applyPRWorkspaceFilters(db *gorm.DB, companyID uint, f WorkspaceFilter) *gorm.DB {
	q := db.Model(&models.PaymentReverseException{}).Where("company_id = ?", companyID)
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	if f.TypeStr != "" {
		q = q.Where("exception_type = ?", f.TypeStr)
	}
	if f.NoAttempts {
		q = q.Where(
			"NOT EXISTS (SELECT 1 FROM payment_reverse_resolution_attempts"+
				" WHERE payment_reverse_exception_id = payment_reverse_exceptions.id"+
				" AND company_id = ?)",
			companyID,
		)
	}
	if f.HasAvailableHooks {
		// Truth-preserving pre-filter: only load exceptions that could plausibly
		// have any payment-reverse hook, then let AvailablePaymentReverseHooks
		// decide the final row/filter truth.  Navigation hooks may be available
		// with only one linked transaction, so this must not require both links or
		// globally exclude prior retry-check successes.
		q = q.Where(
			"status IN ? AND (reverse_txn_id IS NOT NULL OR original_txn_id IS NOT NULL)",
			[]string{
				string(models.PRExceptionStatusOpen),
				string(models.PRExceptionStatusReviewed),
			},
		)
	}
	if f.CursorAfter != nil {
		q = workspaceCursorWhere(q, *f.CursorAfter, string(DomainPaymentReverse))
	}
	return q
}

// countPRWorkspaceRows returns the total number of payment-reverse exceptions
// matching the filter, without pagination.  Used for single-domain pagination.
func countPRWorkspaceRows(db *gorm.DB, companyID uint, f WorkspaceFilter) (int, error) {
	if f.HasAvailableHooks {
		rows, err := listPRWorkspaceRows(db, companyID, f, 0, 0)
		if err != nil {
			return 0, err
		}
		return len(rows), nil
	}
	var total int64
	if err := applyPRWorkspaceFilters(db, companyID, f).Count(&total).Error; err != nil {
		return 0, err
	}
	return int(total), nil
}

// listPRWorkspaceRows loads payment-reverse exception workspace rows.
//
//	limit  > 0: apply DB-level LIMIT (0 = fetch all, capped by caller).
//	offset > 0: apply DB-level OFFSET.
func listPRWorkspaceRows(db *gorm.DB, companyID uint, f WorkspaceFilter, limit, offset int) ([]WorkspaceRow, error) {
	q := applyPRWorkspaceFilters(db, companyID, f).Order("created_at DESC, id DESC")
	sqlPage := !f.HasAvailableHooks
	if sqlPage && limit > 0 {
		q = q.Limit(limit)
	}
	if sqlPage && offset > 0 {
		q = q.Offset(offset)
	}
	// f.HasLinkedPayout exclusion is applied in the caller (ListWorkspaceRows).

	var exceptions []models.PaymentReverseException
	if err := q.Find(&exceptions).Error; err != nil {
		return nil, err
	}
	if len(exceptions) == 0 {
		return nil, nil
	}

	ids := make([]uint, len(exceptions))
	for i, ex := range exceptions {
		ids[i] = ex.ID
	}
	attemptCounts := loadPRAttemptCounts(db, companyID, ids)

	rows := make([]WorkspaceRow, len(exceptions))
	for i, ex := range exceptions {
		rows[i] = WorkspaceRow{
			Domain:              DomainPaymentReverse,
			ID:                  ex.ID,
			TypeLabel:           models.PaymentReverseExceptionTypeLabel(ex.ExceptionType),
			TypeStr:             string(ex.ExceptionType),
			StatusStr:           string(ex.Status),
			Summary:             ex.Summary,
			CreatedAt:           ex.CreatedAt,
			HasLinkedReverseTxn: ex.ReverseTxnID != nil,
			HasAvailableHooks:   hasAvailablePRHooks(db, companyID, &ex),
			AttemptCount:        attemptCounts[ex.ID],
			DetailURL:           fmt.Sprintf("/settings/payment-gateways/reverse-exceptions/%d", ex.ID),
		}
	}
	if f.HasAvailableHooks {
		filtered := rows[:0]
		for _, row := range rows {
			if row.HasAvailableHooks {
				filtered = append(filtered, row)
			}
		}
		rows = paginateWorkspaceRows(filtered, limit, offset)
	}
	return rows, nil
}

// ── Internal: authoritative hook availability ─────────────────────────────────

// hasAvailableReconHooks returns true when the authoritative hook policy says
// at least one hook is currently available for this reconciliation exception.
func hasAvailableReconHooks(db *gorm.DB, companyID uint, ex *models.ReconciliationException) bool {
	for _, hook := range AvailableHooksForException(db, companyID, ex) {
		if hook.Available {
			return true
		}
	}
	return false
}

// hasAvailablePRHooks returns true when the authoritative hook policy says
// at least one hook is currently available for this payment-reverse exception.
func hasAvailablePRHooks(db *gorm.DB, companyID uint, ex *models.PaymentReverseException) bool {
	for _, hook := range AvailablePaymentReverseHooks(db, companyID, ex) {
		if hook.Available {
			return true
		}
	}
	return false
}

// ── Internal: attempt count bulk-loader ──────────────────────────────────────

// loadReconAttemptCounts returns a map from reconciliation exception ID to
// attempt count, populated in a single aggregate query.  Missing IDs return 0.
func loadReconAttemptCounts(db *gorm.DB, companyID uint, exceptionIDs []uint) map[uint]int {
	counts := make(map[uint]int, len(exceptionIDs))
	if len(exceptionIDs) == 0 {
		return counts
	}

	type countRow struct {
		ReconciliationExceptionID uint
		Count                     int
	}
	var rows []countRow
	db.Model(&models.ReconciliationResolutionAttempt{}).
		Select("reconciliation_exception_id, COUNT(*) as count").
		Where("company_id = ? AND reconciliation_exception_id IN ?", companyID, exceptionIDs).
		Group("reconciliation_exception_id").
		Scan(&rows)

	for _, r := range rows {
		counts[r.ReconciliationExceptionID] = r.Count
	}
	return counts
}

// loadPRAttemptCounts returns a map from payment-reverse exception ID to
// attempt count, populated in a single aggregate query.
func loadPRAttemptCounts(db *gorm.DB, companyID uint, exceptionIDs []uint) map[uint]int {
	counts := make(map[uint]int, len(exceptionIDs))
	if len(exceptionIDs) == 0 {
		return counts
	}

	type countRow struct {
		PaymentReverseExceptionID uint
		Count                     int
	}
	var rows []countRow
	db.Model(&models.PaymentReverseResolutionAttempt{}).
		Select("payment_reverse_exception_id, COUNT(*) as count").
		Where("company_id = ? AND payment_reverse_exception_id IN ?", companyID, exceptionIDs).
		Group("payment_reverse_exception_id").
		Scan(&rows)

	for _, r := range rows {
		counts[r.PaymentReverseExceptionID] = r.Count
	}
	return counts
}
