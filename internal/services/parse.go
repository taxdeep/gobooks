// 遵循project_guide.md
package services

import (
	"errors"
	"strings"

	"balanciz/internal/models"

	"github.com/shopspring/decimal"
)

// ParseUint parses a non-negative decimal integer string.
func ParseUint(s string) (uint64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty")
	}
	var n uint64
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch < '0' || ch > '9' {
			return 0, errors.New("not a number")
		}
		n = n*10 + uint64(ch-'0')
	}
	return n, nil
}

// ParseDecimalMoney parses a non-negative money amount; empty string is zero.
// Commas are stripped (e.g. "1,000.00").
func ParseDecimalMoney(s string) (decimal.Decimal, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return decimal.NewFromInt(0), nil
	}
	s = strings.ReplaceAll(s, ",", "")
	d, err := decimal.NewFromString(s)
	if err != nil {
		return decimal.Decimal{}, err
	}
	if d.IsNegative() {
		return decimal.Decimal{}, errors.New("negative")
	}
	return d, nil
}

// ParseParty parses "customer:123" / "vendor:456" or empty for none.
func ParseParty(s string) (models.PartyType, uint64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return models.PartyTypeNone, 0, nil
	}
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return "", 0, errors.New("bad party")
	}
	id, err := ParseUint(parts[1])
	if err != nil || id == 0 {
		return "", 0, errors.New("bad party id")
	}
	switch parts[0] {
	case "customer":
		return models.PartyTypeCustomer, id, nil
	case "vendor":
		return models.PartyTypeVendor, id, nil
	default:
		return "", 0, errors.New("bad party type")
	}
}
