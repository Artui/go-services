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
//
// Run `go mod tidy` with GOWORK=off. Inside the workspace it strips every
// require, because the workspace already supplies them -- and a replace with no
// require is an error the moment the workspace is gone, which is to say in CI.
replace github.com/Artui/go-services => ../

replace github.com/Artui/go-services/httpx => ../httpx

replace github.com/Artui/go-services/ginx => ../ginx

replace github.com/Artui/go-services/mcpx => ../mcpx

require (
	github.com/Artui/go-services v0.3.0
	github.com/Artui/go-services/ginx v0.0.0-00010101000000-000000000000
	github.com/Artui/go-services/httpx v0.0.0-00010101000000-000000000000
	github.com/Artui/go-services/mcpx v0.0.0-00010101000000-000000000000
	github.com/gin-gonic/gin v1.12.0
	github.com/modelcontextprotocol/go-sdk v1.7.0
)

require (
	github.com/bytedance/gopkg v0.1.3 // indirect
	github.com/bytedance/sonic v1.15.0 // indirect
	github.com/bytedance/sonic/loader v0.5.0 // indirect
	github.com/cloudwego/base64x v0.1.6 // indirect
	github.com/gabriel-vasile/mimetype v1.4.12 // indirect
	github.com/gin-contrib/sse v1.1.0 // indirect
	github.com/go-playground/locales v0.14.1 // indirect
	github.com/go-playground/universal-translator v0.18.1 // indirect
	github.com/go-playground/validator/v10 v10.30.1 // indirect
	github.com/goccy/go-json v0.10.5 // indirect
	github.com/goccy/go-yaml v1.19.2 // indirect
	github.com/google/jsonschema-go v0.4.3 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/klauspost/cpuid/v2 v2.3.0 // indirect
	github.com/leodido/go-urn v1.4.0 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.2 // indirect
	github.com/pelletier/go-toml/v2 v2.2.4 // indirect
	github.com/quic-go/qpack v0.6.0 // indirect
	github.com/quic-go/quic-go v0.62.0 // indirect
	github.com/segmentio/asm v1.1.3 // indirect
	github.com/segmentio/encoding v0.5.4 // indirect
	github.com/twitchyliquid64/golang-asm v0.15.1 // indirect
	github.com/ugorji/go/codec v1.3.1 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	go.mongodb.org/mongo-driver/v2 v2.5.0 // indirect
	golang.org/x/arch v0.22.0 // indirect
	golang.org/x/crypto v0.56.0 // indirect
	golang.org/x/net v0.58.0 // indirect
	golang.org/x/oauth2 v0.35.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	google.golang.org/protobuf v1.36.10 // indirect
)
