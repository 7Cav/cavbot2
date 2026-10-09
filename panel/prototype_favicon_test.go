//go:build prototype

// PROTOTYPE for #548, throwaway: never merge. It serves the real panel (over
// the sign-in tests' fake forum) on 127.0.0.1:8548 and injects a favicon
// variant into every HTML page, picked by ?variant=A|B|C|D, plus the
// prototype_favicon/switcher.js overlay. Production code is untouched.
//
//	go test -tags prototype -run TestPrototypeFavicon -timeout 0 -v ./panel
//
// then open http://127.0.0.1:8548/signin?variant=A in your own browser.
package panel

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPrototypeFavicon(t *testing.T) {
	inner := newBrowser(t, newTestPanel(t, newFakeForum(t))).h

	mux := http.NewServeMux()
	mux.Handle("GET /prototype_favicon/", http.StripPrefix("/prototype_favicon/", http.FileServer(http.Dir("prototype_favicon"))))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		rec := httptest.NewRecorder()
		inner.ServeHTTP(rec, r)
		body := rec.Body.Bytes()
		if strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
			v := r.URL.Query().Get("variant")
			if !strings.Contains("ABCD", v) || len(v) != 1 {
				v = "A"
			}
			head := fmt.Sprintf(`<link rel="icon" type="image/png" sizes="16x16" href="/prototype_favicon/%[1]s-16.png">`+
				`<link rel="icon" type="image/png" sizes="32x32" href="/prototype_favicon/%[1]s-32.png">`+
				`<link rel="icon" type="image/png" sizes="48x48" href="/prototype_favicon/%[1]s-48.png">`+
				`<script src="/prototype_favicon/switcher.js" defer></script></head>`, v)
			body = bytes.Replace(body, []byte("</head>"), []byte(head), 1)
		}
		for k, vs := range rec.Header() {
			if k != "Content-Length" {
				w.Header()[k] = vs
			}
		}
		w.WriteHeader(rec.Code)
		_, _ = w.Write(body)
	})

	t.Log("PROTOTYPE favicon server on http://127.0.0.1:8548/signin?variant=A")
	if err := http.ListenAndServe("127.0.0.1:8548", mux); err != nil {
		t.Fatal(err)
	}
}
