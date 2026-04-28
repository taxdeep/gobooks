// 遵循project_guide.md
package pdf

import "balanciz/internal/db"

// init wires SeedSystemPDFTemplates into the migrate pipeline without
// creating an import cycle (db is a lower-level package than services).
// Importing services/pdf anywhere — handlers, services, or main — pulls
// this in and registers the seeder before db.Migrate runs.
func init() {
	db.SetPDFTemplateSeeder(SeedSystemPDFTemplates)
}
