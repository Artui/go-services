package aguiadk

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"maps"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/Artui/go-services/aguix"
)

// The activity types the web component draws. Each is a convention inside
// AG-UI's ACTIVITY_SNAPSHOT, whose activityType is an open string, so a client
// that does not know one ignores it.
const (
	SuggestionsActivityType = "suggestions"
	ChartActivityType       = "chart"
	CompactionActivityType  = "compaction"
)

// The bounds the web component draws within. It drops what is past them and
// has no channel to say so, which is why these refuse rather than trim.
const (
	MaxSuggestions     = 4
	MaxSuggestionChars = 120

	MaxChartLabels    = 2000
	MaxChartPoints    = 20000
	MaxChartMagnitude = 1e15
)

// Every Publish function below queues its frame as PublishInvalidation does:
// from inside a tool call it goes out right after that call's result (a
// streaming tool's, after the next event), and from anywhere else ahead of the
// run's next event. Each reports whether a run was
// listening, and refuses what the web component would not draw.

// PublishSuggestions offers up to MaxSuggestions follow-up prompts as chips;
// clicking one sends its text as the user's message, so write each as the user
// would say it. id names the set: the same id again replaces a row already
// drawn, and empty draws a new one under "suggestions-<uuid>". The id shares the
// namespace of message ids.
func PublishSuggestions(ctx context.Context, id string, prompts ...string) (bool, error) {
	if len(prompts) == 0 {
		return false, errors.New("aguiadk: suggestions need at least one prompt")
	}
	if len(prompts) > MaxSuggestions {
		return false, fmt.Errorf("aguiadk: at most %d suggestions, got %d", MaxSuggestions, len(prompts))
	}
	trimmed := make([]string, len(prompts))
	for i, p := range prompts {
		if n := utf8.RuneCountInString(p); n > MaxSuggestionChars {
			return false, fmt.Errorf("aguiadk: a suggestion is at most %d characters, got %d", MaxSuggestionChars, n)
		}
		if trimmed[i] = strings.TrimSpace(p); trimmed[i] == "" {
			return false, errors.New("aguiadk: a suggestion must not be blank")
		}
	}
	return publishActivity(ctx, SuggestionsActivityType, id, map[string]any{"prompts": trimmed}), nil
}

// ChartSpec is a chart as data: the client draws it, so nothing in it is
// markup. Every series has one point per label. Kind is "bar", "line", "pie",
// "scatter" or "stacked", and empty is "bar"; the client draws a kind it does
// not know as a bar. Metadata's keys are merged into the content, beneath the
// spec's own.
type ChartSpec struct {
	Kind     string
	Title    string
	Labels   []string
	Series   []ChartSeries
	Metadata map[string]any
}

// ChartSeries is one named line of points.
type ChartSeries struct {
	Label  string
	Points []float64
}

// PublishChart draws spec without sending its data to the model. id names the
// chart: the same id again redraws it in place, and empty draws a new one
// under "chart-<uuid>".
func PublishChart(ctx context.Context, id string, spec ChartSpec) (bool, error) {
	content, err := spec.content()
	if err != nil {
		return false, err
	}
	return publishActivity(ctx, ChartActivityType, id, content), nil
}

func (c ChartSpec) content() (map[string]any, error) {
	if len(c.Labels) == 0 {
		return nil, errors.New("aguiadk: a chart needs at least one label")
	}
	if len(c.Series) == 0 {
		return nil, errors.New("aguiadk: a chart needs at least one series")
	}
	if len(c.Labels) > MaxChartLabels {
		return nil, fmt.Errorf("aguiadk: a chart has at most %d labels, got %d", MaxChartLabels, len(c.Labels))
	}
	if n := len(c.Labels) * len(c.Series); n > MaxChartPoints {
		return nil, fmt.Errorf("aguiadk: a chart has at most %d points, got %d", MaxChartPoints, n)
	}
	series := make([]map[string]any, len(c.Series))
	for i, s := range c.Series {
		if len(s.Points) != len(c.Labels) {
			return nil, fmt.Errorf("aguiadk: series %q has %d points for %d labels", s.Label, len(s.Points), len(c.Labels))
		}
		for _, p := range s.Points {
			if math.IsNaN(p) || math.IsInf(p, 0) || math.Abs(p) > MaxChartMagnitude {
				return nil, fmt.Errorf("aguiadk: series %q has a point the client drops: %v", s.Label, p)
			}
		}
		series[i] = map[string]any{"label": s.Label, "points": s.Points}
	}
	content := maps.Clone(c.Metadata)
	if content == nil {
		content = map[string]any{}
	}
	delete(content, "title")
	kind := c.Kind
	if kind == "" {
		kind = "bar"
	}
	content["kind"], content["labels"], content["series"] = kind, c.Labels, series
	if c.Title != "" {
		content["title"] = c.Title
	}
	return content, nil
}

// PublishCompaction tells the page the conversation was compacted before a
// model call: before and after count its messages, and removed is the
// difference. Nothing is sent unless after is below before.
func PublishCompaction(ctx context.Context, before, after int) bool {
	if after >= before {
		return false
	}
	content := map[string]any{"removed": before - after, "before": before, "after": after}
	return publishActivity(ctx, CompactionActivityType, "", content)
}

func publishActivity(ctx context.Context, activityType, id string, content map[string]any) bool {
	s := sinkFrom(ctx)
	if s == nil {
		return false
	}
	if id == "" {
		id = activityType + "-" + newActivityID()
	}
	s.publish(ctx, aguix.ActivitySnapshot(id, activityType, content).WithReplace(true))
	return true
}

// newActivityID names an activity the caller did not; a test fixes it.
var newActivityID = newUUID

// newUUID is a random (version 4) UUID, which crypto/rand never fails to fill.
func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
