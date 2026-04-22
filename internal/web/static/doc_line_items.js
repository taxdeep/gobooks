// doc_line_items.js — shared Alpine factory for transaction-document line-item tables.
// v=2 — adds insertLineBelow(idx) for the per-row "+" button UI.
// Used by Invoice, Quote, SO, Bill, PO, Expense editors via ui.DocLineItems.
//
// The factory returns a partial Alpine component that provides:
//   • lines[] state
//   • addLine() / removeLine(idx) / clearAllLines() methods
//   • auto-grow hook: _autoGrowIfComplete(idx) appends a blank row when the
//     last row satisfies the caller-supplied isLineComplete() predicate.
//
// The caller merges this partial into their own Alpine component via the
// spread operator and layers editor-specific state + methods on top:
//
//   function invoiceEditor() {
//     return Object.assign(gobooksLineItems({
//       defaults: { product_service_id: "", description: "", qty: "1", ... },
//       isLineComplete: (l) => l.product_service_id && l.qty && l.unit_price,
//     }), {
//       // editor-specific state
//       products: [], taxCodes: [],
//       // override hooks
//       onLinesChange() { this._recalcAll(); },
//       init() { /* load data, set initial lines */ },
//     });
//   }
//
// Callers that need to react to line changes should override onLinesChange()
// (no-op by default). It is called after addLine / removeLine / clearAllLines.
//
// Callers that need custom post-remove behaviour (e.g. clearing related state
// when the last row is removed) should override _onRemove(idx, removed).
function gobooksLineItems(config) {
  config = config || {};
  return {
    lines: [],

    // ── Configuration (private; read during method calls) ────────────────
    _lineDefaults:   config.defaults       || {},
    _isLineComplete: config.isLineComplete || (() => false),

    // ── Overridable hooks (no-op defaults) ───────────────────────────────
    // Called after lines array mutations. Override to trigger recalculation.
    onLinesChange() { /* no-op */ },

    // ── Core line-management methods ─────────────────────────────────────

    addLine() {
      this.lines.push(this._blankLine());
      this.onLinesChange();
    },

    // insertLineBelow splices a new blank line immediately after idx. Used by
    // the per-row "+" button in ui.DocLineItems — adds a row in-context rather
    // than at the end, matching the QBO-style editors the refactor targets.
    insertLineBelow(idx) {
      const pos = Math.max(0, Math.min(this.lines.length, idx + 1));
      this.lines.splice(pos, 0, this._blankLine());
      this.onLinesChange();
    },

    removeLine(idx) {
      if (this.lines.length <= 1) return;
      this.lines.splice(idx, 1);
      this.onLinesChange();
    },

    clearAllLines() {
      this.lines = [this._blankLine()];
      this.onLinesChange();
    },

    // Append a new blank line when the last line is "complete" per the
    // caller-supplied predicate. Callers invoke this from blur / change
    // handlers on the last cell of each row (so a partially-typed row
    // doesn't spawn new rows on every keystroke).
    _autoGrowIfComplete(idx) {
      if (idx !== this.lines.length - 1) return;
      if (this._isLineComplete(this.lines[idx])) {
        this.addLine();
      }
    },

    // ── Internal helpers ─────────────────────────────────────────────────

    _blankLine() {
      return Object.assign({}, this._lineDefaults);
    },
  };
}
