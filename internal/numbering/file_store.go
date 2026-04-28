// Package numbering: display rule defaults, merge/parse, and optional file helpers.
//
// Production persistence for numbering rules is DB-backed (numbering_settings.rules_json),
// scoped by company_id via balanciz/internal/services.LoadMergedDisplayRules and SaveMergedDisplayRules.
// LoadMerged/Save on DefaultStorePath are retained for migration tooling or local experiments only.
package numbering

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

const storeFileName = "display_numbering.json"

// persistedFile is the on-disk JSON shape (versioned for future migrations).
type persistedFile struct {
	Version int           `json:"version"`
	Rules   []DisplayRule `json:"rules"`
}

// DefaultStorePath returns the path under the process working directory.
func DefaultStorePath() string {
	return filepath.Join("data", storeFileName)
}

// LoadMerged reads rules from disk when present and merges them onto defaults by module_key.
func LoadMerged(path string) ([]DisplayRule, error) {
	defaults := DefaultDisplayRules()

	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return defaults, nil
		}
		return nil, err
	}

	var pf persistedFile
	if err := json.Unmarshal(b, &pf); err != nil {
		return nil, err
	}
	return MergeSavedOntoDefaults(defaults, pf.Rules), nil
}

// Save writes all rules for known modules to disk.
func Save(path string, rules []DisplayRule) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	pf := persistedFile{Version: 1, Rules: rules}
	b, err := json.MarshalIndent(pf, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}
