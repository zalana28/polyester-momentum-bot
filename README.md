# polyester-momentum-bot

Bot Go otomatis untuk **spot momentum trading** di Polyester **testnet**:
subscribe market data realtime, deteksi sinyal momentum, auto-buy market IOC
dengan slippage protection, dan auto-exit via trigger stop-loss/take-profit
standalone + exit sinyal. Dua mode: **paper** (simulasi, tanpa API key) dan
**live** (order testnet sungguhan).

> Testnet only. Tidak ada koneksi mainnet di konfigurasi default
> (`TestnetEnvironment` → `api-testnet.polyester.com`).

## Arsitektur

```
Feeds (candles/trades/orderbook, closed-candle only)
        │
        ▼
Strategy engine (EMA9/21 cross + ROC + volume surge)   Risk manager (gate)
        │                                                   │
        ▼                                                   ▼
Execution layer (Preview → Market IOC → SL/TP triggers) ──┘
        ▲
State + reconcile (order/trigger stream, startup reconcile, journal)
```

Single event loop: semua mutasi state terjadi di satu goroutine; stream pump
dan timer mengantre closure — bebas data race tanpa lock di handler.

### Keputusan desain penting

- **Closed-candle only**: candle dianggap final hanya ketika candle berikutnya
  muncul di stream → sinyal tidak bisa repaint. Engine di-seed dari REST
  (`GetCandles`, closed saja) dan dedupe by timestamp terhadap stream.
- **Skala harga wire terdeteksi otomatis**: pengamatan pada testnet
  menunjukkan market-data price ticks = harga sebenarnya × 10⁹ (bukan 10⁶).
  Bot mengukur skala ini saat boot dari `quote_volume/volume` candle
  (`market.DetectWirePriceScale`), bekerja internal dalam harga TRUE (skala
  1e6), dan mengkalibrasi unit order-service pada fill pertama. Override:
  `POLYESTER_PRICE_SCALE=9`.
- **Fallback REST polling**: kalau WebSocket tidak tersedia (mis. Cloudflare
  challenge di IP datacenter), feeds otomatis turun ke polling REST (candle
  tiap 5s, trade tiap 3s) dengan semantik closed-candle yang sama. Stream
  privat (live) bersifat opsional — reconcile REST menjadi jaring pengaman.
- **Idempotency**: setiap `ClientOrderID`/`ClientTriggerID` di-journal ke
  `state/journal.jsonl` (fsync) **sebelum** attempt pertama. Retry ambigu
  tidak pernah mengganti ID; lookup by ID dulu (`Orders.Get` / `Triggers.List`).
- **Rate limit**: retry dengan `RetryAfter` server + jitter; error
  validasi/auth tidak pernah di-retry.
- **Preview advisory**: `Orders.Preview` dijalankan dulu; rejection nyata
  membatalkan create; host tanpa preview dilanjutkan (sesuai kontrak SDK).
- **OCO semantics lokal**: SL dan TP dibuat sebagai dua trigger standalone
  (Go SDK tidak mendukung `AttachedRisk`). Event stream memantau: satu leg
  terminal → leg kembar dibatalkan. Reconcile memaksa invariant ini.
- **Tidak pernah naked**: fill tanpa trigger gagal → re-arm → tetap gagal →
  emergency flatten. Sell exit gagal → trigger di-re-arm sebelum retry.
- **Dead-man switch** (live): `CancelAllAfter` 15s di-refresh tiap 5s.
- **Kill switch harian**: equity turun melewati `DAILY_LOSS_LIMIT_PCT` dari
  baseline awal hari UTC → cancel semua + flatten + halt.
- **Exact math**: sizing/budget/harga trigger memakai integer berskala
  (`internal/num`, big.Rat) — tanpa float di money path.

## Persyaratan

- Go 1.26+ (SDK `polyester-sdk-go` pinned ke commit tag `v0.1.0a54`)
- Untuk mode live: API key testnet (permission **Spot trading** + ledger read,
  max order size diset) dan **Account ID** dari Profile.

## Menjalankan

```bash
cp .env.example .env    # edit sesuai kebutuhan
go run ./cmd/bot        # MODE=paper default
```

Mode live:

```bash
# isi API_KEY_ID, API_PRIVATE_KEY, ACCOUNT_ID di .env, lalu
MODE=live go run ./cmd/bot
```

Shutdown: Ctrl-C. Default `FLATTEN_ON_SHUTDOWN=true` menjual posisi terbuka
sebelum keluar; set `false` untuk membiarkan SL/TP trigger tetap armed di
server (mereka tetap mengeksekusi tanpa proses bot hidup).

## Verifikasi

```bash
go build ./... && go vet ./... && go test ./...
```

Checklist mode paper: bot hidup ≥10 menit tanpa error feed, log sinyal
BUY/EXIT + PnL virtual, reconnect/overflow tertangani.
Checklist mode live (butuh API key): satu siklus penuh order kecil — buy →
SL/TP terpasang → exit/SL/TP mengeksekusi → `ListOpen` kosong, posisi flat.

Status terverifikasi (2026-09-29): mode paper dijalankan ~8 menit melawan
data live testnet dengan parameter cepat — siklus penuh BUY → SL trigger
menembak → sibling dibatalkan → posisi ditutup dengan realized PnL benar
(-0.025 USDT = 0.1% × 25). Mode live belum dijalankan (menunggu API key).

Alat diagnostik:

```bash
go run ./cmd/wsprobe       # status handshake WebSocket testnet
go run ./cmd/candleprobe   # harga candle/trade mentah + skala wire
```

## Struktur

| Path | Isi |
|---|---|
| `cmd/bot/main.go` | entrypoint + signal handling |
| `cmd/wsprobe`, `cmd/candleprobe` | diagnostik koneksi & skala harga |
| `internal/config` | env + `.env`, validasi |
| `internal/num` | matematika decimal/scaled/tick eksak (big.Rat) |
| `internal/strategy` | EMA/ROC/volume, closed-candle, testable |
| `internal/journal` | append-only JSONL fsync (idempotency) |
| `internal/state` | posisi, halt, baseline equity, mark |
| `internal/risk` | decider loss-limit harian (pure) |
| `internal/execution` | interface `Executor` + `Live` (SDK) & `Paper` |
| `internal/market` | feeds (WS + fallback REST-poll) + deteksi skala harga |
| `internal/bot` | event loop, sinyal→eksekusi, reconcile |

## Catatan operasional

- Fee: BUY memakai `FeeAsset=quote` sehingga base yang diterima = qty fill
  penuh (fee tidak menggerus base qty yang dijual trigger).
- Partial fill IOC (status `canceled` dengan `cum_qty > 0`) diterima sebagai
  fill parsial dan tetap dilindungi trigger.
- Dust (di bawah `min_qty` pair) tidak bisa dijual — tracking ditutup dan
  dicatat di journal.
- Journal di `state/` menyimpan riwayat intent untuk reconcile; hapus folder
  `state/` hanya saat yakin tidak ada posisi/trigger aktif.
