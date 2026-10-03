# layouts.md — polyester-momentum-bot

Single-page app; no shared layout components exist. The whole layout lives in
`cmd/office/index.html` body:

```html
<div id="ui">        <!-- top-left HUD: title "🏢 Kantor AI — Tim Trading Bot", PnL total, cycles W/L -->
<div id="feed">      <!-- top-right: last 9 bot events, .fitem cards (blurred translucent) -->
<div id="help">      <!-- bottom-left: control hints -->
<script> Three.js scene fills viewport; camera isometric, drag to orbit, wheel zoom
```

Server `cmd/office/main.go`: mux with `/` → ServeFile(index.html), `/api/state` → JSON built from
`logs/dip.log` tail + `state-dip/stats.json` + `state-dip/trades.csv`.
