package torrentvpn

import (
	"encoding/base64"
	"strings"
	"testing"
)

func testConfig() []byte {
	return []byte("[Interface]\nPrivateKey = " + base64.StdEncoding.EncodeToString([]byte(strings.Repeat("a", 32))) + "\nAddress = 10.44.0.1/32\nDNS = 10.44.0.2\n\n[Peer]\nPublicKey = " + base64.StdEncoding.EncodeToString([]byte(strings.Repeat("b", 32))) + "\nEndpoint = 127.0.0.1:51820\nAllowedIPs = 0.0.0.0/0\nPersistentKeepalive = 25\n")
}

func TestParseConfigRejectsInvalidOrUnsafeInputsWithoutKeys(t *testing.T) {
	base := string(testConfig())
	for name, input := range map[string]string{
		"empty": "", "oversize": strings.Repeat("a", MaxConfigBytes+1),
		"private key":        strings.Replace(base, "PrivateKey = ", "PrivateKey = secret", 1),
		"invalid address":    strings.Replace(base, "10.44.0.1/32", "not-an-ip", 1),
		"invalid DNS":        strings.Replace(base, "DNS = 10.44.0.2", "DNS = secret.invalid", 1),
		"no full route":      strings.Replace(base, "0.0.0.0/0", "10.0.0.0/8", 1),
		"IPv6 without route": strings.Replace(base, "10.44.0.1/32", "10.44.0.1/32, fd00::1/128", 1),
		"bad endpoint":       strings.Replace(base, "127.0.0.1:51820", "bad/host:51820", 1),
		"hook":               strings.Replace(base, "[Peer]", "PostUp = echo secret\n[Peer]", 1),
		"duplicate peer":     base + "\n[Peer]\nPublicKey=secret\n",
		"duplicate key":      base + "\nPublicKey=secret\n",
		"unknown field":      base + "\nPassword=secret\n",
		"invalid MTU":        strings.Replace(base, "[Peer]", "MTU = 2\n[Peer]", 1),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parseConfig([]byte(input))
			if err == nil {
				t.Fatal("unsafe configuration accepted")
			}
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "YWFh") {
				t.Fatal("configuration values leaked in error")
			}
		})
	}
}

func TestParseConfigSupportsDNSDefaultsBOMAndIPv6(t *testing.T) {
	base := string(testConfig())
	c, err := parseConfig([]byte("\ufeff" + strings.Replace(base, "DNS = 10.44.0.2\n", "", 1)))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.dns) != 1 || c.dns[0].String() != "1.1.1.1" {
		t.Fatal("missing safe in-tunnel DNS default")
	}
	input := strings.ReplaceAll(base, "10.44.0.1/32", "fd00::1/128")
	input = strings.ReplaceAll(input, "DNS = 10.44.0.2", "DNS = fd00::2")
	input = strings.ReplaceAll(input, "0.0.0.0/0", "::/0")
	c, err = parseConfig([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if !c.addresses[0].Is6() || c.keepalive != 25 || c.mtu != 1420 {
		t.Fatal("IPv6 config not preserved")
	}
}

func TestIPv6OnlyAddressUsesIPv6DefaultDNSWithDualStackAllowedIPs(t *testing.T) {
	input := strings.Replace(string(testConfig()), "10.44.0.1/32", "fd00::1/128", 1)
	input = strings.Replace(input, "DNS = 10.44.0.2\n", "", 1)
	input = strings.Replace(input, "AllowedIPs = 0.0.0.0/0", "AllowedIPs = 0.0.0.0/0, ::/0", 1)
	c, err := parseConfig([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.dns) != 1 || !c.dns[0].Is6() || c.dns[0].String() != "2606:4700:4700::1111" {
		t.Fatal("default DNS must use the address family actually available inside the tunnel")
	}
}
