AR Module Authority Draft
AR.1 Module Definition

AR is the official business module for customer-side revenue flow, receivables truth, receipt truth, payment application, customer credit outcomes, and AR control outputs.

AR owns:

customer receivable lifecycle
invoice-linked open-item truth
customer receipt truth
payment application / unapplication
customer credit / deposit / refund outcomes
statement / aging / collection / write-off outputs

AR does not own payment-channel truth, provider transaction truth, or payout platform truth.
AR must comply with:

Engine Truth
Backend Authority
Historical Honesty
Company Isolation
Source-linked Accounting Truth
AR.2 Official AR Scope

The AR module officially includes:

Customer
Quote
SalesOrder
CustomerDeposit
Invoice
CustomerReceipt
PaymentApplication
CreditNote
Return
Refund
CustomerStatement
ARAging
Collection
WriteOff
AR.3 Upstream / Downstream Boundary

AR may link to upstream or downstream modules, but does not absorb them.

Upstream links may include:

fulfillment / packing-slip outcomes
pricing / tax defaults
payment request initiation

Downstream links may include:

posting engine
payment gateway events
reconciliation
reporting

AR must not replace those modules or take ownership of their truth.

AR.4 AR Core Lifecycle

The recommended AR lifecycle is:

Customer -> Quote -> SalesOrder -> CustomerDeposit(optional) -> Invoice -> CustomerReceipt -> PaymentApplication -> Return / CreditNote / Refund -> Statement / Collection / WriteOff

Rules:

Quote and SalesOrder are commercial documents, not formal AR accounting truth by default.
CustomerDeposit is optional but must be independently modeled.
PaymentApplication is a formal AR capability and must not be hidden inside a generic receive-payment screen.
Return, CreditNote, and Refund must remain separate objects with separate semantics.
AR.5 Accounting Boundary

The following objects do not normally create formal accounting entries by themselves:

Quote
SalesOrder
ReturnRequest
PackingSlip / FulfillmentDocument by itself, unless another governed module adds accounting consequences

The following objects may create or drive formal accounting outcomes through the Posting Engine:

CustomerDeposit
Invoice
CustomerReceipt
CreditNote
Refund
WriteOff

AR business objects own source truth and open-item truth.
The Posting Engine remains the only official path for formal accounting entries.

AR.6 Customer Deposit Rule

CustomerDeposit must be treated as an independent AR-related object.

Rules:

deposit is not revenue by default
deposit may be unapplied, partially applied, fully applied, refunded, or voided
deposit may later be applied to invoice settlement
deposit history must remain auditable and source-linked
AR.7 Customer Receipt Rule

CustomerReceipt is the official AR-side acknowledgment that value has been received from the customer.

Rules:

receipt truth belongs to AR
receipt is not the same thing as gateway transaction status
receipt may come from multiple payment methods
receipt may be fully applied, partially applied, unapplied, reversed, or voided
receipt must retain currency, source, amount, application trail, and customer linkage
AR.8 Payment Application Rule

PaymentApplication is a first-class AR capability.

Rules:

AR must support apply and unapply
partial application must remain traceable
unapplied cash and unapplied credit must be preserved honestly
application results must update invoice balance truth and AR aging truth
payment application legality is backend-owned
AR.9 Credit Note / Return / Refund Separation

The following must remain distinct:

Return = business return fact
CreditNote = AR reduction / credit outcome
Refund = customer fund-outflow outcome

Rules:

return does not automatically equal credit note
credit note does not automatically equal refund
refund may come from overpayment, deposit return, customer credit withdrawal, or paid-invoice reversal
all three must preserve explicit linkage where related
AR.10 AR Control Outputs

AR must formally support control outputs, including:

CustomerStatement
ARAging
collection / reminder flow
write-off / bad debt handling

These are not temporary screens.
They are governed AR outputs and must remain aligned with engine truth and open-item truth.

AR.11 AR Status Discipline

AR objects must have explicit lifecycle states.
At minimum:

Quote: draft, sent, accepted, rejected, expired, cancelled
SalesOrder: draft, confirmed, partially_fulfilled, fulfilled, closed, cancelled
CustomerDeposit: unapplied, partially_applied, fully_applied, refunded, voided
Invoice: draft, issued, partially_paid, paid, overdue, voided, written_off, closed
CustomerReceipt: pending_confirmation, confirmed, partially_applied, fully_applied, unapplied, reversed, voided
CreditNote: draft, issued, partially_applied, fully_applied, voided
Return: requested, approved, rejected, received, inspected, closed, cancelled
Refund: pending, approved, processed, failed, reversed, voided
AR.12 Formal Boundary Conclusion

AR owns customer-side receivables truth.
AR does not own payment-provider truth.
AR does not own inventory truth.
AR does not own posting-engine truth.
AR may consume upstream/downstream facts, but must preserve its own explicit business and accounting boundary.