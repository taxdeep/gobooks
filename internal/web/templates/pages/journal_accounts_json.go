// 遵循project_guide.md
package pages

import (
	"encoding/json"
	"strings"

	"github.com/a-h/templ"

	"balanciz/internal/models"
)

// JournalAccountsJSONScript embeds account picker data for the journal entry combobox (trusted JSON from server).
func JournalAccountsJSONScript(json string) templ.Component {
	return templ.Raw(`<script type="application/json" id="balanciz-journal-accounts-data">` + json + `</script>`)
}

// JournalAccountsDataJSON returns a script-safe JSON array for the journal account combobox.
func JournalAccountsDataJSON(accounts []models.Account) string {
	type row struct {
		ID    uint   `json:"id"`
		Code  string `json:"code"`
		Name  string `json:"name"`
		Class string `json:"class"`
	}
	out := make([]row, 0, len(accounts))
	for _, a := range accounts {
		out = append(out, row{
			ID:    a.ID,
			Code:  a.Code,
			Name:  a.Name,
			Class: models.ClassificationDisplay(a.RootAccountType, a.DetailAccountType),
		})
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "[]"
	}
	s := string(b)
	// Prevent closing a <script> tag if account text ever contained "</script>".
	s = strings.ReplaceAll(s, "<", "\\u003c")
	s = strings.ReplaceAll(s, ">", "\\u003e")
	return s
}
