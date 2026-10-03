# routes.md — polyester-momentum-bot

| URL | Handler | Renders |
|---|---|---|
| `GET /` | `cmd/office/main.go` mux `/` | `cmd/office/index.html` (Three.js office) |
| `GET /api/state` | `apiState` | JSON `{total_net,cycles,wins,losses,events[25 newest],trades[6]}` |

Event kinds emitted by parser `kindOf()`: momentum, filled, sell, sl, skip, profit, loss.
Source of truth: `logs/dip.log` lines `[HH:MM:SS] MSG` (Indonesian keywords today),
`state-dip/stats.json`, `state-dip/trades.csv`.
Other binaries (not web): `bin/pmom-dip` (bot), `bin/dashboard` (ANSI TUI), `bin/dumpcandles`, `bin/probe`, `bin/cancelown`, `bin/rescue*`.
