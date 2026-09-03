package ginx

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/Artui/go-services"
	"github.com/gin-gonic/gin"
)

// Route says where one spec is served.
type Route struct {
	// Method is the HTTP method, in any case: "get" and "GET" both work.
	Method string

	// Path is the route pattern in Gin's own syntax -- ":id" captures one
	// segment, "*rest" captures the remainder.
	//
	// A capture's name is matched against the input schema's properties, so
	// ":id" fills the field whose JSON name is "id". A capture naming a field
	// the operation does not declare is refused by Mount, because a route that
	// scopes an operation the spec cannot read is broken in every request it
	// will ever serve.
	Path string

	// Status overrides the spec's declared success status for this route. Zero
	// means the spec's own, which is the right answer unless one operation is
	// genuinely reachable two ways with different HTTP semantics.
	Status int
}

// plannedRoute is a route that passed every check, held until the whole table
// has passed. See Mount for why nothing registers until then.
type plannedRoute struct {
	method  string
	path    string
	handler gin.HandlerFunc
}

// Mount registers one route per named spec on r.
//
// routes is keyed by spec name; a name the registry does not know is an error,
// and so is a method that contradicts the spec's Kind or a capture naming a
// field the operation cannot receive. Nothing is registered
// unless every entry in the table checks out, so a rejected Mount leaves the
// router exactly as it found it -- a half-mounted router is the failure mode
// where an application starts, serves most of its API, and 404s the rest.
//
// Every problem in the table is reported, joined, rather than only the first:
// this runs once at startup over a table a human wrote, and finding the
// mistakes one restart at a time is a poor trade for four lines of code.
//
// Mount does not defend against Gin's own route conflicts. Two patterns that
// disagree about a wildcard ("/authors/:id" beside "/authors/:name") are
// refused by Gin's tree with a panic, which no error return here can turn into
// a value; exact duplicates, which are the common typo, are caught below.
func Mount[D any](
	r gin.IRouter, reg *services.Registry[D], routes map[string]Route,
	principal PrincipalFunc, opts ...Option,
) error {
	if r == nil {
		return errors.New("ginx: Mount needs a router")
	}
	if reg == nil {
		return errors.New("ginx: Mount needs a registry")
	}
	if principal == nil {
		return errors.New(
			"ginx: Mount needs a principal function; pass ginx.Anonymous to authenticate nobody")
	}
	if len(routes) == 0 {
		return errors.New("ginx: Mount was given no routes")
	}
	// Checked once here rather than repeated by Handler for every row of the
	// table, which would report the same broken option N times.
	if _, err := newConfig(opts); err != nil {
		return err
	}

	// Sorted, so that both the joined error and the order routes reach Gin are
	// the same on every run. Go's map order is not, and a configuration error
	// that reads differently each time is one nobody can diff.
	names := make([]string, 0, len(routes))
	for name := range routes {
		names = append(names, name)
	}
	slices.Sort(names)

	planned := make([]plannedRoute, 0, len(names))
	claimed := map[string]string{} // "METHOD /path" -> the spec that claimed it
	var problems []error

	for _, name := range names {
		route := routes[name]

		entry, ok := reg.Lookup(name)
		if !ok {
			problems = append(problems, fmt.Errorf("ginx: no spec named %q is registered", name))
			continue
		}
		// Upper-cased for Gin's sake rather than for the check's: Handle panics
		// on a method that is not all capitals, while AllowsMethod is
		// case-insensitive and would have taken "get" as it was written.
		method := strings.ToUpper(strings.TrimSpace(route.Method))

		// The whole method rule, and it lives in the kernel. Both HTTP adapters
		// ask the same question of the same Kind, so a method this package
		// invented an opinion about would be a method the other one disagreed
		// with. Anything AllowsMethod accepts is one of the seven Gin's router
		// takes, so there is no separate list of methods to keep in step.
		if err := entry.Kind.AllowsMethod(method); err != nil {
			problems = append(problems, fmt.Errorf("ginx: %q: %w", name, err))
			continue
		}
		if route.Path == "" {
			// Gin would quietly mount this on the group's own base path, which
			// is a route nobody wrote down.
			problems = append(problems, fmt.Errorf("ginx: %q has no path", name))
			continue
		}

		// The route's captures against the operation's own fields. Reading the
		// names out of the pattern is this package's half -- Gin writes ":id"
		// where net/http writes "{id}" -- and judging them is the kernel's, so
		// two adapters cannot end up refusing different tables.
		//
		// Dispatch refuses an undeclared capture as well, and still has to: a
		// Handler placed on somebody else's router has no pattern for anything
		// here to read. This is the same guarantee moved to startup, where a
		// route that cannot work once belongs.
		if err := entry.CheckCaptures(captureNames(route.Path)...); err != nil {
			// The kernel's message already names the spec.
			problems = append(problems, fmt.Errorf("ginx: %w", err))
			continue
		}

		claim := method + " " + route.Path
		if prior, taken := claimed[claim]; taken {
			problems = append(problems, fmt.Errorf(
				"ginx: %q and %q both claim %s", prior, name, claim))
			continue
		}
		claimed[claim] = name

		// Clip so that appending the route's own override allocates rather than
		// writing into the caller's backing array, where it would leak into
		// every route mounted after this one.
		routeOpts := opts
		if route.Status != 0 {
			routeOpts = append(slices.Clip(opts), WithStatus(route.Status))
		}
		handler, err := Handler(reg, name, principal, routeOpts...)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		planned = append(planned, plannedRoute{method: method, path: route.Path, handler: handler})
	}

	if len(problems) > 0 {
		return errors.Join(problems...)
	}
	for _, p := range planned {
		r.Handle(p.method, p.path, p.handler)
	}
	return nil
}

// captureNames returns the parameter names Gin will bind from a route pattern:
// ":id" and "*rest" both name a capture, and everything else is a literal
// segment.
//
// It reads a wildcard only where Gin's own documentation puts one, at the start
// of a segment. Gin's router will also bind one mid-segment ("/a/b:c"), which
// nothing writes on purpose and this does not look for. That direction of
// inaccuracy is the safe one: a capture missed here is still refused by the
// kernel on the first request, whereas a capture invented here would refuse a
// route table that works.
func captureNames(path string) []string {
	var names []string
	for _, segment := range strings.Split(path, "/") {
		// Length two at minimum: ":" alone names nothing, and Gin panics on it
		// rather than binding an empty parameter.
		if len(segment) > 1 && (segment[0] == ':' || segment[0] == '*') {
			names = append(names, segment[1:])
		}
	}
	return names
}
