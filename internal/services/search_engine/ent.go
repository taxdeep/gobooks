// 遵循project_guide.md
package search_engine

import (
	"context"
	"errors"
	"strings"

	entsql "entgo.io/ent/dialect/sql"

	"gobooks/ent"
	"gobooks/ent/predicate"
	"gobooks/ent/searchdocument"
	"gobooks/internal/searchprojection"
)

// EntEngine reads from the search_documents projection (populated by
// internal/searchprojection) using the ent-generated client. All query
// paths are company-scoped at the engine layer — EntEngine.Search
// REFUSES to run without a CompanyID, and every generated SQL includes
// WHERE company_id = ? in the first position for index-hit performance.
//
// Ranking strategy (three tiers, union'd then deduped by entity):
//
//  1. Exact / prefix match on doc_number
//     (invoice number, SKU, etc — highest value for typing "INV-…")
//  2. Substring match on title_native
//     (counterparty name match — "Li" finds "Lighting Geek…")
//  3. Substring match on memo_native
//     (descriptions, notes — lowest priority fallback)
//
// Results are then grouped by entity_type family (transactions /
// contacts / products) and sorted within each group by doc_date DESC.
// Each group is capped at PerGroupLimit so no single family dominates
// the dropdown.
type EntEngine struct {
	client     *ent.Client
	normalizer searchprojection.Normalizer
}

// NewEntEngine wires an EntEngine around an ent client + normalizer.
// Passing nil normalizer falls back to ASCII. Returns an error on nil
// client because the engine can't do anything without it.
func NewEntEngine(client *ent.Client, n searchprojection.Normalizer) (*EntEngine, error) {
	if client == nil {
		return nil, errors.New("search_engine: ent client is required")
	}
	if n == nil {
		n = searchprojection.AsciiNormalizer{}
	}
	return &EntEngine{client: client, normalizer: n}, nil
}

func (*EntEngine) Mode() Mode { return ModeEnt }

// Per-group row cap. Empirically 5 matches the QuickBooks dropdown
// density — enough diversity without flooding the operator. Global
// limit is enforced by SearchRequest.Limit.
const perGroupLimit = 5

// recentLimit is the dropdown's empty-query output: the N most recent
// rows across all types. Matches the QB "Recent transactions" panel.
const recentLimit = 15

// Search is the main entry point. Empty query returns the recent
// bucket; non-empty does a tiered match + group.
func (e *EntEngine) Search(ctx context.Context, req SearchRequest) (*SearchResponse, error) {
	if e == nil || e.client == nil {
		return nil, errors.New("search_engine: EntEngine not initialised")
	}
	if req.CompanyID == 0 {
		return nil, errors.New("search_engine: CompanyID is required")
	}

	q := strings.TrimSpace(req.Query)
	if q == "" {
		return e.searchEmpty(ctx, req)
	}
	return e.searchRanked(ctx, req, q)
}

// searchEmpty returns the dropdown's default state: recent transactions,
// date DESC, capped at recentLimit. Non-dated rows (customers / vendors /
// products — which have null doc_date on create) fall to the bottom
// via the NullsLast order below.
func (e *EntEngine) searchEmpty(ctx context.Context, req SearchRequest) (*SearchResponse, error) {
	rows, err := e.client.SearchDocument.Query().
		Where(searchdocument.CompanyIDEQ(req.CompanyID)).
		Order(searchdocument.ByDocDate(entsql.OrderDesc(), entsql.OrderNullsLast())).
		Limit(recentLimit).
		All(ctx)
	if err != nil {
		return nil, err
	}
	return &SearchResponse{
		Candidates: rowsToCandidates(rows),
		Source:     "recent",
	}, nil
}

// searchRanked runs the three-tier query and merges the results.
// The dedup is keyed by (entity_type, entity_id) so an invoice that
// matches both doc_number AND title is shown exactly once, at its
// best-tier position.
func (e *EntEngine) searchRanked(ctx context.Context, req SearchRequest, rawQ string) (*SearchResponse, error) {
	normalized := e.normalizer.Native(rawQ)
	if normalized == "" {
		// Query collapsed to empty after normalisation (e.g. pure
		// punctuation input). Fall back to recent.
		return e.searchEmpty(ctx, req)
	}

	// Tier 1: exact + substring on doc_number (case-insensitive).
	// Doc numbers are short (64-char cap) so ContainsFold is effectively
	// prefix-grade hit quality and saves a second predicate. The SQL
	// `LOWER(doc_number) LIKE '%li%'` sequential scan is bounded by the
	// (company_id, entity_type, doc_number) btree — index narrows to the
	// company's docs first, then filters.
	tier1, err := e.client.SearchDocument.Query().
		Where(
			searchdocument.CompanyIDEQ(req.CompanyID),
			searchdocument.Or(
				searchdocument.DocNumberEqualFold(rawQ),
				searchdocument.DocNumberContainsFold(rawQ),
			),
			searchdocument.DocNumberNEQ(""), // skip rows without a number
		).
		Order(searchdocument.ByDocDate(entsql.OrderDesc(), entsql.OrderNullsLast())).
		Limit(perGroupLimit * 4). // each entity group needs its own budget
		All(ctx)
	if err != nil {
		return nil, err
	}

	// Tier 2: substring on title_native (counterparty / entity name).
	tier2, err := e.client.SearchDocument.Query().
		Where(
			searchdocument.CompanyIDEQ(req.CompanyID),
			searchdocument.TitleNativeContainsFold(normalized),
		).
		Order(searchdocument.ByDocDate(entsql.OrderDesc(), entsql.OrderNullsLast())).
		Limit(perGroupLimit * 4).
		All(ctx)
	if err != nil {
		return nil, err
	}

	// Tier 3: substring on memo_native (descriptions, notes).
	tier3, err := e.client.SearchDocument.Query().
		Where(
			searchdocument.CompanyIDEQ(req.CompanyID),
			searchdocument.MemoNativeContainsFold(normalized),
		).
		Order(searchdocument.ByDocDate(entsql.OrderDesc(), entsql.OrderNullsLast())).
		Limit(perGroupLimit * 4).
		All(ctx)
	if err != nil {
		return nil, err
	}

	// Merge with dedup — the tier the entity first appears in becomes
	// its rank; later tiers don't override.
	seen := make(map[string]struct{}, 64)
	addUnique := func(rows []*ent.SearchDocument, acc []*ent.SearchDocument) []*ent.SearchDocument {
		for _, r := range rows {
			k := r.EntityType + ":" + uintKey(r.EntityID)
			if _, ok := seen[k]; ok {
				continue
			}
			seen[k] = struct{}{}
			acc = append(acc, r)
		}
		return acc
	}
	var merged []*ent.SearchDocument
	merged = addUnique(tier1, merged)
	merged = addUnique(tier2, merged)
	merged = addUnique(tier3, merged)

	// Group by family and cap each group at perGroupLimit.
	grouped := groupAndCap(merged, perGroupLimit)

	// Global hard cap — respect caller's Limit if set.
	limit := req.Limit
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	cands := rowsToCandidates(grouped)
	if len(cands) > limit {
		cands = cands[:limit]
	}

	return &SearchResponse{
		Candidates: cands,
		Source:     "ranked",
	}, nil
}

// groupAndCap bucketises rows by entity family (transactions / contacts
// / products) in the order the UI dropdown wants to display, keeping at
// most `cap` rows per family. Within each family, order is preserved
// (input is already sorted by doc_date DESC from the SQL side).
func groupAndCap(rows []*ent.SearchDocument, cap int) []*ent.SearchDocument {
	// Three fixed families in display order. Unknown entity types fall
	// into "other" — never dropped but deprioritised.
	families := []struct {
		key    string
		member func(string) bool
		rows   []*ent.SearchDocument
	}{
		{GroupTransactions, isTransactionType, nil},
		{GroupContacts, isContactType, nil},
		{GroupProducts, isProductType, nil},
		{"other", func(string) bool { return true }, nil},
	}
	for _, r := range rows {
		placed := false
		for i := range families {
			if families[i].key == "other" {
				continue // drained last
			}
			if families[i].member(r.EntityType) {
				if len(families[i].rows) < cap {
					families[i].rows = append(families[i].rows, r)
				}
				placed = true
				break
			}
		}
		if !placed {
			if len(families[len(families)-1].rows) < cap {
				families[len(families)-1].rows = append(families[len(families)-1].rows, r)
			}
		}
	}
	var out []*ent.SearchDocument
	for _, f := range families {
		out = append(out, f.rows...)
	}
	return out
}

// Entity → family classifier. Keep in sync with producers/* EntityType*
// constants. Putting the switch here (rather than on each model) keeps
// the search layer ignorant of business packages.
func isTransactionType(t string) bool {
	switch t {
	// Phase 3 + Phase 3 re-audit (Expense)
	case "invoice", "bill", "quote", "sales_order", "purchase_order",
		"customer_receipt", "expense",
		// Phase 5.4 / 5.5
		"journal_entry",
		"credit_note", "vendor_credit_note",
		"ar_return", "vendor_return",
		"ar_refund", "vendor_refund",
		"customer_deposit", "vendor_prepayment":
		return true
	}
	return false
}

func isContactType(t string) bool { return t == "customer" || t == "vendor" }

func isProductType(t string) bool { return t == "product_service" }

// rowsToCandidates projects ent rows to the engine's Candidate shape,
// stamping group metadata and action kind based on entity type.
func rowsToCandidates(rows []*ent.SearchDocument) []Candidate {
	out := make([]Candidate, 0, len(rows))
	for _, r := range rows {
		out = append(out, Candidate{
			ID:         uintKey(r.EntityID),
			Primary:    r.Title,
			Secondary:  r.Subtitle,
			GroupKey:   groupKeyFor(r.EntityType),
			GroupLabel: groupLabelFor(r.EntityType),
			ActionKind: ActionNavigate, // every projection row navigates
			URL:        r.URLPath,
			EntityType: r.EntityType,
			Payload: map[string]string{
				"status":   r.Status,
				"amount":   r.Amount,
				"currency": r.Currency,
				"doc_num":  r.DocNumber,
			},
		})
	}
	return out
}

func groupKeyFor(entityType string) string {
	switch {
	case isTransactionType(entityType):
		return GroupTransactions
	case isContactType(entityType):
		return GroupContacts
	case isProductType(entityType):
		return GroupProducts
	default:
		return entityType
	}
}

func groupLabelFor(entityType string) string {
	switch groupKeyFor(entityType) {
	case GroupTransactions:
		return "Transactions"
	case GroupContacts:
		return "Contacts"
	case GroupProducts:
		return "Products & Services"
	}
	return entityType
}

// SearchAdvanced powers the /advanced-search full-page view. Unlike
// Search (which bucket-caps for the dropdown), this returns the flat
// paginated match set + total row count, with optional entity_type /
// date / status filters layered on top of the same three-tier query.
//
// Sort order: matches by doc_number first (exact-code wins), then by
// doc_date DESC (NULLS LAST). Same predicates as Search so query
// behaviour is consistent across surfaces.
func (e *EntEngine) SearchAdvanced(ctx context.Context, req AdvancedRequest) (*AdvancedResponse, error) {
	if e == nil || e.client == nil {
		return nil, errors.New("search_engine: EntEngine not initialised")
	}
	if req.CompanyID == 0 {
		return nil, errors.New("search_engine: CompanyID is required")
	}
	page := req.Page
	if page < 1 {
		page = 1
	}
	pageSize := req.PageSize
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > 200 {
		pageSize = 200
	}

	// Build the predicate stack. The query is OPTIONAL — empty query
	// + just an entity_type filter is a legitimate "browse all invoices"
	// flow (covers the QB pattern where a user opens advanced search
	// from a sidebar link rather than from the dropdown).
	preds := []predicate.SearchDocument{
		searchdocument.CompanyIDEQ(req.CompanyID),
	}
	if req.EntityType != "" {
		preds = append(preds, searchdocument.EntityTypeEQ(req.EntityType))
	}
	if req.Status != "" {
		preds = append(preds, searchdocument.StatusEQ(req.Status))
	}
	if !req.DateFrom.IsZero() {
		preds = append(preds, searchdocument.DocDateGTE(req.DateFrom))
	}
	if !req.DateTo.IsZero() {
		preds = append(preds, searchdocument.DocDateLTE(req.DateTo))
	}
	if q := strings.TrimSpace(req.Query); q != "" {
		normalized := e.normalizer.Native(q)
		if normalized != "" {
			preds = append(preds, searchdocument.Or(
				searchdocument.DocNumberContainsFold(q),
				searchdocument.TitleNativeContainsFold(normalized),
				searchdocument.MemoNativeContainsFold(normalized),
			))
		}
	}

	// Total first — same predicates, no pagination.
	total, err := e.client.SearchDocument.Query().Where(preds...).Count(ctx)
	if err != nil {
		return nil, err
	}

	rows, err := e.client.SearchDocument.Query().
		Where(preds...).
		Order(searchdocument.ByDocDate(entsql.OrderDesc(), entsql.OrderNullsLast())).
		Order(searchdocument.ByID(entsql.OrderDesc())). // tie-breaker for same date
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		All(ctx)
	if err != nil {
		return nil, err
	}

	return &AdvancedResponse{
		Rows:     rowsToCandidates(rows),
		Total:    total,
		Page:     page,
		PageSize: pageSize,
	}, nil
}

// uintKey stringifies an entity ID for Candidate.ID. Isolated so future
// changes (UUID IDs, composite keys) don't require grep across the file.
func uintKey(id uint) string {
	// Inlined strconv.FormatUint; avoids an import line for a one-liner.
	// Using fmt would pull in reflect at init time — cheap, but this is cheaper.
	if id == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for id > 0 {
		i--
		buf[i] = byte('0' + id%10)
		id /= 10
	}
	return string(buf[i:])
}
