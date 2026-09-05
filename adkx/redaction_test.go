package adkx_test

import (
	"errors"
	"fmt"
	"testing"

	services "github.com/Artui/go-services"
	"github.com/Artui/go-services/adkx"
	"google.golang.org/adk/v2/agent"
)

// The reason this package redacts at all.
//
// ADK renders a returned error as map[string]any{"error": err.Error()} and
// hands it to the model. An adapter returning errors as it found them would put
// a connection string in front of something that will repeat it to a user or
// try to act on it.
func TestAnUnexpectedErrorIsRedactedAndReported(t *testing.T) {
	var (
		reportedTool string
		reported     error
	)
	ts := mustToolset(t, adkx.WithErrorReporter(
		func(_ agent.Context, name string, err error) { reportedTool, reported = name, err }))

	_, err := toolNamed(t, ts, "boom").Run(contextFor(t, "ada"), nil)
	if err == nil {
		t.Fatal("Run succeeded, want a failure")
	}
	if err.Error() != adkx.InternalErrorText {
		t.Errorf("the model was told %q, want the fixed sentence", err)
	}
	if contains(err.Error(), operatorText) {
		t.Error("the operator's words reached the model")
	}

	// Redacting with nowhere to send it would mean nobody ever sees the
	// failure, which is worse than the leak.
	if reportedTool != "boom" {
		t.Errorf("reported tool = %q, want boom", reportedTool)
	}
	if reported == nil || !contains(reported.Error(), operatorText) {
		t.Errorf("reporter got %v, want the real error", reported)
	}
}

// A toolset with no reporter still redacts. It also drops the failure entirely,
// which is a real cost and is why the option's documentation says so.
func TestRedactionWorksWithNoReporter(t *testing.T) {
	_, err := toolNamed(t, mustToolset(t), "boom").Run(contextFor(t, "ada"), nil)
	if err == nil || err.Error() != adkx.InternalErrorText {
		t.Errorf("err = %v, want the fixed sentence", err)
	}
}

// A refusal is not reported: it already told the model what happened, and a log
// where ordinary refusals outnumber real faults is a log nobody reads.
func TestARefusalIsNotReported(t *testing.T) {
	reported := false
	ts := mustToolset(t, adkx.WithErrorReporter(
		func(agent.Context, string, error) { reported = true }))

	_, err := toolNamed(t, ts, "borrow_book").
		Run(contextFor(t, "ada"), map[string]any{"book_id": 99})
	if !errors.Is(err, services.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if reported {
		t.Error("a mapped refusal reached the reporter")
	}
}

// A value the encoder cannot represent is this process's bug, not the model's.
func TestAnUnencodableValueIsRedacted(t *testing.T) {
	var reported error
	ts := mustToolset(t, adkx.WithErrorReporter(
		func(_ agent.Context, _ string, err error) { reported = err }))

	_, err := toolNamed(t, ts, "average").Run(contextFor(t, "ada"), nil)
	if err == nil || err.Error() != adkx.InternalErrorText {
		t.Errorf("err = %v, want the fixed sentence", err)
	}
	if reported == nil {
		t.Error("the encoding failure was not reported")
	}
}

// The shape that ended a process before the kernel and the MCP adapter were
// both hardened: a non-nil error holding a nil *ValidationError, which
// errors.As matches. Classified as a server bug rather than rendered as an
// empty list of argument problems, which would invite a retry that cannot work.
func TestATypedNilValidationErrorIsAServerBug(t *testing.T) {
	var reported error
	ts := mustToolset(t, adkx.WithErrorReporter(
		func(_ agent.Context, _ string, err error) { reported = err }))

	_, err := toolNamed(t, ts, "typed_nil").Run(contextFor(t, "ada"), nil)
	if err == nil || err.Error() != adkx.InternalErrorText {
		t.Errorf("err = %v, want the fixed sentence", err)
	}
	if reported == nil {
		t.Error("a typed-nil validation error was not reported as a bug")
	}
}

// The principal refuses before anything is dispatched.
func TestAnUnauthenticatedInvocationIsRefused(t *testing.T) {
	_, err := toolNamed(t, mustToolset(t), "ping").Run(contextFor(t, ""), nil)
	if !errors.Is(err, services.ErrPermission) {
		t.Fatalf("err = %v, want ErrPermission", err)
	}
	if !contains(err.Error(), "not signed in") {
		t.Errorf("err = %v, want the resolver's own words", err)
	}
}

func TestToolsetRefusesAMissingRegistryOrPrincipal(t *testing.T) {
	if _, err := adkx.Toolset[deps](nil, adkx.UserID); err == nil {
		t.Error("Toolset accepted a nil registry")
	}
	if _, err := adkx.Toolset(newRegistry(t), nil); err == nil {
		t.Error("Toolset accepted a nil principal")
	}
}

func TestToolsetNameIsConfigurable(t *testing.T) {
	if got := mustToolset(t).Name(); got != "services" {
		t.Errorf("Name = %q, want the default", got)
	}
	if got := mustToolset(t, adkx.WithName("library")).Name(); got != "library" {
		t.Errorf("Name = %q, want library", got)
	}
}

// Anonymous is what a toolset over a registry needing no identity says out
// loud. Here the registry does need one, so it refuses -- which is the point:
// authenticating nobody is a choice, not a default.
func TestAnonymousAuthenticatesNobody(t *testing.T) {
	ts, err := adkx.Toolset(newRegistry(t), adkx.Anonymous)
	if err != nil {
		t.Fatalf("Toolset: %v", err)
	}
	if _, err := toolNamed(t, ts, "ping").Run(contextFor(t, "ada"), nil); !errors.Is(
		err, services.ErrPermission) {
		t.Errorf("err = %v, want ErrPermission", err)
	}
}

// A principal function may refuse on its own, before the registry's resolver is
// reached at all -- an ADK invocation whose session carries no signed-in user,
// say. Its refusal is a refusal like any other and keeps its own words.
func TestAPrincipalMayRefuseTheInvocation(t *testing.T) {
	refusing := func(ctx agent.Context) (any, error) {
		if ctx.UserID() == "" {
			return nil, fmt.Errorf(
				"%w: this session has no signed-in user", services.ErrPermission)
		}
		return ctx.UserID(), nil
	}

	ts, err := adkx.Toolset(newRegistry(t), refusing)
	if err != nil {
		t.Fatalf("Toolset: %v", err)
	}

	_, err = toolNamed(t, ts, "ping").Run(contextFor(t, ""), nil)
	if !errors.Is(err, services.ErrPermission) {
		t.Fatalf("err = %v, want ErrPermission", err)
	}
	if !contains(err.Error(), "no signed-in user") {
		t.Errorf("err = %v, want the principal's own words", err)
	}
}
