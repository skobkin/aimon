package resources

import (
	"image"
	"image/color"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/software"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"git.skobk.in/skobkin/aimonitor/internal/domain"
)

// fgTheme is the default theme with a chosen foreground color.
type fgTheme struct {
	fyne.Theme
	fg color.Color
}

func (t fgTheme) Color(n fyne.ThemeColorName, v fyne.ThemeVariant) color.Color {
	if n == theme.ColorNameForeground {
		return t.fg
	}

	return t.Theme.Color(n, v)
}

// render draws the logo in a 48px icon with the given foreground. A themed
// resource resolves its color through the app's current theme, so the theme
// is installed on the (test) app, not only handed to the renderer.
func render(app fyne.App, res fyne.Resource, fg color.Color) image.Image {
	th := fgTheme{Theme: theme.DefaultTheme(), fg: fg}
	app.Settings().SetTheme(th)
	icon := widget.NewIcon(res)
	icon.Resize(fyne.NewSquareSize(48))

	return software.Render(icon, th)
}

// mark reports how many pixels differ from the background (the pixel at
// the origin: icons leave their corners empty) and the most common of those
// colors.
func mark(img image.Image) (int, color.NRGBA) {
	b := img.Bounds()
	bg := color.NRGBAModel.Convert(img.At(b.Min.X, b.Min.Y)).(color.NRGBA)
	hist := map[color.NRGBA]int{}
	n := 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
			if c != bg {
				hist[c]++
				n++
			}
		}
	}
	var top color.NRGBA
	most := 0
	for c, k := range hist {
		if k > most {
			top, most = c, k
		}
	}

	return n, top
}

// near reports whether two colors agree per channel within the slack that
// antialiasing leaves on the most common painted pixel.
func near(a, b color.NRGBA) bool {
	d := func(x, y uint8) int {
		if x > y {
			return int(x - y)
		}

		return int(y - x)
	}

	return d(a.R, b.R) < 90 && d(a.G, b.G) < 90 && d(a.B, b.B) < 90
}

func TestRemoteKindsHaveRenderableLogos(t *testing.T) {
	app := test.NewTempApp(t)

	for _, kind := range []domain.SourceKind{
		domain.KindClaude, domain.KindCodex, domain.KindZAI,
		domain.KindMiniMax, domain.KindKimi, domain.KindOpenRouter,
	} {
		res := ProviderLogo(kind)
		if res == nil {
			t.Errorf("%s: no logo", kind)

			continue
		}
		if n, _ := mark(render(app, res, color.White)); n < 50 {
			t.Errorf("%s: logo paints %d pixels, want a visible mark", kind, n)
		}
	}
}

func TestLocalKindsHaveNoLogo(t *testing.T) {
	for _, kind := range []domain.SourceKind{domain.KindAMDGPUWeb, domain.KindLlamaSwap, domain.KindHindsight} {
		if ProviderLogo(kind) != nil {
			t.Errorf("%s: unexpected logo", kind)
		}
	}
}

func TestProviderLogoStableAcrossCalls(t *testing.T) {
	if ProviderLogo(domain.KindZAI) != ProviderLogo(domain.KindZAI) {
		t.Error("ProviderLogo must return the same resource instance")
	}
}

// Mono marks follow the theme foreground; the multi-color Claude mark does
// not change with it.
func TestMonoLogosFollowForeground(t *testing.T) {
	app := test.NewTempApp(t)

	// Two strongly different foregrounds, neither close to the background.
	light := color.NRGBA{R: 255, G: 0, B: 0, A: 255} // stands in for a light theme
	dark := color.NRGBA{R: 0, G: 0, B: 255, A: 255}

	for _, kind := range []domain.SourceKind{domain.KindCodex, domain.KindZAI, domain.KindKimi, domain.KindOpenRouter} {
		_, onLight := mark(render(app, ProviderLogo(kind), light))
		_, onDark := mark(render(app, ProviderLogo(kind), dark))
		if !near(onLight, light) || !near(onDark, dark) {
			t.Errorf("%s: mark colors = %v / %v, want the foregrounds %v / %v", kind, onLight, onDark, light, dark)
		}
	}

	_, a := mark(render(app, ProviderLogo(domain.KindClaude), light))
	_, b := mark(render(app, ProviderLogo(domain.KindClaude), dark))
	if a != b {
		t.Errorf("claude: original color changed with the theme (%v vs %v)", a, b)
	}
}
