package websocket

import (
	"fmt"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v2/logger"

	"github.com/op/go-logging"
)

// The hub logs on every path exercised here, and the package logger is nil until
// initialised — dereferencing it panics inside the hub's own recover(), which then
// restarts the loop and masks the failure.
func init() { logger.InitLogger(logging.CRITICAL) }

// The hub loop is the ONLY reader of h.unregister, and broadcastParallel runs inside
// that loop and waits for its workers. So a worker that blocks sending to
// h.unregister deadlocks the whole hub: the workers wait for the loop to drain the
// channel and the loop waits for the workers. The 100-slot buffer hid it until more
// than 100 clients were slow on the same tick, at which point the hub stopped serving
// broadcasts, connects and disconnects permanently.
func TestBroadcastDoesNotDeadlockWhenEveryClientIsStuck(t *testing.T) {
	h := NewHub()
	go h.Run()
	defer h.Stop()

	// Comfortably past both the unregister buffer (100) and the worker-pool
	// threshold, so this exercises the pooled branch with a full channel.
	const n = 250
	for i := 0; i < n; i++ {
		c := &Client{
			ID: fmt.Sprintf("stuck-%d", i),
			// Capacity 0: every send hits the `default` arm and unregisters.
			Send:   make(chan []byte),
			Hub:    h,
			Topics: map[MessageType]bool{},
		}
		h.Register(c)
	}

	// Let the registrations land before broadcasting.
	deadline := time.Now().Add(2 * time.Second)
	for h.GetClientCount() < n && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := h.GetClientCount(); got != n {
		t.Fatalf("registered %d of %d clients", got, n)
	}

	h.Broadcast(MessageType("test"), map[string]any{"x": 1})

	// The hub must still be answering. GetClientCount takes the hub's own mutex but
	// not its loop, so prove liveness the way a real caller would: register one more
	// client and wait for the LOOP to pick it up.
	probe := &Client{
		ID:     "probe",
		Send:   make(chan []byte, 8),
		Hub:    h,
		Topics: map[MessageType]bool{},
	}
	done := make(chan struct{})
	go func() {
		h.Register(probe)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Register blocked: the hub loop is deadlocked on the broadcast path")
	}

	// And the stuck clients must actually have been dropped, not merely survived.
	deadline = time.Now().Add(5 * time.Second)
	for h.GetClientCount() > n && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if h.GetClientCount() > n {
		t.Fatalf("clients with a full send buffer were never unregistered (count=%d)", h.GetClientCount())
	}
}

// A client that can receive must still get the payload.
func TestBroadcastReachesAHealthyClient(t *testing.T) {
	h := NewHub()
	go h.Run()
	defer h.Stop()

	c := &Client{
		ID:     "ok",
		Send:   make(chan []byte, 4),
		Hub:    h,
		Topics: map[MessageType]bool{},
	}
	h.Register(c)

	deadline := time.Now().Add(2 * time.Second)
	for h.GetClientCount() < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	h.Broadcast(MessageType("test"), map[string]any{"x": 1})

	select {
	case msg := <-c.Send:
		if len(msg) == 0 {
			t.Fatal("empty payload delivered")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a healthy client received nothing")
	}
}
