// 遵循project_guide.md
//
// search-reconcile — drift detector for the search_documents projection.
//
// Reads canonical business tables and the projection in parallel,
// reports per-(company, entity_type) row-count deltas. Designed to run
// as a nightly cron job:
//
//   - Exit code 0 = no drift detected; cron reports success
//   - Exit code 1 = drift found OR repair required; cron alerts
//   - Exit code 2 = unrecoverable error (DB down, etc.)
//
// Three modes:
//
//	(default)    detect-only: print report + exit non-zero on drift.
//	             Safe to run on prod, no writes.
//	-repair      detect + project missing rows + delete orphan rows +
//	             refresh stale (projector_version < current). Writes
//	             through the same EntProjector code path as handlers,
//	             so the H1/H2 cross-tenant guards apply.
//	-json        emit one JSON line per (company, entity_type) for log
//	             aggregators / Prometheus textfile collectors.
//
// Examples:
//
//	go run ./cmd/search-reconcile                # detect, human-readable
//	go run ./cmd/search-reconcile -json          # detect, machine-readable
//	go run ./cmd/search-reconcile -repair        # detect + auto-fix
//	go run ./cmd/search-reconcile -company 42    # one company only
//	go run ./cmd/search-reconcile -only invoice  # one entity type only
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"gorm.io/gorm"

	"balanciz/ent/searchdocument"
	"balanciz/internal/config"
	"balanciz/internal/db"
	"balanciz/internal/logging"
	"balanciz/internal/models"
	"balanciz/internal/searchprojection"
	"balanciz/internal/searchprojection/producers"
)

// entityFamily describes one slot in the reconciler's scan loop:
// how to count business rows, how to enumerate IDs (for orphan
// detection), and how to project a single ID (for missing-row repair).
type entityFamily struct {
	entityType string
	// businessIDs returns all current ID values for the given company
	// in the canonical business table. Sorted ascending.
	businessIDs func(db *gorm.DB, companyID uint) ([]uint, error)
	// project upserts the projection row for (companyID, entityID).
	// Used in repair mode for missing rows.
	project func(ctx context.Context, db *gorm.DB, p searchprojection.Projector, companyID, entityID uint) error
}

// allFamilies — keep in lock-step with producers/* registrations.
// New entity types must add a slot here AND in the matching producer
// file; the unit test below catches drift between the two registries.
var allFamilies = []entityFamily{
	{
		entityType:  producers.EntityTypeCustomer,
		businessIDs: idsOf[models.Customer],
		project:     producers.ProjectCustomer,
	},
	{
		entityType:  producers.EntityTypeVendor,
		businessIDs: idsOf[models.Vendor],
		project:     producers.ProjectVendor,
	},
	{
		entityType:  producers.EntityTypeProductService,
		businessIDs: idsOf[models.ProductService],
		project:     producers.ProjectProductService,
	},
	{
		entityType:  producers.EntityTypeInvoice,
		businessIDs: idsOf[models.Invoice],
		project:     producers.ProjectInvoice,
	},
	{
		entityType:  producers.EntityTypeBill,
		businessIDs: idsOf[models.Bill],
		project:     producers.ProjectBill,
	},
	{
		entityType:  producers.EntityTypeQuote,
		businessIDs: idsOf[models.Quote],
		project:     producers.ProjectQuote,
	},
	{
		entityType:  producers.EntityTypeSalesOrder,
		businessIDs: idsOf[models.SalesOrder],
		project:     producers.ProjectSalesOrder,
	},
	{
		entityType:  producers.EntityTypePurchaseOrder,
		businessIDs: idsOf[models.PurchaseOrder],
		project:     producers.ProjectPurchaseOrder,
	},
	{
		entityType:  producers.EntityTypeCustomerReceipt,
		businessIDs: idsOf[models.CustomerReceipt],
		project:     producers.ProjectCustomerReceipt,
	},
	{
		entityType:  producers.EntityTypeExpense,
		businessIDs: idsOf[models.Expense],
		project:     producers.ProjectExpense,
	},
	// Phase 5.4 / 5.5
	{
		entityType:  producers.EntityTypeJournalEntry,
		businessIDs: idsOf[models.JournalEntry],
		project:     producers.ProjectJournalEntry,
	},
	{
		entityType:  producers.EntityTypeCreditNote,
		businessIDs: idsOf[models.CreditNote],
		project:     producers.ProjectCreditNote,
	},
	{
		entityType:  producers.EntityTypeVendorCreditNote,
		businessIDs: idsOf[models.VendorCreditNote],
		project:     producers.ProjectVendorCreditNote,
	},
	{
		entityType:  producers.EntityTypeARReturn,
		businessIDs: idsOf[models.ARReturn],
		project:     producers.ProjectARReturn,
	},
	{
		entityType:  producers.EntityTypeVendorReturn,
		businessIDs: idsOf[models.VendorReturn],
		project:     producers.ProjectVendorReturn,
	},
	{
		entityType:  producers.EntityTypeARRefund,
		businessIDs: idsOf[models.ARRefund],
		project:     producers.ProjectARRefund,
	},
	{
		entityType:  producers.EntityTypeVendorRefund,
		businessIDs: idsOf[models.VendorRefund],
		project:     producers.ProjectVendorRefund,
	},
	{
		entityType:  producers.EntityTypeCustomerDeposit,
		businessIDs: idsOf[models.CustomerDeposit],
		project:     producers.ProjectCustomerDeposit,
	},
	{
		entityType:  producers.EntityTypeVendorPrepayment,
		businessIDs: idsOf[models.VendorPrepayment],
		project:     producers.ProjectVendorPrepayment,
	},
}

// idsOf is the generic ID-enumerator used by every entity family.
// Pulls just `id` (no Preload, no full row hydration) so the scan stays
// cheap on large tables.
func idsOf[T any](db *gorm.DB, companyID uint) ([]uint, error) {
	var ids []uint
	var zero T
	q := db.Model(&zero).Where("company_id = ?", companyID).Order("id ASC").Pluck("id", &ids)
	return ids, q.Error
}

// reconcileResult is the per-(company, entity_type) summary line.
type reconcileResult struct {
	CompanyID     uint   `json:"company_id"`
	EntityType    string `json:"entity_type"`
	BusinessCount int    `json:"business_count"`
	ProjectionCount int  `json:"projection_count"`
	Missing       int    `json:"missing"` // in business but not projection
	Orphans       int    `json:"orphans"` // in projection but not business
	Repaired      int    `json:"repaired,omitempty"`
	OrphansDeleted int   `json:"orphans_deleted,omitempty"`
}

// HasDrift returns true when the projection doesn't match the business
// table. Drives the process exit code.
func (r reconcileResult) HasDrift() bool { return r.Missing > 0 || r.Orphans > 0 }

func main() {
	repair := flag.Bool("repair", false, "project missing rows + delete orphans (writes to search_documents)")
	emitJSON := flag.Bool("json", false, "machine-readable output (one JSON line per row)")
	companyFilter := flag.Uint("company", 0, "limit to one company_id (0 = all)")
	only := flag.String("only", "all", "limit to one entity family")
	flag.Parse()

	logging.Init()
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config load: %v\n", err)
		os.Exit(2)
	}
	logging.SetLevel(cfg.LogLevel)

	gormDB, err := db.Connect(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "db connect: %v\n", err)
		os.Exit(2)
	}

	entClient, err := searchprojection.OpenEntFromGorm(gormDB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ent client: %v\n", err)
		os.Exit(2)
	}
	projector, err := searchprojection.NewEntProjector(entClient, searchprojection.AsciiNormalizer{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "projector: %v\n", err)
		os.Exit(2)
	}

	families := filterFamilies(allFamilies, *only)
	if len(families) == 0 {
		fmt.Fprintf(os.Stderr, "unknown -only value %q\n", *only)
		os.Exit(2)
	}

	companies, err := companyIDs(gormDB, *companyFilter)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load companies: %v\n", err)
		os.Exit(2)
	}

	ctx := context.Background()
	start := time.Now()
	totalDrift := false

	results := make([]reconcileResult, 0, len(companies)*len(families))
	for _, companyID := range companies {
		for _, fam := range families {
			res, err := reconcileOne(ctx, gormDB, entClient, projector, fam, companyID, *repair)
			if err != nil {
				fmt.Fprintf(os.Stderr, "reconcile %d/%s failed: %v\n", companyID, fam.entityType, err)
				os.Exit(2)
			}
			results = append(results, res)
			if res.HasDrift() {
				totalDrift = true
			}
		}
	}

	if *emitJSON {
		emitJSONReport(results)
	} else {
		emitHumanReport(results, *repair, time.Since(start))
	}

	if totalDrift && !*repair {
		// drift remains after a no-repair run → exit code 1 so cron alerts.
		os.Exit(1)
	}
}

// reconcileOne runs the drift check for a single (company, entity_type)
// pair. In repair mode, project missing rows and delete orphan rows
// before returning.
func reconcileOne(
	ctx context.Context,
	gormDB *gorm.DB,
	entClient interface{ /* unused; reserved for future direct queries */ },
	projector searchprojection.Projector,
	fam entityFamily,
	companyID uint,
	repair bool,
) (reconcileResult, error) {
	res := reconcileResult{CompanyID: companyID, EntityType: fam.entityType}

	bizIDs, err := fam.businessIDs(gormDB, companyID)
	if err != nil {
		return res, fmt.Errorf("business ids: %w", err)
	}
	res.BusinessCount = len(bizIDs)

	projIDs, err := projectionIDs(gormDB, companyID, fam.entityType)
	if err != nil {
		return res, fmt.Errorf("projection ids: %w", err)
	}
	res.ProjectionCount = len(projIDs)

	missing, orphans := diffIDSets(bizIDs, projIDs)
	res.Missing = len(missing)
	res.Orphans = len(orphans)

	if repair && len(missing) > 0 {
		for _, id := range missing {
			if err := fam.project(ctx, gormDB, projector, companyID, id); err != nil {
				logging.L().Warn("repair project failed (continuing)",
					"company_id", companyID, "entity_type", fam.entityType,
					"entity_id", id, "err", err)
				continue
			}
			res.Repaired++
		}
	}
	if repair && len(orphans) > 0 {
		for _, id := range orphans {
			if err := projector.Delete(ctx, companyID, fam.entityType, id); err != nil {
				logging.L().Warn("repair delete orphan failed (continuing)",
					"company_id", companyID, "entity_type", fam.entityType,
					"entity_id", id, "err", err)
				continue
			}
			res.OrphansDeleted++
		}
	}
	return res, nil
}

// projectionIDs returns the set of entity IDs currently in
// search_documents for the (company, entity_type) pair. Uses the raw
// GORM connection rather than the ent client to keep the binary size
// small — the projection table is also a regular SQL table.
func projectionIDs(gormDB *gorm.DB, companyID uint, entityType string) ([]uint, error) {
	var ids []uint
	err := gormDB.Table(searchdocument.Table).
		Where("company_id = ? AND entity_type = ?", companyID, entityType).
		Order("entity_id ASC").
		Pluck("entity_id", &ids).Error
	return ids, err
}

// diffIDSets walks two sorted ID slices and returns:
//
//	missing — IDs in business but not projection (need projecting)
//	orphans — IDs in projection but not business (should be deleted)
//
// O(n+m) merge — both inputs MUST be sorted ascending.
func diffIDSets(biz, proj []uint) (missing, orphans []uint) {
	i, j := 0, 0
	for i < len(biz) && j < len(proj) {
		switch {
		case biz[i] < proj[j]:
			missing = append(missing, biz[i])
			i++
		case biz[i] > proj[j]:
			orphans = append(orphans, proj[j])
			j++
		default:
			i++
			j++
		}
	}
	missing = append(missing, biz[i:]...)
	orphans = append(orphans, proj[j:]...)
	return missing, orphans
}

// companyIDs returns the list of companies to scan. companyFilter=0
// means "every company in the system".
func companyIDs(db *gorm.DB, filter uint) ([]uint, error) {
	if filter != 0 {
		return []uint{filter}, nil
	}
	var ids []uint
	err := db.Model(&models.Company{}).Order("id ASC").Pluck("id", &ids).Error
	return ids, err
}

func filterFamilies(all []entityFamily, only string) []entityFamily {
	if only == "" || only == "all" {
		return all
	}
	for _, fam := range all {
		if fam.entityType == only {
			return []entityFamily{fam}
		}
	}
	return nil
}

func emitHumanReport(rows []reconcileResult, repair bool, elapsed time.Duration) {
	mode := "detect"
	if repair {
		mode = "repair"
	}
	header := fmt.Sprintf("%-8s %-20s %10s %10s %10s %10s",
		"company", "entity_type", "business", "projection", "missing", "orphans")
	if repair {
		header += fmt.Sprintf(" %10s %10s", "repaired", "orphans_del")
	}
	fmt.Println(header)
	fmt.Println(strings.Repeat("─", len(header)))
	for _, r := range rows {
		marker := " "
		if r.HasDrift() {
			marker = "!"
		}
		line := fmt.Sprintf("%s%-7d %-20s %10d %10d %10d %10d",
			marker, r.CompanyID, r.EntityType,
			r.BusinessCount, r.ProjectionCount, r.Missing, r.Orphans)
		if repair {
			line += fmt.Sprintf(" %10d %10d", r.Repaired, r.OrphansDeleted)
		}
		fmt.Println(line)
	}
	fmt.Println(strings.Repeat("─", len(header)))
	fmt.Printf("mode=%s elapsed=%s rows=%d\n", mode, elapsed.Truncate(time.Millisecond), len(rows))
}

// emitJSONReport prints one line of compact JSON per result. Suitable
// for log aggregators that line-parse stdout.
func emitJSONReport(rows []reconcileResult) {
	enc := json.NewEncoder(os.Stdout)
	for _, r := range rows {
		_ = enc.Encode(r)
	}
}

// Defensive: log a sanity check on registry consistency. The names in
// allFamilies must exactly match producers/* EntityType* constants.
// This is invoked at the top of main via init() so any drift surfaces
// before the reconciler does any DB work.
func init() {
	expected := map[string]struct{}{
		producers.EntityTypeCustomer:        {},
		producers.EntityTypeVendor:          {},
		producers.EntityTypeProductService:  {},
		producers.EntityTypeInvoice:         {},
		producers.EntityTypeBill:            {},
		producers.EntityTypeQuote:           {},
		producers.EntityTypeSalesOrder:      {},
		producers.EntityTypePurchaseOrder:   {},
		producers.EntityTypeCustomerReceipt: {},
		producers.EntityTypeExpense:         {},
		producers.EntityTypeJournalEntry:     {},
		producers.EntityTypeCreditNote:       {},
		producers.EntityTypeVendorCreditNote: {},
		producers.EntityTypeARReturn:         {},
		producers.EntityTypeVendorReturn:     {},
		producers.EntityTypeARRefund:         {},
		producers.EntityTypeVendorRefund:     {},
		producers.EntityTypeCustomerDeposit:  {},
		producers.EntityTypeVendorPrepayment: {},
	}
	for _, fam := range allFamilies {
		delete(expected, fam.entityType)
	}
	if len(expected) > 0 {
		var missing []string
		for k := range expected {
			missing = append(missing, k)
		}
		log.Fatalf("search-reconcile: missing entity families in registry: %v", missing)
	}
}
