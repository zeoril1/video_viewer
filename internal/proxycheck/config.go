// Package proxycheck maintains a bounded pool of recently verified public HTTP
// proxies. It never falls back to a direct connection for application traffic.
package proxycheck

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	maxCandidates       = 300
	maxParsedCandidates = 3000
	maxSources          = 10
	maxSourceBytes      = 1 << 20
	maxConfigBytes      = 256 << 10
)

type Config struct {
	Enabled bool     `json:"enabled"`
	Sources []string `json:"sources"`
	Static  []string `json:"static"`
}

func DefaultConfig() Config {
	return Config{Enabled: true, Sources: []string{
		"https://raw.githubusercontent.com/TheSpeedX/PROXY-List/master/http.txt",
		"https://raw.githubusercontent.com/monosans/proxy-list/main/proxies/http.txt",
		"https://raw.githubusercontent.com/proxifly/free-proxy-list/main/proxies/protocols/http/data.txt",
	}, Static: []string{}}
}

func copyConfig(c Config) Config {
	c.Sources = append([]string{}, c.Sources...)
	c.Static = append([]string{}, c.Static...)
	return c
}

func validateConfig(c Config) (Config, error) {
	if len(c.Sources) > maxSources || len(c.Static) > maxCandidates {
		return Config{}, errors.New("Допускается не более 10 источников и 300 заданных прокси")
	}
	out := Config{Enabled: c.Enabled, Sources: []string{}, Static: []string{}}
	seen := map[string]bool{}
	for _, raw := range c.Sources {
		raw = strings.TrimSpace(raw)
		if !validSourceURL(raw) {
			return Config{}, errors.New("Источники должны быть публичными HTTPS-ссылками без логина и пароля")
		}
		if !seen[raw] {
			seen[raw] = true
			out.Sources = append(out.Sources, raw)
		}
	}
	seen = map[string]bool{}
	for _, raw := range c.Static {
		addr, ok := normalizeProxy(raw)
		if !ok {
			return Config{}, errors.New("Укажите публичные HTTP(S)-прокси в формате адрес:порт без логина и пароля")
		}
		if !seen[addr] {
			seen[addr] = true
			out.Static = append(out.Static, addr)
		}
	}
	return out, nil
}

func validSourceURL(raw string) bool {
	if len(raw) > 2048 {
		return false
	}
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.User == nil && u.Fragment == "" &&
		u.Opaque == "" && publicHost(u.Hostname()) && (u.Port() == "" || u.Port() == "443")
}

func normalizeProxy(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 512 || strings.ContainsAny(raw, "\r\n\t @") {
		return "", false
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil ||
		u.Opaque != "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || !publicHost(u.Hostname()) {
		return "", false
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return "", false
	}
	return (&url.URL{Scheme: u.Scheme, Host: net.JoinHostPort(strings.ToLower(u.Hostname()), strconv.Itoa(port))}).String(), true
}

// DNS names are resolved and pinned to public addresses at connection time.
// Application destination names remain remote-resolved by the proxy.
func publicHost(host string) bool {
	if ip, err := netip.ParseAddr(host); err == nil {
		return publicIP(ip)
	}
	host = strings.ToLower(host)
	if len(host) > 253 || !strings.Contains(host, ".") || strings.HasSuffix(host, ".") {
		return false
	}
	for _, suffix := range []string{".localhost", ".local", ".internal", ".lan", ".home", ".test", ".invalid", ".onion"} {
		if strings.HasSuffix(host, suffix) {
			return false
		}
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	return true
}

var nonPublicNets = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"), netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"), netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("fec0::/10"),
}

func publicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, prefix := range nonPublicNets {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

func parseList(data []byte) []string {
	var values []string
	seen := map[string]bool{}
	add := func(raw string) {
		if p, ok := normalizeProxy(raw); ok && !seen[p] && len(values) < maxParsedCandidates {
			seen[p] = true
			values = append(values, p)
		}
	}
	var value any
	if json.Unmarshal(data, &value) == nil {
		var visit func(any, int)
		visit = func(v any, depth int) {
			if depth > 5 || len(values) >= maxParsedCandidates {
				return
			}
			switch x := v.(type) {
			case string:
				add(x)
			case []any:
				for _, child := range x {
					visit(child, depth+1)
				}
			case map[string]any:
				if ip, ok := x["ip"].(string); ok {
					port := ""
					switch p := x["port"].(type) {
					case string:
						port = p
					case float64:
						if p == float64(int(p)) {
							port = strconv.Itoa(int(p))
						}
					}
					protocol := "http"
					if p, ok := x["protocol"].(string); ok {
						protocol = strings.ToLower(p)
					}
					if port != "" {
						add(protocol + "://" + net.JoinHostPort(ip, port))
					}
				}
				for _, key := range []string{"data", "proxies", "items"} {
					if child, ok := x[key]; ok {
						visit(child, depth+1)
					}
				}
			}
		}
		visit(value, 0)
		return values
	}
	for _, line := range strings.Split(strings.TrimPrefix(string(data), "\ufeff"), "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "@") {
			continue
		}
		if fields := strings.Fields(line); len(fields) > 0 {
			add(fields[0])
		}
		if len(values) >= maxParsedCandidates {
			break
		}
	}
	return values
}

func readConfig(path string) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxConfigBytes+1))
	if err != nil || len(data) > maxConfigBytes {
		return Config{}, errors.New("cannot read saved proxy configuration")
	}
	var c Config
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || d.Decode(new(any)) != io.EOF {
		return Config{}, errors.New("invalid saved proxy configuration")
	}
	return validateConfig(c)
}

func saveConfig(path string, c Config) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".proxy-config-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(append(data, '\n'))
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
