module github.com/Artui/go-services/conformance

go 1.26.0

// This module is never published. It exists to hold one spec set against every
// adapter at once, so it necessarily depends on all of them, and it must build
// against the working tree rather than the last tag -- a suite that can only
// see released adapters cannot catch a divergence before it is released, which
// is the entire point of it.
//
// A replace in a publishable module is a trap, because Go ignores replace
// directives in dependencies and the module resolves for nobody else. That
// argument does not apply here: nothing depends on this and nothing ever will.
// verify-modules skips it for the same reason.
replace github.com/Artui/go-services => ../

replace github.com/Artui/go-services/httpx => ../httpx

replace github.com/Artui/go-services/ginx => ../ginx

replace github.com/Artui/go-services/mcpx => ../mcpx
