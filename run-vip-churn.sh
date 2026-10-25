#!/bin/zsh
# VIP volume push v3: keputusan berdasar SALDO NYATA (bukan exit code).
# Siklus: tunggu DNS -> bersihkan order/trigger/inventory -> cek dana bebas ->
# cukup (>= MIN_FREE) ? churn jalan : berhenti permanen (perlu faucet).
cd "$(dirname "$0")" || exit 1
MIN_FREE=500
BUDGET=600
while true; do
  if [ -z "$(dig +short api.testnet.polyester.com @1.1.1.1 2>/dev/null | head -1)" ]; then
    echo "[$(date -u +%H:%M:%S)] DNS mati — tunggu 30s"
    sleep 30
    continue
  fi
  ./bin/cancelown >/dev/null 2>&1
  for p in USDC-USDT LTC-USDT BTC-USDT; do
    for t in 1 2 3 4 5; do
      if MAX_SLIPPAGE_BPS=200 ./bin/sell "$p" >/dev/null 2>&1; then break; fi
      sleep 4
    done
  done
  free=$(./bin/freeusdt 2>/dev/null | tail -1)
  echo "[$(date -u +%H:%M:%S)] dana bebas: $free (min $MIN_FREE)"
  ok=$(awk -v f="$free" -v m="$MIN_FREE" 'BEGIN{print (f>=m)?1:0}')
  if [ "$ok" != "1" ]; then
    echo "[$(date -u +%H:%M:%S)] SALDO HABIS — berhenti permanen. PERLU FAUCET TOP-UP." 
    break
  fi
  CHURN_SKIP_PREVIEW=1 CHURN_SYMBOLS="USDC-USDT,LTC-USDT,BTC-USDT" CHURN_BUDGET=$BUDGET CHURN_CYCLES=1000000 CHURN_REST_SEC=4 ./bin/pmom-churn >> logs/churn-vip.log 2>&1
  echo "[$(date -u +%H:%M:%S)] churn keluar (code $?) — self-heal dalam 15s"
  sleep 15
done
