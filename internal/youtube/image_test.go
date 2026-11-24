package youtube

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func pngBytes(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// imageServer serves an avatar over TLS and records whether the API key was sent.
func imageServer(t *testing.T, body []byte) (*httptest.Server, *Client, *bool) {
	t.Helper()
	keySent := false
	mux := http.NewServeMux()
	mux.HandleFunc("/avatar", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Goog-Api-Key") != "" {
			keySent = true
		}
		w.Header().Set("Content-Type", "image/jpeg") // the header is ignored, content decides
		w.Write(body)
	})
	mux.HandleFunc("/redirect-ok", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/avatar", http.StatusFound)
	})
	mux.HandleFunc("/redirect-out", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://example.com/avatar", http.StatusFound)
	})
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	c, err := New(Options{
		ChannelID:     testChannel,
		APIKey:        primaryKey,
		HTTPClient:    srv.Client(),
		ImageHosts:    []string{"127.0.0.1"},
		MaxImageBytes: 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	return srv, c, &keySent
}

func TestFetchImage(t *testing.T) {
	img := pngBytes(t)
	srv, c, keySent := imageServer(t, img)
	for _, path := range []string{"/avatar", "/redirect-ok"} {
		data, ctype, err := c.FetchImage(context.Background(), srv.URL+path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if ctype != "image/png" || !bytes.Equal(data, img) {
			t.Errorf("%s: typ %q, %d bajtów", path, ctype, len(data))
		}
	}
	if *keySent {
		t.Error("klucz API został wysłany do hosta obrazków")
	}
}

func TestFetchImageRejects(t *testing.T) {
	cases := []struct {
		name string
		body []byte
		url  func(srv string) string
	}{
		{"http zamiast https", nil, func(s string) string { return strings.Replace(s, "https://", "http://", 1) + "/avatar" }},
		{"przekierowanie poza dozwolone hosty", nil, func(s string) string { return s + "/redirect-out" }},
		{"nie obrazek", []byte("<html>hello</html>"), func(s string) string { return s + "/avatar" }},
		{"za duży", bytes.Repeat([]byte{0x89}, 2048), func(s string) string { return s + "/avatar" }},
		{"404", nil, func(s string) string { return s + "/brak" }},
		{"niepoprawny URL", nil, func(string) string { return "::" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := tc.body
			if body == nil {
				body = pngBytes(t)
			}
			srv, c, _ := imageServer(t, body)
			if _, _, err := c.FetchImage(context.Background(), tc.url(srv.URL)); err == nil {
				t.Fatal("oczekiwano błędu")
			}
		})
	}
}

func TestImageURLAllowedDefaults(t *testing.T) {
	c, err := New(Options{ChannelID: testChannel, APIKey: primaryKey})
	if err != nil {
		t.Fatal(err)
	}
	for raw, want := range map[string]bool{
		"https://yt3.ggpht.com/abc=s240":           true,
		"https://YT3.GGPHT.COM/abc":                true,
		"https://lh3.googleusercontent.com/abc":    true,
		"https://i.ytimg.com/vi/x/default.jpg":     true,
		"http://yt3.ggpht.com/abc":                 false,
		"https://evilggpht.com/abc":                false,
		"https://ggpht.com.evil.example/abc":       false,
		"https://user@yt3.ggpht.com/abc":           false,
		"https://example.com/?u=https://ggpht.com": false,
	} {
		u, _ := url.Parse(raw)
		if got := c.imageURLAllowed(u); got != want {
			t.Errorf("%s: %v, oczekiwano %v", raw, got, want)
		}
	}
}
