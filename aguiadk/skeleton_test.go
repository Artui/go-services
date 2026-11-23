package aguiadk

import (
	services "github.com/Artui/go-services"
	"github.com/Artui/go-services/adkx"
	"google.golang.org/adk/v2/model/anthropicmodel"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/tool"
)

var (
	_ = services.New[struct{}]
	_ = adkx.Toolset[struct{}]
	_ = anthropicmodel.NewModel
	_ = runner.New
	_ tool.Tool
)
