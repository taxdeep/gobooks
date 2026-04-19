// 遵循project_guide.md
package inventory

// tracking_queries.go — Phase F4 inquiry / traceability / expiry
// visibility.
//
// Scope: read-only views over Phase F tracking truth. Nothing here
// mutates state; these are the answers operators need to work with
// lot / serial / expiry data — "what lots of item X do I have?",
// "which serials did invoice Y consume?", "what expires within 30 days?".
//
// Company isolation
// -----------------
// Every query takes a CompanyID and scopes to it unconditionally. A
// caller that accidentally passes CompanyID=0 will get either an
// error or an empty result, never a cross-company leak.

import (
	"fmt"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

// ── Lot inventory inquiry ────────────────────────────────────────────────────

// LotInfo is one row returned by GetLotsForItem. Ordered FIFO (oldest
// received_date first, ties broken by id).
type LotInfo struct {
	LotID             uint
	LotNumber         string
	ExpiryDate        *time.Time
	ReceivedDate      time.Time
	OriginalQuantity  decimal.Decimal
	RemainingQuantity decimal.Decimal
}

// GetLotsForItem returns all lots for (company, item). includeZero=false
// omits lots whose RemainingQuantity has drained to zero. Useful for the
// lot-selection UI on a tracked outbound.
func GetLotsForItem(db *gorm.DB, companyID, itemID uint, includeZero bool) ([]LotInfo, error) {
	if companyID == 0 || itemID == 0 {
		return nil, fmt.Errorf("inventory.GetLotsForItem: CompanyID and ItemID required")
	}
	q := db.Model(&models.InventoryLot{}).
		Where("company_id = ? AND item_id = ?", companyID, itemID)
	if !includeZero {
		q = q.Where("remaining_quantity > 0")
	}
	var rows []models.InventoryLot
	if err := q.Order("received_date ASC, id ASC").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("inventory.GetLotsForItem: %w", err)
	}
	out := make([]LotInfo, 0, len(rows))
	for _, r := range rows {
		out = append(out, LotInfo{
			LotID:             r.ID,
			LotNumber:         r.LotNumber,
			ExpiryDate:        r.ExpiryDate,
			ReceivedDate:      r.ReceivedDate,
			OriginalQuantity:  r.OriginalQuantity,
			RemainingQuantity: r.RemainingQuantity,
		})
	}
	return out, nil
}

// ── Serial inventory inquiry ─────────────────────────────────────────────────

// SerialInfo is one row returned by GetSerialsForItem.
type SerialInfo struct {
	SerialUnitID uint
	SerialNumber string
	CurrentState models.SerialState
	ExpiryDate   *time.Time
	ReceivedDate time.Time
}

// GetSerialsForItem returns serial units for (company, item), filtered
// by the given state set. Passing an empty stateFilter returns all
// states. Ordered by serial_number for stable UI rendering.
func GetSerialsForItem(db *gorm.DB, companyID, itemID uint, stateFilter []models.SerialState) ([]SerialInfo, error) {
	if companyID == 0 || itemID == 0 {
		return nil, fmt.Errorf("inventory.GetSerialsForItem: CompanyID and ItemID required")
	}
	q := db.Model(&models.InventorySerialUnit{}).
		Where("company_id = ? AND item_id = ?", companyID, itemID)
	if len(stateFilter) > 0 {
		q = q.Where("current_state IN ?", stateFilter)
	}
	var rows []models.InventorySerialUnit
	if err := q.Order("serial_number ASC, id ASC").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("inventory.GetSerialsForItem: %w", err)
	}
	out := make([]SerialInfo, 0, len(rows))
	for _, r := range rows {
		out = append(out, SerialInfo{
			SerialUnitID: r.ID,
			SerialNumber: r.SerialNumber,
			CurrentState: r.CurrentState,
			ExpiryDate:   r.ExpiryDate,
			ReceivedDate: r.ReceivedDate,
		})
	}
	return out, nil
}

// ── Traceability ─────────────────────────────────────────────────────────────

// TraceEntry documents one lot or serial consumption anchor linked to
// a movement. Exactly one of LotID / SerialUnitID is non-nil. Reversed
// entries remain in the result set with ReversedByMovementID populated
// so operators can see the full history rather than just "currently
// live" anchors.
type TraceEntry struct {
	ConsumptionID        uint
	IssueMovementID      uint
	ItemID               uint
	LotID                *uint
	LotNumber            string
	SerialUnitID         *uint
	SerialNumber         string
	QuantityDrawn        decimal.Decimal
	ReversedByMovementID *uint
	CreatedAt            time.Time
}

// GetTracesForMovement returns every consumption anchor tied to the
// given outbound movement. Useful for "what did invoice X consume?"
// views. Company-scoped.
func GetTracesForMovement(db *gorm.DB, companyID, movementID uint) ([]TraceEntry, error) {
	if companyID == 0 || movementID == 0 {
		return nil, fmt.Errorf("inventory.GetTracesForMovement: CompanyID and movementID required")
	}
	return loadTracesWhere(db, "c.company_id = ? AND c.issue_movement_id = ?", []any{companyID, movementID})
}

// GetTracesForItem returns every consumption anchor for the item
// within [fromDate, toDate], joined against the movement for its date.
// Both bounds inclusive. Used for "trace movement history for this lot"
// or "audit what ever happened to serial X" flows.
func GetTracesForItem(db *gorm.DB, companyID, itemID uint, fromDate, toDate time.Time) ([]TraceEntry, error) {
	if companyID == 0 || itemID == 0 {
		return nil, fmt.Errorf("inventory.GetTracesForItem: CompanyID and ItemID required")
	}
	return loadTracesWhere(db,
		"c.company_id = ? AND c.item_id = ? AND m.movement_date >= ? AND m.movement_date <= ?",
		[]any{companyID, itemID, fromDate, toDate})
}

// loadTracesWhere is the shared backend for the trace queries. Joins
// consumption rows with lot / serial / movement tables so the returned
// TraceEntry carries human-readable identifiers (LotNumber,
// SerialNumber) without the caller having to do a second round-trip.
func loadTracesWhere(db *gorm.DB, whereClause string, args []any) ([]TraceEntry, error) {
	type scanRow struct {
		ConsumptionID        uint
		IssueMovementID      uint
		ItemID               uint
		LotID                *uint
		LotNumber            *string
		SerialUnitID         *uint
		SerialNumber         *string
		QuantityDrawn        decimal.Decimal
		ReversedByMovementID *uint
		CreatedAt            time.Time
	}
	var rows []scanRow
	query := db.Table("inventory_tracking_consumption AS c").
		Select(`c.id AS consumption_id,
			c.issue_movement_id,
			c.item_id,
			c.lot_id,
			l.lot_number AS lot_number,
			c.serial_unit_id,
			s.serial_number AS serial_number,
			c.quantity_drawn,
			c.reversed_by_movement_id,
			c.created_at`).
		Joins("LEFT JOIN inventory_lots AS l ON l.id = c.lot_id").
		Joins("LEFT JOIN inventory_serial_units AS s ON s.id = c.serial_unit_id").
		Joins("JOIN inventory_movements AS m ON m.id = c.issue_movement_id").
		Where(whereClause, args...).
		Order("c.created_at ASC, c.id ASC")

	if err := query.Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("inventory.trace: %w", err)
	}
	out := make([]TraceEntry, 0, len(rows))
	for _, r := range rows {
		e := TraceEntry{
			ConsumptionID:        r.ConsumptionID,
			IssueMovementID:      r.IssueMovementID,
			ItemID:               r.ItemID,
			LotID:                r.LotID,
			SerialUnitID:         r.SerialUnitID,
			QuantityDrawn:        r.QuantityDrawn,
			ReversedByMovementID: r.ReversedByMovementID,
			CreatedAt:            r.CreatedAt,
		}
		if r.LotNumber != nil {
			e.LotNumber = *r.LotNumber
		}
		if r.SerialNumber != nil {
			e.SerialNumber = *r.SerialNumber
		}
		out = append(out, e)
	}
	return out, nil
}

// ── Expiry visibility ────────────────────────────────────────────────────────

// ExpiringLotRow is one lot at risk of expiry.
type ExpiringLotRow struct {
	LotID             uint
	ItemID            uint
	LotNumber         string
	ExpiryDate        time.Time
	DaysUntilExpiry   int
	RemainingQuantity decimal.Decimal
}

// GetExpiringLots returns lots with remaining > 0 whose expiry_date
// falls within [asOf, asOf+withinDays]. Passing withinDays=0 returns
// only lots that have already expired or expire today. Negative
// DaysUntilExpiry means the lot has already expired.
//
// Ordered by expiry_date ASC so the most urgent rows surface first.
// Phase F4: visibility only — this does NOT block outbound on
// already-expired stock. Expiry policy is an F5 decisions item.
func GetExpiringLots(db *gorm.DB, companyID uint, asOf time.Time, withinDays int) ([]ExpiringLotRow, error) {
	if companyID == 0 {
		return nil, fmt.Errorf("inventory.GetExpiringLots: CompanyID required")
	}
	cutoff := asOf.AddDate(0, 0, withinDays)
	var lots []models.InventoryLot
	if err := db.Where("company_id = ? AND remaining_quantity > 0 AND expiry_date IS NOT NULL AND expiry_date <= ?",
		companyID, cutoff).
		Order("expiry_date ASC, id ASC").
		Find(&lots).Error; err != nil {
		return nil, fmt.Errorf("inventory.GetExpiringLots: %w", err)
	}
	out := make([]ExpiringLotRow, 0, len(lots))
	for _, l := range lots {
		if l.ExpiryDate == nil {
			continue // defensive; filter above guarantees this
		}
		days := int(l.ExpiryDate.Sub(asOf).Hours() / 24)
		out = append(out, ExpiringLotRow{
			LotID:             l.ID,
			ItemID:            l.ItemID,
			LotNumber:         l.LotNumber,
			ExpiryDate:        *l.ExpiryDate,
			DaysUntilExpiry:   days,
			RemainingQuantity: l.RemainingQuantity,
		})
	}
	return out, nil
}

// ExpiringSerialRow is one serial unit at risk of expiry.
type ExpiringSerialRow struct {
	SerialUnitID    uint
	ItemID          uint
	SerialNumber    string
	CurrentState    models.SerialState
	ExpiryDate      time.Time
	DaysUntilExpiry int
}

// GetExpiringSerials returns serial units in live states (on_hand |
// reserved) whose expiry_date falls within [asOf, asOf+withinDays].
// Same visibility-only semantics as GetExpiringLots.
func GetExpiringSerials(db *gorm.DB, companyID uint, asOf time.Time, withinDays int) ([]ExpiringSerialRow, error) {
	if companyID == 0 {
		return nil, fmt.Errorf("inventory.GetExpiringSerials: CompanyID required")
	}
	cutoff := asOf.AddDate(0, 0, withinDays)
	var units []models.InventorySerialUnit
	if err := db.Where("company_id = ? AND current_state IN ? AND expiry_date IS NOT NULL AND expiry_date <= ?",
		companyID,
		[]models.SerialState{models.SerialStateOnHand, models.SerialStateReserved},
		cutoff).
		Order("expiry_date ASC, id ASC").
		Find(&units).Error; err != nil {
		return nil, fmt.Errorf("inventory.GetExpiringSerials: %w", err)
	}
	out := make([]ExpiringSerialRow, 0, len(units))
	for _, u := range units {
		if u.ExpiryDate == nil {
			continue
		}
		days := int(u.ExpiryDate.Sub(asOf).Hours() / 24)
		out = append(out, ExpiringSerialRow{
			SerialUnitID:    u.ID,
			ItemID:          u.ItemID,
			SerialNumber:    u.SerialNumber,
			CurrentState:    u.CurrentState,
			ExpiryDate:      *u.ExpiryDate,
			DaysUntilExpiry: days,
		})
	}
	return out, nil
}
