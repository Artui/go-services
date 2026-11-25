package aguiadk

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/Artui/go-services/aguix"
)

// TestRecorderMatchesTranscripts rebuilds every recorded run's messages and
// compares them with testdata/agui/messages.json, which scripts/agui-wire
// checks against the messages @ag-ui/client's own reducer files from the same
// frames.
func TestRecorderMatchesTranscripts(t *testing.T) {
	dir := filepath.Join("testdata", "agui", "transcripts")
	names, err := filepath.Glob(filepath.Join(dir, "*.sse"))
	if err != nil || len(names) == 0 {
		t.Fatalf("no transcripts: %v", err)
	}
	sort.Strings(names)
	got := map[string][]aguix.Message{}
	for _, path := range names {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var r recorder
		for _, chunk := range strings.Split(string(body), "\n\n") {
			r.observe(decodeFrame(t, path, chunk))
		}
		got[strings.TrimSuffix(filepath.Base(path), ".sse")] = r.messages()
	}
	encoded, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	path := filepath.Join("testdata", "agui", "messages.json")
	if *update {
		if err := os.WriteFile(path, encoded, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run the tests with -update to write it)", err)
	}
	if !bytes.Equal(encoded, want) {
		t.Errorf("the recorded messages drifted from %s:\n%s", path, encoded)
	}
}

// decodeFrame reads one frame's flat fields; a payload-carrying frame keeps its
// type only, which is all the recorder reads of it.
func decodeFrame(t *testing.T, path, chunk string) aguix.Event {
	t.Helper()
	raw := []byte(strings.TrimPrefix(strings.TrimSpace(chunk), "data: "))
	var e aguix.Event
	if err := json.Unmarshal(raw, &e); err == nil {
		return e
	}
	if err := json.Unmarshal(raw, &struct {
		Type *aguix.EventType `json:"type"`
	}{&e.Type}); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return e
}
