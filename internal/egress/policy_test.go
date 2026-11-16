package egress

import (
	"net"
	"testing"
)

func TestIsPublic(t *testing.T) {
	public := []string{"8.8.8.8", "1.1.1.1", "140.82.112.3", "2606:4700:4700::1111"}
	private := []string{
		"127.0.0.1", "10.1.2.3", "172.16.0.1", "172.31.255.1", "192.168.0.1", "169.254.169.254",
		"100.64.1.1", "100.100.100.100", "0.0.0.0", "224.0.0.1", "255.255.255.255", "240.0.0.1",
		"::1", "fe80::1", "fd00::1", "::ffff:192.168.0.1", "::ffff:127.0.0.1", "64:ff9b::c0a8:1", "198.18.0.1",
	}
	for _, s := range public {
		if !isPublic(net.ParseIP(s)) {
			t.Errorf("%s should be public", s)
		}
	}
	for _, s := range private {
		if isPublic(net.ParseIP(s)) {
			t.Errorf("%s should NOT be public", s)
		}
	}
	// 172.32.x is outside RFC 1918's 172.16/12 and so is public.
	if !isPublic(net.ParseIP("172.32.0.1")) {
		t.Error("172.32.0.1 is public")
	}
}

func TestParseEntry(t *testing.T) {
	good := map[string]struct {
		host string
		port int
	}{
		"git.example.com":      {"git.example.com", 0},
		"Git.Example.com:3000": {"git.example.com", 3000},
		"192.168.0.50":         {"", 0},
		"192.168.0.50:5432":    {"", 5432},
		"192.168.0.0/24":       {"", 0},
		"192.168.0.0/24:8080":  {"", 8080},
		"[fd00::1]:80":         {"", 80},
		"fd00::/8":             {"", 0},
		"my_host-1.local":      {"my_host-1.local", 0},
	}
	for in, want := range good {
		e, err := ParseEntry(in)
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if e.Host != want.host || e.Port != want.port {
			t.Errorf("%q = host %q port %d, want %q %d", in, e.Host, e.Port, want.host, want.port)
		}
	}
	for _, in := range []string{"", "  ", "a b", "host:0", "host:99999", "host:abc", "10.0.0.0/33", "bad host!", "-x", "http://x", "a/b/c"} {
		if _, err := ParseEntry(in); err == nil {
			t.Errorf("%q should fail", in)
		}
	}
}

func TestDecideInternetMode(t *testing.T) {
	entries, err := ParseEntries([]string{"192.168.0.242:3300", "192.168.0.50", "10.9.0.0/16:5432", "nas.local"})
	if err != nil {
		t.Fatal(err)
	}
	p := Policy{Mode: ModeInternet, Entries: entries}
	cases := []struct {
		host string
		ip   string
		port int
		want bool
	}{
		{"github.com", "140.82.112.3", 443, true},
		{"example.com", "8.8.8.8", 80, true},
		{"192.168.0.1", "192.168.0.1", 80, false},      // the router
		{"192.168.0.242", "192.168.0.242", 3300, true}, // allowed git server
		{"192.168.0.242", "192.168.0.242", 22, false},  // same host, other port
		{"192.168.0.50", "192.168.0.50", 9999, true},   // any port
		{"10.9.1.1", "10.9.1.1", 5432, true},           // range with port
		{"10.9.1.1", "10.9.1.1", 22, false},
		{"nas.local", "192.168.0.77", 445, true},     // by name
		{"evil.example", "192.168.0.77", 445, false}, // a name that resolves to a LAN address
		{"localhost", "127.0.0.1", 80, false},
		{"metadata", "169.254.169.254", 80, false},
		{"x", "::ffff:10.0.0.1", 80, false},
	}
	for _, c := range cases {
		got, reason := p.decide(c.host, net.ParseIP(c.ip), c.port)
		if got != c.want {
			t.Errorf("%s (%s:%d) = %v (%s), want %v", c.host, c.ip, c.port, got, reason, c.want)
		}
		if !got && reason == "" {
			t.Errorf("%s: a refusal needs a reason", c.host)
		}
	}
}

func TestDecideAgentOnlyMode(t *testing.T) {
	git, _ := ParseEntry("192.168.0.242:3300")
	p := Policy{Mode: ModeAgentOnly, Entries: []Entry{git}, AgentHosts: []string{"api.anthropic.com"}}
	if ok, _ := p.decide("api.anthropic.com", net.ParseIP("160.79.104.10"), 443); !ok {
		t.Error("the agent's own host should be allowed")
	}
	if ok, _ := p.decide("example.com", net.ParseIP("93.184.216.34"), 443); ok {
		t.Error("other internet hosts should be blocked in agent-only mode")
	}
	if ok, _ := p.decide("192.168.0.242", net.ParseIP("192.168.0.242"), 3300); !ok {
		t.Error("the git server should be allowed")
	}
	if ok, _ := p.decide("api.anthropic.com", net.ParseIP("10.0.0.5"), 443); ok {
		t.Error("an agent host name that resolves to a private address must be blocked")
	}
}

func TestDecideFull(t *testing.T) {
	p := Policy{Mode: ModeFull}
	if ok, _ := p.decide("router", net.ParseIP("192.168.0.1"), 80); !ok {
		t.Error("full mode allows everything")
	}
}

func TestParseMode(t *testing.T) {
	for _, m := range []string{"internet", "agent-only", "full"} {
		if _, ok := ParseMode(m); !ok {
			t.Errorf("%s should parse", m)
		}
	}
	if _, ok := ParseMode("none"); ok {
		t.Error("unknown mode accepted")
	}
}
