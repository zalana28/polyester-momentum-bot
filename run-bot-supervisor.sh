#!/bin/zsh
# Supervisor v4 (config-aware) — dibaca dari bot.config. Jangan edit manual;
# gunakan ./bot.sh. Hanya SATU supervisor aktif pada satu waktu (pidfile).
cd "$(dirname "$0")" || exit 1
mkdir -p logs state-dip
echo $$ > logs/supervisor.pid
cleanup_exit() { rm -f logs/supervisor.pid; exit 0; }
trap cleanup_exit INT TERM

cfg() { awk -F= -v k="$1" '$1==k{print $2; exit}' bot.config; }
MODE="$(cfg MODE)"; PAIRS="$(cfg PAIRS)"; BUDGET="$(cfg BUDGET_USD)"
REST="$(cfg REST_SEC)"; MINFREE="$(cfg MIN_FREE_USD)"

while true; do
  if [ -z "$(dig +short api.testnet.polyester.com @1.1.1.1 2>/dev/null | head -1)" ]; then
    echo "[$(date -u +%H:%M:%S)] DNS mati — tunggu 30s" >> logs/supervisor.log
    sleep 30; continue
  fi
  [ "$MODE" = "off" ] && { echo "MODE=off — supervisor keluar" >> logs/supervisor.log; break; }
  ./bin/cancelown >/dev/null 2>&1
  for p in ${(s:,:)PAIRS}; do
    for t in 1 2 3 4 5; do
      if [ "$MODE" = "vip" ]; then MAX_SLIPPAGE_BPS=200 ./bin/sell "$p" >/dev/null 2>&1; else break; fi
      [ $? -eq 0 ] && break; sleep 4
    done
  done
  if [ "$MODE" = "vip" ]; then
    free=$(./bin/freeusdt 2>/dev/null | tail -1)
    if [ "$free" = "ERR" ]; then
      echo "[$(date -u +%H:%M:%S)] baca saldo gagal — coba lagi 20s" >> logs/supervisor.log
      sleep 20; continue
    fi
    ok=$(awk -v f="$free" -v m="$MINFREE" 'BEGIN{print (f>=m)?1:0}')
    if [ "$ok" != "1" ]; then
      # konfirmasi ganda: 1x baca bisa basi/0 palsu — cek ulang 15 dtk
      echo "[$(date -u +%H:%M:%S)] saldo rendah ($free) — konfirmasi ulang 15s" >> logs/supervisor.log
      sleep 15
      # percaya "habis" hanya setelah upaya pemulihan inventory berulang:
      # posisi nyangkut yang gagal dijual pertama kali sering laku di retry kedua
      for r in 1 2 3; do
        for p in ${(s:,:)PAIRS}; do
          MAX_SLIPPAGE_BPS=200 ./bin/sell "$p" >/dev/null 2>&1
        done
        sleep 8
        free2=$(./bin/freeusdt 2>/dev/null | tail -1)
        ok2=$(awk -v f="$free2" -v m="$MINFREE" 'BEGIN{print (f>=m)?1:0}')
        [ "$ok2" = "1" ] && break
      done
      if [ "$free2" != "ERR" ] && [ "$ok2" != "1" ]; then
        echo "[$(date -u +%H:%M:%S)] SALDO HABIS terkonfirmasi ($free2, inventory habis dijual) — PERLU FAUCET." >> logs/supervisor.log
        break
      fi
      free="$free2"
    fi
    echo "[$(date -u +%H:%M:%S)] dana bebas: $free (min $MINFREE)" >> logs/supervisor.log
    CHURN_SKIP_PREVIEW=1 CHURN_SYMBOLS="$PAIRS" CHURN_BUDGET="$BUDGET" CHURN_CYCLES=1000000 CHURN_REST_SEC="$REST" \
      ./bin/pmom-churn >> logs/churn-vip.log 2>&1
    echo "[$(date -u +%H:%M:%S)] churn keluar (code $?) — self-heal 15s" >> logs/supervisor.log
    sleep 15
  else
    # MODE=dip: pakai keep-alive dip; bendera STOP menghormati loss-cap
    rm -f state-dip/STOP
    DIP_SYMBOLS="$(echo $PAIRS | tr ',' ' ')" DIP_BUDGET="$BUDGET" DIP_MAX_LOSS="$(cfg MAX_LOSS_USD)" \
      ./bin/pmom-dip >> logs/dip.log 2>&1
    echo "[$(date -u +%H:%M:%S)] dip keluar (code $?)" >> logs/supervisor.log
    [ -f state-dip/STOP ] && { echo "dip berhenti oleh loss-cap/STOP" >> logs/supervisor.log; break; }
    sleep 10
  fi
done
cleanup_exit
