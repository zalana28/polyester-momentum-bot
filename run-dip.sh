#!/bin/zsh
cd "$(dirname "$0")" || exit 1
echo "=== DIP MAKER MEAN-REVERSION — \$300/cycle, semua pair (Ctrl+C stop) ==="
DIP_SYMBOLS=all DIP_BUDGET=2500 DIP_OFFSET_BPS=30 DIP_TARGET_BPS=60 DIP_SL_BPS=120 DIP_REPRICE_SEC=90 DIP_MAX_HOLD_MIN=45 DIP_REST_SEC=2 DIP_MAX_LOSS=25 ./bin/pmom-dip 2>&1 | tee -a logs/dip.log
DIP_MOM_BUDGET=500
