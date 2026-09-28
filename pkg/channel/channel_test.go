package channel

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

func TestRegistryBindStartsHandlersAndRoutes(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	if got := r.All(); got != nil {
		t.Fatalf("expected nil slice, got %#v", got)
	}
	h := &stubHandler{id: "x", route: "/channels/x/inbound"}
	if err := r.Bind(context.Background(), "main", []Handler{h}, nil); err != nil {
		t.Fatalf("bind: %v", err)
	}
	all := r.All()
	if len(all) != 1 || all[0].ID() != "x" {
		t.Fatalf("unexpected registry contents: %#v", all)
	}
	if r.AgentID() != "main" {
		t.Fatalf("agent = %q, want main", r.AgentID())
	}
	if _, ok := r.Route(http.MethodPost, "/channels/x/inbound"); !ok {
		t.Fatal("route mounted by the bound channel is not reachable")
	}
	// Method and path both take part in the lookup.
	if _, ok := r.Route(http.MethodGet, "/channels/x/inbound"); ok {
		t.Fatal("route matched on the wrong method")
	}
}

// Binding a new primary agent's channels must retire the previous agent's
// completely: its handlers stopped and its endpoints gone. Anything left
// running would keep delivering another tenant's messages into this one.
func TestRegistryBindRetiresThePreviousAgentsChannels(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	old := &stubHandler{id: "old", route: "/channels/old/inbound"}
	if err := r.Bind(context.Background(), "main", []Handler{old}, nil); err != nil {
		t.Fatalf("bind main: %v", err)
	}
	next := &stubHandler{id: "next", route: "/channels/next/inbound"}
	if err := r.Bind(context.Background(), "acme", []Handler{next}, nil); err != nil {
		t.Fatalf("bind acme: %v", err)
	}

	if !old.stopped {
		t.Error("previous agent's handler was not stopped")
	}
	if _, ok := r.Route(http.MethodPost, "/channels/old/inbound"); ok {
		t.Error("previous agent's inbound route is still mounted")
	}
	if _, ok := r.Route(http.MethodPost, "/channels/next/inbound"); !ok {
		t.Error("new agent's inbound route is not mounted")
	}
	all := r.All()
	if len(all) != 1 || all[0].ID() != "next" {
		t.Fatalf("handlers after rebind = %#v, want only the new agent's", all)
	}
	if r.AgentID() != "acme" {
		t.Fatalf("agent = %q, want acme", r.AgentID())
	}
}

// One channel that cannot start (a rejected token, an unreachable bridge) must
// not take the agent's other channels down with it.
func TestRegistryBindReportsStartFailuresAndKeepsTheRest(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	bad := &stubHandler{id: "bad", startErr: errors.New("bad token")}
	good := &stubHandler{id: "good", route: "/channels/good/inbound"}

	err := r.Bind(context.Background(), "main", []Handler{bad, good}, nil)
	if err == nil {
		t.Fatal("expected the failing channel to be reported")
	}
	all := r.All()
	if len(all) != 1 || all[0].ID() != "good" {
		t.Fatalf("handlers = %#v, want only the channel that started", all)
	}
	if _, ok := r.Route(http.MethodPost, "/channels/good/inbound"); !ok {
		t.Error("healthy channel's route is missing")
	}
}

func TestRegistryStopClearsEverything(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	h := &stubHandler{id: "x", route: "/channels/x/inbound"}
	if err := r.Bind(context.Background(), "main", []Handler{h}, nil); err != nil {
		t.Fatalf("bind: %v", err)
	}
	if err := r.Stop(context.Background()); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if !h.stopped {
		t.Error("handler was not stopped")
	}
	if r.All() != nil || r.AgentID() != "" {
		t.Fatalf("registry still bound: handlers=%#v agent=%q", r.All(), r.AgentID())
	}
	if _, ok := r.Route(http.MethodPost, "/channels/x/inbound"); ok {
		t.Error("route survived Stop")
	}
}

type stubHandler struct {
	id       string
	route    string
	startErr error
	stopped  bool
}

func (s *stubHandler) ID() string { return s.id }

func (s *stubHandler) Start(_ context.Context, add RouteAdder, _ Bus) error {
	if s.startErr != nil {
		return s.startErr
	}
	if s.route != "" && add != nil {
		add(http.MethodPost, s.route, func(http.ResponseWriter, *http.Request) {})
	}
	return nil
}

func (s *stubHandler) Stop(context.Context) error {
	s.stopped = true
	return nil
}

func TestChannelTypeAlias(t *testing.T) {
	t.Parallel()
	var _ Channel = &stubHandler{id: "x"}
	var _ Handler = &stubHandler{id: "x"}
	var _ RouteAdder = func(string, string, http.HandlerFunc) {}
}
