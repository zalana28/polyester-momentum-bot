# pages.md — polyester-momentum-bot

## / (Office 3D)
Entry: `cmd/office/index.html`
Dependencies:
- three@0.160.0 via unpkg CDN (only external dep)
- `/api/state` JSON from `cmd/office/main.go`
  - reads `logs/dip.log` (regex `^\[(\d\d:\d\d:\d\d)\] (.*)$`, kindOf() classifier)
  - reads `state-dip/stats.json` (total_net, cycles, wins, losses, per_pair)
  - reads `state-dip/trades.csv` (last 6 rows)

Single page; no router, no other pages. `bin/dashboard` (cmd/dashboard) is a
separate ANSI TUI, not web.
