// 遵循project_guide.md
package web

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

// SmartPickerItem is the canonical response shape for a single entity in the picker.
// Primary: the main display label (e.g. account name).
// Secondary: supplementary info shown below/beside primary (e.g. account code).
// Meta: key-value pairs rendered visually in the dropdown (e.g. type label, tags).
// Payload: machine-readable data passed to JS select events but never rendered in UI.
//
//	Use this for auto-fill values (e.g. default_price) that downstream Alpine
//	components can act on without polluting the visual display.
type SmartPickerItem struct {
	ID           string            `json:"id"`
	Primary      string            `json:"primary"`
	Secondary    string            `json:"secondary"`
	Meta         map[string]string `json:"meta,omitempty"`
	Payload      map[string]string `json:"payload,omitempty"`
	Score        float64           `json:"score,omitempty"`
	Reason       string            `json:"reason,omitempty"`
	RankPosition int               `json:"rank_position,omitempty"`
}

// SmartPickerResult is the top-level JSON response from the search endpoint.
type SmartPickerResult struct {
	// Candidates is the ranked list of matching items.
	// JS reads data.candidates (not data.items — renamed in Batch 30).
	Candidates []SmartPickerItem `json:"candidates"`
	// Source identifies whether the returned ordering came directly from the
	// provider, from the short-TTL cache, or from persisted usage ranking.
	Source string `json:"source,omitempty"`
	// RequiresBackendValidation makes the authority boundary explicit:
	// picker results accelerate UI search only; form submit still revalidates.
	RequiresBackendValidation bool `json:"requires_backend_validation"`
	// RequestID is a per-request UUID echoed back to the frontend so the JS
	// can discard stale out-of-order responses (last-write-wins by sequence).
	RequestID string `json:"request_id,omitempty"`
	TraceID   string `json:"trace_id,omitempty"`
}

// SmartPickerContext carries per-request scope that providers receive.
// CompanyID is always sourced from the authenticated session — never from query params.
type SmartPickerContext struct {
	CompanyID        uint
	Context          string // discriminates purpose within an entity type (e.g. "expense_form_category")
	Limit            int
	UserID           *uuid.UUID
	EntityType       string
	Query            string
	AnchorContext    string
	AnchorEntityType string
	AnchorEntityID   *uint
	TraceEnabled     bool
	TraceSampleRate  float64
}

// SmartPickerProvider is the interface each entity domain must implement.
type SmartPickerProvider interface {
	// EntityType returns the stable string key used in API requests (e.g. "account").
	EntityType() string

	// Search returns matching items for the given query string.
	// An empty query may return a default set (e.g. top N most-used).
	Search(db *gorm.DB, ctx SmartPickerContext, query string) (*SmartPickerResult, error)

	// GetByID rehydrates a single item by its string ID for edit-page pre-population.
	// Returns nil, nil when the ID is not found or not accessible to companyID.
	GetByID(db *gorm.DB, ctx SmartPickerContext, id string) (*SmartPickerItem, error)
}

// ── Registry ─────────────────────────────────────────────────────────────────

// SmartPickerRegistry maps entity type strings to their provider implementations.
type SmartPickerRegistry struct {
	providers map[string]SmartPickerProvider
}

func newSmartPickerRegistry(providers ...SmartPickerProvider) *SmartPickerRegistry {
	r := &SmartPickerRegistry{providers: make(map[string]SmartPickerProvider, len(providers))}
	for _, p := range providers {
		r.providers[p.EntityType()] = p
	}
	return r
}

func (r *SmartPickerRegistry) get(entity string) (SmartPickerProvider, bool) {
	p, ok := r.providers[entity]
	return p, ok
}

// defaultRegistry is the application-wide registry, initialized at startup.
var defaultSmartPickerRegistry = newSmartPickerRegistry(
	&ExpenseAccountProvider{},
	&CompanyProvider{},
	&CustomerProvider{},
	&VendorProvider{},
	&ProductServiceProvider{},
	&PaymentAccountProvider{},
)

func smartPickerLimit(ctx SmartPickerContext) int {
	limit := ctx.Limit
	if limit <= 0 || limit > 50 {
		return 20
	}
	return limit
}

func applySmartPickerTextSearch(db *gorm.DB, dialect string, query string, fields ...string) *gorm.DB {
	query = strings.TrimSpace(query)
	if query == "" || len(fields) == 0 {
		return db
	}
	operator := "LIKE"
	if dialect == "postgres" {
		operator = "ILIKE"
	}
	clauses := make([]string, 0, len(fields))
	args := make([]any, 0, len(fields))
	for _, field := range fields {
		clauses = append(clauses, field+" "+operator+" ?")
		args = append(args, "%"+query+"%")
	}
	return db.Where("("+strings.Join(clauses, " OR ")+")", args...)
}

// ── ExpenseAccountProvider ───────────────────────────────────────────────────

// CompanyProvider handles entity="company" for user-owned company switching.
// It is deliberately user-scoped: candidates are restricted to active
// memberships for the authenticated user before name matching.
type CompanyProvider struct{}

func (p *CompanyProvider) EntityType() string { return "company" }

func (p *CompanyProvider) scopedQuery(db *gorm.DB, ctx SmartPickerContext) *gorm.DB {
	if ctx.UserID == nil {
		return db.Where("1 = 0")
	}
	return db.Model(&models.Company{}).
		Joins("JOIN company_memberships ON company_memberships.company_id = companies.id").
		Where("company_memberships.user_id = ? AND company_memberships.is_active = true AND companies.is_active = true", *ctx.UserID)
}

func (p *CompanyProvider) Search(db *gorm.DB, ctx SmartPickerContext, query string) (*SmartPickerResult, error) {
	var companies []models.Company
	limit := smartPickerLimit(ctx)
	fetchLimit := limit
	if strings.TrimSpace(query) != "" {
		fetchLimit = limit * 4
		if fetchLimit < 50 {
			fetchLimit = 50
		}
		if fetchLimit > 100 {
			fetchLimit = 100
		}
	}
	q := p.scopedQuery(db, ctx).
		Order("companies.name ASC").
		Limit(fetchLimit)
	q = applySmartPickerTextSearch(q, db.Dialector.Name(), query, "companies.name")
	if err := q.Find(&companies).Error; err != nil {
		return nil, fmt.Errorf("company search: %w", err)
	}
	if strings.TrimSpace(query) != "" {
		sort.Slice(companies, func(i, j int) bool {
			leftRank := companyNameMatchRank(companies[i].Name, query)
			rightRank := companyNameMatchRank(companies[j].Name, query)
			if leftRank != rightRank {
				return leftRank < rightRank
			}
			return companies[i].Name < companies[j].Name
		})
		if len(companies) > limit {
			companies = companies[:limit]
		}
	}

	items := make([]SmartPickerItem, 0, len(companies))
	for _, co := range companies {
		items = append(items, SmartPickerItem{
			ID:        fmt.Sprintf("%d", co.ID),
			Primary:   co.Name,
			Secondary: "Company",
		})
	}
	return &SmartPickerResult{Candidates: items}, nil
}

func (p *CompanyProvider) GetByID(db *gorm.DB, ctx SmartPickerContext, id string) (*SmartPickerItem, error) {
	var company models.Company
	err := p.scopedQuery(db, ctx).
		Where("companies.id = ?", id).
		First(&company).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("company get by id: %w", err)
	}
	return &SmartPickerItem{
		ID:        fmt.Sprintf("%d", company.ID),
		Primary:   company.Name,
		Secondary: "Company",
	}, nil
}

// ExpenseAccountProvider handles entity="account". It used to be a single-
// context provider (expense_form_category, returning only RootExpense
// accounts), but contexts now select between scope policies:
//
//   - "" / "expense_form_category" → expense-root only (legacy default; the
//     empty-context fallback preserves backward compat with callers that
//     forgot to set Context)
//   - "journal_entry_account" → ALL active accounts in the company, ordered
//     by code. Used by the JE list page's "filter by account" picker so
//     accountants can answer "which JEs touched account X?"
//
// The type name still says "Expense" for source-history compat — adding a
// new Provider type would require touching the registry (one entity →
// one provider) and every downstream test. Renaming on its own is a
// trivial follow-up if the broader account-picker use case grows.
type ExpenseAccountProvider struct{}

func (p *ExpenseAccountProvider) EntityType() string { return "account" }

// scopedQuery returns a base GORM query narrowed to the right account
// subset for the calling context. Keeping the scope policy in one place
// means Search + GetByID can never disagree about what "an account in
// this context" means.
func (p *ExpenseAccountProvider) scopedQuery(db *gorm.DB, ctx SmartPickerContext) *gorm.DB {
	switch ctx.Context {
	case "journal_entry_account":
		return db.Where("company_id = ? AND is_active = true", ctx.CompanyID)
	default: // "" or "expense_form_category" → legacy expense-only behaviour
		return db.Where("company_id = ? AND root_account_type = ? AND is_active = true",
			ctx.CompanyID, models.RootExpense)
	}
}

func (p *ExpenseAccountProvider) Search(db *gorm.DB, ctx SmartPickerContext, query string) (*SmartPickerResult, error) {
	var accounts []models.Account
	q := p.scopedQuery(db, ctx).
		Order("code ASC").
		Limit(smartPickerLimit(ctx))
	q = applySmartPickerTextSearch(q, db.Dialector.Name(), query, "name", "code")

	if err := q.Find(&accounts).Error; err != nil {
		return nil, fmt.Errorf("account search: %w", err)
	}

	items := make([]SmartPickerItem, 0, len(accounts))
	for _, a := range accounts {
		items = append(items, SmartPickerItem{
			ID:        fmt.Sprintf("%d", a.ID),
			Primary:   a.Name,
			Secondary: a.Code,
		})
	}

	return &SmartPickerResult{
		Candidates: items,
	}, nil
}

func (p *ExpenseAccountProvider) GetByID(db *gorm.DB, ctx SmartPickerContext, id string) (*SmartPickerItem, error) {
	var account models.Account
	err := p.scopedQuery(db, ctx).
		Where("id = ?", id).
		First(&account).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("account get by id: %w", err)
	}
	return &SmartPickerItem{
		ID:        fmt.Sprintf("%d", account.ID),
		Primary:   account.Name,
		Secondary: account.Code,
	}, nil
}

// ── CustomerProvider ─────────────────────────────────────────────────────────

// CustomerProvider handles entity="customer". Customers are company-scoped; the
// current Customer model has no IsActive field, so this provider does not invent
// an active filter.
type CustomerProvider struct{}

func (p *CustomerProvider) EntityType() string { return "customer" }

func (p *CustomerProvider) Search(db *gorm.DB, ctx SmartPickerContext, query string) (*SmartPickerResult, error) {
	var customers []models.Customer
	q := db.
		Where("company_id = ?", ctx.CompanyID).
		Order("name ASC").
		Limit(smartPickerLimit(ctx))
	q = applySmartPickerTextSearch(q, db.Dialector.Name(), query,
		"name", "email", "addr_city", "addr_province", "addr_postal_code", "addr_country")

	if err := q.Find(&customers).Error; err != nil {
		return nil, fmt.Errorf("customer search: %w", err)
	}

	items := make([]SmartPickerItem, 0, len(customers))
	for _, c := range customers {
		// Secondary: currency code takes priority (most relevant in invoice/JE contexts);
		// fall back to email so non-multi-currency setups still show useful info.
		secondary := c.CurrencyCode
		if secondary == "" {
			secondary = c.Email
		}
		item := SmartPickerItem{
			ID:        fmt.Sprintf("%d", c.ID),
			Primary:   c.Name,
			Secondary: secondary,
			Payload:   customerPickerPayload(db, c),
		}
		items = append(items, item)
	}
	return &SmartPickerResult{Candidates: items}, nil
}

func (p *CustomerProvider) GetByID(db *gorm.DB, ctx SmartPickerContext, id string) (*SmartPickerItem, error) {
	var customer models.Customer
	err := db.
		Where("id = ? AND company_id = ?", id, ctx.CompanyID).
		First(&customer).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("customer get by id: %w", err)
	}
	return &SmartPickerItem{
		ID:        fmt.Sprintf("%d", customer.ID),
		Primary:   customer.Name,
		Secondary: customer.Email,
		Payload:   customerPickerPayload(db, customer),
	}, nil
}

// customerPickerPayload assembles the payload map the SmartPicker ships to the
// frontend when a customer is selected. The Invoice editor uses this to pre-
// fill email / bill-to / ship-to without a separate fetch: default_currency
// (unchanged from prior behaviour), email, bill_to, and a JSON list of
// shipping addresses with the default marked first.
func customerPickerPayload(db *gorm.DB, c models.Customer) map[string]string {
	payload := make(map[string]string, 4)
	if c.CurrencyCode != "" {
		payload["default_currency"] = c.CurrencyCode
	}
	if c.Email != "" {
		payload["email"] = c.Email
	}
	if billTo := c.FormattedAddress(); billTo != "" {
		payload["bill_to"] = billTo
	}
	var shipAddrs []models.CustomerShippingAddress
	if err := db.
		Where("customer_id = ?", c.ID).
		Order("is_default DESC, id ASC").
		Find(&shipAddrs).Error; err == nil && len(shipAddrs) > 0 {
		// Serialise shipping addresses as JSON so the Alpine editor can
		// populate a dropdown; the first element (default) is what the
		// editor preselects.
		type sa struct {
			Label   string `json:"label"`
			Address string `json:"address"`
			Default bool   `json:"is_default"`
		}
		entries := make([]sa, 0, len(shipAddrs))
		for _, a := range shipAddrs {
			entries = append(entries, sa{Label: a.Label, Address: a.FormattedAddress(), Default: a.IsDefault})
		}
		if b, err := json.Marshal(entries); err == nil {
			payload["shipping_addresses"] = string(b)
		}
	}
	return payload
}

// ── VendorProvider ───────────────────────────────────────────────────────────

// VendorProvider handles entity="vendor". New-document picker contexts return
// active vendors only; reporting/filter contexts can still find historical
// inactive vendors.
type VendorProvider struct{}

func (p *VendorProvider) EntityType() string { return "vendor" }

func (p *VendorProvider) scopedQuery(db *gorm.DB, ctx SmartPickerContext) *gorm.DB {
	q := db.Where("company_id = ?", ctx.CompanyID)
	switch ctx.Context {
	case "bills_filter", "purchase_orders_filter", "vendor_credit_notes_filter",
		"vendor_prepayments_filter", "vendor_refunds_filter", "vendor_returns_filter":
		return q
	default:
		return q.Where("is_active = true")
	}
}

func (p *VendorProvider) Search(db *gorm.DB, ctx SmartPickerContext, query string) (*SmartPickerResult, error) {
	var vendors []models.Vendor
	q := p.scopedQuery(db, ctx).
		Order("name ASC").
		Limit(smartPickerLimit(ctx))
	q = applySmartPickerTextSearch(q, db.Dialector.Name(), query,
		"name", "email", "phone", "currency_code")

	if err := q.Find(&vendors).Error; err != nil {
		return nil, fmt.Errorf("vendor search: %w", err)
	}

	items := make([]SmartPickerItem, 0, len(vendors))
	for _, v := range vendors {
		items = append(items, *vendorSmartPickerItem(v))
	}
	return &SmartPickerResult{Candidates: items}, nil
}

func (p *VendorProvider) GetByID(db *gorm.DB, ctx SmartPickerContext, id string) (*SmartPickerItem, error) {
	var vendor models.Vendor
	err := p.scopedQuery(db, ctx).
		Where("id = ?", id).
		First(&vendor).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("vendor get by id: %w", err)
	}
	return vendorSmartPickerItem(vendor), nil
}

func vendorSmartPickerItem(v models.Vendor) *SmartPickerItem {
	item := &SmartPickerItem{
		ID:        fmt.Sprintf("%d", v.ID),
		Primary:   v.Name,
		Secondary: vendorSmartPickerSecondary(v),
	}
	// currency_code in payload lets the expense form auto-set currency on vendor select.
	if cc := strings.TrimSpace(v.CurrencyCode); cc != "" {
		item.Payload = map[string]string{"currency_code": cc}
	}
	return item
}

func vendorSmartPickerSecondary(v models.Vendor) string {
	if strings.TrimSpace(v.Email) != "" {
		return strings.TrimSpace(v.Email)
	}
	if strings.TrimSpace(v.Phone) != "" {
		return strings.TrimSpace(v.Phone)
	}
	return strings.TrimSpace(v.CurrencyCode)
}

// ── ProductServiceProvider ──────────────────────────────────────────────────

// ProductServiceProvider handles entity="product_service". The context controls
// the subset. context="task_form_service_item" returns only active service-type
// items for the authenticated company.
type ProductServiceProvider struct{}

func (p *ProductServiceProvider) EntityType() string { return "product_service" }

func (p *ProductServiceProvider) Search(db *gorm.DB, ctx SmartPickerContext, query string) (*SmartPickerResult, error) {
	var items []models.ProductService
	q := p.scopedQuery(db, ctx).
		Order("name ASC").
		Limit(smartPickerLimit(ctx))
	q = applySmartPickerTextSearch(q, db.Dialector.Name(), query, "name", "sku", "description")

	if err := q.Find(&items).Error; err != nil {
		return nil, fmt.Errorf("product service search: %w", err)
	}

	out := make([]SmartPickerItem, 0, len(items))
	for _, item := range items {
		out = append(out, productServiceSmartPickerItem(item))
	}
	return &SmartPickerResult{Candidates: out}, nil
}

func (p *ProductServiceProvider) GetByID(db *gorm.DB, ctx SmartPickerContext, id string) (*SmartPickerItem, error) {
	var item models.ProductService
	err := p.scopedQuery(db, ctx).
		Where("id = ?", id).
		First(&item).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("product service get by id: %w", err)
	}
	spItem := productServiceSmartPickerItem(item)
	return &spItem, nil
}

func (p *ProductServiceProvider) scopedQuery(db *gorm.DB, ctx SmartPickerContext) *gorm.DB {
	q := db.Where("company_id = ? AND is_active = true", ctx.CompanyID)
	if ctx.Context == "task_form_service_item" {
		q = q.Where("type = ?", models.ProductServiceTypeService)
	}
	return q
}

func productServiceSmartPickerItem(item models.ProductService) SmartPickerItem {
	spItem := SmartPickerItem{
		ID:        fmt.Sprintf("%d", item.ID),
		Primary:   item.Name,
		Secondary: productServiceSmartPickerSecondary(item),
		// Payload carries default_price for JS auto-fill (e.g. Task Form Rate field).
		// It is not rendered in the dropdown UI.
		Payload: map[string]string{
			"default_price": item.DefaultPrice.StringFixed(2),
		},
	}
	if strings.TrimSpace(item.SKU) != "" {
		spItem.Meta = map[string]string{"type": models.ProductServiceTypeLabel(item.Type)}
	}
	return spItem
}

func productServiceSmartPickerSecondary(item models.ProductService) string {
	if strings.TrimSpace(item.SKU) != "" {
		return "SKU: " + strings.TrimSpace(item.SKU)
	}
	return models.ProductServiceTypeLabel(item.Type)
}

// ── PaymentAccountProvider ───────────────────────────────────────────────────

// PaymentAccountProvider handles entity="payment_account".
// It returns active accounts suitable for recording a payment outflow:
// bank accounts (asset), credit card accounts (liability), and
// petty-cash / other current asset accounts — i.e. detail_account_type IN
// (bank, credit_card, other_current_asset).
type PaymentAccountProvider struct{}

func (p *PaymentAccountProvider) EntityType() string { return "payment_account" }

func (p *PaymentAccountProvider) Search(db *gorm.DB, ctx SmartPickerContext, query string) (*SmartPickerResult, error) {
	var accounts []models.Account
	q := db.
		Where("company_id = ? AND detail_account_type IN ? AND is_active = true",
			ctx.CompanyID,
			models.PaymentSourceDetailTypes()).
		Order("code ASC").
		Limit(smartPickerLimit(ctx))
	q = applySmartPickerTextSearch(q, db.Dialector.Name(), query, "name", "code")

	if err := q.Find(&accounts).Error; err != nil {
		return nil, fmt.Errorf("payment account search: %w", err)
	}

	items := make([]SmartPickerItem, 0, len(accounts))
	for _, a := range accounts {
		items = append(items, SmartPickerItem{
			ID:        fmt.Sprintf("%d", a.ID),
			Primary:   a.Name,
			Secondary: a.Code,
			Meta: map[string]string{
				"type": models.DetailSnakeToLabel(string(a.DetailAccountType)),
			},
		})
	}
	return &SmartPickerResult{Candidates: items}, nil
}

func (p *PaymentAccountProvider) GetByID(db *gorm.DB, ctx SmartPickerContext, id string) (*SmartPickerItem, error) {
	var account models.Account
	err := db.
		Where("id = ? AND company_id = ? AND detail_account_type IN ? AND is_active = true",
			id, ctx.CompanyID,
			models.PaymentSourceDetailTypes()).
		First(&account).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("payment account get by id: %w", err)
	}
	return &SmartPickerItem{
		ID:        fmt.Sprintf("%d", account.ID),
		Primary:   account.Name,
		Secondary: account.Code,
		Meta: map[string]string{
			"type": models.DetailSnakeToLabel(string(account.DetailAccountType)),
		},
	}, nil
}
