package aguiadk

import (
	"encoding/json"
	"testing"

	"github.com/Artui/go-services/aguix"
)

func TestTheRecorderKeepsEachActivitysLastSnapshotInPlace(t *testing.T) {
	r := &recorder{}
	r.observe(aguix.ActivitySnapshot("plan", "plan", map[string]any{"step": 1}))
	r.observe(aguix.TextMessageStart("m1"))
	r.observe(aguix.ActivitySnapshot("plan", "plan", map[string]any{"step": 2}))
	r.observe(aguix.ActivitySnapshot("plan", "plan", map[string]any{"step": 3}).WithReplace(true))
	r.observe(aguix.ActivitySnapshot("plan", "plan", map[string]any{"step": 4}).WithReplace(false))
	r.observe(aguix.ActivitySnapshot("m1", "note", map[string]any{"n": 1}).WithReplace(false))
	sub := aguix.ActivitySnapshot("sub", "chart", map[string]any{"k": "bar"})
	sub.SubagentRunID = "sr1"
	r.observe(sub)
	got, err := json.Marshal(r.messages())
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"id":"plan","role":"activity","content":{"step":3},"activityType":"plan"},` +
		`{"id":"m1","role":"assistant","content":""},` +
		`{"id":"sub","role":"activity","content":{"k":"bar"},"activityType":"chart","subagentRunId":"sr1"}]`
	if string(got) != want {
		t.Errorf("%s\nwant %s", got, want)
	}
	r.observe(aguix.ActivitySnapshot("m1", "note", map[string]any{"n": 2}))
	if m := r.messages()[1]; m.Role != "activity" || m.ActivityType != "note" || string(m.Content) != `{"n":2}` {
		t.Errorf("a snapshot over a text message: %+v", m)
	}
}
