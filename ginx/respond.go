package ginx

import (
	"errors"
	"net/http"

	"github.com/Artui/go-services"
	"github.com/gin-gonic/gin"
)

// errorBody is the envelope for a refusal the client is entitled to read: the
// kernel's ErrPermission, ErrNotFound and ErrConflict are answers about the
// request, and their text was written to be read by whoever sent it.
type errorBody struct {
	Error string `json:"error"`
}

// fieldsBody is the envelope for a validation failure. It carries
// ValidationError.Fields unchanged, keyed by the JSON name rather than the Go
// field name, because the key is going onto a wire the client speaks.
type fieldsBody struct {
	Errors map[string][]string `json:"errors"`
}

// fail answers a kernel error.
//
// The status is services.StatusFor's, not this package's. So are the three
// sentences a client is allowed to read for the answers that must not repeat
// the error's own words. Both used to be tables here, and both are things the
// other HTTP adapter needs to agree with exactly -- a status table copied into
// two adapters is two chances to copy it differently, and a client-visible
// sentence copied into two adapters is a contract with two owners.
//
// What is left is the part that really is the adapter's: which of the two
// envelope shapes an answer takes.
func fail(c *gin.Context, err error, onError func(*gin.Context, error)) {
	// Gin's own error channel, before any mapping. An application that already
	// runs a logging or reporting middleware reads c.Errors and now sees the
	// unredacted error without configuring anything here.
	_ = c.Error(err)

	status := services.StatusFor(err)

	var invalid *services.ValidationError
	switch {
	case errors.As(err, &invalid):
		// The one member of the taxonomy carrying structure, and the reason
		// this is a switch rather than a single line. StatusFor tests the same
		// thing first, so the two cannot disagree about which answer this is.
		//
		// FieldMap rather than Fields: &ValidationError{} is constructible, and
		// its nil map would put {"errors": null} on the wire, which a client
		// parsing errors as an object cannot read.
		c.AbortWithStatusJSON(status, fieldsBody{Errors: invalid.FieldMap()})
	case status == services.StatusBodyTooLarge:
		c.AbortWithStatusJSON(status, errorBody{Error: services.BodyTooLargeText})
	case status == http.StatusInternalServerError:
		// Called before the response is written, so a callback that wants to
		// put a correlation id on the response still can: after
		// AbortWithStatusJSON the header is already on the wire.
		if onError != nil {
			onError(c, err)
		}
		c.AbortWithStatusJSON(status, errorBody{Error: services.InternalErrorText})
	default:
		// 403, 404 and 409: refusals whose own words were written to be read by
		// whoever sent the request, so they travel unchanged.
		c.AbortWithStatusJSON(status, errorBody{Error: err.Error()})
	}
}
