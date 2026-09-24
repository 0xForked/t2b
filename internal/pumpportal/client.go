// Package pumpportal is a thin client for the public PumpPortal data
// websocket (wss://pumpportal.fun/api/data), which streams pump.fun token
// creation and trade events. No API key required for the data feed — a key
// is only needed for PumpPortal's trade-execution endpoint, which this
// package does not call (t2b trades in paper mode only for now).
package pumpportal

import (
	"context"
	"encoding/json"
	"log"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const dataBaseURL = "wss://pumpportal.fun/api/data"

// TokenEvent is a "create" event: a brand new pump.fun token just minted.
type TokenEvent struct {
	Mint         string  `json:"mint"`
	Name         string  `json:"name"`
	Symbol       string  `json:"symbol"`
	TraderPubkey string  `json:"traderPublicKey"`
	InitialBuy   float64 `json:"initialBuy"`   // token quantity the dev bought, NOT sol
	SolAmount    float64 `json:"solAmount"`    // SOL the dev spent at creation — use this for SOL-denominated filters
	MarketCapSol float64 `json:"marketCapSol"` // implies price = MarketCapSol / TotalSupply
	Pool         string  `json:"pool"`
}

// TradeEvent is a buy/sell on an already-created mint. TraderPubkey is who
// placed it — used both for the per-mint feed a held position watches and
// for the account feed a followed wallet's trades arrive on.
type TradeEvent struct {
	Mint         string  `json:"mint"`
	Symbol       string  `json:"symbol"` // not always populated by PumpPortal; may be empty
	TxType       string  `json:"txType"` // "buy" or "sell"
	TraderPubkey string  `json:"traderPublicKey"`
	SolAmount    float64 `json:"solAmount"`
	MarketCapSol float64 `json:"marketCapSol"`
}

// Client streams pump.fun events over the PumpPortal data websocket,
// reconnecting on drop, and lets callers subscribe/unsubscribe to
// per-mint trade feeds on the fly (used to track a held paper position or
// a momentum-confirmation candidate).
//
// watchedMints/watchedWallets are this client's own authoritative record of
// what should be subscribed — not just a convenience, but load-bearing:
// every WatchTrades/UnwatchTrades call resends the FULL current mint list
// rather than a single delta, because it's unverified whether PumpPortal's
// subscribeTokenTrade adds to the existing watch set or replaces it
// outright. If it replaces, watching a second mint would otherwise go
// silently deaf on the first. Resending the full set is correct either
// way. The same state is also replayed after every reconnect, since a
// fresh connection has no memory of prior subscriptions.
type Client struct {
	NewTokens chan TokenEvent
	Trades    chan TradeEvent

	apiKey string // required for subscribeTokenTrade/subscribeAccountTrade to deliver anything — subscribeNewToken works without it
	conn   *websocket.Conn
	sendCh chan any

	mu             sync.Mutex
	watchedMints   map[string]bool
	watchedWallets []string
}

// New creates a client. apiKey is optional — the free public firehose
// (new-mint creates) works without one, but PumpPortal only delivers
// subscribeTokenTrade/subscribeAccountTrade data over an authenticated
// connection (api-key on the connection URL itself, not a subscribe
// parameter), and the linked wallet needs ≥0.02 SOL funded. Per-mint trade
// watching (momentum confirmation, TP/SL price tracking, wallet-follow)
// silently receives nothing without this.
func New(apiKey string) *Client {
	return &Client{
		NewTokens:    make(chan TokenEvent, 64),
		Trades:       make(chan TradeEvent, 256),
		apiKey:       apiKey,
		sendCh:       make(chan any, 32),
		watchedMints: make(map[string]bool),
	}
}

// Run connects and blocks, reconnecting with backoff until ctx is cancelled.
func (c *Client) Run(ctx context.Context) {
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		if err := c.runOnce(ctx); err != nil {
			log.Printf("pumpportal: %v, retrying in %s", err, backoff)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (c *Client) runOnce(ctx context.Context) error {
	dialURL := dataBaseURL
	if c.apiKey != "" {
		dialURL += "?api-key=" + url.QueryEscape(c.apiKey)
	}
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, dialURL, nil)
	if err != nil {
		return err
	}
	defer conn.Close()
	c.conn = conn

	if err := conn.WriteJSON(map[string]any{"method": "subscribeNewToken"}); err != nil {
		return err
	}

	c.mu.Lock()
	mints := c.mintList()
	wallets := append([]string(nil), c.watchedWallets...)
	c.mu.Unlock()
	if len(mints) > 0 {
		if err := conn.WriteJSON(map[string]any{"method": "subscribeTokenTrade", "keys": mints}); err != nil {
			return err
		}
	}
	if len(wallets) > 0 {
		if err := conn.WriteJSON(map[string]any{"method": "subscribeAccountTrade", "keys": wallets}); err != nil {
			return err
		}
	}

	errCh := make(chan error, 1)
	go func() {
		for {
			select {
			case msg := <-c.sendCh:
				if err := conn.WriteJSON(msg); err != nil {
					errCh <- err
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		c.dispatch(raw)
		select {
		case err := <-errCh:
			return err
		default:
		}
	}
}

func (c *Client) dispatch(raw []byte) {
	var probe struct {
		TxType string `json:"txType"`
		Mint   string `json:"mint"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return
	}
	switch probe.TxType {
	case "create":
		var ev TokenEvent
		if json.Unmarshal(raw, &ev) == nil {
			select {
			case c.NewTokens <- ev:
			default:
			}
		}
	case "buy", "sell":
		var ev TradeEvent
		if json.Unmarshal(raw, &ev) == nil {
			select {
			case c.Trades <- ev:
			default:
			}
		}
	}
}

// mintList returns the current watch set as a slice. Caller must hold c.mu.
func (c *Client) mintList() []string {
	out := make([]string, 0, len(c.watchedMints))
	for m := range c.watchedMints {
		out = append(out, m)
	}
	return out
}

// WatchTrades subscribes to trade events for the given mint (called after a
// buy, or when a momentum-confirmation candidate starts being watched).
// Resends the FULL current watch set every time — see the Client doc
// comment for why a single-mint delta isn't safe here.
func (c *Client) WatchTrades(mint string) {
	c.mu.Lock()
	c.watchedMints[mint] = true
	mints := c.mintList()
	c.mu.Unlock()
	c.sendCh <- map[string]any{"method": "subscribeTokenTrade", "keys": mints}
}

// UnwatchTrades drops one mint and resends the reduced full set — covers
// the case where PumpPortal's subscribe is additive (an explicit unsubscribe
// is also sent) and the case where it replaces (the resent set is already
// correct without it).
func (c *Client) UnwatchTrades(mint string) {
	c.mu.Lock()
	delete(c.watchedMints, mint)
	mints := c.mintList()
	c.mu.Unlock()
	c.sendCh <- map[string]any{"method": "unsubscribeTokenTrade", "keys": []string{mint}}
	if len(mints) > 0 {
		c.sendCh <- map[string]any{"method": "subscribeTokenTrade", "keys": mints}
	}
}

// WatchWallets subscribes to every trade a given set of wallet addresses
// makes, on any mint — the copy-trading feed. Safe to call before the
// connection is up; the request queues on sendCh and goes out once
// runOnce's writer goroutine is running. Stored so it also gets replayed
// on reconnect.
func (c *Client) WatchWallets(addresses []string) {
	if len(addresses) == 0 {
		return
	}
	c.mu.Lock()
	c.watchedWallets = append(c.watchedWallets, addresses...)
	c.mu.Unlock()
	c.sendCh <- map[string]any{"method": "subscribeAccountTrade", "keys": addresses}
}
