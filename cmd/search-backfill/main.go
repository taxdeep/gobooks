// 遵循project_guide.md
//
// search-backfill — rebuilds the search_documents projection from the
// canonical business tables. Run this:
//
//   - Once after deploying Phase 1 to populate the projection for all
//     existing customers + vendors (handlers only cover rows touched
//     after deploy).
//   - Any time projection logic (producers/*) changes in a way that
//     rewrites existing rows — bumping searchprojection.CurrentProjectorVersion
//     makes this explicit.
//   - To recover from a projection drift incident (rare — projector
//     failures are logged by the handlers).
//
// The actual scan + upsert logic lives in
// internal/searchprojection/backfill so the SysAdmin "Rebuild search
// index" button can run the same code path in-process. This binary is a
// thin CLI wrapper around backfill.RunAll / backfill.RunFamily.
//
// Usage:
//
//	go run ./cmd/search-backfill                        # all entity families
//	go run ./cmd/search-backfill -only customer         # one family (see backfill.AllFamilies)
//	go run ./cmd/search-backfill -dry                   # log progress, skip upserts
//	go run ./cmd/search-backfill -company 42            # restrict to one company
//	go run ./cmd/search-backfill -batch 100             # smaller batches (gentler on pool)
package main

import (
	"context"
	"flag"
	"log"
	"time"

	"balanciz/internal/config"
	"balanciz/internal/db"
	"balanciz/internal/logging"
	"balanciz/internal/searchprojection"
	"balanciz/internal/searchprojection/backfill"
)

func main() {
	only := flag.String("only", "all", "restrict to one family (see internal/searchprojection/backfill.AllFamilies) — or 'all'")
	dry := flag.Bool("dry", false, "scan + log counts but skip projection upserts")
	companyFilter := flag.Uint("company", 0, "limit to a single company_id (0 = all companies)")
	batchSize := flag.Int("batch", 500, "rows per batch; lower = gentler on the pool")
	flag.Parse()

	logging.Init()
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config load failed: %v", err)
	}
	logging.SetLevel(cfg.LogLevel)

	gormDB, err := db.Connect(cfg)
	if err != nil {
		log.Fatalf("db connect failed: %v", err)
	}

	var projector searchprojection.Projector
	if *dry {
		projector = searchprojection.NoopProjector{}
	} else {
		client, err := searchprojection.OpenEntFromGorm(gormDB)
		if err != nil {
			log.Fatalf("ent client init failed: %v", err)
		}
		p, err := searchprojection.NewEntProjector(client, searchprojection.AsciiNormalizer{})
		if err != nil {
			log.Fatalf("projector init failed: %v", err)
		}
		projector = p
	}

	ctx := context.Background()
	opts := backfill.Options{CompanyFilter: *companyFilter, Batch: *batchSize}

	start := time.Now()
	if *only == "all" {
		res := backfill.RunAll(ctx, gormDB, projector, opts)
		for _, fr := range res.Families {
			if fr.Err != nil {
				log.Fatalf("%s backfill failed: %v", fr.Family, fr.Err)
			}
		}
		logging.L().Info("search-backfill complete",
			"elapsed_ms", time.Since(start).Milliseconds(),
			"total_rows", res.TotalRows(),
			"dry", *dry,
		)
		return
	}

	fam, ok := backfill.ParseFamily(*only)
	if !ok {
		log.Fatalf("unknown family %q (see internal/searchprojection/backfill.AllFamilies)", *only)
	}
	fr := backfill.RunFamily(ctx, gormDB, projector, fam, opts)
	if fr.Err != nil {
		log.Fatalf("%s backfill failed: %v", fr.Family, fr.Err)
	}
	logging.L().Info("search-backfill complete",
		"family", fr.Family,
		"elapsed_ms", fr.Duration.Milliseconds(),
		"rows", fr.Rows,
		"dry", *dry,
	)
}
