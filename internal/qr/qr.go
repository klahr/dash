// Package qr draws QR codes as PNG images.
package qr

import (
	"bytes"
	"image"
	"image/color"
	"image/png"

	"github.com/yeqown/go-qrcode/v2"
)

// Size is the target width of a QR code image in pixels, and QuietZone the
// white margin around it in modules (the minimum the spec asks for).
const (
	Size      = 320
	QuietZone = 4
)

// PNG returns text as a PNG QR code at Medium error correction, about Size
// pixels wide including a QuietZone-module margin.
//
// The image is drawn here with the standard library rather than go-qrcode's
// writer/standard package, which pulls in the unmaintained fogleman/gg and
// golang/freetype.
func PNG(text string) ([]byte, error) {
	qrc, err := qrcode.NewWith(text, qrcode.WithErrorCorrectionLevel(qrcode.ErrorCorrectionMedium))
	if err != nil {
		return nil, err
	}
	w := &matrixWriter{}
	if err := qrc.Save(w); err != nil {
		return nil, err
	}
	return render(w.bitmap)
}

// render draws bitmap (true = dark module, indexed [row][col]) with whole
// pixels per module, so the image is at most Size wide.
func render(bitmap [][]bool) ([]byte, error) {
	n := len(bitmap)
	block := max(1, Size/(n+2*QuietZone))
	side := (n + 2*QuietZone) * block
	img := image.NewPaletted(image.Rect(0, 0, side, side), color.Palette{color.White, color.Black})
	// Index 0 (white) is the zero value, so only dark modules need drawing.
	for y, row := range bitmap {
		for x, dark := range row {
			if !dark {
				continue
			}
			x0, y0 := (x+QuietZone)*block, (y+QuietZone)*block
			for py := y0; py < y0+block; py++ {
				for px := x0; px < x0+block; px++ {
					img.SetColorIndex(px, py, 1)
				}
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// matrixWriter captures the encoded QR matrix as a bitmap.
type matrixWriter struct{ bitmap [][]bool }

func (m *matrixWriter) Write(mat qrcode.Matrix) error {
	m.bitmap = mat.Bitmap()
	return nil
}

func (m *matrixWriter) Close() error { return nil }
