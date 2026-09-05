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
	// segment, "*rest" captures the remainder. A catch-all's value reaches the
	// operation without the leading slash Gin matched it from, so that both
	// forms deliver the same string and so that this agrees with net/http's
	// "{rest...}".
	//
	// A name used twice in one pattern is refused: Gin binds both, and the two
	// readers of that capture would not agree on which value won.
	//
	// A capture's name is matched against the input schema's properties, so
	// ":id" fills the field whose JSON name is "id". A capture naming a field
	// the operation does not declare is refused by Mount, because a route that
	// scopes an operation the spec cannot read is broken in every request it
	// will ever serve.
	Path string

	// Location sets a Location header on a successful response, from a template
	// naming fields of the operation's output: "/loans/{loan_id}". Empty sends
	// no header.
	//
	// The syntax is {name} and not the ":name" this route's Path uses. They are
	// unrelated: a capture is matched out of the request path, and this is
	// filled from the response value. It is checked against the operation's
	// output schema at Mount.
	Location string

	// Status overrides the spec's declared success status for this route. Zero
	// means the spec's own, which is the right answer unless one operation is
	// genuinely reachable two ways with different HTTP semantics.
	//
	// It must be one services.ValidSuccessStatus accepts, 200 to 599. A 1xx
	// never commits, so a route asking for one would serve an implicit 200 with
	// an empty body instead.
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
		names := captureNames(route.Path)
		if repeated := firstRepeat(names); repeated != "" {
			// Gin binds both, c.Param returns the first and the payload takes
			// the last, so a middleware authorising on c.Param and the
			// operation reading its own field would disagree about which value
			// is in force. Nothing downstream can tell that happened.
			problems = append(problems, fmt.Errorf(
				"ginx: %q captures %q twice in %s, and the two would not agree on a value",
				name, repeated, route.Path))
			continue
		}
		if err := entry.CheckCaptures(names...); err != nil {
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

		// A fresh slice per route, so that appending a route's own overrides
		// cannot write into the caller's backing array and leak into every
		// route mounted after this one. Clipping would do for a single
		// override; with two, always allocating is the version that stays
		// obviously correct, and it is what the net/http adapter does.
		routeOpts := make([]Option, 0, len(opts)+2)
		routeOpts = append(routeOpts, opts...)
		if route.Status != 0 {
			routeOpts = append(routeOpts, WithStatus(route.Status))
		}
		if route.Location != "" {
			routeOpts = append(routeOpts, WithLocation(route.Location))
		}
		handler, err := Handler(reg, name, principal, routeOpts...)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		planned = append(planned, plannedRoute{method: method, path: route.Path, handler: handler})
	}

	// Gin's router has path rules this package deliberately does not restate --
	// an unnamed wildcard, a catch-all that is not last, a path that normalises
	// onto one already taken -- and it enforces them by panicking from inside
	// Handle. A panic is a bad way to report configuration: it names one fault
	// where the joined error names all of them, and it happens midway through
	// registration, leaving behind exactly the half-mounted router this
	// function promises never to produce.
	//
	// So the table is offered to a throwaway engine first. Whatever Gin objects
	// to becomes an ordinary problem in the report, generically, without this
	// package having to keep a copy of Gin's routing rules in step with Gin.
	//
	// Only when the table is otherwise sound: a plan with rows missing is not
	// the table the caller wrote, so Gin's opinion of it would not be about
	// their table either.
	if len(problems) == 0 {
		if err := rehearse(planned); err != nil {
			problems = append(problems, err)
		}
	}

	if len(problems) > 0 {
		return errors.Join(problems...)
	}
	for _, p := range planned {
		r.Handle(p.method, p.path, p.handler)
	}
	return nil
}

// rehearse registers the plan on a throwaway engine, turning any panic from
// Gin's router into an error.
//
// The cost is that Gin's debug mode prints each route twice, once for the
// rehearsal and once for the real thing. Every lever that would silence it --
// the mode, DefaultWriter, DebugPrintRouteFunc -- is a package-level global,
// and reaching for one of those to tidy up startup output would be a far worse
// trade than a duplicated line.
func rehearse(planned []plannedRoute) (err error) {
	defer func() {
		if fault := recover(); fault != nil {
			err = fmt.Errorf("ginx: Gin rejected the route table: %v", fault)
		}
	}()
	scratch := gin.New()
	for _, p := range planned {
		scratch.Handle(p.method, p.path, p.handler)
	}
	return nil
}

// firstRepeat returns the first name that appears more than once, or "".
func firstRepeat(names []string) string {
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if seen[name] {
			return name
		}
		seen[name] = true
	}
	return ""
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
