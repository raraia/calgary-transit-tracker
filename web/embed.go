// Package web holds the dashboard's static files (HTML, CSS, JavaScript).
//
// They're embedded into the API binary, so deploying the dashboard means
// deploying one file. The API serves them at "/".
package web

import "embed"

//go:embed index.html app.js style.css
var Files embed.FS
