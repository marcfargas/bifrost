package hub_test

import (
	"io"
	"testing"

	"github.com/marcfargas/bifrost/internal/hub"
	"github.com/marcfargas/bifrost/internal/transport"
	"github.com/marcfargas/bifrost/pkg/core"
)

// nopRWC is a no-op ReadWriteCloser used to construct test Conns.
type nopRWC struct{ io.Reader }

func (nopRWC) Write(p []byte) (int, error) { return len(p), nil }
func (nopRWC) Close() error                { return nil }

func newTestConn() *transport.Conn {
	r, _ := io.Pipe()
	return transport.NewConn(nopRWC{r})
}

func TestConnManagerAddRemove(t *testing.T) {
	m := hub.NewConnManager()

	if m.Count() != 0 {
		t.Fatalf("expected count 0, got %d", m.Count())
	}

	conn := newTestConn()
	m.Add("agent-1", conn)

	if m.Count() != 1 {
		t.Fatalf("expected count 1 after Add, got %d", m.Count())
	}

	got, ok := m.Get("agent-1")
	if !ok {
		t.Fatal("expected Get to find agent-1")
	}
	if got != conn {
		t.Fatal("Get returned wrong conn")
	}

	m.Remove("agent-1")

	if m.Count() != 0 {
		t.Fatalf("expected count 0 after Remove, got %d", m.Count())
	}

	_, ok = m.Get("agent-1")
	if ok {
		t.Fatal("expected Get to return not-found after Remove")
	}
}

func TestConnManagerNotifyUnknown(t *testing.T) {
	m := hub.NewConnManager()

	notif := core.Notification{Type: "test", Payload: "hello"}
	delivered := m.Notify("nonexistent-agent", notif)
	if delivered {
		t.Fatal("expected Notify to return false for unknown agent")
	}
}
