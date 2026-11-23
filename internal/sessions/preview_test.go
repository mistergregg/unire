package sessions

import (
	"strings"
	"testing"

	"github.com/mistergregg/unire/internal/egress"
)

func startedSession(t *testing.T, m *Manager, mode egress.Mode) string {
	t.Helper()
	r := req("")
	r.Network = mode
	s, err := m.Start(r)
	if err != nil {
		t.Fatal(err)
	}
	m.Wait()
	return s.ID
}

func TestPreviewAccessFollowsTheChatsMode(t *testing.T) {
	for _, mode := range []egress.Mode{egress.ModeInternet, egress.ModeAgentOnly, egress.ModeFull} {
		m, _, proxy, _ := protectedSetup(t, &fakeSandbox{}, true, "nas.local")
		id := startedSession(t, m, mode)
		a, err := m.PreviewAccess(id)
		if err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		if a.Network != "unire-sandbox" || a.Env["HTTPS_PROXY"] != "http://unire-egress:3128" {
			t.Fatalf("%s: access %+v", mode, a)
		}
		if a.Workspace != "unire-ws-"+id || a.Root != id {
			t.Fatalf("%s: workspace %q root %q", mode, a.Workspace, a.Root)
		}
		unregister := a.Register("172.30.0.77")
		pol, ok := proxy.Policy("172.30.0.77")
		if !ok || pol.Mode != mode {
			t.Fatalf("%s: the proxy must hold the chat's mode for the preview, got %+v ok=%v", mode, pol, ok)
		}
		unregister()
		if _, ok := proxy.Policy("172.30.0.77"); ok {
			t.Fatalf("%s: a stopped preview must be forgotten by the proxy", mode)
		}
	}
}

func TestPreviewPolicyKeepsTheAllowListAndAnswersLikeTheChat(t *testing.T) {
	m, _, proxy, _ := protectedSetup(t, &fakeSandbox{}, true, "192.168.0.50:5432")
	id := startedSession(t, m, egress.ModeInternet)
	a, _ := m.PreviewAccess(id)
	a.Register("172.30.0.78")
	if ok, _ := proxy.Decide("172.30.0.78", "pub.dev", []byte{151, 101, 1, 1}, 443); !ok {
		t.Error("internet only must let a preview download packages")
	}
	if ok, _ := proxy.Decide("172.30.0.78", "router", []byte{192, 168, 0, 1}, 80); ok {
		t.Error("internet only must keep a preview off the home network")
	}
	if ok, _ := proxy.Decide("172.30.0.78", "nas", []byte{192, 168, 0, 50}, 5432); !ok {
		t.Error("an address on the owner's allowed list must work for a preview too")
	}
}

func TestAgentOnlyPreviewCannotInstallPackages(t *testing.T) {
	m, _, proxy, _ := protectedSetup(t, &fakeSandbox{}, true)
	id := startedSession(t, m, egress.ModeAgentOnly)
	a, _ := m.PreviewAccess(id)
	a.Register("172.30.0.79")
	if ok, _ := proxy.Decide("172.30.0.79", "pub.dev", []byte{151, 101, 1, 1}, 443); ok {
		t.Error("agent only must block package hosts for a preview as for the agent")
	}
}

func TestPreviewAccessUsesTheConversationRoot(t *testing.T) {
	m, _, _, _ := protectedSetup(t, &fakeSandbox{}, true)
	first := startedSession(t, m, egress.ModeInternet)
	r := req("")
	r.Network = egress.ModeInternet
	r.Parent = first
	second, err := m.Start(r)
	if err != nil {
		t.Fatal(err)
	}
	m.Wait()
	a, err := m.PreviewAccess(second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if a.Root != first || a.Workspace != "unire-ws-"+first {
		t.Fatalf("a later turn must use the conversation's workspace: %+v", a)
	}
}

func TestPreviewAccessNeedsProtectionAndAKnownChat(t *testing.T) {
	m, _, _, _ := protectedSetup(t, &fakeSandbox{}, false)
	id := startedSession(t, m, egress.ModeFull)
	_, err := m.PreviewAccess(id)
	if err == nil || !strings.Contains(err.Error(), "test says no") {
		t.Fatalf("want the reason protection is off, got %v", err)
	}
	m2, _, _, _ := protectedSetup(t, &fakeSandbox{}, true)
	if _, err := m2.PreviewAccess("nope"); err == nil {
		t.Fatal("an unknown chat must be an error")
	}
}
