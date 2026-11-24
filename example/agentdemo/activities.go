package main

import (
	"database/sql"
	"fmt"

	"github.com/Artui/go-services/aguiadk"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/agenttool"
	"google.golang.org/adk/v2/tool/functiontool"
	"google.golang.org/genai"
)

// The tools and the callback here are the page's, not the library's: each draws
// one of the activities the web component knows, so the demo shows them all.

const catalogueInstruction = `You look things up in the library's catalogue for the librarian.
Use the tools, then answer the request in a sentence or two.`

// keepContents is how much history a model call sees before older turns are cut.
const keepContents = 20

// ChartIn takes no arguments: the chart is always loans per book.
type ChartIn struct{}

// ChartOut is what the model hears: the chart itself goes only to the page.
type ChartOut struct {
	Books int    `json:"books"`
	Note  string `json:"note"`
}

// SuggestIn is the follow-ups the model offers.
type SuggestIn struct {
	Prompts []string `json:"prompts" jsonschema:"up to four follow-up messages, written as the member would type them"`
}

// SuggestOut says how many chips the page drew.
type SuggestOut struct {
	Shown int `json:"shown"`
}

func chartTool(db *sql.DB) (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name:        "chart_loans",
		Description: "Draws a bar chart of how many times each book has been borrowed, on the member's page.",
	}, func(ctx agent.Context, _ ChartIn) (ChartOut, error) {
		spec, err := loansPerBook(ctx, db)
		if err != nil {
			return ChartOut{}, err
		}
		if _, err := aguiadk.PublishChart(ctx, "chart-loans", spec); err != nil {
			return ChartOut{}, err
		}
		return ChartOut{Books: len(spec.Labels), Note: "the chart is on the member's page"}, nil
	})
}

func loansPerBook(ctx agent.Context, db *sql.DB) (aguiadk.ChartSpec, error) {
	rows, err := db.QueryContext(ctx, `SELECT b.title, COUNT(l.id) FROM books b
		LEFT JOIN loans l ON l.book_id = b.id GROUP BY b.id ORDER BY b.id LIMIT ?`, aguiadk.MaxChartLabels)
	if err != nil {
		return aguiadk.ChartSpec{}, fmt.Errorf("loans per book: %w", err)
	}
	defer func() { _ = rows.Close() }()
	series := aguiadk.ChartSeries{Label: "Loans"}
	spec := aguiadk.ChartSpec{Kind: "bar", Title: "Loans per book"}
	for rows.Next() {
		var title string
		var n float64
		if err := rows.Scan(&title, &n); err != nil {
			return aguiadk.ChartSpec{}, fmt.Errorf("loans per book: %w", err)
		}
		spec.Labels, series.Points = append(spec.Labels, title), append(series.Points, n)
	}
	spec.Series = []aguiadk.ChartSeries{series}
	return spec, rows.Err()
}

func suggestTool() (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name:        "suggest_follow_ups",
		Description: "Offers the member up to four follow-up messages as buttons under your answer.",
	}, func(ctx agent.Context, in SuggestIn) (SuggestOut, error) {
		if _, err := aguiadk.PublishSuggestions(ctx, "suggestions-librarian", in.Prompts...); err != nil {
			return SuggestOut{}, err
		}
		return SuggestOut{Shown: len(in.Prompts)}, nil
	})
}

func pageTools(llm model.LLM, specs tool.Toolset, db *sql.DB) ([]tool.Tool, error) {
	chart, err := chartTool(db)
	if err != nil {
		return nil, fmt.Errorf("chart: %w", err)
	}
	suggest, err := suggestTool()
	if err != nil {
		return nil, fmt.Errorf("suggestions: %w", err)
	}
	delegate, err := catalogue(llm, specs)
	if err != nil {
		return nil, err
	}
	return []tool.Tool{chart, suggest, delegate}, nil
}

// catalogue is a sub-agent the librarian delegates to, so its calls show as
// progress on the delegating call's card.
func catalogue(llm model.LLM, specs tool.Toolset) (tool.Tool, error) {
	a, err := llmagent.New(llmagent.Config{
		Name: "catalogue", Model: llm, Instruction: catalogueInstruction,
		Description: "Looks up books and loans in the catalogue and answers a question about them.",
		Toolsets:    []tool.Toolset{specs},
	})
	if err != nil {
		return nil, fmt.Errorf("catalogue: %w", err)
	}
	return agenttool.New(a, nil), nil
}

// compact cuts a long history down to its last turns, starting at a member's
// message so that no tool result loses its call, and tells the page.
func compact(ctx agent.Context, req *model.LLMRequest) (*model.LLMResponse, error) {
	before := len(req.Contents)
	cut := before - keepContents
	for cut > 0 && cut < before && !startsATurn(req.Contents[cut]) {
		cut++
	}
	if cut <= 0 || cut == before {
		return nil, nil
	}
	req.Contents = req.Contents[cut:]
	aguiadk.PublishCompaction(ctx, before, len(req.Contents))
	return nil, nil
}

func startsATurn(c *genai.Content) bool {
	if c.Role != genai.RoleUser {
		return false
	}
	for _, p := range c.Parts {
		if p.FunctionResponse != nil {
			return false
		}
	}
	return true
}
