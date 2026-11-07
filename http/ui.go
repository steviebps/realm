package http

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// uiContentSecurityPolicy lets the UI load only its own scripts, styles,
// and fonts, and talk only to this server: an injected script or a planted
// link to another site's code does not run. The UI has no inline scripts or
// style elements; its icon is a data: URL.
const uiContentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; " +
	"font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

// uiHandler serves the single-page UI in fsys (its build output): a file as
// it is, and index.html for every other path, each a route the app renders
// itself. Routes are served in place, because http.FileServer would redirect
// "/ui/prod/a%3Fb/" to "/ui/prod/a?b", dropping the trailing slash and the
// encoding of the chamber name, so a reload would open another chamber.
// Mount it with the "/ui/" prefix stripped.
func uiHandler(fsys fs.FS) http.Handler {
	files := http.FileServer(http.FS(fsys))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", uiContentSecurityPolicy)
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name == "" {
			// the root, whose index.html the file server serves
			files.ServeHTTP(w, r)
			return
		}
		if info, err := fs.Stat(fsys, name); err == nil && !info.IsDir() {
			files.ServeHTTP(w, r)
			return
		}
		index, err := fs.ReadFile(fsys, "index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(index)
	})
}
