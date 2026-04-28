// 遵循project_guide.md
package services

import (
	"errors"
	"strings"

	"balanciz/internal/models"

	"github.com/shopspring/decimal"
)

// JournalLineDraft is raw journal line input (e.g. from HTML form).
type JournalLineDraft struct {
	AccountID string
	Debit     string
	Credit    string
	Memo      string
	Party     string
}

var decimalZero = decimal.NewFromInt(0)

// LineIsCompletelyEmpty reports whether the row can be skipped.
func LineIsCompletelyEmpty(d JournalLineDraft) bool {
	return d.AccountID == "" && d.Debit == "" && d.Credit == "" &&
		strings.TrimSpace(d.Memo) == "" && d.Party == ""
}

// ValidateJournalLines enforces PROJECT_GUIDE rules:
// - at least 2 posted lines
// - each line with an amount has an account
// - debit and credit not both positive
// - debits sum to credits
func ValidateJournalLines(drafts []JournalLineDraft) ([]models.JournalLine, error) {
	var validLines []models.JournalLine
	totalDebits := decimalZero
	totalCredits := decimalZero

	for _, pl := range drafts {
		if LineIsCompletelyEmpty(pl) {
			continue
		}

		if strings.TrimSpace(pl.AccountID) == "" {
			return nil, errors.New("Each line with an amount must have an Account.")
		}

		accountID, err := ParseUint(pl.AccountID)
		if err != nil || accountID == 0 {
			return nil, errors.New("Invalid account selected.")
		}

		debit, err := ParseDecimalMoney(pl.Debit)
		if err != nil {
			return nil, errors.New("Debit must be a non-negative number.")
		}
		credit, err := ParseDecimalMoney(pl.Credit)
		if err != nil {
			return nil, errors.New("Credit must be a non-negative number.")
		}

		if debit.GreaterThan(decimalZero) && credit.GreaterThan(decimalZero) {
			return nil, errors.New("A line cannot have both Debit and Credit.")
		}

		if debit.Equal(decimalZero) && credit.Equal(decimalZero) {
			continue
		}

		pt, pid, err := ParseParty(pl.Party)
		if err != nil {
			return nil, errors.New("Invalid Name selection.")
		}

		totalDebits = totalDebits.Add(debit)
		totalCredits = totalCredits.Add(credit)

		line := models.JournalLine{
			AccountID: uint(accountID),
			Debit:     debit,
			Credit:    credit,
			Memo:      strings.TrimSpace(pl.Memo),
			PartyType: pt,
			PartyID:   uint(pid),
		}
		if line.Debit.IsZero() {
			line.Debit = decimalZero
		}
		if line.Credit.IsZero() {
			line.Credit = decimalZero
		}
		validLines = append(validLines, line)
	}

	if len(validLines) < 2 {
		return nil, errors.New("At least 2 valid lines are required.")
	}
	if !totalDebits.Equal(totalCredits) {
		return nil, errors.New("Total debits must equal total credits.")
	}

	return validLines, nil
}
