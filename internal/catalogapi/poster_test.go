package catalogapi

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

type posterTransport func(*http.Request) (*http.Response, error)

func (f posterTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

const testPosterURL = "https://image.tmdb.org/t/p/w500/abc.jpg"

var testPosterBytes = []byte("\x89PNG\r\n\x1a\nimage content")

func posterRequest(p *posterProxy, u string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "/api/poster?url="+url.QueryEscape(u), nil)
	r.Header.Set("Cookie", "session=private")
	r.Header.Set("Authorization", "Bearer private")
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	return w
}
func TestPosterURLAllowlist(t *testing.T) {
	for _, u := range []string{testPosterURL, "https://m.media-amazon.com/images/M/MV5Babc@._V1_.jpg"} {
		if _, err := allowedPosterURL(u); err != nil {
			t.Fatal(u, err)
		}
	}
	for _, u := range []string{"", "http://image.tmdb.org/t/p/w500/abc.jpg", "https://localhost/a.jpg", "https://image.tmdb.org.evil.test/t/p/w500/abc.jpg", "https://user@image.tmdb.org/t/p/w500/abc.jpg", "https://image.tmdb.org:443/t/p/w500/abc.jpg", "https://image.tmdb.org/t/p/w200/abc.jpg", testPosterURL + "?url=http://localhost", "https://m.media-amazon.com/images/../secret.jpg"} {
		if _, err := allowedPosterURL(u); err == nil {
			t.Fatal("allowed", u)
		}
	}
}
func TestPosterCacheAndPrivateHeaders(t *testing.T) {
	p := newPosterProxy()
	calls := 0
	p.client.Transport = posterTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
			t.Fatal("client credentials forwarded")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(testPosterBytes))}, nil
	})
	for i := 0; i < 2; i++ {
		w := posterRequest(p, testPosterURL)
		if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), testPosterBytes) || w.Header().Get("Content-Type") != "image/png" {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if calls != 1 {
		t.Fatal("cache missed", calls)
	}
	v, _ := p.cached(testPosterURL)
	r := httptest.NewRequest("GET", "/api/poster?url="+url.QueryEscape(testPosterURL), nil)
	r.Header.Set("If-None-Match", v.etag)
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	if w.Code != 304 || w.Body.Len() != 0 {
		t.Fatal(w.Code)
	}
	v.expires = time.Now().Add(-time.Second)
	p.put(v)
	posterRequest(p, testPosterURL)
	if calls != 2 {
		t.Fatal("expired cache not refreshed")
	}
}
func TestPosterErrorsNotCachedAndBodiesValidated(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"upstream", "failure", 503}, {"html", "<html>failure</html>", 200}, {"oversize", strings.Repeat("x", posterMaxBytes+1), 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newPosterProxy()
			calls := 0
			p.client.Transport = posterTransport(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: tc.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})
			for i := 0; i < 2; i++ {
				w := posterRequest(p, testPosterURL)
				if w.Code != 502 || w.Header().Get("Cache-Control") != "no-store" {
					t.Fatal(w.Code)
				}
			}
			if calls != 2 || p.bytes != 0 {
				t.Fatal("error cached")
			}
		})
	}
}
func TestPosterRejectsRedirectToInternalHost(t *testing.T) {
	p := newPosterProxy()
	calls := 0
	p.client.Transport = posterTransport(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"http://127.0.0.1/admin"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	if w := posterRequest(p, testPosterURL); w.Code != 502 {
		t.Fatal(w.Code)
	}
	if calls != 1 {
		t.Fatal("followed unsafe redirect")
	}
}
func TestPosterCacheBounded(t *testing.T) {
	p := newPosterProxy()
	for i := 0; i < 12; i++ {
		p.put(posterEntry{key: fmt.Sprint(i), body: make([]byte, posterMaxBytes), expires: time.Now().Add(time.Hour)})
	}
	if p.bytes > posterCacheBytes || len(p.entries) != 8 {
		t.Fatal(p.bytes, len(p.entries))
	}
	if _, ok := p.cached("0"); ok {
		t.Fatal("oldest entry not evicted")
	}
}
