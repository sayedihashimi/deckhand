package termshot

import (
	"image"
	"image/color"
	"testing"
)

func TestPaletted(t *testing.T) {
	// 300 colours: a background that fills most of the image, then one
	// pixel each of 299 others.
	img := image.NewRGBA(image.Rect(0, 0, 40, 40))
	bg := color.RGBA{0x1a, 0x1b, 0x26, 0xff}
	for y := 0; y < 40; y++ {
		for x := 0; x < 40; x++ {
			img.SetRGBA(x, y, bg)
		}
	}
	for i := 0; i < 299; i++ {
		img.SetRGBA(i%40, i/40, color.RGBA{uint8(i), uint8(i >> 8), 0x80, 0xff})
	}
	p := paletted(img)
	if len(p.Palette) != 256 {
		t.Fatalf("palette has %d colours, want 256", len(p.Palette))
	}
	if got := p.At(39, 39); got != bg {
		t.Errorf("background is %v, want %v", got, bg)
	}
	// The same image gives the same palette and pixels.
	q := paletted(img)
	for i := range p.Palette {
		if p.Palette[i] != q.Palette[i] {
			t.Fatalf("palette differs at %d: %v and %v", i, p.Palette[i], q.Palette[i])
		}
	}
	for i := range p.Pix {
		if p.Pix[i] != q.Pix[i] {
			t.Fatalf("pixel %d differs", i)
		}
	}
}
