package images

import (
	"image"
	"image/color"
	"math"
)

// The PQ (SMPTE ST 2084) constants.
const (
	pqM1 = 2610.0 / 16384
	pqM2 = 2523.0 / 4096 * 128
	pqC1 = 3424.0 / 4096
	pqC2 = 2413.0 / 4096 * 32
	pqC3 = 2392.0 / 4096 * 32
)

// pqToNits decodes a PQ signal value (0–1) to luminance in cd/m².
func pqToNits(e float64) float64 {
	p := math.Pow(e, 1/pqM2)
	return 10000 * math.Pow(math.Max(p-pqC1, 0)/(pqC2-pqC3*p), 1/pqM1)
}

// bt2020To709 converts linear BT.2020 RGB to linear BT.709 RGB.
var bt2020To709 = [3][3]float64{
	{1.6605, -0.5876, -0.0728},
	{-0.1246, 1.1329, -0.0083},
	{-0.0182, -0.1006, 1.1187},
}

// sdrWhite is the luminance mapped to SDR white before compression, and
// hdrPeak the luminance that ends up at full white.
const (
	sdrWhite = 120.0
	hdrPeak  = 1000.0
)

func srgbEncode(v float64) float64 {
	if v <= 0.0031308 {
		return 12.92 * v
	}
	return 1.055*math.Pow(v, 1/2.4) - 0.055
}

// toneMapPQ turns a PQ/BT.2020 frame, decoded to RGB without any range
// conversion, into an SDR sRGB image. Luminance is compressed with the
// extended Reinhard curve, so hues stay as they are.
func toneMapPQ(src image.Image) *image.RGBA {
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	// Signal values repeat a lot; cache the expensive PQ decode.
	var lut [65536]float64
	for i := range lut {
		lut[i] = pqToNits(float64(i)/65535) / sdrWhite
	}
	w := hdrPeak / sdrWhite
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r16, g16, b16, _ := src.At(x, y).RGBA()
			in := [3]float64{lut[r16], lut[g16], lut[b16]}
			var lin [3]float64
			for i := range 3 {
				lin[i] = math.Max(0, bt2020To709[i][0]*in[0]+bt2020To709[i][1]*in[1]+bt2020To709[i][2]*in[2])
			}
			lum := 0.2126*lin[0] + 0.7152*lin[1] + 0.0722*lin[2]
			scale := 0.0
			if lum > 0 {
				mapped := lum * (1 + lum/(w*w)) / (1 + lum)
				scale = mapped / lum
			}
			var out [3]uint8
			for i := range 3 {
				out[i] = uint8(math.Round(255 * math.Min(1, srgbEncode(lin[i]*scale))))
			}
			dst.SetRGBA(x-b.Min.X, y-b.Min.Y, color.RGBA{out[0], out[1], out[2], 255})
		}
	}
	return dst
}

func luma(c color.Color) float64 {
	r, g, b, _ := c.RGBA()
	return (0.2126*float64(r) + 0.7152*float64(g) + 0.0722*float64(b)) / 257
}

// barThreshold is the brightest a row or column of a black bar may be,
// on a 0–255 scale.
const barThreshold = 24

// trimBars removes black bars (letterbox and pillarbox), never more than
// 30 % per side.
func trimBars(img image.Image) image.Image {
	b := img.Bounds()
	dark := func(x0, y0, x1, y1 int) bool {
		stepX, stepY := max(1, (x1-x0)/64), max(1, (y1-y0)/64)
		for y := y0; y < y1; y += stepY {
			for x := x0; x < x1; x += stepX {
				if luma(img.At(x, y)) > barThreshold {
					return false
				}
			}
		}
		return true
	}
	top, bottom, left, right := b.Min.Y, b.Max.Y, b.Min.X, b.Max.X
	maxY, maxX := b.Dy()*3/10, b.Dx()*3/10
	for top-b.Min.Y < maxY && dark(left, top, right, top+1) {
		top++
	}
	for b.Max.Y-bottom < maxY && dark(left, bottom-1, right, bottom) {
		bottom--
	}
	for left-b.Min.X < maxX && dark(left, top, left+1, bottom) {
		left++
	}
	for b.Max.X-right < maxX && dark(right-1, top, right, bottom) {
		right--
	}
	if top == b.Min.Y && bottom == b.Max.Y && left == b.Min.X && right == b.Max.X {
		return img
	}
	r := image.Rect(left, top, right, bottom)
	if si, ok := img.(interface {
		SubImage(image.Rectangle) image.Image
	}); ok {
		return si.SubImage(r)
	}
	return img
}

// score rates how well a frame works as artwork: contrast counts, frames
// that are almost black or washed out lose.
func score(img image.Image) float64 {
	b := img.Bounds()
	stepX, stepY := max(1, b.Dx()/80), max(1, b.Dy()/45)
	var sum, sq, n float64
	for y := b.Min.Y; y < b.Max.Y; y += stepY {
		for x := b.Min.X; x < b.Max.X; x += stepX {
			l := luma(img.At(x, y))
			sum += l
			sq += l * l
			n++
		}
	}
	if n == 0 {
		return 0
	}
	mean := sum / n
	sd := math.Sqrt(math.Max(0, sq/n-mean*mean))
	s := sd
	switch {
	case mean < 35:
		s *= mean / 35
	case mean > 220:
		s *= (255 - mean) / 35
	}
	return s
}
