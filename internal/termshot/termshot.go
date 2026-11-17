// Package termshot renders a terminal screen (a grid of styled cells) to a
// PNG, for screenshots in pull requests. Text is drawn with system fonts
// (Consolas, with Segoe UI Symbol for glyphs Consolas lacks, on Windows);
// box-drawing and block characters are drawn directly so lines join up.
package termshot

import (
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// Cell is one terminal cell.
type Cell struct {
	Text       string      // the grapheme; "" or " " for blank
	Width      int         // 0 for the second half of a wide character
	Fg, Bg     color.Color // nil for the default colour
	Bold       bool
	Faint      bool
	Reverse    bool
	Underlined bool
}

// Screen is a grid of cells, row by row.
type Screen [][]Cell

// Options control the rendering.
type Options struct {
	FontSize float64     // pixels; default 20
	Padding  int         // pixels around the screen; default 16
	Fg, Bg   color.Color // default colours; default a dark theme
	Fonts    []string    // regular fonts in fallback order; default system fonts
	Bold     []string    // bold fonts in fallback order
}

var (
	defaultFg = color.RGBA{0xc0, 0xca, 0xf5, 0xff}
	defaultBg = color.RGBA{0x1a, 0x1b, 0x26, 0xff}
)

func fontDir() string {
	if d := os.Getenv("WINDIR"); d != "" {
		return filepath.Join(d, "Fonts")
	}
	return `C:\Windows\Fonts`
}

// DefaultFonts are the regular and bold font files, in fallback order.
// TERMSHOT_FONTS (a ;-separated list) overrides the regular ones.
func DefaultFonts() (regular, bold []string) {
	d := fontDir()
	regular = []string{filepath.Join(d, "consola.ttf"), filepath.Join(d, "seguisym.ttf"), filepath.Join(d, "segoeui.ttf")}
	bold = []string{filepath.Join(d, "consolab.ttf"), filepath.Join(d, "seguisym.ttf"), filepath.Join(d, "segoeuib.ttf")}
	if env := os.Getenv("TERMSHOT_FONTS"); env != "" {
		regular = strings.Split(env, ";")
		bold = regular
	}
	return regular, bold
}

type faceSet struct {
	fonts []*opentype.Font
	faces []font.Face
}

func loadFaces(paths []string, size float64) (*faceSet, error) {
	fs := &faceSet{}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		f, err := opentype.Parse(data)
		if err != nil {
			continue
		}
		face, err := opentype.NewFace(f, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
		if err != nil {
			continue
		}
		fs.fonts = append(fs.fonts, f)
		fs.faces = append(fs.faces, face)
	}
	if len(fs.faces) == 0 {
		return nil, errors.New("termshot: no usable fonts in " + strings.Join(paths, ", "))
	}
	return fs, nil
}

// faceFor is the first face that has a glyph for r.
func (fs *faceSet) faceFor(r rune) font.Face {
	for _, f := range fs.faces {
		if _, ok := f.GlyphAdvance(r); ok {
			return f
		}
	}
	return fs.faces[0]
}

// Render writes the screen as a PNG.
func Render(w io.Writer, scr Screen, opt Options) error {
	img, err := RenderImage(scr, opt)
	if err != nil {
		return err
	}
	return png.Encode(w, img)
}

// RenderImage draws the screen.
func RenderImage(scr Screen, opt Options) (*image.RGBA, error) {
	if opt.FontSize == 0 {
		opt.FontSize = 20
	}
	if opt.Padding == 0 {
		opt.Padding = 16
	}
	if opt.Fg == nil {
		opt.Fg = defaultFg
	}
	if opt.Bg == nil {
		opt.Bg = defaultBg
	}
	reg, bold := DefaultFonts()
	if opt.Fonts != nil {
		reg = opt.Fonts
	}
	if opt.Bold != nil {
		bold = opt.Bold
	}
	regular, err := loadFaces(reg, opt.FontSize)
	if err != nil {
		return nil, err
	}
	bolds, err := loadFaces(bold, opt.FontSize)
	if err != nil {
		bolds = regular
	}

	// Cell size from the primary font.
	m := regular.faces[0].Metrics()
	adv, _ := regular.faces[0].GlyphAdvance('M')
	cw := adv.Ceil()
	ch := (m.Ascent + m.Descent).Ceil() + 2
	ascent := m.Ascent.Ceil() + 1

	rows := len(scr)
	cols := 0
	for _, r := range scr {
		if len(r) > cols {
			cols = len(r)
		}
	}
	W, H := cols*cw+2*opt.Padding, rows*ch+2*opt.Padding
	img := image.NewRGBA(image.Rect(0, 0, W, H))
	draw.Draw(img, img.Bounds(), image.NewUniform(opt.Bg), image.Point{}, draw.Src)

	for y, row := range scr {
		for x, c := range row {
			if c.Width == 0 && c.Text == "" && x > 0 {
				continue // second half of a wide character
			}
			fg, bg := c.Fg, c.Bg
			if fg == nil {
				fg = opt.Fg
			}
			if c.Reverse {
				if bg == nil {
					bg = opt.Bg
				}
				fg, bg = bg, fg
			}
			if c.Faint {
				fg = blend(fg, opt.Bg, 0.5)
			}
			wcells := c.Width
			if wcells < 1 {
				wcells = 1
			}
			rect := image.Rect(opt.Padding+x*cw, opt.Padding+y*ch, opt.Padding+(x+wcells)*cw, opt.Padding+(y+1)*ch)
			if bg != nil {
				draw.Draw(img, rect, image.NewUniform(bg), image.Point{}, draw.Src)
			}
			text := c.Text
			if text == "" || text == " " {
				continue
			}
			r := []rune(text)[0]
			if drawBox(img, rect, r, fg) {
				continue
			}
			fs := regular
			if c.Bold {
				fs = bolds
			}
			face := fs.faceFor(r)
			d := &font.Drawer{Dst: img, Src: image.NewUniform(fg), Face: face}
			// Centre narrower fallback glyphs in their cell.
			gadv := d.MeasureString(text)
			off := (fixed.I(rect.Dx()) - gadv) / 2
			if off < 0 {
				off = 0
			}
			d.Dot = fixed.Point26_6{X: fixed.I(rect.Min.X) + off, Y: fixed.I(rect.Min.Y + ascent)}
			d.DrawString(text)
			if c.Underlined {
				draw.Draw(img, image.Rect(rect.Min.X, rect.Max.Y-2, rect.Max.X, rect.Max.Y-1), image.NewUniform(fg), image.Point{}, draw.Src)
			}
		}
	}
	return img, nil
}

func blend(a, b color.Color, t float64) color.Color {
	ar, ag, ab, _ := a.RGBA()
	br, bg, bb, _ := b.RGBA()
	mix := func(x, y uint32) uint8 { return uint8((float64(x)*(1-t) + float64(y)*t) / 257) }
	return color.RGBA{mix(ar, br), mix(ag, bg), mix(ab, bb), 0xff}
}

// drawBox draws light box-drawing lines and block elements so they fill
// the cell exactly. It reports false for other characters.
func drawBox(img *image.RGBA, r image.Rectangle, ch rune, c color.Color) bool {
	u := image.NewUniform(c)
	fill := func(x0, y0, x1, y1 int) { draw.Draw(img, image.Rect(x0, y0, x1, y1), u, image.Point{}, draw.Src) }
	mx, my := (r.Min.X+r.Max.X)/2, (r.Min.Y+r.Max.Y)/2
	const t = 1 // line thickness
	left := func() { fill(r.Min.X, my, mx+t, my+t) }
	right := func() { fill(mx, my, r.Max.X, my+t) }
	up := func() { fill(mx, r.Min.Y, mx+t, my+t) }
	down := func() { fill(mx, my, mx+t, r.Max.Y) }
	switch ch {
	case '─':
		left()
		right()
	case '│':
		up()
		down()
	case '┌':
		right()
		down()
	case '┐':
		left()
		down()
	case '└':
		right()
		up()
	case '┘':
		left()
		up()
	case '├':
		up()
		down()
		right()
	case '┤':
		up()
		down()
		left()
	case '┬':
		left()
		right()
		down()
	case '┴':
		left()
		right()
		up()
	case '┼':
		left()
		right()
		up()
		down()
	case '█':
		fill(r.Min.X, r.Min.Y, r.Max.X, r.Max.Y)
	case '▌':
		fill(r.Min.X, r.Min.Y, mx, r.Max.Y)
	case '▐':
		fill(mx, r.Min.Y, r.Max.X, r.Max.Y)
	case '▀':
		fill(r.Min.X, r.Min.Y, r.Max.X, my)
	case '▄':
		fill(r.Min.X, my, r.Max.X, r.Max.Y)
	default:
		return false
	}
	return true
}
