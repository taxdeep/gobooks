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
)
