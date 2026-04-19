// 遵循project_guide.md
package inventory

import "errors"

// Sentinel errors for the Inventory API. Callers translate these into
// HTTP responses at the web layer. See INVENTORY_MODULE_API.md §5.
var (
	// ErrInsufficientStock — attempted IssueStock/TransferStock beyond the
	// available quantity and AllowNegative was false.
	ErrInsufficientStock = errors.New("inventory: insufficient stock")

	// ErrItemNotTracked — the item's product record has inventory tracking
	// disabled; stock events on it are a programming error.
	ErrItemNotTracked = errors.New("inventory: item does not track inventory")

	// ErrInvalidWarehouse — warehouse does not exist, belongs to another
	// company, or has been deactivated.
	ErrInvalidWarehouse = errors.New("inventory: warehouse not found or not active")

	// ErrNegativeQuantity — Quantity field must be positive. Callers signal
	// direction via the function choice (Receive vs Issue), not sign.
	ErrNegativeQuantity = errors.New("inventory: quantity must be positive")

	// ErrDuplicateIdempotency — an event with this IdempotencyKey has
	// already been recorded. The previous result is reused when possible;
	// this error surfaces when reuse is not safe (e.g. different inputs).
	ErrDuplicateIdempotency = errors.New("inventory: idempotency key already used with different inputs")

	// ErrBOMCycle — a recursive BOM explosion detected a back-edge, meaning
	// the graph contains a cycle (A -> B -> A). Blocks the operation.
	ErrBOMCycle = errors.New("inventory: BOM contains a cycle")

	// ErrBOMTooDeep — recursion exceeded the configured maximum depth
	// (default 5). Deep BOMs usually indicate data error.
	ErrBOMTooDeep = errors.New("inventory: BOM depth exceeds maximum")

	// ErrCostingLayerExhausted — FIFO mode requested an issue that cannot
	// be satisfied by the available cost layers (only reachable in Phase E
	// when FIFO is implemented; kept here for API stability).
	ErrCostingLayerExhausted = errors.New("inventory: FIFO layers cannot satisfy issue")

	// ErrCurrencyRateRequired — a foreign-currency movement was submitted
	// without an ExchangeRate. Base-currency movements should pass 1.
	ErrCurrencyRateRequired = errors.New("inventory: exchange rate required for foreign-currency movement")

	// ErrMovementImmutable — attempted to UPDATE or DELETE an existing
	// movement. History is append-only; callers must ReverseMovement.
	ErrMovementImmutable = errors.New("inventory: movement cannot be modified; issue a reversal")

	// ErrReversalAlreadyApplied — ReverseMovement called on a movement
	// that already has ReversedByMovementID set.
	ErrReversalAlreadyApplied = errors.New("inventory: this movement has already been reversed")

	// ErrInvalidItem — item does not exist or belongs to another company.
	ErrInvalidItem = errors.New("inventory: item not found")

	// ErrInvalidSource — SourceType is empty or unrecognized.
	ErrInvalidSource = errors.New("inventory: source_type is required and must be a known value")

	// ErrInsufficientAvailable — ReserveStock attempted to reserve more than
	// the currently available quantity (on-hand − already-reserved).
	ErrInsufficientAvailable = errors.New("inventory: insufficient available stock to reserve")

	// ErrReservationUnderflow — ReleaseStock attempted to release more than
	// the currently-reserved quantity for the target balance.
	ErrReservationUnderflow = errors.New("inventory: cannot release more than is currently reserved")

	// ── Phase F tracking (lot / serial / expiry) ───────────────────────

	// ErrTrackingDataMissing — a tracked item's IN/OUT event did not
	// carry the tracking data it requires (lot number for lot-tracked,
	// serial list for serial-tracked).
	ErrTrackingDataMissing = errors.New("inventory: tracking data missing for tracked item")

	// ErrTrackingDataOnUntrackedItem — an untracked item's event
	// carried lot/serial data that would be silently ignored. Reject
	// loudly so the caller notices the misconfiguration.
	ErrTrackingDataOnUntrackedItem = errors.New("inventory: lot/serial data supplied for untracked item")

	// ErrSerialCountMismatch — serial_numbers length does not equal
	// the event's quantity. Serial tracking is one-per-unit by
	// definition, so a mismatch is always wrong.
	ErrSerialCountMismatch = errors.New("inventory: serial count must equal quantity")

	// ErrDuplicateSerialInbound — attempted to receive a serial that
	// is already in a live (on_hand | reserved) state for the same
	// (company, item). Enforced by the partial unique index as well;
	// this sentinel is the friendly form.
	ErrDuplicateSerialInbound = errors.New("inventory: serial is already on hand or reserved")

	// ErrTrackingModeMismatch — caller supplied lot data for a
	// serial-tracked item or vice versa. The capture shape must
	// match the item's configured tracking mode.
	ErrTrackingModeMismatch = errors.New("inventory: tracking data does not match item tracking_mode")

	// ── Phase F3 outbound tracking errors ──────────────────────────────

	// ErrLotSelectionMissing — a lot-tracked outbound event was
	// submitted without lot selections or with selections whose sum
	// doesn't match the quantity.
	ErrLotSelectionMissing = errors.New("inventory: lot selections required for lot-tracked outbound")

	// ErrLotSelectionExceedsRemaining — a lot selection asked for more
	// units than the lot currently has available.
	ErrLotSelectionExceedsRemaining = errors.New("inventory: lot selection exceeds remaining quantity")

	// ErrLotNotFound — a lot selection referenced a lot ID that doesn't
	// exist or doesn't belong to the expected (company, item).
	ErrLotNotFound = errors.New("inventory: selected lot not found or does not belong to this item")

	// ErrSerialSelectionMissing — a serial-tracked outbound event was
	// submitted with the wrong count of serial selections.
	ErrSerialSelectionMissing = errors.New("inventory: serial selections required for serial-tracked outbound")

	// ErrSerialNotOnHand — a serial selection referenced a serial that
	// is not currently in on_hand state (already issued, reserved-but-
	// not-released, or void-archived).
	ErrSerialNotOnHand = errors.New("inventory: selected serial is not on hand")

	// ErrSerialNotFound — a serial selection referenced a serial
	// number that doesn't exist for the expected (company, item).
	ErrSerialNotFound = errors.New("inventory: selected serial not found for this item")

	// ErrTrackingAnchorMissing — ReverseMovement was called on a
	// tracked outbound movement but no inventory_tracking_consumption
	// rows exist to restore from. Reject rather than silently skipping
	// tracking restoration — same principle as the E2.1 consumption
	// log for FIFO cost layers.
	ErrTrackingAnchorMissing = errors.New("inventory: cannot reverse tracked outbound without consumption anchor")
)
