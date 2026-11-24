package youtube

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"unicode"
)

// DefaultMaxImageBytes limits the size of a downloaded channel avatar.
const DefaultMaxImageBytes = 1 << 20

// DefaultImageHosts are the Google hosts serving channel avatars. A host
// matches itself and its subdomains.
var DefaultImageHosts = []string{"ggpht.com", "googleusercontent.com", "ytimg.com"}

// imageTypes are the accepted avatar types, recognised by content rather than
// by the Content-Type header.
var imageTypes = map[string]bool{
	"image/jpeg": true,
	"image/png":  true,
	"image/webp": true,
	"image/gif":  true,
}

// thumbnailOrder is the preferred avatar size: medium (240 px) is enough for
// an 80 px logo on high-density screens.
var thumbnailOrder = []string{"medium", "high", "default"}

func pickThumbnail(t map[string]struct {
	URL string `json:"url"`
}) string {
	for _, k := range thumbnailOrder {
		if u := t[k].URL; u != "" {
			return u
		}
	}
	return ""
}

// validHandle returns the channel handle (e.g. @name) or "" when the API
// value does not look like one.
func validHandle(s string) string {
	if len(s) < 2 || len(s) > 100 || s[0] != '@' {
		return ""
	}
	for _, r := range s[1:] {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("._-·", r) {
			return ""
		}
	}
	return s
}

// FetchImage downloads a channel avatar. Only https URLs on the allowed hosts
// are fetched (also after redirects), the API key is never sent, and the body
// must be an image of an accepted type no larger than MaxImageBytes. It returns
// the image and its detected content type.
func (c *Client) FetchImage(ctx context.Context, rawURL string) ([]byte, string, error) {
	u, err := url.Parse(rawURL)
	if err != nil || !c.imageURLAllowed(u) {
		return nil, "", errors.New("adres obrazka spoza dozwolonych hostów")
	}
	ctx, cancel := context.WithTimeout(ctx, c.reqTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Accept", "image/*")

	client := *c.http
	client.CheckRedirect = func(r *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("za dużo przekierowań")
		}
		if !c.imageURLAllowed(r.URL) {
			return errors.New("przekierowanie obrazka spoza dozwolonych hostów")
		}
		return nil
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("pobranie obrazka: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("pobranie obrazka: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, c.maxImage+1))
	if err != nil {
		return nil, "", fmt.Errorf("pobranie obrazka: %w", err)
	}
	if int64(len(body)) > c.maxImage {
		return nil, "", fmt.Errorf("obrazek większy niż %d bajtów", c.maxImage)
	}
	ctype := http.DetectContentType(body)
	if !imageTypes[ctype] {
		return nil, "", fmt.Errorf("nieobsługiwany typ obrazka %q", ctype)
	}
	return body, ctype, nil
}

func (c *Client) imageURLAllowed(u *url.URL) bool {
	if u == nil || u.Scheme != "https" || u.User != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, h := range c.imageHosts {
		if host == h || strings.HasSuffix(host, "."+h) {
			return true
		}
	}
	return false
}
