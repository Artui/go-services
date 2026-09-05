module github.com/Artui/go-services/conformance

go 1.26.6

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

replace github.com/Artui/go-services/adkx => ../adkx

require (
	github.com/Artui/go-services v0.5.0
	github.com/Artui/go-services/adkx v0.0.0-20260905112458-1746f3264ed4
	github.com/Artui/go-services/ginx v0.0.0-00010101000000-000000000000
	github.com/Artui/go-services/httpx v0.0.0-00010101000000-000000000000
	github.com/Artui/go-services/mcpx v0.0.0-00010101000000-000000000000
	github.com/gin-gonic/gin v1.12.0
	github.com/modelcontextprotocol/go-sdk v1.7.0
	google.golang.org/adk/v2 v2.3.0
	google.golang.org/genai v1.71.0
)

require (
	cloud.google.com/go v0.123.0 // indirect
	cloud.google.com/go/auth v0.23.0 // indirect
	cloud.google.com/go/compute/metadata v0.9.0 // indirect
	github.com/bytedance/gopkg v0.1.3 // indirect
	github.com/bytedance/sonic v1.15.0 // indirect
	github.com/bytedance/sonic/loader v0.5.0 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/cloudwego/base64x v0.1.6 // indirect
	github.com/felixge/httpsnoop v1.0.4 // indirect
	github.com/gabriel-vasile/mimetype v1.4.12 // indirect
	github.com/gin-contrib/sse v1.1.0 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/go-playground/locales v0.14.1 // indirect
	github.com/go-playground/universal-translator v0.18.1 // indirect
	github.com/go-playground/validator/v10 v10.30.1 // indirect
	github.com/goccy/go-json v0.10.5 // indirect
	github.com/goccy/go-yaml v1.19.2 // indirect
	github.com/google/go-cmp v0.7.0 // indirect
	github.com/google/jsonschema-go v0.4.3 // indirect
	github.com/google/s2a-go v0.1.9 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/googleapis/enterprise-certificate-proxy v0.3.20 // indirect
	github.com/googleapis/gax-go/v2 v2.23.0 // indirect
	github.com/gorilla/websocket v1.5.3 // indirect
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
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.68.0 // indirect
	go.opentelemetry.io/otel v1.45.0 // indirect
	go.opentelemetry.io/otel/log v0.21.0 // indirect
	go.opentelemetry.io/otel/metric v1.45.0 // indirect
	go.opentelemetry.io/otel/trace v1.45.0 // indirect
	golang.org/x/arch v0.22.0 // indirect
	golang.org/x/crypto v0.56.0 // indirect
	golang.org/x/net v0.58.0 // indirect
	golang.org/x/oauth2 v0.36.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	google.golang.org/api v0.293.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260807164820-c8921c73eeea // indirect
	google.golang.org/grpc v1.83.1 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
	rsc.io/omap v1.2.0 // indirect
	rsc.io/ordered v1.1.1 // indirect
)
