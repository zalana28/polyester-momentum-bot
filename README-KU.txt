════════════════════════════════════════════════════
  CARA PAKAI BOT TANPA CHAT AI — PANEL MANDIRI
════════════════════════════════════════════════════

Buka Terminal, ketik:

    cd ~/zcode-workspace 2>/dev/null; cd ~/.zcode/workspace/default/polyester-momentum-bot
    ./bot.sh

Muncul menu angka. Yang sering dipakai:

  [4] GANTI PAIR / MODAL
      Akan terbuka editor nano. Ubah baris:
        PAIRS=USDC-USDT,LTC-USDT,BTC-USDT   <- daftar coin (pisah koma)
        BUDGET_USD=600                       <- modal per trade (min 400)
      Simpan: Ctrl+O lalu Enter. Keluar: Ctrl+X.
      Kalau bot sedang jalan, menu akan menawarkan restart otomatis.

  [2] NYALAKAN  |  [3] STOP
      Stop selalu bersih: order dibatalkan dulu.

  [5] GANTI API KEY / SUB-AKUN (tanpa chat AI!)
      Siapkan dari app Polyester: API key id, private key
      (yang muncul sekali), dan Account id (halaman Profile).
      Private key diketik tersembunyi, tidak pernah tampil.

  [6] GANTI MODE:  vip = push volume  |  dip = maker  |  off = mati

  [7] LAPORAN VIP — jumlah siklus, volume, fee terbakar
      (tier resmi akun: ./bin/vipinfo)

  [8] BERSIHKAN POSISI NYANGKUT — jual manual sisa coin

  [9] LIHAT TRADE LIVE (Ctrl+C kembali ke menu)

CATATAN PENTING
- Bot jalan sendiri 24 jam: kalau server Polygon... maksudnya Polyester
  ngambek, dia sabar menunggu & memulihkan sendiri (self-heal).
- Kalau log bilang "PERLU FAUCET TOP-UP" (cek di menu 1/7): saldo USDT
  bebas sudah di bawah batas — top-up faucet lalu menu [2].
- File pengaturan mentah: bot.config (boleh diedit langsung juga).
- JANGAN edit .env manual kalau ragu — pakai menu [5].
- Semua kode ada di GitHub (repo private zalana28/polyester-momentum-bot).
