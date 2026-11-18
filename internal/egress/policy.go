// Package egress controls what a sandbox can reach on the network. Sandboxes
// run on a Docker network with no route out; the only way out is the filtering
// proxy in proxy.go, which applies a per-session Policy.
package egress

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// Mode is how much of the network a session may reach.
type Mode string

const (
	// ModeInternet allows the public internet and the session's git servers
	// and allowed addresses, and blocks private networks. The default.
	ModeInternet Mode = "internet"
	// ModeAgentOnly allows only the agent's own service hosts and the
	// session's git servers.
	ModeAgentOnly Mode = "agent-only"
	// ModeFull applies no filtering at all.
	ModeFull Mode = "full"
)

// ParseMode validates a mode name.
func ParseMode(s string) (Mode, bool) {
	switch m := Mode(s); m {
	case ModeInternet, ModeAgentOnly, ModeFull:
		return m, true
	}
	return "", false
}

// Entry is one allowed destination: a host name, an IP address or a network
// (CIDR), optionally limited to one port.
type Entry struct {
	Host string     // lower-case host name
	IP   net.IP     // exact address
	Net  *net.IPNet // address range
	Port int        // 0 means any port
	text string
}

func (e Entry) String() string { return e.text }

// ParseEntry reads "name", "name:port", "10.0.0.5", "10.0.0.5:5432",
// "192.168.0.0/24", "192.168.0.0/24:5432" or "[fd00::1]:80".
func ParseEntry(s string) (Entry, error) {
	orig := strings.TrimSpace(s)
	s = orig
	if s == "" {
		return Entry{}, fmt.Errorf("empty address")
	}
	if strings.ContainsAny(s, " \t/\\") && !strings.Contains(s, "/") {
		return Entry{}, fmt.Errorf("%q has spaces or slashes", orig)
	}
	var port int
	switch {
	case strings.HasPrefix(s, "["):
		host, p, err := net.SplitHostPort(s)
		if err != nil {
			return Entry{}, fmt.Errorf("%q: %v", orig, err)
		}
		s = host
		port, err = parsePort(p)
		if err != nil {
			return Entry{}, fmt.Errorf("%q: %v", orig, err)
		}
	case strings.Count(s, ":") == 1:
		i := strings.LastIndex(s, ":")
		var err error
		if port, err = parsePort(s[i+1:]); err != nil {
			return Entry{}, fmt.Errorf("%q: %v", orig, err)
		}
		s = s[:i]
	}
	e := Entry{Port: port, text: orig}
	switch {
	case strings.Contains(s, "/"):
		_, n, err := net.ParseCIDR(s)
		if err != nil {
			return Entry{}, fmt.Errorf("%q is not a valid network range", orig)
		}
		e.Net = n
	case net.ParseIP(s) != nil:
		e.IP = net.ParseIP(s)
	default:
		if !validHostname(s) {
			return Entry{}, fmt.Errorf("%q is not a valid host name or address", orig)
		}
		e.Host = strings.ToLower(s)
	}
	return e, nil
}

// refusedAddress reports whether the entry names loopback, an unspecified address or a
// link-local address (169.254.x.x, where cloud metadata services live). Allowing one would
// not let a sandbox reach its own localhost (that traffic never goes through the proxy);
// it would let it reach the proxy's own services or the host's metadata endpoint.
func (e Entry) refusedAddress() bool {
	bad := func(ip net.IP) bool { return ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() }
	switch {
	case e.Host != "":
		return e.Host == "localhost" || strings.HasSuffix(e.Host, ".localhost")
	case e.IP != nil:
		return bad(e.IP)
	case e.Net != nil:
		for _, probe := range []string{"127.0.0.1", "::1", "169.254.169.254"} {
			if e.Net.Contains(net.ParseIP(probe)) {
				return true
			}
		}
	}
	return false
}

func parsePort(p string) (int, error) {
	n, err := strconv.Atoi(p)
	if err != nil || n < 1 || n > 65535 {
		return 0, fmt.Errorf("port must be 1 to 65535")
	}
	return n, nil
}

func validHostname(h string) bool {
	if h == "" || len(h) > 253 {
		return false
	}
	for _, r := range h {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '.', r == '_':
		default:
			return false
		}
	}
	return !strings.HasPrefix(h, ".") && !strings.HasPrefix(h, "-")
}

// ParseEntries parses a list, skipping blank lines. It reports the first bad one.
func ParseEntries(lines []string) ([]Entry, error) {
	var out []Entry
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		e, err := ParseEntry(l)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

// matches reports whether the destination (as named by the client, resolved
// to ip) is covered by this entry.
func (e Entry) matches(host string, ip net.IP, port int) bool {
	if e.Port != 0 && e.Port != port {
		return false
	}
	switch {
	case e.Host != "":
		return strings.EqualFold(e.Host, host)
	case e.IP != nil:
		return e.IP.Equal(ip)
	case e.Net != nil:
		return e.Net.Contains(ip)
	}
	return false
}

// Policy is what one session may reach.
type Policy struct {
	SessionID string
	Mode      Mode
	// Entries are always allowed: the session's git servers and, in internet
	// mode, the owner's allowed addresses.
	Entries []Entry
	// AgentHosts are the host names the agent needs (used in agent-only mode).
	AgentHosts []string
}

// decide says whether a connection may go to ip (which the client reached by
// naming host) on port. The reason explains a refusal.
func (p *Policy) decide(host string, ip net.IP, port int) (bool, string) {
	if p.Mode == ModeFull {
		return true, ""
	}
	for _, e := range p.Entries {
		if e.matches(host, ip, port) {
			return true, ""
		}
	}
	public := isPublic(ip)
	switch p.Mode {
	case ModeInternet:
		if public {
			return true, ""
		}
		return false, "private network address"
	case ModeAgentOnly:
		if public && hostIn(p.AgentHosts, host) {
			return true, ""
		}
		return false, "not one of the agent's own hosts"
	}
	return false, "unknown network mode"
}

func hostIn(list []string, host string) bool {
	for _, h := range list {
		if strings.EqualFold(h, host) {
			return true
		}
	}
	return false
}

var nonPublic = mustCIDRs(
	"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15",
	"198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "255.255.255.255/32",
	"64:ff9b::/96", "2001:db8::/32", "100::/64",
)

func mustCIDRs(list ...string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(list))
	for _, c := range list {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			panic(err)
		}
		out = append(out, n)
	}
	return out
}

// isPublic reports whether ip is an ordinary internet address: not loopback,
// private (RFC 1918 and IPv6 unique-local), link-local, carrier-grade NAT
// (which includes Tailscale), multicast, unspecified or reserved.
func isPublic(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4 // treats ::ffff:10.0.0.1 as 10.0.0.1
	}
	if ip.IsUnspecified() || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() || ip.IsMulticast() {
		return false
	}
	for _, n := range nonPublic {
		if n.Contains(ip) {
			return false
		}
	}
	return true
}

// Refusal is why the owner may not add this entry to the allowed list, or nil. It is
// checked where the owner adds entries (the proxy itself still honours any entry, so its
// own tests can allow a local test server).
func (e Entry) Refusal() error {
	if e.refusedAddress() {
		return fmt.Errorf("%q cannot be allowed: it is the proxy's own address (or one a sandbox must never reach, such as a cloud metadata service), not the sandbox's. A server running inside the sandbox on localhost needs no permission: tools reach it directly", e.text)
	}
	return nil
}
