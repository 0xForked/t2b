# t2b

?????

**Starts in paper mode. No real funds move unless you pass `--live` and type `ARM`.**

## Install

```
go build -o t2b .
```

Go 1.27+. Optional AI backends: [Claude Code](https://claude.com/claude-code),
[Antigravity](https://antigravity.google), or Apple Foundation Fr CLI on `PATH` — or build `t2bfm` for a free,
fully local option (macOS 26+/Apple Silicon):

```
cd swift/t2bfm && swift build -c release
cp .build/release/t2bfm /usr/local/bin/
```

## Quick start

```
./t2b dashboard
```

First run walks you through wallet setup automatically. Press `q` to quit.

## Commands

```
t2b init                                     set up wallet + config
t2b dashboard                                run in paper mode
t2b dashboard --ai [--ai-provider claude|gemini|antigravity|applefm]
t2b dashboard --live                         real PumpPortal Lightning orders
t2b follow add|remove|list <address>         copy-trade a wallet's buys
```

## How it decides

New mint → **filter** (dev buy size, mcap; AI opinion layered on top if `--ai`) →
**risk** check (position cap, equity) → **buy** (paper or live) → **position**
tracked with hard exits (+50% TP / -20% SL / 3min timeout, AI can also flag an
early sell) → **logbook**. The SCANNER panel shows every token seen, including
rejects and why.

Wallets can also be copy-traded directly (`t2b follow add`, skips the mint
filters) — manually, or auto-promoted after a win streak.

## Going live

Needs a PumpPortal API key in config. `t2b dashboard --live` then type `ARM` to
confirm (asked every run). No on-chain balance check beyond `Params` in
`internal/engine/engine.go` — read it first.

## Storage

```
~/.t2b/config.json        settings, wallet pubkey, follow list
~/.t2b/wallet.enc         encrypted private key (scrypt + nacl secretbox)
~/.t2b/logs/trades.jsonl  append-only trade journal
```

Nothing is auto-deleted; rotate the journal yourself if it grows too large.

## Layout

```
main.go              CLI entry
internal/config/      config.json
internal/wallet/      keypair + encryption
internal/pumpportal/  feed + trade API client
internal/engine/      trading pipeline, paper/live bookkeeping
internal/brain/       AI backends + prompts
internal/journal/     JSONL trade log
internal/tui/         dashboard + init wizard
swift/t2bfm/          on-device Apple FoundationModels CLI (macOS 26+ only)
```

## Tests

```
go test ./...
```

## Disclaimer

pump.fun tokens are extremely high risk. Not financial advice. Paper-mode PnL
uses feed price, not confirmed on-chain fills.
