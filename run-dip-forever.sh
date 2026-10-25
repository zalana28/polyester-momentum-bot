#!/bin/zsh
# Keep-alive: restart bot kalau mati karena sinyal misterius (context canceled),
# TIDAK restart kalau loss-cap / user stop (file state-dip/STOP ada).
cd "$(dirname "$0")" || exit 1
while true; do
  [ -f state-dip/STOP ] && { echo "STOP flag ada — keep-alive berhenti"; break; }
  DIP_SYMBOLS=all DIP_BUDGET=1000 DIP_MOM_BUDGET=1000 DIP_MOM_SCORE=65 DIP_MOM_TARGET_BPS=60 DIP_MOM_MAX_HOLD_MIN=20 DIP_OFFSET_BPS=30 DIP_TARGET_BPS=60 DIP_SL_BPS=120 DIP_REPRICE_SEC=90 DIP_MAX_HOLD_MIN=45 DIP_REST_SEC=2 DIP_MAX_LOSS=25 DIP_MOM_BUDGET=500 ./bin/pmom-dip >> logs/dip.log 2>&1
  code=$?
  echo "[$(date -u +%H:%M:%S)] bot keluar (code $code) — keep-alive cek flag..."
  [ -f state-dip/STOP ] && break
  sleep 5
done
