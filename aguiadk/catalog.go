package aguiadk

import (
	"encoding/json"
	"net/http"
	"strings"

	services "github.com/Artui/go-services"
)

// catalogEntry is one row of the catalog the web component reads through its
// data-tools-url attribute.
type catalogEntry struct {
	Name        string `json:"name"`
	Summary     string `json:"summary"`
	Description string `json:"description,omitempty"`
}

// ToolCatalog serves the labels of the server's tools, which the browser never
// sees declared, so the web component can name a server call's card.
//
// It answers GET with a JSON array of {name, summary, description}; the
// summary is Metadata["x-summary"] when that is a non-empty string, else the
// name made readable. It names every operation the agent can run, so mount it
// behind the same authentication as the agent's endpoint.
func ToolCatalog(entries []services.Entry) http.Handler {
	rows := make([]catalogEntry, 0, len(entries))
	for _, e := range entries {
		summary, _ := e.Metadata["x-summary"].(string)
		if strings.TrimSpace(summary) == "" {
			summary = prettify(e.Name)
		}
		rows = append(rows, catalogEntry{Name: e.Name, Summary: summary, Description: e.Description})
	}
	// Strings only, which always encode.
	body, _ := json.Marshal(rows)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	})
}

// prettify turns query_model into "Query model", as django-spec-agents does.
func prettify(name string) string {
	text := strings.Join(strings.FieldsFunc(name, func(r rune) bool {
		return r == '_' || r == '-' || r == ' ' || r == '\t' || r == '\n'
	}), " ")
	if text == "" {
		return ""
	}
	return strings.ToUpper(text[:1]) + text[1:]
}
