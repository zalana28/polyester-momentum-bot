#!/bin/zsh
# ==========================================================
#  PANEL KONTROL BOT — jalankan: ./bot.sh
#  Semua pengaturan di bot.config (menu 4) & .env (menu 5).
# ==========================================================
cd "$(dirname "$0")" || exit 1
mkdir -p logs state-dip

cfg() { awk -F= -v k="$1" '$1==k{print $2; exit}' bot.config; }
setcfg() { sed -i '' "s|^$1=.*|$1=$2|" bot.config; }

bot_pid()   { pgrep -f "bin/pmom-churn|bin/pmom-dip" | head -1; }
sup_pid()   { [ -f logs/supervisor.pid ] && kill -0 $(cat logs/supervisor.pid) 2>/dev/null && cat logs/supervisor.pid; }
sup_alive() { [ -n "$(pgrep -f 'run-bot-supervisor.sh|run-vip-churn.sh|run-dip-forever.sh')" ] || [ -n "$(sup_pid)" ]; }

menu_status() {
  echo "════════════ STATUS BOT ════════════"
  echo "Mode      : $(cfg MODE)   Pair: $(cfg PAIRS)"
  echo "Budget    : \$$(cfg BUDGET_USD)/trade   Jeda: $(cfg REST_SEC)s"
  if [ -n "$(bot_pid)" ]; then echo "Bot       : 🟢 JALAN (PID $(bot_pid))"; else echo "Bot       : 🔴 MATI"; fi
  if sup_alive; then echo "Supervisor: 🟢 jalan"; else echo "Supervisor: 🔴 mati"; fi
  echo "Saldo     : USDT bebas \$$(./bin/freeusdt 2>/dev/null | tail -1 | cut -d. -f1)"
  echo "Siklus VIP selesai: $(grep -cE 'PnL siklus' logs/churn-vip.log 2>/dev/null || echo 0)"
  echo "════════════════════════════════════"
}

menu_start() {
  if [ "$(cfg MODE)" = "off" ]; then echo "MODE=off di bot.config — ubah dulu di menu 4/6."; return; fi
  if sup_alive || [ -n "$(bot_pid)" ]; then echo "Bot sudah jalan. Stop dulu (menu 3) kalau mau restart."; return; fi
  nohup ./run-bot-supervisor.sh >> logs/supervisor.log 2>&1 &
  sleep 2
  echo "✅ Bot MODE=$(cfg MODE) dinyalakan (supervisor jalan). Pantau: ./bot.sh (1)"
}

menu_stop() {
  touch state-dip/STOP
  pkill -INT -f "bin/pmom-churn" 2>/dev/null
  pkill -INT -f "bin/pmom-dip" 2>/dev/null
  pkill -f "run-vip-churn.sh" 2>/dev/null
  pkill -f "run-dip-forever.sh" 2>/dev/null
  if [ -f logs/supervisor.pid ]; then kill $(cat logs/supervisor.pid) 2>/dev/null; rm -f logs/supervisor.pid; fi
  pkill -f "run-bot-supervisor.sh" 2>/dev/null
  sleep 2
  ./bin/cancelown >/dev/null 2>&1
  echo "✅ Bot & supervisor berhenti, order sisa dibatalkan. (Posisi terbuka dijual pakai menu 8)"
}

menu_edit() {
  ${EDITOR:-nano} bot.config
  echo "— Pengaturan baru: MODE=$(cfg MODE) PAIRS=$(cfg PAIRS) BUDGET=\$$(cfg BUDGET_USD)"
  if [ -n "$(bot_pid)" ]; then
    printf "Bot sedang jalan. Terapkan sekarang (restart)? [y/N] "; read r
    [[ "$r" == y* ]] && { menu_stop; sleep 1; menu_start; }
  fi
}

menu_apikey() {
  echo "── Ganti API key (dari app Polyester → sub-akun → API) ──"
  printf "API key id (publik, ak_...): "; read keyid
  [ -z "$keyid" ] && { echo "batal"; return; }
  printf "PRIVATE key (input tersembunyi): "; read -s privkey; echo
  [ ${#privkey} -ne 64 ] && { echo "❌ Private key harus 64 karakter (dapat ${#privkey}). Batal."; return; }
  printf "Account id (Profile page): "; read acct
  [ -z "$acct" ] && { echo "batal"; return; }
  # validasi: derive pubkey dari private key
  derived=$(go run ./cmd/keycheck "$keyid" "$privkey" 2>/dev/null | grep -o '[0-9a-f]\{64\}' | head -1)
  [ -z "$derived" ] && { echo "❌ Private key tidak valid."; return; }
  cp .env .env.bak.$(date +%s)
  sed -i '' "s|^API_KEY_ID=.*|API_KEY_ID=$keyid|; s|^API_PRIVATE_KEY=.*|API_PRIVATE_KEY=$privkey|; s|^ACCOUNT_ID=.*|ACCOUNT_ID=$acct|" .env
  chmod 600 .env
  echo "✅ .env diperbarui (backup lama disimpan). Public key turunan: ${derived:0:16}..."
  echo "   Tes koneksi: ./bot.sh (1) — saldo terbaca berarti kunci valid."
}

menu_mode() {
  printf "Mode baru [vip/dip/off] (sekarang: $(cfg MODE)): "; read m
  case "$m" in vip|dip|off) setcfg MODE "$m"; echo "MODE=$m tersimpan." ;; *) echo "pilihan tidak dikenal" ;; esac
  if [ -n "$(bot_pid)" ]; then
    printf "Bot jalan. Restart dengan mode baru? [y/N] "; read r
    [[ "$r" == y* ]] && { menu_stop; sleep 1; menu_start; }
  fi
}

menu_vip() {
  cyc=$(grep -cE 'PnL siklus' logs/churn-vip.log 2>/dev/null || echo 0)
  bud=$(cfg BUDGET_USD)
  vol=$(awk -v c="$cyc" -v b="$bud" 'BEGIN{printf "%.0f", c*2*b}')
  echo "════════ LAPORAN VIP ════════"
  echo "Siklus selesai : $cyc"
  echo "Volume (est.)  : \$$(printf "%'d" $vol)"
  awk -v v="$vol" 'BEGIN{
    t[0]=0; t[1]=100000; t[2]=500000; t[3]=1000000; t[4]=5000000; t[5]=10000000;
    t[6]=25000000; t[7]=50000000; t[8]=100000000; t[9]=250000000; t[10]=500000000;
    cur=0; for(i=10;i>=0;i--){ if(v+t[3]>=t[i]){cur=i;break} }
    # tier dari volume TOTAL akun (angka ini hanya sesi bot) — pakai vipinfo utk resmi
  }'
  echo "Tier resmi akun: jalankan  ./bin/vipinfo"
  echo "Fee terbakar   : \$$(grep -oE 'PnL siklus: -[0-9.]+' logs/churn-vip.log 2>/dev/null | awk '{s+=$3} END{printf "%.2f", -s}')"
  # simpan snapshot harian
  today=$(date -u +%F)
  grep -v "^$today" logs/vip-hari.ini 2>/dev/null > /tmp/vh; echo "$today cycles=$cyc vol=$vol" >> /tmp/vh; sort -u /tmp/vh > logs/vip-hari.ini 2>/dev/null
  echo "────────────────────────────"
}

menu_cleanup() {
  ./bin/cancelown 2>&1 | tail -2
  for p in ${(s:,:)$(cfg PAIRS)}; do
    echo "jual $p (bila ada):"; MAX_SLIPPAGE_BPS=200 ./bin/sell "$p" 2>&1 | tail -1
  done
}

while true; do
  echo ""
  echo "╔════════ PANEL KONTROL BOT ════════╗"
  echo "║ 1. Status bot                     ║"
  echo "║ 2. Nyalakan bot                   ║"
  echo "║ 3. Stop bot                       ║"
  echo "║ 4. Edit pengaturan (pair/modal)   ║"
  echo "║ 5. Ganti API key / sub-akun       ║"
  echo "║ 6. Ganti mode (vip/dip/off)       ║"
  echo "║ 7. Laporan VIP                    ║"
  echo "║ 8. Bersihkan posisi nyangkut      ║"
  echo "║ 9. Lihat log live                 ║"
  echo "║ 0. Keluar                         ║"
  echo "╚═══════════════════════════════════╝"
  printf "Pilih: "; read pil
  case "$pil" in
    1) menu_status ;;
    2) menu_start ;;
    3) menu_stop ;;
    4) menu_edit ;;
    5) menu_apikey ;;
    6) menu_mode ;;
    7) menu_vip ;;
    8) menu_cleanup ;;
    9) echo "(Ctrl+C untuk kembali ke menu)"; if [ "$(cfg MODE)" = "dip" ]; then tail -f logs/dip.log; else tail -f logs/churn-vip.log; fi ;;
    0) break ;;
    *) echo "pilihan tidak ada" ;;
  esac
done
