package mcptools

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// ShowImageTool is what the chat calls to show the user an image file it
// made: the image shows in its reply.
const ShowImageTool = "show_image"

// MaxImage is the largest file show_image shows.
const MaxImage = 10 << 20

// Image is an image file show_image read, to show in the chat's reply.
type Image struct {
	Name string `json:"name"` // the file's base name
	Type string `json:"type"` // its media type
	Data []byte `json:"data"`
}

// imageTypes are the kinds of image show_image shows, by extension.
var imageTypes = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp", ".svg": "image/svg+xml",
}

// ReadImage reads the image file at path, an absolute path on this
// computer: a regular file of at most MaxImage bytes, named like a PNG,
// JPEG, GIF, WebP or SVG image and holding one.
func ReadImage(path string) (Image, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return Image{}, errors.New("path is required")
	}
	if !filepath.IsAbs(path) {
		return Image{}, fmt.Errorf("%s isn't an absolute path: pass the image's full path", path)
	}
	typ, ok := imageTypes[strings.ToLower(filepath.Ext(path))]
	if !ok {
		return Image{}, fmt.Errorf("%s isn't an image Dash shows: write a .png, .jpg, .gif, .webp or .svg file", filepath.Base(path))
	}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Image{}, fmt.Errorf("there is no file %s", path)
		}
		return Image{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return Image{}, err
	}
	if !info.Mode().IsRegular() {
		return Image{}, fmt.Errorf("%s isn't a file", path)
	}
	if info.Size() > MaxImage {
		return Image{}, fmt.Errorf("%s is %.1f MB; images can be at most %d MB", filepath.Base(path), float64(info.Size())/(1<<20), MaxImage>>20)
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxImage+1))
	if err != nil {
		return Image{}, err
	}
	if len(data) > MaxImage {
		return Image{}, fmt.Errorf("%s is over %d MB; images can be at most %d MB", filepath.Base(path), MaxImage>>20, MaxImage>>20)
	}
	if len(data) == 0 {
		return Image{}, fmt.Errorf("%s is empty", path)
	}
	if !holdsImage(typ, data) {
		return Image{}, fmt.Errorf("%s doesn't hold the %s image its name says", filepath.Base(path), strings.ToUpper(strings.TrimPrefix(filepath.Ext(path), ".")))
	}
	return Image{Name: filepath.Base(path), Type: typ, Data: data}, nil
}

// holdsImage reports whether data is an image of type typ: by its magic
// bytes, or for SVG, UTF-8 text with an <svg element.
func holdsImage(typ string, data []byte) bool {
	if typ == "image/svg+xml" {
		return utf8.Valid(data) && bytes.Contains(bytes.ToLower(data), []byte("<svg"))
	}
	return http.DetectContentType(data) == typ
}

// ShowImage returns show_image. It only checks and reads the file: the
// chat (see agent's chat streams) reads it again from the tool call that
// worked and sends it with the reply.
func ShowImage() Tool {
	return Tool{ShowImageTool,
		"Show the user an image file you made (a chart, a diagram, a screenshot): it shows in your reply in the chat, " +
			"and the user can click it to see it full size. The file must be on this computer: a PNG, JPEG, GIF, WebP or SVG image " +
			fmt.Sprintf("of at most %d MB. For a simple drawing, a ```svg code block in your reply shows as a picture too, without a file.", MaxImage>>20),
		[]Param{{"path", "string", "The image file's absolute path, e.g. /tmp/chart.png.", true}},
		func(a Args) (any, error) {
			img, err := ReadImage(a.Str("path"))
			if err != nil {
				return nil, err
			}
			return fmt.Sprintf("The user sees %s in your reply. Don't open it in another viewer.", img.Name), nil
		}}
}

// CallsTool reports whether body, an MCP request, calls the tool name.
func CallsTool(body []byte, name string) bool {
	var req Request
	if json.Unmarshal(body, &req) != nil || req.Method != "tools/call" {
		return false
	}
	var p struct {
		Name string `json:"name"`
	}
	return json.Unmarshal(req.Params, &p) == nil && p.Name == name
}
