// Package web embeds the Dash frontend.
package web

import (
	"embed"
	"io/fs"
)

//go:embed static
var embedded embed.FS

// Static is the frontend root (index.html, app.js, style.css).
var Static, _ = fs.Sub(embedded, "static")
