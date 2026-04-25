// 遵循project_guide.md
package models

// over_shipment.go — Over-shipment buffer policy shared by Company (default)
// and Warehouse (override).
//
// Why: stock items contractually allow shipping a small percentage above
// the SO-line qty to absorb breakage / process tolerance. Operators can
// then bump SO-line Qty above the original after partial invoicing without
// having to issue a new SO. The buffer is configurable per-company and
// optionally overridden per-warehouse.

import "github.com/shopspring/decimal"

// OverShipmentMode picks how OverShipmentValue is interpreted.
//
//   "percent" → value is a percentage of the original line qty.
//               e.g. value=5 on a qty-100 line → buffer = 5 units.
//   "qty"     → value is a fixed unit count regardless of line size.
//               e.g. value=2 on a qty-100 line → buffer = 2 units.
//
// Empty string is normalised to "percent" by the validator so legacy /
// unset rows have a deterministic default.
type OverShipmentMode string

const (
	OverShipmentModePercent OverShipmentMode = "percent"
	OverShipmentModeQty     OverShipmentMode = "qty"
)

// AllOverShipmentModes returns the legal values in display order.
// Used by the dropdown picker on Company + Warehouse settings pages.
func AllOverShipmentModes() []OverShipmentMode {
	return []OverShipmentMode{OverShipmentModePercent, OverShipmentModeQty}
}

// OverShipmentModeLabel returns a human-readable label for a mode.
func OverShipmentModeLabel(m OverShipmentMode) string {
	switch m {
	case OverShipmentModePercent:
		return "Percent of line qty"
	case OverShipmentModeQty:
		return "Fixed extra units"
	default:
		return string(m)
	}
}

// OverShipmentPolicy is the resolved (post-precedence) policy a service
// layer applies when computing max-allowed qty for a line. Returned by
// services.ResolveOverShipmentPolicy.
type OverShipmentPolicy struct {
	Enabled bool
	Mode    OverShipmentMode
	Value   decimal.Decimal
	// Source is "warehouse" when a warehouse override won, "company" when
	// the company default applied, or "" when no policy is in effect.
	Source string
}

// MaxAllowedQty returns the largest qty an SO line can be raised to,
// given the original (contracted) qty and the resolved policy. When the
// policy is disabled the original qty is returned unchanged — no buffer.
//
// Stock-item integer rounding (S1) is the caller's responsibility: this
// function returns the raw decimal so the caller can decide whether to
// floor (stock items) or keep the fractional buffer (services, which
// don't need this anyway since the buffer is a stock-item concern).
func (p OverShipmentPolicy) MaxAllowedQty(originalQty decimal.Decimal) decimal.Decimal {
	if !p.Enabled || !p.Value.IsPositive() {
		return originalQty
	}
	switch p.Mode {
	case OverShipmentModePercent:
		// originalQty * (1 + value/100)
		factor := p.Value.Div(decimal.NewFromInt(100))
		return originalQty.Add(originalQty.Mul(factor))
	case OverShipmentModeQty:
		return originalQty.Add(p.Value)
	default:
		return originalQty
	}
}
