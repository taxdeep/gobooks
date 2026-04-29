// Package version holds the application release string.
package version

const (
	// Major, Minor, and Patch are decimal release components.
	Major = 0
	Minor = 0
	Patch = 0

	// BuildCode is a fixed-width uppercase base36 build counter.
	BuildCode = "000D"

	// Version format: M.mm.ppp.BBBB
	// M/mm/ppp are decimal; BBBB is a four-character base36 build code.
	Version = "0.00.000.000D"
)
