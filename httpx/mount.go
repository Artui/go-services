package httpx

import (
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"

	services "github.com/Artui/go-services"
)

// Route says where and how one spec answers over HTTP.
//
// Method is its own field rather than part of Pattern, even though ServeMux
// takes the two as one string, because this adapter has to read it back: the
// spec's Kind decides which methods it may be mounted on, and that check is
// the point of declaring Kind in the first place.
type Route struct {
	// Method is the HTTP method. Required. It is trimmed and upper-cased
	// before use, so "post" and "POST" are the same route -- a lowercase
	// method reaching ServeMux would not be an error there, it would be a
	// route that never matches anything, which is worse.
	Method string

	// Pattern is the ServeMux pattern with the method left off:
	// "/authors/{id}", or "example.com/authors/{id}" to scope it to a host.
	//
	// Every {name} capture is bound to the input property of that name --
	// coerced to the type the schema declares, and taking precedence over both
	// the query string and the body. A capture the input declares no field for
	// is refused here, at Mount, because such a route is broken in every
	// request it could ever serve; the kernel refuses it again at dispatch, for
	// the handler that never went through Mount.
	Pattern string

	// Status overrides the success status the spec declared. Zero uses the
	// spec's own.
	Status int
}

// Mount attaches every named spec to mux.
//
// routes is keyed by spec name, so a registry holding more specs than routes is
// the normal case: the ones left out are still reachable over another
// transport. A spec that needs more than one route is mounted with Handler
// directly -- one key cannot hold two routes, and inventing a shape that could
// would make the common declaration read worse for the rarer case.
//
//	err := httpx.Mount(mux, reg, map[string]httpx.Route{
//	    "list_authors":  {Method: "GET", Pattern: "/authors"},
//	    "get_author":    {Method: "GET", Pattern: "/authors/{id}"},
//	    "create_author": {Method: "POST", Pattern: "/authors", Status: 201},
//	}, principal)
//
// Every problem in the table is reported, not just the first: the errors are
// joined and ordered by spec name, because a route table is usually wrong in
// more than one way at once and fixing it one restart at a time is the slowest
// possible loop.
//
// Nothing is registered unless the whole table passes. Patterns are proved
// against a throwaway ServeMux first, which is the only way to ask net/http
// whether it will accept one. The single case that cannot be pre-proved is a
// conflict with a route the caller had already put on mux itself: that is
// returned as an error too, but by then earlier routes from this table are
// mounted.
func Mount[D any](
	mux *http.ServeMux, reg *services.Registry[D], routes map[string]Route,
	principal Principal, opts ...Option,
) error {
	if len(routes) == 0 {
		return errors.New("httpx: a mount must name at least one route")
	}
	// Handler refuses a nil principal too, but saying it once for the mount is
	// clearer than repeating it for every route in the table.
	if principal == nil {
		return errors.New(
			"httpx: a principal is required; pass httpx.Anonymous to mount unauthenticated")
	}

	// Map iteration order is random, so the names are sorted: the same
	// misconfiguration must produce the same report in the same order every
	// run, or a fix gets chased around a map.
	names := slices.Sorted(maps.Keys(routes))

	type mounting struct {
		pattern string
		handler http.Handler
	}
	var (
		problems []error
		pending  []mounting
		// claimed maps a mounted pattern to the spec that took it, so a clash
		// can name both sides rather than only the second one.
		claimed = map[string]string{}
		// scratch proves a pattern is one ServeMux will accept, without
		// putting anything on the caller's mux until the whole table has
		// passed.
		scratch = http.NewServeMux()
	)

	for _, name := range names {
		route := routes[name]

		entry, ok := reg.Lookup(name)
		if !ok {
			problems = append(problems, fmt.Errorf("httpx: no spec named %q is registered", name))
			continue
		}
		if err := checkRoute(entry, route); err != nil {
			problems = append(problems, err)
			continue
		}

		pattern := normaliseMethod(route.Method) + " " + route.Pattern
		if owner, taken := claimed[pattern]; taken {
			problems = append(problems, fmt.Errorf(
				"httpx: %q and %q both claim %q", owner, name, pattern))
			continue
		}
		if err := handle(scratch, pattern, http.NotFoundHandler()); err != nil {
			problems = append(problems, fmt.Errorf("httpx: %q: %w", name, err))
			continue
		}
		claimed[pattern] = name

		// A fresh slice per route: appending to the caller's opts would let one
		// route's status leak into the next through a shared backing array.
		routeOpts := make([]Option, 0, len(opts)+1)
		routeOpts = append(routeOpts, opts...)
		if route.Status != 0 {
			routeOpts = append(routeOpts, WithStatus(route.Status))
		}

		h, err := Handler(reg, name, principal, routeOpts...)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		pending = append(pending, mounting{pattern: pattern, handler: h})
	}

	if len(problems) > 0 {
		return errors.Join(problems...)
	}

	for _, m := range pending {
		if err := handle(mux, m.pattern, m.handler); err != nil {
			return err
		}
	}
	return nil
}

// handle registers one pattern, turning ServeMux's panic into an error.
//
// ServeMux panics on a malformed pattern and on one that overlaps an
// already-registered route without either being more specific. Both are
// configuration bugs that Mount exists to report, and Mount reports
// configuration bugs by returning them. The recover is scoped to the single
// Handle call so that it cannot swallow anything else.
func handle(mux *http.ServeMux, pattern string, h http.Handler) (err error) {
	defer func() {
		if v := recover(); v != nil {
			err = fmt.Errorf("httpx: mounting %q: %v", pattern, v)
		}
	}()
	mux.Handle(pattern, h)
	return nil
}

// normaliseMethod puts a method in the form ServeMux matches on. The kernel's
// Kind.AllowsMethod does the same to what it is given, so the two cannot
// disagree about which method a route names.
func normaliseMethod(method string) string {
	return strings.ToUpper(strings.TrimSpace(method))
}

// checkRoute rejects a route that cannot work, or that would work dangerously.
//
// It is a checklist rather than a set of small predicates because that is what
// it is: each clause names one way a mount goes wrong, and reading them in one
// place is how a reviewer sees which ways are not covered.
func checkRoute(entry services.Entry, route Route) error {
	method := normaliseMethod(route.Method)
	if method == "" {
		return fmt.Errorf("httpx: %q: a route must name a method", entry.Name)
	}
	if route.Pattern == "" {
		return fmt.Errorf("httpx: %q: a route must have a pattern", entry.Name)
	}
	if strings.ContainsAny(route.Pattern, " \t") {
		// Almost always the method written into the pattern as well as into
		// the field, which ServeMux would reject with a message about the
		// pattern rather than about the duplication.
		return fmt.Errorf(
			"httpx: %q: pattern %q must not contain the method or any whitespace",
			entry.Name, route.Pattern,
		)
	}
	if route.Status != 0 && !validStatus(route.Status) {
		return fmt.Errorf(
			"httpx: %q: %d cannot be sent as a response status", entry.Name, route.Status,
		)
	}
	// The method rule is the kernel's, not this adapter's. It was an adapter's
	// once: two of them were given the same pair of prohibitions, and the pair
	// was incomplete in the same way in both. Kind states the whole rule now,
	// in a package that imports no transport at all.
	if err := entry.Kind.AllowsMethod(method); err != nil {
		return fmt.Errorf("httpx: %q: %w", entry.Name, err)
	}
	// Every capture the pattern names has to be one the operation can receive.
	// The kernel refuses an undeclared capture at dispatch as well, and the two
	// are not redundant: a route table naming a capture the input has no field
	// for is broken in every request it will ever serve, so refusing it at
	// start-up is strictly better than answering 500 forever. Only Mount has a
	// pattern to read the names out of, which is why the dispatch-time half
	// still has to exist for a Handler on somebody else's router.
	//
	// Pulling the names out of the pattern is this adapter's half of the job,
	// because path syntax is the one thing two HTTP adapters genuinely cannot
	// share: net/http writes {name} where Gin writes :name.
	//
	// CheckCaptures already names the spec, so this wraps without repeating it.
	if err := entry.CheckCaptures(wildcards(route.Pattern)...); err != nil {
		return fmt.Errorf("httpx: %w", err)
	}
	return nil
}
