# 📖 TUTORIAL LENGKAP BOT TRADING POLYESTER
### Untuk pemilik: jalan tanpa perlu chat AI — dari nol sampai VIP

> Versi: 1.0 · Bot: polyester-momentum-bot · Lokasi: `~/.zcode/workspace/default/polyester-momentum-bot`
> Panduan cepat harian ada di `README-KU.txt` — dokumen ini versi lengkapnya.

---

## BAGIAN 1 — KENALAN 5 MENIT

### Apa yang dimiliki
| Komponen | Fungsi |
|---|---|
| `bot.sh` | **Panel kontrol utama** — semua yang kamu butuh ada di sini |
| `bot.config` | Pengaturan: mode, pair, modal, jeda |
| `.env` | Kunci API (rahasia, jangan dibagikan) |
| `run-bot-supervisor.sh` | Penjaga bot: auto-restart, self-heal, anti-gangguan server |
| `logs/` | Semua catatan: trade, volume, error |
| `state-dip/` | Hasil trade + status bot maker |

### Dua mode bot
| Mode | Cara kerja | Untuk |
|---|---|---|
| **vip** | Beli market → jual market, terus berulang | Menambah volume → naik tier VIP |
| **dip** | Pasang beli 30bps di bawah harga → jual +60bps saat bounce | Profit kecil berulang (butuh pasar sideways) |

### Tingkat VIP (data resmi Polyester)
| Tier | Volume | Fee taker |
|---|---|---|
| VIP 3 | $1 jt | 0.105% |
| VIP 4 | $5 jt | 0.09% |
| VIP 5 | $10 jt | 0.08% |
| VIP 10 | **$500 jt** | 0.045% |

**Matematika penting:** 1 siklus VIP $600 = $1.200 volume, fee ≈ $1.26.
Setiap $1 modal ≈ $950 volume sebelum habis terbakar fee → faucet ulang.

---

## BAGIAN 2 — HARI PERTAMA (SETUP)

### Langkah 1: Buka panel
Buka aplikasi **Terminal** di Mac, ketik:
```
cd ~/.zcode/workspace/default/polyester-momentum-bot
./bot.sh
```

### Langkah 2: Cek status (menu 1)
Harus terlihat: Bot 🟢 / Supervisor 🟢 / Saldo terbaca.
Kalau saldo $0 → cek internet dulu, atau kunci API belum valid (menu 5).

### Langkah 3: Set pair & modal (menu 4)
Editor nano terbuka. Baris penting:
```
PAIRS=USDC-USDT,LTC-USDT,BTC-USDT   ← coin apa saja (pisah koma)
BUDGET_USD=600                       ← modal per trade (VIP min 400)
```
Simpan: **Ctrl+O → Enter → Ctrl+X**. Tawarkan restart → jawab `y`.

### Langkah 4: Biarkan jalan
Tutup Terminal TIDAK mematikan bot (jalan di background 24 jam).

---

## BAGIAN 3 — OPERASI HARIAN

### Rutinitas pagi (2 menit)
1. `./bot.sh` → menu **1** (status) → menu **7** (laporan VIP)
2. Lihat baris "Volume (est.)" — itu progres ke tier berikutnya
3. Tier resmi akun: keluar menu, jalankan `./bin/vipinfo`

### Kalau log bilang "PERLU FAUCET TOP-UP"
Artinya USDT bebas sudah di bawah batas ($500 default):
1. Ambil testnet USDC/USDT dari faucet Polyester (web app → deposit/faucet)
2. Pastikan masuk ke **sub-akun** yang sama dengan API key-nya
3. Tukar ke USDT bila perlu (panel menu 8 membantu jual coin lain)
4. `./bot.sh` → menu **2** (nyalakan lagi)

### Ganti coin yang diperdagangkan (menu 4)
Ubah `PAIRS=` — contoh mau fokus BTC saja:
```
PAIRS=BTC-USDT
```
 atau campuran: `PAIRS=BTC-USDT,ETH-USDT,SOL-USDT,DOGE-USDT`

**Tips pilih pair untuk VIP:** pilih yang bukunya cair (USDC, BTC, LTC, ETH).
Coin eksotis (book tipis) bikin slippage besar → fee terbakar lebih cepat.

### Naikkan/turunkan modal
Menu 4 → `BUDGET_USD=`. Batas bawah VIP: **400** (minimum order server).
Atas: bebas, asal < saldo bebas. Modal besar = volume per siklus besar.

### Ganti mode (menu 6)
- `vip` — push volume (default)
- `dip` — strategi maker (butuh modal ≥ 1000; SL otomatis terpasang)
- `off` — mati total

---

## BAGIAN 4 — GANTI API KEY / SUB-AKUN (menu 5)

Kapan perlu: pindah sub-akun baru, kunci lama dicabut, atau awal setup.

1. Di **web app Polyester** (browser): masuk ke sub-akun → menu API → **Create API Key**
2. Grant: **Spot trading + Ledger read**. Catat:
   - API key id (`ak_...`) — publik
   - **Private key** — muncul SEKALI, copy langsung
   - Account id — dari halaman Profile
3. `./bot.sh` → **5** → ikut pertanyaan (private key diketik tersembunyi)
4. Validasi otomatis: kunci salah → ditolak; benar → `.env` diperbarui (backup lama dibuat)
5. Tes: menu **1** — saldo terbaca = sukses

⚠️ **Keamanan:** private key TIDAK PERnah dikirim via chat/Discord/screenshot.
Kalau tidak sengaja terekspos: Regenerate di web app, lalu ulangi menu 5.

---

## BAGIAN 5 — TROUBLESHOOTING

| Gejala | Penyebab | Solusi |
|---|---|---|
| "DNS mati — tunggu 30s" di log | Server Polyester ngambek | Sabar — otomatis lanjut saat sehat |
| Bot 🟢 tapi volume naik pelan | Server lambat / jeda 4-7 dtk | Normal. Kurangi `REST_SEC` (min 2) kalau server sehat |
| "SELL gagal / posisi tersisa" | Jual ditolak server sesaat | Menu **8** (bersihkan) — dana kembali |
| Saldo terbaca $0 padahal ada | Cache server basi beberapa detik | Tunggu 30 dtk, cek lagi menu 1 |
| Bot mati sendiri + "PERLU FAUCET" | Dana bebas < batas | Faucet → menu 2 |
| `vipinfo` error "cannot read" | Kunci tanpa izin VIP metrics | Normal — tier resmi lihat di web app |
| Pair baru ditolak terus | Notional < minimum atau symbol salah | Cek nama pair persis (menu Markets web) |

### Perintah darurat (Terminal)
```
./bot.sh                        # panel lengkap
./bin/probe                     # cek saldo & order mentah
./bin/cancelown                 # batalkan semua order bot
tail -f logs/churn-vip.log      # intip trade live
tail -f logs/supervisor.log     # intip kesehatan supervisor
```

---

## BAGIAN 6 — CARA KERJA MESIN (untuk yang penasaran)

### Mode VIP (churn)
```
siklus: beli market $600 → jual market $600 → jeda 4-7 dtk → ulang
```
- **Skip preview**: server yang memvalidasi dana (preview RPC server sering flaky)
- **Retry jual 10×**: cache saldo server bisa basi ~30 dtk
- **Self-heal**: posisi nyangkut → supervisor jual otomatis → lanjut
- **Rem faucet**: dana < $500 → berhenti rapi, tidak spin panas

### Mode dip (maker)
```
pasang beli 30bps di bawah harga → terisi → SL server-side + jual +60bps
```
- SL **server-side**: tetap aktif walau Mac/bot mati (pelajaran mahal 2 insiden)
- Oracle harga: keputusan exit butuh konfirmasi candle 1m (anti data basi)
- Loss cap: −$25 dari puncak → stop (ubah `MAX_LOSS_USD`)

### Penjaga (supervisor)
```
loop: cek DNS → bersihkan order/posisi → cek saldo → cukup? jalankan bot : stop
```

---

## BAGIAN 7 — FAQ

**T: Bisa jalan 24/7 tanpa Mac nyala?**
J: Mac harus tetap nyala (tidur dimatikan via caffeinate yang sudah jalan). Tutup lid = bot mati.

**T: Kena rugi besar?**
J: Mode VIP risikonya cuma fee (0.2%/siklus). Mode dip ada SL server-side + loss cap $25.

**T: Mau dua mode sekaligus?**
J: Belum — satu mode satu waktu (menu 6). Minta AI kalau mau paralel.

**T: Data trading saya hilang?**
J: Tidak — `state-dip/trades.csv` + `logs/` permanen, semua kode di GitHub private.

**T: Minta fitur baru?**
J: Itu satu-satunya alasan chat AI lagi 🙂 (atau pull-request sendiri di GitHub).
