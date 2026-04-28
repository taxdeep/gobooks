// 遵循project_guide.md
package searchprojection

import (
	"fmt"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"gorm.io/gorm"

	"balanciz/ent"
)

// OpenEntFromGorm returns an *ent.Client that drives the same underlying
// *sql.DB as the given GORM instance. Connection pool, auth, and TLS
// config are therefore shared — operators only configure one set of DB
// credentials, and ent/GORM cannot starve each other at the pool layer.
//
// The caller owns the returned client's lifecycle but should NOT call
// Close() on it during normal operation: closing the ent client closes
// the shared *sql.DB out from under GORM. Normal process shutdown relies
// on the OS to release handles.
func OpenEntFromGorm(db *gorm.DB) (*ent.Client, error) {
	if db == nil {
		return nil, fmt.Errorf("searchprojection: nil gorm.DB")
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("searchprojection: extract sql.DB from gorm: %w", err)
	}
	drv := entsql.OpenDB(dialect.Postgres, sqlDB)
	return ent.NewClient(ent.Driver(drv)), nil
}
