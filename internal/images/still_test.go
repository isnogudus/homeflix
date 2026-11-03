package images

import (
	"image"
	"image/color"
	"math"
	"testing"
)

func TestPQToNits(t *testing.T) {
	// Reference points of SMPTE ST 2084.
	for _, tc := range []struct{ signal, nits float64 }{
		{0, 0}, {0.5081, 100}, {0.7518, 1000}, {1, 10000},
	} {
		if got := pqToNits(tc.signal); math.Abs(got-tc.nits) > tc.nits*0.01+0.01 {
			t.Errorf("pqToNits(%v) = %v, want %v", tc.signal, got, tc.nits)
		}
	}
}

func TestToneMapPQ(t *testing.T) {
	src := image.NewRGBA64(image.Rect(0, 0, 4, 1))
	level := func(nitsSignal float64) color.RGBA64 {
		v := uint16(nitsSignal * 65535)
		return color.RGBA64{v, v, v, 65535}
	}
	src.SetRGBA64(0, 0, level(0))      // black
	src.SetRGBA64(1, 0, level(0.5081)) // 100 cd/m², about diffuse white
	src.SetRGBA64(2, 0, level(0.7518)) // 1000 cd/m², the assumed peak
	src.SetRGBA64(3, 0, level(1))      // 10000 cd/m², clipped
	out := toneMapPQ(src)
	g := func(x int) uint8 { return out.RGBAAt(x, 0).G }
	if g(0) != 0 || g(2) < 245 || g(3) != 255 {
		t.Errorf("levels %d %d %d %d", g(0), g(1), g(2), g(3))
	}
	if g(1) < 120 || g(1) > 200 {
		t.Errorf("100 cd/m² maps to %d, want a mid-to-bright grey", g(1))
	}
	if !(g(0) < g(1) && g(1) < g(2)) {
		t.Error("tone curve is not monotonic")
	}
}

func TestTrimBars(t *testing.T) {
	letterbox := frame(90, 128)
	if b := trimBars(letterbox).Bounds(); b.Dy() != 540 || b.Dx() != 1280 {
		t.Errorf("letterbox trimmed to %v", b)
	}
	pillar := image.NewRGBA(image.Rect(0, 0, 1280, 720))
	for y := 0; y < 720; y++ {
		for x := 160; x < 1120; x++ {
			pillar.SetRGBA(x, y, color.RGBA{200, 120, 50, 255})
		}
	}
	if b := trimBars(pillar).Bounds(); b.Dx() != 960 || b.Dy() != 720 {
		t.Errorf("pillarbox trimmed to %v", b)
	}
	black := image.NewRGBA(image.Rect(0, 0, 1000, 1000))
	if b := trimBars(black).Bounds(); b.Dx() < 400 || b.Dy() < 400 {
		t.Errorf("black frame trimmed beyond 30 %% per side: %v", b)
	}
}

func TestScorePrefersContrast(t *testing.T) {
	dark := frame(0, 5)
	for y := 0; y < 720; y++ {
		for x := 0; x < 1280; x++ {
			dark.SetRGBA(x, y, color.RGBA{uint8(x / 60), 5, uint8(y / 60), 255})
		}
	}
	normal := frame(0, 128)
	if score(normal) <= score(dark) {
		t.Errorf("score(normal) %v <= score(dark) %v", score(normal), score(dark))
	}
}
