// Package web embeds the dashboard assets into the binary.
package web

import "embed"

//go:embed *.html *.css *.js
var Assets embed.FS
