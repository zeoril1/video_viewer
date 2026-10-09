// Package remoteauth checks sessions through the internal auth service.
package remoteauth

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
}

type Client struct {
	URL  string
	HTTP *http.Client
}

func New(base string) *Client {
	return &Client{URL: strings.TrimRight(base, "/"), HTTP: &http.Client{
		Timeout:       3 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// Current returns 401 for absent/expired sessions and 503 when auth cannot verify
// them. Only the session cookie is forwarded; client-supplied roles are ignored.
func (c *Client) Current(r *http.Request) (User, int) {
	cookie, err := r.Cookie("video_viewer_session")
	if err != nil || cookie.Value == "" {
		return User{}, http.StatusUnauthorized
	}
	if c == nil || c.URL == "" {
		return User{}, http.StatusServiceUnavailable
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, c.URL+"/api/auth/me", nil)
	if err != nil {
		return User{}, http.StatusServiceUnavailable
	}
	req.AddCookie(cookie)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return User{}, http.StatusServiceUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return User{}, http.StatusUnauthorized
	}
	if resp.StatusCode != http.StatusOK {
		return User{}, http.StatusServiceUnavailable
	}
	var body struct {
		User User `json:"user"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8192)).Decode(&body); err != nil || body.User.ID <= 0 || body.User.Username == "" {
		return User{}, http.StatusServiceUnavailable
	}
	return body.User, http.StatusOK
}

func (c *Client) RequireAdmin(w http.ResponseWriter, r *http.Request) bool {
	u, status := c.Current(r)
	if status == http.StatusOK && u.Role != "admin" {
		status = http.StatusForbidden
	}
	if status != http.StatusOK {
		http.Error(w, http.StatusText(status), status)
		return false
	}
	return true
}
