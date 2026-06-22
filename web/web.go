package web

import (
	"embed"
	"io/fs"
)

// files embeds the Bun build output. `all:static` is used (rather than
// `static/*`) so the .gitkeep placeholder is always embedded, guaranteeing the
// embed has at least one file even before `bun run build` has produced
// index.html and the hashed assets. The build (see web/app/build.ts, run via
// the `ui` task) populates static/index.html and static/assets/* which this
// then serves.
//
//go:embed all:static
var files embed.FS

// Static returns the full embedded static tree (index.html at its root, hashed
// build artifacts under assets/).
func Static() fs.FS {
	static, err := fs.Sub(files, "static")
	if err != nil {
		panic(err)
	}
	return static
}

// AssetsFS returns the embedded static/assets subtree: the content-hashed,
// immutable JS/CSS bundles emitted by the build. The HTTP layer mounts this at
// GET /assets/ with a long-lived immutable Cache-Control header. When no build
// has run yet the subtree is empty and the file server simply 404s.
func AssetsFS() fs.FS {
	assets, err := fs.Sub(files, "static/assets")
	if err != nil {
		panic(err)
	}
	return assets
}
