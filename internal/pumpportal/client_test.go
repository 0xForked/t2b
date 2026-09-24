package pumpportal

import (
	"testing"
	"time"
)

func drainSend(t *testing.T, c *Client) map[string]any {
	t.Helper()
	select {
	case msg := <-c.sendCh:
		return msg.(map[string]any)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for a queued subscribe message")
		return nil
	}
}

// TestWatchTradesResendsFullAccumulatedSet is a regression test for the
// production bug found live: watching a second mint was silently dropping
// the first, because each WatchTrades call sent only that one mint. If
// PumpPortal's subscribeTokenTrade replaces the watch set rather than
// adding to it, that meant every earlier-watched candidate went deaf the
// moment a new one started — matching the observed 100% "no momentum
// confirmed" failure rate on a real run with heavy scan volume.
func TestWatchTradesResendsFullAccumulatedSet(t *testing.T) {
	c := New("")
	c.WatchTrades("A")
	c.WatchTrades("B")
	c.WatchTrades("C")

	var lastKeys []string
	for i := 0; i < 3; i++ {
		msg := drainSend(t, c)
		if msg["method"] != "subscribeTokenTrade" {
			t.Fatalf("expected subscribeTokenTrade, got %v", msg["method"])
		}
		lastKeys = msg["keys"].([]string)
	}

	if len(lastKeys) != 3 {
		t.Fatalf("expected the final subscribe call to carry all 3 watched mints, got %v", lastKeys)
	}
}

func TestUnwatchTradesRemovesFromAccumulatedSet(t *testing.T) {
	c := New("")
	c.WatchTrades("A")
	c.WatchTrades("B")
	drainSend(t, c)
	drainSend(t, c)

	c.UnwatchTrades("A")

	unsub := drainSend(t, c)
	if unsub["method"] != "unsubscribeTokenTrade" {
		t.Fatalf("expected an explicit unsubscribeTokenTrade for the removed mint, got %v", unsub)
	}

	resub := drainSend(t, c)
	if resub["method"] != "subscribeTokenTrade" {
		t.Fatalf("expected a resubscribe with the reduced set, got %v", resub)
	}
	keys := resub["keys"].([]string)
	if len(keys) != 1 || keys[0] != "B" {
		t.Fatalf("expected the resubscribe to carry only the remaining mint [B], got %v", keys)
	}
}
