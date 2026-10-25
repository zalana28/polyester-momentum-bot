#!/bin/zsh
# VIP volume push: tunggu DNS sehat -> bersihkan order sisa -> churn terus.
# Pasangan & budget sesuai misi VIP (USDC/LTC/BTC, market order).
cd "$(dirname "$0")" || exit 1
while true; do
  if ! dig +short api-testnet.polyester.com @1.1.1.1 >/dev/null 2>&1 || [ -z "$(dig +short api-testnet.polyester.com @1.1.1.1 2>/dev/null | head -1)" ]; then
    echo "[$(date -u +%H:%M:%S)] DNS mati — tunggu 30s"
    sleep 30
    continue
  fi
  ./bin/cancelown >/dev/null 2>&1
  echo "[$(date -u +%H:%M:%S)] DNS OK — churn mulai"
  CHURN_SYMBOLS="USDC-USDT,LTC-USDT,BTC-USDT" CHURN_BUDGET=1000 CHURN_CYCLES=1000000 CHURN_REST_SEC=2 ./bin/pmom-churn >> logs/churn-vip.log 2>&1
  echo "[$(date -u +%H:%M:%S)] churn kelur (code $?) — cek DNS lagi dalam 30s"
  sleep 30
done
