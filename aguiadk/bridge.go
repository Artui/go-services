package aguiadk

import (
	"errors"

	"github.com/Artui/go-services/aguix"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

var (
	_ *session.Event
	_ *genai.Content
)

// New is a placeholder until the bridge lands.
func New(root agent.Agent) (aguix.Agent, error) {
	return nil, errors.New("aguiadk: not implemented")
}
