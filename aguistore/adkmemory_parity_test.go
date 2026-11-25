package aguistore

import (
	"context"
	"testing"

	"google.golang.org/adk/v2/memory"
	"google.golang.org/adk/v2/session"
)

func TestMemorySearchMatchesADKsInMemoryService(t *testing.T) {
	cases := []struct{ name, stored, query string }{
		{"a Japanese word inside a sentence", "私は東京に住んでいます", "東京"},
		{"a Latin word inside a Japanese one", "私はPythonを使う", "python"},
		{"the other script of a mixed word", "私はPythonを使う", "私は"},
		{"decomposed against composed", "Un café au lait.", "café"},
		{"composed against decomposed", "Un café au lait.", "café"},
		{"a capital beyond ASCII", "ÉCOLE du soir", "école"},
		{"a snake_case name whole", "call load_memory now", "load_memory"},
		{"half a snake_case name", "call load_memory now", "load"},
		{"an ASCII word is not a substring", "bookshelf", "book"},
		{"a non-ASCII word is a substring", "Straßenbahn", "straße"},
		{"no shared word", "owls in barns", "herons"},
	}
	ctx := context.Background()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := fakeSession{"app", "alice", "th1", []*session.Event{said("e1", "user", c.stored, 0)}}
			oracle := memory.InMemoryService()
			if err := oracle.AddSessionToMemory(ctx, s); err != nil {
				t.Fatal(err)
			}
			req := &memory.SearchRequest{AppName: "app", UserID: "alice", Query: c.query}
			want, err := oracle.SearchMemory(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			m := openMemory(t)
			remember(t, m, s)
			got, err := m.SearchMemory(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Memories) != len(want.Memories) {
				t.Errorf("%q against %q: ADK finds %d, aguistore %d", c.query, c.stored, len(want.Memories), len(got.Memories))
			}
		})
	}
}
