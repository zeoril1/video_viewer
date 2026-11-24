// Package torrentvpn isolates selected HTTP downloads inside a userspace
// WireGuard network. It never changes the host or container routing table.
package torrentvpn

import (
	"bufio"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

const MaxConfigBytes = 64 << 10

type tunnelConfig struct {
	addresses                           []netip.Addr
	dns                                 []netip.Addr
	privateKey, publicKey, presharedKey string
	endpoint                            string
	allowed                             []netip.Prefix
	mtu, keepalive                      int
}

// Configuration errors deliberately identify fields without repeating values:
// an uploaded file may contain a private or pre-shared key.
func parseConfig(data []byte) (tunnelConfig, error) {
	c := tunnelConfig{mtu: 1420}
	if len(data) == 0 || len(data) > MaxConfigBytes {
		return c, errors.New("Конфиг должен быть непустым файлом размером до 64 КиБ")
	}
	section := ""
	seen := map[string]bool{}
	peerCount := 0
	scanner := bufio.NewScanner(strings.NewReader(strings.TrimPrefix(string(data), "\ufeff")))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if i := strings.IndexAny(line, "#;"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") {
			switch strings.ToLower(line) {
			case "[interface]":
				if seen["interface"] {
					return c, errors.New("Допускается только один раздел Interface")
				}
				seen["interface"] = true
				section = "interface"
			case "[peer]":
				peerCount++
				if peerCount > 1 {
					return c, errors.New("Допускается только один VPN-сервер Peer")
				}
				section = "peer"
			default:
				return c, errors.New("Конфиг содержит неподдерживаемый раздел")
			}
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || section == "" {
			return c, errors.New("Некорректный формат WireGuard-конфига")
		}
		key, value = strings.ToLower(strings.TrimSpace(key)), strings.TrimSpace(value)
		field := section + "." + key
		if seen[field] && key != "address" && key != "dns" && key != "allowedips" {
			return c, errors.New("Конфиг содержит повторяющееся поле")
		}
		seen[field] = true
		switch field {
		case "interface.privatekey", "peer.publickey", "peer.presharedkey":
			decoded, err := base64.StdEncoding.DecodeString(value)
			if err != nil || len(decoded) != 32 || (field != "peer.presharedkey" && allZero(decoded)) {
				return c, errors.New("Некорректный ключ WireGuard")
			}
			x := hex.EncodeToString(decoded)
			if field == "interface.privatekey" {
				c.privateKey = x
			} else if field == "peer.publickey" {
				c.publicKey = x
			} else {
				c.presharedKey = x
			}
		case "interface.address":
			for _, v := range strings.Split(value, ",") {
				p, err := netip.ParsePrefix(strings.TrimSpace(v))
				if err != nil || p.Addr().IsUnspecified() || p.Addr().IsMulticast() {
					return c, errors.New("Некорректный Address WireGuard")
				}
				c.addresses = append(c.addresses, p.Addr())
			}
		case "interface.dns":
			for _, v := range strings.Split(value, ",") {
				ip, err := netip.ParseAddr(strings.TrimSpace(v))
				if err != nil || ip.IsUnspecified() || ip.IsMulticast() {
					return c, errors.New("DNS должен содержать IP-адреса серверов")
				}
				c.dns = append(c.dns, ip)
			}
		case "interface.mtu":
			mtu, err := strconv.Atoi(value)
			if err != nil || mtu < 1280 || mtu > 9000 {
				return c, errors.New("MTU должен быть числом от 1280 до 9000")
			}
			c.mtu = mtu
		case "interface.listenport":
			port, err := strconv.Atoi(value)
			if err != nil || port < 0 || port > 65535 {
				return c, errors.New("Некорректный ListenPort")
			}
			// A client binds an ephemeral host UDP port to avoid taking over a host service.
		case "peer.endpoint":
			host, portText, err := net.SplitHostPort(value)
			port, portErr := strconv.Atoi(portText)
			if err != nil || portErr != nil || port < 1 || port > 65535 || !validHost(host) {
				return c, errors.New("Endpoint должен содержать адрес VPN-сервера и порт")
			}
			c.endpoint = value
		case "peer.allowedips":
			for _, v := range strings.Split(value, ",") {
				p, err := netip.ParsePrefix(strings.TrimSpace(v))
				if err != nil {
					return c, errors.New("Некорректный AllowedIPs")
				}
				c.allowed = append(c.allowed, p.Masked())
			}
		case "peer.persistentkeepalive":
			n, err := strconv.Atoi(value)
			if err != nil || n < 0 || n > 65535 {
				return c, errors.New("Некорректный PersistentKeepalive")
			}
			c.keepalive = n
		default:
			return c, errors.New("Конфиг содержит неподдерживаемые поля или команды. PreUp/PostUp/PreDown/PostDown не разрешены")
		}
	}
	if scanner.Err() != nil {
		return c, errors.New("Не удалось прочитать WireGuard-конфиг")
	}
	if c.privateKey == "" || c.publicKey == "" || c.endpoint == "" || len(c.addresses) == 0 {
		return c, errors.New("Нужны PrivateKey, Address, PublicKey и Endpoint")
	}
	v4, v6 := false, false
	for _, p := range c.allowed {
		if p.Bits() == 0 {
			if p.Addr().Is4() {
				v4 = true
			} else {
				v6 = true
			}
		}
	}
	hasIPv4Address := false
	for _, ip := range c.addresses {
		if ip.Is4() {
			hasIPv4Address = true
		}
		if ip.Is4() && !v4 || ip.Is6() && !v6 {
			return c, errors.New("AllowedIPs должен включать 0.0.0.0/0 для IPv4 и ::/0 для IPv6-адреса туннеля")
		}
	}
	if len(c.dns) == 0 {
		if hasIPv4Address {
			c.dns = []netip.Addr{netip.MustParseAddr("1.1.1.1")}
		} else {
			c.dns = []netip.Addr{netip.MustParseAddr("2606:4700:4700::1111")}
		}
	}
	return c, nil
}

func allZero(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}

func validHost(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return true
	}
	for _, label := range strings.Split(strings.TrimSuffix(host, "."), ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, ch := range label {
			if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-') {
				return false
			}
		}
	}
	return true
}
