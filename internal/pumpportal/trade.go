package pumpportal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const tradeURL = "https://pumpportal.fun/api/trade"

// TradeClient calls PumpPortal's Lightning Trade API: a custodial endpoint
// that signs and broadcasts the order server-side against the wallet tied
// to the API key. No local Solana signing needed.
type TradeClient struct {
	apiKey string
	http   *http.Client
}

func NewTradeClient(apiKey string) *TradeClient {
	return &TradeClient{apiKey: apiKey, http: &http.Client{Timeout: 15 * time.Second}}
}

type tradeRequest struct {
	Action           string  `json:"action"`
	Mint             string  `json:"mint"`
	Amount           any     `json:"amount"` // float64 SOL/tokens, or "N%" string for sells
	DenominatedInSol string  `json:"denominatedInSol"`
	Slippage         float64 `json:"slippage"`
	PriorityFee      float64 `json:"priorityFee"`
	Pool             string  `json:"pool"`
}

type tradeResponse struct {
	Signature string   `json:"signature"`
	Error     string   `json:"error"`
	Errors    []string `json:"errors"` // PumpPortal actually returns this shape on rejection, not singular "error"
}

func (c *TradeClient) send(req tradeRequest) (string, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	httpReq, err := http.NewRequest(http.MethodPost, tradeURL+"?api-key="+c.apiKey, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return parseTradeResponse(resp.StatusCode, raw)
}

// parseTradeResponse is pulled out of send() so the response-shape handling
// (PumpPortal's actual "errors" array vs the singular "error" string some
// docs suggest) is directly testable without a real HTTP round trip.
func parseTradeResponse(statusCode int, raw []byte) (string, error) {
	var tr tradeResponse
	_ = json.Unmarshal(raw, &tr) // best-effort; fall through to raw body on parse failure

	if statusCode != http.StatusOK || tr.Signature == "" {
		switch {
		case tr.Error != "":
			return "", fmt.Errorf("pumpportal trade rejected: %s", tr.Error)
		case len(tr.Errors) > 0:
			return "", fmt.Errorf("pumpportal trade rejected: %s", strings.Join(tr.Errors, "; "))
		}
		return "", fmt.Errorf("pumpportal trade failed (status %d): %s", statusCode, string(raw))
	}
	return tr.Signature, nil
}

// pool "auto" — not "pump" — because we can't know at order time whether a
// mint is still on the bonding curve or has already migrated to the AMM
// pool (PumpPortal rejects a bonding-curve-only pool value for a migrated
// mint with "Failed to find pump.fun bonding curve ... pool: pump-amm is
// the correct option for migrated tokens"). "auto" lets PumpPortal resolve
// the right venue itself.
const defaultPool = "auto"

// Buy spends solAmount SOL buying mint at up to slippagePct slippage.
func (c *TradeClient) Buy(mint string, solAmount, slippagePct, priorityFeeSol float64) (signature string, err error) {
	return c.send(tradeRequest{
		Action:           "buy",
		Mint:             mint,
		Amount:           solAmount,
		DenominatedInSol: "true",
		Slippage:         slippagePct,
		PriorityFee:      priorityFeeSol,
		Pool:             defaultPool,
	})
}

// SellAll sells the entire held position in mint.
func (c *TradeClient) SellAll(mint string, slippagePct, priorityFeeSol float64) (signature string, err error) {
	return c.send(tradeRequest{
		Action:           "sell",
		Mint:             mint,
		Amount:           "100%",
		DenominatedInSol: "false",
		Slippage:         slippagePct,
		PriorityFee:      priorityFeeSol,
		Pool:             defaultPool,
	})
}
