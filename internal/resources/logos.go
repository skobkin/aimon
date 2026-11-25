package resources

import (
	"embed"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"

	"git.skobk.in/skobkin/aimonitor/internal/domain"
)

// logoFiles embeds the provider marks; see logos/NOTICE for their origin
// and license.
//
//go:embed logos/*.svg
var logoFiles embed.FS

// logoSpec names one provider mark and whether it is a one-color glyph
// (tinted with the theme's foreground so it stays legible in light and dark
// themes) or a multi-color original that is drawn as is.
type logoSpec struct {
	file string
	mono bool
}

// providerLogos maps the remote source kinds onto their marks. Local kinds
// (GPU, llama-swap, Hindsight) have none: they are not third-party
// services.
var providerLogos = map[domain.SourceKind]logoSpec{
	domain.KindClaude:     {file: "claude.svg"},
	domain.KindCodex:      {file: "openai.svg", mono: true},
	domain.KindZAI:        {file: "zai.svg", mono: true},
	domain.KindMiniMax:    {file: "minimax.svg"},
	domain.KindKimi:       {file: "kimi.svg", mono: true},
	domain.KindOpenRouter: {file: "openrouter.svg", mono: true},
}

// providerLogoCache holds the resources, built once at first use.
var providerLogoCache = buildProviderLogos()

func buildProviderLogos() map[domain.SourceKind]fyne.Resource {
	out := make(map[domain.SourceKind]fyne.Resource, len(providerLogos))
	for kind, spec := range providerLogos {
		data, err := logoFiles.ReadFile("logos/" + spec.file)
		if err != nil {
			continue // unreachable: the pattern above embeds every listed file
		}
		var res fyne.Resource = &fyne.StaticResource{StaticName: spec.file, StaticContent: data}
		if spec.mono {
			res = theme.NewThemedResource(res)
		}
		out[kind] = res
	}

	return out
}

// ProviderLogo returns the provider mark of a source kind, or nil when the
// kind has none. Mono marks follow the theme's foreground color.
func ProviderLogo(kind domain.SourceKind) fyne.Resource { return providerLogoCache[kind] }
