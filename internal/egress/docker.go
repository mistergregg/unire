package egress

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Names and ports used for the sandbox network.
const (
	NetworkName = "unire-sandbox" // Docker network with no route out
	Alias       = "unire-egress"  // the proxy's name on that network
	Port        = 3128
)

// Info says whether sandbox network protection is working.
type Info struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason"` // why it is not, when Available is false
	Network   string `json:"network"`
	ProxyURL  string `json:"proxyUrl"`
}

// ProxyEnv returns the environment that points a sandbox's tools at the proxy.
func (i Info) ProxyEnv() map[string]string {
	u := i.ProxyURL
	return map[string]string{
		"HTTP_PROXY": u, "HTTPS_PROXY": u, "http_proxy": u, "https_proxy": u,
		"NO_PROXY": "", "no_proxy": "",
	}
}

func docker(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func inContainer() bool {
	_, err := os.Stat("/.dockerenv")
	return err == nil
}

// Setup prepares the sandbox network and starts the proxy on it. It needs
// Unire to run inside a Docker container with the Docker socket mounted, so it
// can join the sandbox network. When that is not the case it returns an Info
// explaining why protection is unavailable, and sessions in a protected mode
// are refused rather than run unprotected.
func Setup(ctx context.Context, p *Proxy) Info {
	off := func(why string) Info { return Info{Reason: why} }

	if os.Getenv("UNIRE_EGRESS") == "off" {
		return off("turned off with UNIRE_EGRESS=off")
	}
	if !inContainer() {
		return off("Unire is not running in a Docker container, so it cannot join the sandbox network. Run the Unire container (see docs/deploy.md).")
	}
	if _, err := docker(ctx, "version", "--format", "{{.Server.Version}}"); err != nil {
		return off("Unire cannot reach Docker (is the Docker socket mounted?)")
	}

	// Create the network with no route out, or check an existing one is such.
	internal, err := docker(ctx, "network", "inspect", "--format", "{{.Internal}}", NetworkName)
	if err != nil {
		if _, err := docker(ctx, "network", "create", "--internal", "--label", "unire=1", NetworkName); err != nil {
			return off("could not create the sandbox network: " + err.Error())
		}
	} else if internal != "true" {
		return off("a Docker network named " + NetworkName + " exists but is not internal; remove it and restart Unire")
	}

	// Join it, so sandboxes can reach the proxy by name.
	self := os.Getenv("UNIRE_CONTAINER_NAME")
	if self == "" {
		self, _ = os.Hostname()
	}
	if out, err := docker(ctx, "network", "connect", "--alias", Alias, NetworkName, self); err != nil && !strings.Contains(out, "already exists") {
		return off("could not join the sandbox network (set UNIRE_CONTAINER_NAME to this container's name): " + out)
	}
	ip, err := docker(ctx, "inspect", "--format", `{{(index .NetworkSettings.Networks "`+NetworkName+`").IPAddress}}`, self)
	if err != nil || net.ParseIP(ip) == nil {
		return off("could not find this container's address on the sandbox network")
	}

	// Listen only on the sandbox network's address, not on every interface.
	l, err := net.Listen("tcp", net.JoinHostPort(ip, fmt.Sprint(Port)))
	if err != nil {
		return off("could not start the proxy: " + err.Error())
	}
	go func() { _ = p.Serve(l) }()
	return Info{Available: true, Network: NetworkName, ProxyURL: fmt.Sprintf("http://%s:%d", Alias, Port)}
}
