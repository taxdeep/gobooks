// 遵循project_guide.md
package pages

import (
	"time"

	"gobooks/internal/models"
	"gobooks/internal/services"
)

// ── Reconcile page VM ─────────────────────────────────────────────────────────

type BankReconcileVM struct {
	HasCompany bool

	Accounts []models.Account

	AccountID     string
	StatementDate string
	EndingBalance string

	Active string

	FormError     string
	Saved         bool
	Voided        bool
	ProgressSaved bool // true when redirected after /save-progress POST

	// BeginningBalance = sum of already-cleared lines for this account as of
	// statement date (equals PreviouslyCleared — alias kept for Alpine init).
	BeginningBalance  string
	PreviouslyCleared string

	Candidates     []services.ReconcileCandidate
	CandidatesJSON string // JSON for Alpine: [{id, amount}]

	StatementDateTime time.Time

	// LatestReconciliation is the most recent non-voided reconciliation for
	// the selected account, used to render the Void section.
	LatestReconciliation *models.Reconciliation

	// Match-engine suggestion data
	Suggestions        []MatchSuggestionVM
	SuggestionCount    int
	AcceptedLineIDs    []uint
	AcceptedLineIDsJSON string // JSON for Alpine pre-selection: [lineID, ...]
	AutoMatchRan       bool   // true when redirected after /auto-match POST
}

// ── Suggestion VM ─────────────────────────────────────────────────────────────

// MatchSuggestionVM is the template-facing view of one engine suggestion.
type MatchSuggestionVM struct {
	ID             uint
	SuggestionType string
	TypeLabel      string
	Status         string
	// ConfidenceScore [0,1] as float for display math.
	ConfidenceScore float64
	// ConfidencePct is ConfidenceScore formatted as "75%" for display.
	ConfidencePct string
	// ConfidenceTier: "high" | "medium" | "low"
	ConfidenceTier string
	// LineIDs are the journal_line IDs proposed for reconciliation.
	LineIDs []uint
	// JournalNos are the journal entry numbers of the proposed lines.
	JournalNos []string
	// NetAmount is the sum of proposed lines, formatted as money.
	NetAmount string
	// Summary is the one-line explanation for collapsed view.
	Summary string
	// Signals are the named scoring signals for the expanded detail view.
	Signals []MatchSignalVM
}

// MatchSignalVM is one evidence item within a suggestion explanation.
type MatchSignalVM struct {
	Name   string
	Score  float64
	Detail string
	// StarsFull and StarsEmpty count stars out of 5 for visual display.
	StarsFull  int
	StarsEmpty int
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// SourceTypeLabel returns a human-readable label for a journal entry source type.
func SourceTypeLabel(t string) string {
	switch t {
	case "invoice":
		return "Invoice"
	case "bill":
		return "Bill"
	case "payment":
		return "Payment"
	case "reversal":
		return "Reversal"
	case "opening_balance":
		return "Opening Bal."
	default:
		return "Journal"
	}
}

// SuggestionTypeLabel converts a suggestion type constant to a display label.
func SuggestionTypeLabel(t string) string {
	switch t {
	case models.SuggTypeOneToOne:
		return "1:1"
	case models.SuggTypeOneToMany:
		return "1:N"
	case models.SuggTypeManyToOne:
		return "N:1"
	case models.SuggTypeSplit:
		return "Split"
	default:
		return t
	}
}

// ConfidenceTierBadgeClass returns Tailwind CSS classes for a confidence tier badge.
func ConfidenceTierBadgeClass(tier string) string {
	switch tier {
	case services.ConfTierHigh:
		return "bg-success-soft text-success-hover"
	case services.ConfTierMedium:
		return "bg-amber-100 text-amber-700"
	default:
		return "bg-background text-text-muted"
	}
}

// signalStars maps a [0,1] score to a count of filled stars out of 5.
func signalStars(score float64) (full, empty int) {
	full = int(score*5 + 0.5)
	if full > 5 {
		full = 5
	}
	empty = 5 - full
	return
}

// BuildMatchSuggestionVMs converts engine suggestions to VM structs.
// candidatesByLineID is used to enrich suggestions with journal numbers.
func BuildMatchSuggestionVMs(
	suggestions []models.ReconciliationMatchSuggestion,
	candidatesByLineID map[uint]services.ReconcileCandidate,
) []MatchSuggestionVM {
	vms := make([]MatchSuggestionVM, 0, len(suggestions))
	for _, s := range suggestions {
		confF, _ := s.ConfidenceScore.Float64()
		tier := services.ConfidenceTier(confF)

		lineIDs := make([]uint, 0, len(s.Lines))
		journalNos := make([]string, 0, len(s.Lines))
		for _, sl := range s.Lines {
			lineIDs = append(lineIDs, sl.JournalLineID)
			if c, ok := candidatesByLineID[sl.JournalLineID]; ok && c.JournalNo != "" {
				journalNos = append(journalNos, c.JournalNo)
			}
		}

		expl, _ := services.ParseMatchExplanation(s.ExplanationJSON)

		// Build signal VMs.
		sigVMs := make([]MatchSignalVM, 0, len(expl.Signals))
		for _, sig := range expl.Signals {
			full, empty := signalStars(sig.Score)
			sigVMs = append(sigVMs, MatchSignalVM{
				Name:       sig.Name,
				Score:      sig.Score,
				Detail:     sig.Detail,
				StarsFull:  full,
				StarsEmpty: empty,
			})
		}

		pct := int(confF * 100)

		vms = append(vms, MatchSuggestionVM{
			ID:              s.ID,
			SuggestionType:  s.SuggestionType,
			TypeLabel:       SuggestionTypeLabel(s.SuggestionType),
			Status:          s.Status,
			ConfidenceScore: confF,
			ConfidencePct:   Itoa(pct) + "%",
			ConfidenceTier:  tier,
			LineIDs:         lineIDs,
			JournalNos:      journalNos,
			NetAmount:       expl.NetAmount,
			Summary:         expl.Summary,
			Signals:         sigVMs,
		})
	}
	return vms
}

