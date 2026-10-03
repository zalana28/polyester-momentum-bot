#!/usr/bin/env python3
"""Lab backtest offline untuk bot dip maker + momentum-hybrid Polyester.

Membaca tools/lab/candles/<SYM>.json (dari cmd/dumpcandles), mensimulasikan:
  1. dip-maker    : beli maker di close*(1-off), jual maker di entry*(1+tp),
                    SL bot-side, max-hold -> taker exit.
  2. momentum     : tren naik (ER tinggi, close>EMA15) + lonjakan volume ->
                    entry taker di close, target maker, SL, max-hold pendek.

Asumsi konservatif: dalam satu candle, SL dicek SEBELUM target (pesimis),
fill maker butuh low < harga order (bukan sentuh).
Output: laporan markdown tools/lab/report.md.
"""
import json, os, glob, itertools, sys

FEE_MAKER = 0.0010
FEE_TAKER = 0.0015

def load_pairs():
    out = {}
    for path in glob.glob(os.path.join(os.path.dirname(__file__), "candles", "*.json")):
        d = json.load(open(path))
        out[d["symbol"]] = d["candles"]
    return out

def ema(xs, p):
    if not xs: return 0.0
    k = 2.0/(p+1); e = xs[0]
    for x in xs[1:]: e = x*k + e*(1-k)
    return e

def efficiency_ratio(closes, look=10):
    w = closes[-(look+1):]
    if len(w) < look+1: return 0.0
    net = abs(w[-1]-w[0]); path = sum(abs(w[i]-w[i-1]) for i in range(1,len(w)))
    return net/path if path > 0 else 0.0

def atr_pct(cs, n=14):
    if len(cs) < n+1: return 0.0
    tr = 0.0
    for i in range(len(cs)-n, len(cs)):
        h,l = cs[i]["h"], cs[i]["l"]
        pc = cs[i-1]["c"] if i > 0 else cs[i]["c"]
        tr += max(h-l, abs(h-pc), abs(l-pc))
    return tr/n/cs[-1]["c"]*100

def vol_surge(cs, mult=1.5):
    if len(cs) < 21: return False
    mean = sum(c["v"] for c in cs[-21:-1])/20
    return mean > 0 and cs[-1]["v"] >= mult*mean

def backtest_dip(cs, off_bps, tp_bps, sl_bps, max_hold_candles, budget, skip_trend_down=True):
    """Kembalikan (net_usdt_total, trades) untuk satu pair."""
    net_total, trades = 0.0, []
    i = 20
    while i < len(cs)-2:
        window = cs[max(0,i-60):i+1]
        closes = [c["c"] for c in window]
        er = efficiency_ratio(closes)
        drift = closes[-1] - closes[-11] if len(closes) >= 11 else 0.0
        if skip_trend_down and er > 0.35 and drift < 0:
            i += 1; continue
        entry = cs[i]["c"]*(1-off_bps/10000.0)
        # cari fill di candle berikutnya (low < entry = maker fill)
        filled_at = None
        for j in range(i+1, min(i+6, len(cs))):
            if cs[j]["l"] < entry:
                filled_at = j; break
        if filled_at is None:
            i += 1; continue
        e = filled_at
        target = entry*(1+tp_bps/10000.0); sl = entry*(1-sl_bps/10000.0)
        exit_px, reason = None, None
        # candle fill sendiri: SL dulu (pesimis), lalu target
        if cs[e]["l"] <= sl: exit_px, reason = sl, "sl"
        elif cs[e]["h"] >= target: exit_px, reason = target, "target"
        if exit_px is None:
            for j in range(e+1, min(e+max_hold_candles, len(cs))):
                if cs[j]["l"] <= sl: exit_px, reason = sl, "sl"; break
                if cs[j]["h"] >= target: exit_px, reason = target, "target"; break
            if exit_px is None:
                j = min(e+max_hold_candles, len(cs)-1)
                exit_px, reason = cs[j]["c"], "maxhold"
        qty = budget/entry
        gross = (exit_px-entry)*qty
        fe = entry*qty*FEE_MAKER
        fx = exit_px*qty*(FEE_TAKER if reason=="maxhold" else FEE_MAKER)
        net = gross-fe-fx
        net_total += net
        trades.append({"entry_i": e, "exit_i": j if reason=="maxhold" else e+1, "net": net, "reason": reason})
        i = (j if reason=="maxhold" else e+1) + 1
    return net_total, trades

def backtest_momentum(cs, tp_bps, sl_bps, max_hold_candles, budget, score_min=65):
    net_total, trades = 0.0, []
    i = 25
    while i < len(cs)-2:
        window = cs[max(0,i-60):i+1]
        closes = [c["c"] for c in window]
        er = efficiency_ratio(closes)
        bull = closes[-1] > ema(closes[:-1], 15)
        if er > 0.35 and bull and vol_surge(cs[:i+1]) and atr_pct(cs[:i+1]) >= 0.15:
            entry = cs[i]["c"]  # IOC di ask ~ close
            target = entry*(1+tp_bps/10000.0); sl = entry*(1-sl_bps/10000.0)
            exit_px, reason, j = None, None, None
            for j in range(i+1, min(i+1+max_hold_candles, len(cs))):
                if cs[j]["l"] <= sl: exit_px, reason = sl, "sl"; break
                if cs[j]["h"] >= target: exit_px, reason = target, "target"; break
            if exit_px is None:
                j = min(i+max_hold_candles, len(cs)-1)
                exit_px, reason = cs[j]["c"], "maxhold"
            qty = budget/entry
            gross = (exit_px-entry)*qty
            fe = entry*qty*FEE_TAKER
            fx = exit_px*qty*(FEE_TAKER if reason in ("maxhold","sl") else FEE_MAKER)
            net = gross-fe-fx
            net_total += net
            trades.append({"entry_i": i, "exit_i": j, "net": net, "reason": reason})
            i = j+1
        else:
            i += 1
    return net_total, trades

def main():
    pairs = load_pairs()
    if not pairs:
        print("tidak ada data candle - jalankan cmd/dumpcandles dulu"); sys.exit(1)
    budget = 2500.0
    lines = ["# Laporan Lab Backtest Polyester (candle 5m)", "",
             f"Data: {sum(len(c) for c in pairs.values())} candle, {len(pairs)} pair. Fee maker 10bps, taker 15bps. SL dicek sebelum target (pesimis). Budget simulasi ${budget:.0f}/trade.", ""]
    # --- dip grid ---
    lines += ["## Dip maker - grid parameter (agregat semua pair)", "",
              "| off | tp | sl | trades | winrate | net $ | avg $ |", "|---|---|---|---|---|---|---|"]
    rows = []
    for off, tp, sl in itertools.product([15,30,45],[40,60,90],[80,120,160]):
        tot, n, w = 0.0, 0, 0
        for sym, cs in pairs.items():
            nt, tr = backtest_dip(cs, off, tp, sl, 9, budget)
            tot += nt; n += len(tr); w += sum(1 for t in tr if t["net"] > 0)
        rows.append((off,tp,sl,n,w/n if n else 0,tot,tot/n if n else 0))
    for r in sorted(rows, key=lambda x:-x[5])[:10]:
        lines.append(f"| {r[0]} | {r[1]} | {r[2]} | {r[3]} | {r[4]*100:.0f}% | {r[5]:+.2f} | {r[6]:+.3f} |")
    best = rows[0]
    lines += ["", f"**Kombinasi terbaik: offset {best[0]}bps, target +{best[1]}bps, SL -{best[2]}bps -> net ${best[5]:+.2f} dari {best[3]} trade.**", ""]
    # --- dip detail pakai kombinasi default & terbaik ---
    for label,(off,tp,sl) in [("default (30/60/120)",(30,60,120)), (f"terbaik ({best[0]}/{best[1]}/{best[2]})",(best[0],best[1],best[2]))]:
        lines += [f"## Dip maker - per pair, param {label}", "", "| pair | trades | winrate | net $ |", "|---|---|---|---|"]
        tot = 0
        for sym in sorted(pairs):
            nt, tr = backtest_dip(pairs[sym], off, tp, sl, 9, budget)
            tot += nt
            if tr: lines.append(f"| {sym} | {len(tr)} | {sum(1 for t in tr if t['net']>0)/len(tr)*100:.0f}% | {nt:+.2f} |")
        lines += ["", f"**Total {label}: ${tot:+.2f}**", ""]
    # --- momentum ---
    lines += ["## Momentum hybrid - per pair (entry taker, budget $500)", "", "| pair | trades | winrate | net $ |", "|---|---|---|---|"]
    tot, n = 0.0, 0
    for sym in sorted(pairs):
        nt, tr = backtest_momentum(pairs[sym], 60, 120, 4, 500)
        tot += nt; n += len(tr)
        if tr: lines.append(f"| {sym} | {len(tr)} | {sum(1 for t in tr if t['net']>0)/len(tr)*100:.0f}% | {nt:+.2f} |")
    lines += ["", f"**Total momentum: ${tot:+.2f} dari {n} trade**", "",
              "Catatan: simulasi candle-level tidak memodelkan posisi antrean order book; hasil arah, bukan jaminan."]
    open(os.path.join(os.path.dirname(__file__), "report.md"), "w").write("\n".join(lines))
    print("\n".join(lines[:40]))
    print(f"\n[lengkap di tools/lab/report.md]")

if __name__ == "__main__":
    main()
