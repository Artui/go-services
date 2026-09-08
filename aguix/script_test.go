package aguix_test

import (
	"testing"

	"github.com/Artui/go-services/aguix"
)

// said builds the smallest run that carries one user message.
func said(text string) aguix.RunInput {
	return aguix.RunInput{
		Messages: []aguix.Message{{ID: "m", Role: "user", Content: text}},
	}
}

// The two matchers differ only when a rule names more than one word, which is
// exactly the case no rule in this repository had -- every WhenUserSays call in
// the suite and in the example passes a single word, and with one word the two
// are indistinguishable.
//
// A consumer outside this repository wrote four rules with synonyms, read the
// name as "says any of these", and got the fallback for every phrase. Nothing
// failed: an unmatched rule loses to the next one, so a script with a fallback
// answers something plausible. The doc comment was right all along and the name
// is what was read.
func TestTheTwoMatchersDisagreeOnlyWhenARuleNamesSeveralWords(t *testing.T) {
	for _, probe := range []struct {
		name     string
		message  string
		words    []string
		all, any bool
	}{
		{"one word, present", "what is my tariff", []string{"tariff"}, true, true},
		{"one word, absent", "what is my tariff", []string{"invoice"}, false, false},
		{"synonyms, one present", "what is my tariff", []string{"account", "tariff", "rate"}, false, true},
		{"synonyms, all present", "my account tariff rate", []string{"account", "tariff", "rate"}, true, true},
		{"synonyms, none present", "hello there", []string{"account", "tariff"}, false, false},
		{"case is ignored", "MY TARIFF", []string{"tariff"}, true, true},
		{"no words at all", "anything", nil, true, false},
	} {
		t.Run(probe.name, func(t *testing.T) {
			if got := aguix.WhenUserSays(probe.words...)(said(probe.message)); got != probe.all {
				t.Errorf("WhenUserSays(%q) on %q = %v, want %v",
					probe.words, probe.message, got, probe.all)
			}
			if got := aguix.WhenUserSaysAny(probe.words...)(said(probe.message)); got != probe.any {
				t.Errorf("WhenUserSaysAny(%q) on %q = %v, want %v",
					probe.words, probe.message, got, probe.any)
			}
		})
	}
}

// The empty-list row above is the one worth stating on its own, because the two
// answers are opposite and both are right. A conjunction over nothing is
// vacuously true and a disjunction over nothing is vacuously false -- so
// WhenUserSays() matches every run and is a fallback written the long way,
// while WhenUserSaysAny() matches none and is a rule that can never fire.
func TestAMatcherWithNoWordsIsTrueForAllAndFalseForAny(t *testing.T) {
	if !aguix.WhenUserSays()(said("anything at all")) {
		t.Error("WhenUserSays() refused a run, but a conjunction over nothing holds")
	}
	if aguix.WhenUserSaysAny()(said("anything at all")) {
		t.Error("WhenUserSaysAny() matched a run, but a disjunction over nothing does not")
	}
}

// A run with nothing from the user matches neither, which is the guard that
// keeps a scripted agent from acting on a conversation it was not addressed in.
func TestNeitherMatcherActsOnARunWithNoUserMessage(t *testing.T) {
	empty := aguix.RunInput{Messages: []aguix.Message{{ID: "a", Role: "assistant", Content: "tariff"}}}

	if aguix.WhenUserSays("tariff")(empty) {
		t.Error("WhenUserSays matched an assistant message")
	}
	if aguix.WhenUserSaysAny("tariff")(empty) {
		t.Error("WhenUserSaysAny matched an assistant message")
	}
}
