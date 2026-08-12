// Package web embeds the built browser client (web/dist, produced by
// `make wasm`) into the confab binary.
package web

import "embed"

//go:embed all:dist
var Dist embed.FS
