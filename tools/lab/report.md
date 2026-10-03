# Laporan Lab Backtest Polyester (candle 5m)

Data: 38328 candle, 14 pair. Fee maker 10bps, taker 15bps. SL dicek sebelum target (pesimis). Budget simulasi $2500/trade.

## Dip maker - grid parameter (agregat semua pair)

| off | tp | sl | trades | winrate | net $ | avg $ |
|---|---|---|---|---|---|---|
| 45 | 40 | 80 | 1995 | 69% | -4522.79 | -2.267 |
| 45 | 40 | 120 | 1947 | 70% | -5865.87 | -3.013 |
| 45 | 40 | 160 | 1934 | 72% | -5947.09 | -3.075 |
| 45 | 60 | 80 | 1790 | 44% | -9981.91 | -5.576 |
| 30 | 40 | 80 | 2744 | 59% | -10629.75 | -3.874 |
| 45 | 60 | 120 | 1713 | 47% | -10710.43 | -6.252 |
| 45 | 90 | 80 | 1661 | 36% | -10751.07 | -6.473 |
| 45 | 60 | 160 | 1687 | 48% | -11159.73 | -6.615 |
| 30 | 40 | 120 | 2655 | 61% | -11522.36 | -4.340 |
| 45 | 90 | 120 | 1580 | 38% | -11751.21 | -7.437 |

**Kombinasi terbaik: offset 15bps, target +40bps, SL -80bps -> net $-19644.82 dari 3808 trade.**

## Dip maker - per pair, param default (30/60/120)

| pair | trades | winrate | net $ |
|---|---|---|---|
| AVAX-USDT | 181 | 39% | -1725.68 |
| BCH-USDT | 211 | 52% | -604.47 |
| BNB-USDT | 110 | 35% | -427.16 |
| BTC-USDT | 117 | 35% | -512.63 |
| DOGE-USDT | 208 | 49% | -568.75 |
| ETC-USDT | 204 | 50% | -727.75 |
| ETH-USDT | 120 | 26% | -1708.90 |
| HYPE-USDT | 279 | 47% | -2773.95 |
| LTC-USDT | 206 | 43% | -1092.54 |
| POL-USDT | 226 | 57% | -676.39 |
| SOL-USDT | 178 | 44% | -1023.93 |
| TRX-USDT | 41 | 10% | -196.34 |
| USDC-USDT | 56 | 25% | -1275.91 |
| XRP-USDT | 178 | 44% | -840.62 |

**Total default (30/60/120): $-14155.01**

## Dip maker - per pair, param terbaik (15/40/80)

| pair | trades | winrate | net $ |
|---|---|---|---|
| AVAX-USDT | 253 | 46% | -2071.72 |
| BCH-USDT | 372 | 64% | -1117.45 |
| BNB-USDT | 210 | 35% | -970.52 |
| BTC-USDT | 258 | 38% | -1082.08 |
| DOGE-USDT | 336 | 62% | -936.49 |
| ETC-USDT | 354 | 62% | -1076.11 |
| ETH-USDT | 186 | 20% | -1951.70 |
| HYPE-USDT | 376 | 50% | -2964.14 |
| LTC-USDT | 329 | 54% | -1513.88 |
| POL-USDT | 341 | 62% | -1476.14 |
| SOL-USDT | 285 | 52% | -1399.90 |
| TRX-USDT | 158 | 14% | -724.17 |
| USDC-USDT | 65 | 17% | -1183.65 |
| XRP-USDT | 285 | 53% | -1176.87 |

**Total terbaik (15/40/80): $-19644.82**

## Momentum hybrid - per pair (entry taker, budget $500)

| pair | trades | winrate | net $ |
|---|---|---|---|
| AVAX-USDT | 43 | 28% | -85.01 |
| BCH-USDT | 31 | 19% | -53.35 |
| BNB-USDT | 24 | 8% | -39.06 |
| BTC-USDT | 21 | 10% | -35.64 |
| DOGE-USDT | 39 | 13% | -70.31 |
| ETC-USDT | 36 | 14% | -63.87 |
| ETH-USDT | 20 | 10% | -53.97 |
| HYPE-USDT | 51 | 22% | -127.48 |
| LTC-USDT | 49 | 24% | -62.95 |
| POL-USDT | 36 | 36% | -37.78 |
| SOL-USDT | 53 | 28% | -85.61 |
| TRX-USDT | 5 | 0% | -9.40 |
| USDC-USDT | 14 | 7% | -57.16 |
| XRP-USDT | 29 | 17% | -47.16 |

**Total momentum: $-828.76 dari 451 trade**

Catatan: simulasi candle-level tidak memodelkan posisi antrean order book; hasil arah, bukan jaminan.