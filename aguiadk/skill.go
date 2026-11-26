package aguiadk

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"unicode"

	"github.com/Artui/go-services/aguix"
)

// Skill is a pre-defined action the user picks from the web component's slash
// palette or its chips, by name.
//
// Its Body stays on the server: picking it sends the bare /name, and a run
// whose latest user message opens with it adds the body to its instructions.
type Skill struct {
	// Title labels the skill in the palette and the chips; the name when empty.
	Title string
	// Description is the palette's secondary line, if any.
	Description string
	// Body is what the agent is told to do when the user picks the skill.
	Body string
	// SendImmediately sends the pick at once instead of pre-filling the composer.
	SendImmediately bool
	// Chip also shows the skill as a chip above the composer.
	Chip bool
}

// WithSkills offers skills by name, the token the user types after a slash.
//
// A run whose latest user message opens with /name, a skill's name, adds
// "# Skill: <name>" and the skill's body to the instructions of every model
// call the run makes, a delegated sub-agent's included, as WithRunContext adds
// its block. The message itself is left as the user sent it, so /name stays in
// the conversation, and a name that is not a skill's changes nothing. The
// latest user message decides, so a run that answers a browser tool call keeps
// the skill its user picked.
// Serve the same skills to the web component with SkillCatalog.
//
// New refuses a name that is empty, starts with a slash or holds white space,
// since the user could not pick it, and a skill whose body is blank, since
// picking it would tell the agent nothing.
func WithSkills(skills map[string]Skill) Option {
	return func(b *bridge) {
		for _, name := range slices.Sorted(maps.Keys(skills)) {
			if err := checkSkill(name, skills[name]); err != nil {
				b.optErr = err
				return
			}
		}
		b.instructions.skills = maps.Clone(skills)
	}
}

func checkSkill(name string, s Skill) error {
	if name == "" || strings.HasPrefix(name, "/") || strings.ContainsFunc(name, unicode.IsSpace) {
		return fmt.Errorf("aguiadk: skill name %q cannot be picked as /name", name)
	}
	if strings.TrimSpace(s.Body) == "" {
		return fmt.Errorf("aguiadk: skill %q has no body", name)
	}
	return nil
}

// skill is the picked skill's instructions, empty when the run picked none.
func (ri runInstructions) skill(in aguix.RunInput) string {
	msg, ok := in.LastUserMessage()
	if !ok {
		return ""
	}
	words := strings.Fields(msg.Text())
	if len(words) == 0 || !strings.HasPrefix(words[0], "/") {
		return ""
	}
	name := words[0][1:]
	s, ok := ri.skills[name]
	if !ok {
		return ""
	}
	return fmt.Sprintf("# Skill: %s\n\n%s", name, s.Body)
}

// skillEntry is one row of the catalog, in the camelCase keys the web
// component's data-skills-url reader takes. It never carries a prompt, so the
// component sends /name and the body stays here.
type skillEntry struct {
	Name            string `json:"name"`
	Title           string `json:"title"`
	Description     string `json:"description,omitempty"`
	SendImmediately bool   `json:"sendImmediately,omitempty"`
	Chip            bool   `json:"chip,omitempty"`
}

// SkillCatalog serves skills to the web component's data-skills-url, the
// palette WithSkills resolves.
//
// It answers GET with a JSON array of {name, title, description?,
// sendImmediately?, chip?}, sorted by name, and never a skill's body. A skill
// still names an internal workflow, so mount it behind the same authentication
// as the agent's endpoint.
func SkillCatalog(skills map[string]Skill) http.Handler {
	rows := make([]skillEntry, 0, len(skills))
	for _, name := range slices.Sorted(maps.Keys(skills)) {
		s := skills[name]
		title := s.Title
		if strings.TrimSpace(title) == "" {
			title = name
		}
		rows = append(rows, skillEntry{
			Name: name, Title: title, Description: s.Description,
			SendImmediately: s.SendImmediately, Chip: s.Chip,
		})
	}
	// Strings and booleans only, which always encode.
	body, _ := json.Marshal(rows)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	})
}
