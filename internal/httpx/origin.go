package httpx

import (
	"net/http"
	"net/url"
	"strings"
)

// CheckOrigin applies the same mutation policy as auth: browser requests must
// originate on this host, while internal clients may omit Origin and Referer.
func CheckOrigin(w http.ResponseWriter, r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		origin = r.Header.Get("Referer")
	}
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || !strings.EqualFold(u.Host, r.Host) {
		http.Error(w, "forbidden: cross-origin request", http.StatusForbidden)
		return false
	}
	return true
}
