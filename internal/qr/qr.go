// Package qr draws QR codes as PNG images.
package qr

import (
	"bytes"
	"io"

	"github.com/yeqown/go-qrcode/v2"
	"github.com/yeqown/go-qrcode/writer/standard"
)

// Size is the target width of a QR code image in pixels, and QuietZone the
// white margin around it in modules (the minimum the spec asks for).
const (
	Size      = 320
	QuietZone = 4
)

// PNG returns text as a PNG QR code at Medium error correction, about Size
// pixels wide including a QuietZone-module margin.
func PNG(text string) ([]byte, error) {
	qrc, err := qrcode.NewWith(text, qrcode.WithErrorCorrectionLevel(qrcode.ErrorCorrectionMedium))
	if err != nil {
		return nil, err
	}
	// Whole pixels per module, so the image is at most Size wide.
	block := max(1, min(255, Size/(qrc.Dimension()+2*QuietZone)))
	var buf bytes.Buffer
	wr := standard.NewWithWriter(nopCloser{&buf},
		standard.WithBuiltinImageEncoder(standard.PNG_FORMAT),
		standard.WithQRWidth(uint8(block)),
		standard.WithBorderWidth(QuietZone*block),
	)
	if err := qrc.Save(wr); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// nopCloser lets the QR writer, which closes its output, write to a buffer.
type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }
