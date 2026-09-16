package db

import (
	"encoding/base32"
	"encoding/hex"
	"net/url"
	"testing"
)

func TestSourceMagnetHashFormats(t *testing.T) {
	const hash = "0123456789abcdef0123456789abcdef01234567"
	data, _ := hex.DecodeString(hash)
	for _, xt := range []string{"urn:btih:" + hash, "urn:btih:" + base32.StdEncoding.EncodeToString(data)} {
		for _, value := range []string{xt, url.QueryEscape(xt)} {
			if got := magnetInfoHash("magnet:?xt=" + value); got != hash {
				t.Fatalf("%s: got %q", value, got)
			}
		}
	}
	for _, value := range []string{"invalid", "magnet:?xt=urn:btih:broken", "magnet:?xt=urn:btih:" + hash + "0"} {
		if got := magnetInfoHash(value); got != "" {
			t.Fatalf("invalid magnet accepted: %s", value)
		}
	}
}
