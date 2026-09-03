package httpx_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"

	services "github.com/Artui/go-services"
	"github.com/Artui/go-services/httpx"
)

type exampleDeps struct{ viewer string }

type exampleIn struct {
	ID    int64 `json:"id"`
	Limit int   `json:"limit,omitempty"`
}

type exampleOut struct {
	ID     int64  `json:"id"`
	Limit  int    `json:"limit"`
	Viewer string `json:"viewer"`
}

// Mount attaches a whole registry to a ServeMux in one call, and refuses at
// start-up anything that could only fail at request time.
func ExampleMount() {
	reg := services.New(func(_ context.Context, principal any) (exampleDeps, error) {
		name, ok := principal.(string)
		if !ok {
			return exampleDeps{}, services.ErrPermission
		}
		return exampleDeps{viewer: name}, nil
	})

	services.MustRegister(reg, services.Spec[exampleDeps, exampleIn, exampleOut]{
		Name: "get_author",
		Kind: services.Query,
		Run: func(c services.Ctx[exampleDeps], in exampleIn) (exampleOut, error) {
			return exampleOut{ID: in.ID, Limit: in.Limit, Viewer: c.Deps.viewer}, nil
		},
	})

	mux := http.NewServeMux()
	err := httpx.Mount(mux, reg, map[string]httpx.Route{
		"get_author": {Method: http.MethodGet, Pattern: "/authors/{id}"},
	}, func(r *http.Request) (any, error) {
		return r.Header.Get("X-Viewer"), nil
	})
	if err != nil {
		panic(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/authors/7?limit=3", nil)
	req.Header.Set("X-Viewer", "ada")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	fmt.Println(rec.Code, strings.TrimSpace(rec.Body.String()))
	// Output: 200 {"id":7,"limit":3,"viewer":"ada"}
}

// A Kind that cannot live on the method the route names is a configuration bug,
// so it is reported when the route is mounted rather than when it is called.
func ExampleMount_refusesAnUnsafeRoute() {
	reg := services.New[exampleDeps](nil)
	services.MustRegister(reg, services.Spec[exampleDeps, exampleIn, exampleOut]{
		Name: "delete_author",
		Kind: services.Mutation,
		Run: func(services.Ctx[exampleDeps], exampleIn) (exampleOut, error) {
			return exampleOut{}, nil
		},
	})

	err := httpx.Mount(http.NewServeMux(), reg, map[string]httpx.Route{
		"delete_author": {Method: http.MethodGet, Pattern: "/authors/{id}"},
	}, httpx.Anonymous)

	fmt.Println(err)
	// Output: httpx: "delete_author": services: a mutation changes state and cannot be mounted on GET; use POST, PUT, PATCH or DELETE
}
