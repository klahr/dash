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

// Landing is the landing page's template (html/template), shown at / to
// visitors who aren't signed in when the landing page is on.
//
//go:embed landing.html
var Landing string
