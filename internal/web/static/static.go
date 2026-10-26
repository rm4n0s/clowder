// Package static holds the files the server sends as they are: the Datastar
// client, the stylesheet and the scripts. They are embedded in the binary.
package static

import "embed"

// FS holds the embedded static files: datastar.js, app.css and the files
// added to this folder later. Its paths are relative to the folder, for
// example "app.css".
//
//go:embed datastar.js app.css
var FS embed.FS
