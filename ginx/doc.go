// Package ginx mounts a services.Registry onto a Gin router.
//
// It exists because a Gin application already has gin.Context, its middleware
// chain and its own route table, not because it needs different logic from
// package httpx. If this package grows large, that is evidence about the
// kernel rather than about Gin.
package ginx
