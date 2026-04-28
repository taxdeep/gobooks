// 遵循project_guide.md
package services

// reconcile_match_engine.go implements a three-layer matching engine for
// bank reconciliation auto-match:
//
//   Layer 1 – Deterministic: exact amount, exact subset-sum combinations.
//   Layer 2 – Heuristic scoring: date proximity, source reliability, payee
//              clustering, historical memory boost.
//   Layer 3 – Structured explanation: named signals with scores and a human-
//              readable summary; clean extension point for future ML/LLM ranking.
//
// Design principles (per spec):
//   • Rules first, AI second.
//   • Engine suggests; user confirms — no silent accounting changes.
//   • Every suggestion is explainable via named signals.
//   • Multi-company isolation enforced at every query.
//   • Extension points: GeneratedBy version tag, signal registry, memory boost.

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"balanciz/internal/models"
)

// ── Confidence tiers ──────────────────────────────────────────────────────────

const (
	ConfTierHigh   = "high"   // confidence ≥ 0.75
	ConfTierMedium = "medium" // confidence ≥ 0.45
	ConfTierLow    = "low"    // confidence < 0.45
)

// ConfidenceTier maps a raw score to a display tier.
func ConfidenceTier(score float64) string {
	switch {
	case score >= 0.75:
		return ConfTierHigh
	case score >= 0.45:
		return ConfTierMedium
	default:
		return ConfTierLow
	}
}

// ── Explanation types (Layer 3) ───────────────────────────────────────────────

// MatchSignal is one scored evidence item within a suggestion's explanation.
// Future ML/LLM integrations can add their own signal names alongside these
// deterministic ones without breaking existing consumers.
type MatchSignal struct {
	Name   string  `json:"name"`
	Score  float64 `json:"score"`  // [0, 1]
	Detail string  `json:"detail"` // human-readable sentence
}

// MatchExplanation is the structured explanation stored in explanation_json.
// The UI renders Summary for a one-liner and Signals for the expandable detail view.
type MatchExplanation struct {
	Summary    string        `json:"summary"`
	Signals    []MatchSignal `json:"signals"`
	TotalLines int           `json:"total_lines"`
	NetAmount  string        `json:"net_amount"`
	Tier       string        `json:"tier"`
}

// ToJSON serialises the explanation to the string stored in the DB column.
func (e MatchExplanation) ToJSON() string {
	b, _ := json.Marshal(e)
	return string(b)
}

// ParseMatchExplanation deserialises the explanation from its DB string.
func ParseMatchExplanation(raw string) (MatchExplanation, error) {
	var e MatchExplanation
	err := json.Unmarshal([]byte(raw), &e)
	return e, err
}

// ── Engine input / output ─────────────────────────────────────────────────────

// AutoMatchParams holds everything the engine needs to generate suggestions.
type AutoMatchParams struct {
	CompanyID        uint
	AccountID        uint
	StatementDate    time.Time
	EndingBalance    decimal.Decimal
	BeginningBalance decimal.Decimal
	Candidates       []ReconcileCandidate
}

// scoredCandidate couples a candidate with its Layer-2 score for reuse
// across suggestion builders.
type scoredCandidate struct {
	cand       ReconcileCandidate
	confidence float64
	signals    []MatchSignal
}

// generatedSugg is the internal representation produced by the engine before
// DB persistence.
type generatedSugg struct {
	SuggestionType  string
	LineIDs         []uint
	ConfidenceScore float64
	RankingScore    float64
	Explanation     MatchExplanation
}

// ── Public entry point ────────────────────────────────────────────────────────

// AutoMatch expires stale pending suggestions for the account, runs the three-layer
// engine, and persists new suggestions. Returns the count of new suggestions created.
//
// It does NOT modify any journal line or reconciliation record; the user must
// still complete the reconciliation via the normal "Finish Now" flow.
//
// Previously pending suggestions are transitioned to "expired" rather than deleted
// to preserve the full audit history of what the engine produced.
func AutoMatch(db *gorm.DB, params AutoMatchParams) (int, error) {
	// 1. Expire stale pending suggestions; accepted/rejected/archived remain unchanged.
	if err := ExpirePendingSuggestions(db, params.CompanyID, params.AccountID); err != nil {
		return 0, err
	}

	if len(params.Candidates) == 0 {
		return 0, nil
	}

	// 2. Load memory entries for this account to power the historical signal.
	var memories []models.ReconciliationMemory
	_ = db.Where("company_id = ? AND account_id = ?", params.CompanyID, params.AccountID).
		Find(&memories).Error
	memMap := buildMemoryMap(memories)

	// 3. Compute the net change needed: how much the cleared balance must move.
	targetNet := params.EndingBalance.Sub(params.BeginningBalance)

	// 4. Layer 1+2: score every candidate individually.
	scoredCands := make([]scoredCandidate, len(params.Candidates))
	for i, c := range params.Candidates {
		conf, sigs := scoreCandidate(c, targetNet, params.StatementDate, memMap)
		scoredCands[i] = scoredCandidate{cand: c, confidence: conf, signals: sigs}
	}

	// Pre-build lookup maps for group suggestion builders.
	candByID := make(map[uint]ReconcileCandidate, len(params.Candidates))
	scoreByID := make(map[uint]float64, len(params.Candidates))
	for _, sc := range scoredCands {
		candByID[sc.cand.LineID] = sc.cand
		scoreByID[sc.cand.LineID] = sc.confidence
	}

	var suggestions []generatedSugg

	// 5. Individual line suggestions.
	for _, sc := range scoredCands {
		suggestions = append(suggestions, buildSingleLineSugg(sc, targetNet))
	}

	// 6. Layer 1: exact-sum combinations (pairs and triples).
	maxComboDepth := 3
	if len(params.Candidates) > 60 {
		maxComboDepth = 2 // limit depth for large candidate sets
	}
	combos := findExactSumCombos(params.Candidates, targetNet, maxComboDepth)
	for _, lineIDs := range combos {
		suggestions = append(suggestions, buildGroupSugg(lineIDs, candByID, scoreByID, targetNet, "exact_sum_group"))
	}

	// 7. Layer 2: cluster lines that share date + payee → likely one bank transaction.
	for _, lineIDs := range clusterByDateAndPayee(params.Candidates) {
		if len(lineIDs) < 2 {
			continue
		}
		sg := buildGroupSugg(lineIDs, candByID, scoreByID, targetNet, "date_payee_cluster")
		sg.SuggestionType = models.SuggTypeOneToMany
		suggestions = append(suggestions, sg)
	}

	// 8. Deduplicate, rank, cap at 15.
	suggestions = deduplicateSuggestions(suggestions)
	sort.Slice(suggestions, func(i, j int) bool {
		return suggestions[i].RankingScore > suggestions[j].RankingScore
	})
	if len(suggestions) > 15 {
		suggestions = suggestions[:15]
	}

	// 9. Persist to DB.
	count := 0
	now := time.Now()
	for _, sg := range suggestions {
		if sg.ConfidenceScore < 0.10 {
			continue // skip near-zero confidence noise
		}
		rec := models.ReconciliationMatchSuggestion{
			CompanyID:       params.CompanyID,
			AccountID:       params.AccountID,
			SuggestionType:  sg.SuggestionType,
			Status:          models.SuggStatusPending,
			ConfidenceScore: decimal.NewFromFloat(math.Round(sg.ConfidenceScore*10000) / 10000),
			RankingScore:    decimal.NewFromFloat(math.Round(sg.RankingScore*10000) / 10000),
			ExplanationJSON: sg.Explanation.ToJSON(),
			GeneratedBy:     "engine_v1",
			GeneratedAt:     now,
		}
		if err := db.Create(&rec).Error; err != nil {
			continue
		}
		// Populate line records with company isolation and amount context.
		for _, lineID := range sg.LineIDs {
			cand, hasCand := candByID[lineID]
			var amtApplied *decimal.Decimal
			if hasCand {
				// For full matches, AmountApplied = the line's total amount.
				// Split cardinality would set a partial value; not yet used by the engine.
				a := cand.Amount
				amtApplied = &a
			}
			_ = db.Create(&models.ReconciliationMatchSuggestionLine{
				SuggestionID:  rec.ID,
				CompanyID:     params.CompanyID,
				JournalLineID: lineID,
				AmountApplied: amtApplied,
				Role:          models.SuggLineRoleMatch,
			}).Error
		}
		count++
	}

	return count, nil
}

// ── Layer 1 + 2 scoring ───────────────────────────────────────────────────────

// scoreCandidate applies the deterministic + heuristic signal battery to one
// journal line and returns a [0,1] confidence and the contributing signals.
//
// The final confidence is a weighted average (see weightedSignals). Signal
// weights are defined in the signalWeights registry. The amount signals are
// mutually exclusive: exact_amount_match (0.35) fires when the candidate
// equals targetNet exactly; amount_proximity (0.20) fires otherwise.
//
//	exact_amount_match (exclusive) : 0.35
//	amount_proximity   (exclusive) : 0.20
//	date_proximity                 : 0.25
//	source_reliability             : 0.15
//	historical_match (if present)  : 0.25
func scoreCandidate(
	cand ReconcileCandidate,
	targetNet decimal.Decimal,
	stmtDate time.Time,
	memMap map[string]*models.ReconciliationMemory,
) (float64, []MatchSignal) {

	var signals []MatchSignal

	// ── Amount signal (weight 0.35) ───────────────────────────────────────────
	if !targetNet.IsZero() {
		if cand.Amount.Equal(targetNet) {
			signals = append(signals, MatchSignal{
				Name:   "exact_amount_match",
				Score:  1.0,
				Detail: "Amount exactly equals the outstanding balance to reconcile",
			})
		} else {
			// Partial amount: score falls as the ratio diverges from 1.
			ratio := amountRatio(cand.Amount, targetNet)
			if ratio > 0 {
				signals = append(signals, MatchSignal{
					Name:   "amount_proximity",
					Score:  ratio * 0.6,
					Detail: fmt.Sprintf("Amount covers %.0f%% of the outstanding balance", ratio*100),
				})
			}
		}
	}

	// ── Date proximity signal (weight 0.25) ──────────────────────────────────
	daysAgo := stmtDate.Sub(cand.EntryDate).Hours() / 24
	dateScore := dateProximityScore(daysAgo)
	signals = append(signals, MatchSignal{
		Name:   "date_proximity",
		Score:  dateScore,
		Detail: fmt.Sprintf("Posted %.0f day(s) before the statement date", math.Max(0, daysAgo)),
	})

	// ── Source reliability signal (weight 0.15) ──────────────────────────────
	srcScore, srcDetail := sourceReliabilityScore(cand.SourceType)
	signals = append(signals, MatchSignal{
		Name:   "source_reliability",
		Score:  srcScore,
		Detail: srcDetail,
	})

	// ── Historical match signal (weight 0.25) ────────────────────────────────
	normMemo := NormalizeMemo(cand.Memo)
	if mem, ok := memMap[memoryKey(normMemo, cand.SourceType)]; ok && mem.MatchedCount > 0 {
		boost, _ := mem.ConfidenceBoost.Float64()
		memScore := math.Min(1.0, 0.50+boost)
		signals = append(signals, MatchSignal{
			Name:   "historical_match",
			Score:  memScore,
			Detail: fmt.Sprintf("This memo pattern was accepted %d time(s) in previous reconciliations", mem.MatchedCount),
		})
	}

	conf := weightedSignals(signals)
	return conf, signals
}

func amountRatio(amount, target decimal.Decimal) float64 {
	if target.IsZero() {
		return 0
	}
	// Both same sign is a prerequisite for partial coverage.
	aF, _ := amount.Float64()
	tF, _ := target.Float64()
	if (aF > 0) != (tF > 0) {
		return 0 // opposite directions — not a partial match
	}
	ratio := math.Abs(aF) / math.Abs(tF)
	if ratio > 1 {
		ratio = 1 / ratio // normalise to [0,1]
	}
	return ratio
}

func dateProximityScore(daysAgo float64) float64 {
	switch {
	case daysAgo < 0:
		return 0.20 // future-dated entries are unusual but valid
	case daysAgo <= 7:
		return 1.00
	case daysAgo <= 14:
		return 0.90
	case daysAgo <= 30:
		return 0.75
	case daysAgo <= 60:
		return 0.55
	case daysAgo <= 90:
		return 0.35
	default:
		return math.Max(0.05, 0.30-float64(daysAgo-90)*0.002)
	}
}

func sourceReliabilityScore(sourceType string) (float64, string) {
	switch sourceType {
	case "payment":
		return 0.90, "Bank payment — highest reliability for bank-side matching"
	case "invoice":
		return 0.70, "Customer invoice — typically clears as a bank deposit"
	case "bill":
		return 0.70, "Supplier bill — typically clears as a bank payment"
	case "reversal":
		return 0.55, "Reversal entry — unusual on bank statements"
	case "manual":
		return 0.45, "Manual journal — verify carefully against bank statement"
	case "opening_balance":
		return 0.30, "Opening balance entry — rarely appears as a discrete bank transaction"
	default:
		return 0.45, "Journal entry — verify against bank statement"
	}
}

// weightedSignals computes a weighted average: sum(score*weight) / sum(weight).
// Because it is an average, weights are relative — they do NOT need to sum to
// any particular value. A higher weight simply pulls that signal's score closer
// to the final result. Signals whose names are not in the registry default to
// weight 0.10 so they still contribute but are treated as low-confidence.
//
// Note: group_exact_sum signals are stored in ExplanationJSON for display only;
// they are NOT passed to weightedSignals (buildGroupSugg scores groups by
// averaging member scores directly). The entry is absent intentionally.
var signalWeights = map[string]float64{
	"exact_amount_match": 0.35,
	"amount_proximity":   0.20,
	"date_proximity":     0.25,
	"source_reliability": 0.15,
	"historical_match":   0.25,
	"date_payee_cluster": 0.20, // context signal for group display
}

func weightedSignals(signals []MatchSignal) float64 {
	totalWeight := 0.0
	weightedSum := 0.0
	for _, s := range signals {
		w := signalWeights[s.Name]
		if w == 0 {
			w = 0.10
		}
		weightedSum += s.Score * w
		totalWeight += w
	}
	if totalWeight == 0 {
		return 0
	}
	return math.Min(1.0, weightedSum/totalWeight)
}

// ── Suggestion builders ───────────────────────────────────────────────────────

func buildSingleLineSugg(sc scoredCandidate, targetNet decimal.Decimal) generatedSugg {
	isExact := !targetNet.IsZero() && sc.cand.Amount.Equal(targetNet)

	summary := "Individual transaction candidate"
	if isExact {
		summary = "Single transaction with exact amount match — ready to reconcile"
	}

	tier := ConfidenceTier(sc.confidence)
	expl := MatchExplanation{
		Summary:    summary,
		Signals:    sc.signals,
		TotalLines: 1,
		NetAmount:  sc.cand.Amount.StringFixed(2),
		Tier:       tier,
	}

	ranking := sc.confidence
	if isExact {
		ranking = math.Min(1.5, ranking*1.6) // boost exact single-line matches to the top
	}

	return generatedSugg{
		SuggestionType:  models.SuggTypeOneToOne,
		LineIDs:         []uint{sc.cand.LineID},
		ConfidenceScore: sc.confidence,
		RankingScore:    ranking,
		Explanation:     expl,
	}
}

func buildGroupSugg(
	lineIDs []uint,
	candByID map[uint]ReconcileCandidate,
	scoreByID map[uint]float64,
	targetNet decimal.Decimal,
	groupReason string,
) generatedSugg {
	var netAmount decimal.Decimal
	totalConf := 0.0
	valid := 0

	for _, id := range lineIDs {
		c, ok := candByID[id]
		if !ok {
			continue
		}
		netAmount = netAmount.Add(c.Amount)
		totalConf += scoreByID[id]
		valid++
	}
	if valid == 0 {
		return generatedSugg{SuggestionType: models.SuggTypeOneToMany, LineIDs: lineIDs}
	}

	avgConf := totalConf / float64(valid)
	var extraSignals []MatchSignal

	isExact := !targetNet.IsZero() && netAmount.Equal(targetNet)
	if isExact {
		extraSignals = append(extraSignals, MatchSignal{
			Name:   "group_exact_sum",
			Score:  1.0,
			Detail: fmt.Sprintf("Group of %d lines sums exactly to the outstanding balance", valid),
		})
		avgConf = math.Min(1.0, avgConf*1.25)
	}

	// Always include a context signal explaining why these lines are grouped —
	// ensures the "View signals" button is shown and the detail panel is not empty.
	switch groupReason {
	case "date_payee_cluster":
		extraSignals = append(extraSignals, MatchSignal{
			Name:   "date_payee_cluster",
			Score:  0.70,
			Detail: fmt.Sprintf("%d transactions share the same entry date and payee — likely one bank event", valid),
		})
	case "exact_sum_group":
		// group_exact_sum signal already added above; add amount coverage context.
		if !isExact {
			extraSignals = append(extraSignals, MatchSignal{
				Name:   "amount_proximity",
				Score:  amountRatio(netAmount, targetNet) * 0.6,
				Detail: fmt.Sprintf("Combined amount covers %.0f%% of the outstanding balance", amountRatio(netAmount, targetNet)*100),
			})
		}
	}

	var summaryReason string
	switch groupReason {
	case "exact_sum_group":
		summaryReason = fmt.Sprintf("Combination of %d transactions that together match the outstanding balance", valid)
	case "date_payee_cluster":
		summaryReason = fmt.Sprintf("Cluster of %d transactions sharing the same date and payee", valid)
	default:
		summaryReason = fmt.Sprintf("Group of %d related transactions (net %s)", valid, netAmount.StringFixed(2))
	}

	tier := ConfidenceTier(avgConf)
	expl := MatchExplanation{
		Summary:    summaryReason,
		Signals:    extraSignals,
		TotalLines: valid,
		NetAmount:  netAmount.StringFixed(2),
		Tier:       tier,
	}

	suggType := models.SuggTypeOneToMany
	if valid == 1 {
		suggType = models.SuggTypeOneToOne
	}

	ranking := avgConf * math.Sqrt(float64(valid))
	if isExact {
		ranking = math.Min(1.5, ranking*1.4)
	}

	return generatedSugg{
		SuggestionType:  suggType,
		LineIDs:         lineIDs,
		ConfidenceScore: avgConf,
		RankingScore:    ranking,
		Explanation:     expl,
	}
}

// ── Layer 1: exact subset-sum search ─────────────────────────────────────────

// findExactSumCombos searches for subsets of at most maxDepth candidates
// whose amounts sum exactly to targetNet. Returns at most 5 results.
//
// Complexity: O(n²) for pairs, O(n³) for triples — acceptable for n ≤ 100.
// For larger candidate sets maxDepth is reduced by the caller.
func findExactSumCombos(cands []ReconcileCandidate, targetNet decimal.Decimal, maxDepth int) [][]uint {
	if targetNet.IsZero() || len(cands) < 2 {
		return nil
	}
	const maxResults = 5
	var results [][]uint

	// Pairs
	for i := 0; i < len(cands) && len(results) < maxResults; i++ {
		for j := i + 1; j < len(cands) && len(results) < maxResults; j++ {
			if cands[i].Amount.Add(cands[j].Amount).Equal(targetNet) {
				results = append(results, []uint{cands[i].LineID, cands[j].LineID})
			}
		}
	}

	// Triples (only if depth allows and candidate set is manageable)
	if maxDepth >= 3 && len(cands) <= 60 {
		for i := 0; i < len(cands) && len(results) < maxResults; i++ {
			for j := i + 1; j < len(cands) && len(results) < maxResults; j++ {
				for k := j + 1; k < len(cands) && len(results) < maxResults; k++ {
					sum := cands[i].Amount.Add(cands[j].Amount).Add(cands[k].Amount)
					if sum.Equal(targetNet) {
						results = append(results, []uint{cands[i].LineID, cands[j].LineID, cands[k].LineID})
					}
				}
			}
		}
	}

	return results
}

// ── Layer 2: date + payee clustering ─────────────────────────────────────────

type clusterKey struct {
	Date    string
	Payee   string
	SrcType string
}

// clusterByDateAndPayee groups candidates that share the same entry date,
// payee name, and source type. Only named payees are clustered (anonymous
// journal lines are left as individual suggestions).
func clusterByDateAndPayee(cands []ReconcileCandidate) [][]uint {
	clusters := make(map[clusterKey][]uint)
	for _, c := range cands {
		if c.PayeeName == "" {
			continue
		}
		key := clusterKey{
			Date:    c.EntryDate.Format("2006-01-02"),
			Payee:   c.PayeeName,
			SrcType: c.SourceType,
		}
		clusters[key] = append(clusters[key], c.LineID)
	}
	var result [][]uint
	for _, ids := range clusters {
		if len(ids) >= 2 {
			result = append(result, ids)
		}
	}
	return result
}

// ── Deduplication ─────────────────────────────────────────────────────────────

func deduplicateSuggestions(suggestions []generatedSugg) []generatedSugg {
	seen := make(map[string]struct{}, len(suggestions))
	result := make([]generatedSugg, 0, len(suggestions))
	for _, sg := range suggestions {
		key := suggKey(sg.LineIDs)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, sg)
	}
	return result
}

func suggKey(lineIDs []uint) string {
	ids := make([]int, len(lineIDs))
	for i, id := range lineIDs {
		ids[i] = int(id)
	}
	sort.Ints(ids)
	var sb strings.Builder
	for i, id := range ids {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(strconv.Itoa(id))
	}
	return sb.String()
}

// ── Memory helpers ────────────────────────────────────────────────────────────

// buildMemoryMap indexes memory records by their composite key for O(1) lookup.
func buildMemoryMap(memories []models.ReconciliationMemory) map[string]*models.ReconciliationMemory {
	m := make(map[string]*models.ReconciliationMemory, len(memories))
	for i := range memories {
		key := memoryKey(memories[i].NormalizedBookMemo, memories[i].SourceType)
		m[key] = &memories[i]
	}
	return m
}

func memoryKey(normalizedMemo, sourceType string) string {
	return sourceType + "|" + normalizedMemo
}

// UpdateMemoryFromAcceptedLines upserts reconciliation_memory for each journal
// line in an accepted suggestion. Called immediately after a suggestion is accepted.
//
// Rules:
//   - confidence_boost grows by 0.05 per acceptance, capped at 0.30.
//   - matched_count and last_matched_at are updated on each call.
//   - Rows with empty normalized memos are skipped (no signal value).
func UpdateMemoryFromAcceptedLines(db *gorm.DB, companyID, accountID uint, lineIDs []uint) error {
	if len(lineIDs) == 0 {
		return nil
	}

	type lineRow struct {
		Memo       string
		SourceType string
		PartyType  string
		PartyID    uint
	}
	var rows []lineRow
	if err := db.Raw(`
		SELECT jl.memo,
		       COALESCE(je.source_type, '') AS source_type,
		       COALESCE(jl.party_type, '')  AS party_type,
		       jl.party_id
		FROM journal_lines jl
		JOIN journal_entries je ON je.id = jl.journal_entry_id
		WHERE jl.id IN ? AND jl.company_id = ?
	`, lineIDs, companyID).Scan(&rows).Error; err != nil {
		return err
	}

	now := time.Now()
	for _, r := range rows {
		normMemo := NormalizeMemo(r.Memo)
		if normMemo == "" {
			continue
		}

		var vendorID *uint
		var customerID *uint
		if r.PartyType == "vendor" && r.PartyID > 0 {
			id := r.PartyID
			vendorID = &id
		} else if r.PartyType == "customer" && r.PartyID > 0 {
			id := r.PartyID
			customerID = &id
		}

		// Atomic upsert: INSERT … ON CONFLICT DO UPDATE.
		// Avoids the SELECT-then-CREATE/UPDATE race when two concurrent accepts
		// produce the same (company_id, account_id, normalized_book_memo, source_type).
		// The CASE WHEN expression computes the capped boost purely from matched_count
		// so the value remains correct regardless of prior state.
		rec := models.ReconciliationMemory{
			CompanyID:          companyID,
			AccountID:          accountID,
			NormalizedBookMemo: normMemo,
			SourceType:         r.SourceType,
			VendorID:           vendorID,
			CustomerID:         customerID,
			MatchedCount:       1,
			LastMatchedAt:      now,
			ConfidenceBoost:    decimal.NewFromFloat(0.05),
		}
		if err := db.Clauses(clause.OnConflict{
			Columns: []clause.Column{
				{Name: "company_id"},
				{Name: "account_id"},
				{Name: "normalized_book_memo"},
				{Name: "source_type"},
			},
			DoUpdates: clause.Assignments(map[string]any{
				"matched_count":   gorm.Expr("matched_count + 1"),
				"last_matched_at": now,
				// Boost = min(0.30, new_count * 0.05).
				// Uses matched_count+1 (the post-increment value) to stay in sync
				// with the count update above. Works in both PostgreSQL and SQLite.
				"confidence_boost": gorm.Expr(
					"CASE WHEN (matched_count + 1) * 0.05 > 0.30 THEN 0.30 ELSE (matched_count + 1) * 0.05 END",
				),
				"updated_at": now,
			}),
		}).Create(&rec).Error; err != nil {
			return err
		}
	}

	return nil
}

// ── Suggestion lifecycle helpers ──────────────────────────────────────────────

// ExpirePendingSuggestions transitions all pending suggestions for an account to
// "expired". Called at the start of each AutoMatch run to displace the previous
// generation without losing the history of what was previously suggested.
func ExpirePendingSuggestions(db *gorm.DB, companyID, accountID uint) error {
	return db.Model(&models.ReconciliationMatchSuggestion{}).
		Where("company_id = ? AND account_id = ? AND status = ?",
			companyID, accountID, models.SuggStatusPending).
		Update("status", models.SuggStatusExpired).Error
}

// LinkSuggestionsToReconciliation sets reconciliation_id on all accepted
// suggestions for the account that are not yet linked to any reconciliation.
// Called after "Finish Now" successfully creates a Reconciliation record,
// connecting the suggestion history to the completed reconciliation for audit.
func LinkSuggestionsToReconciliation(db *gorm.DB, companyID, accountID, reconciliationID uint) error {
	return db.Model(&models.ReconciliationMatchSuggestion{}).
		Where("company_id = ? AND account_id = ? AND status = ? AND reconciliation_id IS NULL",
			companyID, accountID, models.SuggStatusAccepted).
		Update("reconciliation_id", reconciliationID).Error
}

// ArchiveSuggestionsForReconciliation transitions accepted suggestions linked to
// a voided reconciliation from "accepted" to "archived". This preserves the
// audit trail (what was suggested, who accepted it) while clearly marking that
// the associated reconciliation no longer exists.
//
// companyID is included as a defense-in-depth guard — consistent with every
// other engine query and guards against accidental cross-company mutations.
//
// This does NOT touch accounting data — it only updates the suggestion status.
func ArchiveSuggestionsForReconciliation(db *gorm.DB, companyID, reconciliationID uint) error {
	return db.Model(&models.ReconciliationMatchSuggestion{}).
		Where("company_id = ? AND reconciliation_id = ? AND status = ?",
			companyID, reconciliationID, models.SuggStatusAccepted).
		Update("status", models.SuggStatusArchived).Error
}

// LoadPendingSuggestions returns pending suggestions for an account, ordered
// by ranking_score descending, with their lines pre-loaded.
func LoadPendingSuggestions(db *gorm.DB, companyID, accountID uint) ([]models.ReconciliationMatchSuggestion, error) {
	var suggestions []models.ReconciliationMatchSuggestion
	err := db.Preload("Lines").
		Where("company_id = ? AND account_id = ? AND status = ?",
			companyID, accountID, models.SuggStatusPending).
		Order("ranking_score DESC").
		Limit(15).
		Find(&suggestions).Error
	return suggestions, err
}

// LoadActiveSuggestions returns all pending and accepted suggestions for an
// account, ordered by status (pending first) then ranking_score descending.
// This is used by the reconcile UI so accepted suggestions remain visible
// after the user confirms them — they show a static "Accepted" badge rather
// than action buttons, giving the user full context for pre-selected lines.
func LoadActiveSuggestions(db *gorm.DB, companyID, accountID uint) ([]models.ReconciliationMatchSuggestion, error) {
	var suggestions []models.ReconciliationMatchSuggestion
	err := db.Preload("Lines").
		Where("company_id = ? AND account_id = ? AND status IN ?",
			companyID, accountID, []string{models.SuggStatusPending, models.SuggStatusAccepted}).
		Order("CASE status WHEN 'pending' THEN 0 ELSE 1 END, ranking_score DESC").
		Limit(20).
		Find(&suggestions).Error
	return suggestions, err
}

// LoadAcceptedLineIDs returns the union of journal line IDs from all accepted
// suggestions for the account. Used to pre-populate the reconcile checkboxes.
func LoadAcceptedLineIDs(db *gorm.DB, companyID, accountID uint) ([]uint, error) {
	var lineIDs []uint
	err := db.Raw(`
		SELECT DISTINCT sl.journal_line_id
		FROM reconciliation_match_suggestion_lines sl
		JOIN reconciliation_match_suggestions s ON s.id = sl.suggestion_id
		WHERE s.company_id = ?
		  AND s.account_id = ?
		  AND s.status = ?
	`, companyID, accountID, models.SuggStatusAccepted).Scan(&lineIDs).Error
	return lineIDs, err
}
