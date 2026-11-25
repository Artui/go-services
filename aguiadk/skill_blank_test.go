package aguiadk

import (
	"strings"
	"testing"
)

func TestWithSkillsRefusesASkillWithNoBody(t *testing.T) {
	for _, body := range []string{"", " \n\t"} {
		_, err := New(librarian(t, &scriptLLM{}), WithUserID(Anonymous), WithSkills(map[string]Skill{
			"triage": {Body: "Sort the inbox."}, "summarise": {Title: "Summarise", Body: body},
		}))
		if err == nil || err.Error() != `aguiadk: skill "summarise" has no body` {
			t.Errorf("%q: %v", body, err)
		}
	}
}

func TestWithSkillsNamesTheFirstBadSkillByName(t *testing.T) {
	_, err := New(librarian(t, &scriptLLM{}), WithUserID(Anonymous), WithSkills(map[string]Skill{
		"zeta": {}, "alpha": {}, "mid": {Body: "x"},
	}))
	if err == nil || !strings.Contains(err.Error(), `"alpha"`) {
		t.Errorf("%v", err)
	}
}
