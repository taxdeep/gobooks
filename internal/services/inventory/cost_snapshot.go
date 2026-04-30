// 遵循project_guide.md
package inventory

import (
	"strings"

	"github.com/shopspring/decimal"

	"balanciz/internal/models"
)

type movementCostSnapshot struct {
	UnitCostDoc   decimal.Decimal
	TotalCostDoc  decimal.Decimal
	CurrencyCode  string
	ExchangeRate  decimal.Decimal
	UnitCostBase  decimal.Decimal
	TotalCostBase decimal.Decimal
}

func costSnapshotForQuantity(orig models.InventoryMovement, qty, unitCostBase decimal.Decimal) movementCostSnapshot {
	unitCostDoc := unitCostBase
	currencyCode := strings.ToUpper(strings.TrimSpace(orig.CurrencyCode))
	exchangeRate := decimal.NewFromInt(1)

	if currencyCode != "" && orig.UnitCost != nil && orig.UnitCost.IsPositive() {
		unitCostDoc = *orig.UnitCost
		if orig.ExchangeRate != nil && orig.ExchangeRate.IsPositive() {
			exchangeRate = *orig.ExchangeRate
		} else {
			exchangeRate = unitCostBase.Div(unitCostDoc).RoundBank(8)
		}
	} else {
		currencyCode = ""
	}

	absQty := qty.Abs()
	return movementCostSnapshot{
		UnitCostDoc:   unitCostDoc,
		TotalCostDoc:  absQty.Mul(unitCostDoc).RoundBank(2),
		CurrencyCode:  currencyCode,
		ExchangeRate:  exchangeRate,
		UnitCostBase:  unitCostBase,
		TotalCostBase: absQty.Mul(unitCostBase).RoundBank(2),
	}
}
