#!/usr/bin/env python3
"""Lab v2: hipotesis perbaikan di atas v1.
  A. dip-chop     : dip maker HANYA di pasar ranging/choppy (ER<0.25)
  B. dip-confirm  : bid hanya dipasang setelah candle capitulation
                    (sumbu bawah >= 50% range + volume >= 1.2x rata2)
  C. mom-pullback : di tren naik, jangan kejar - pasang bid maker 30bps
                    di bawah harga, tunggu micro-pullback
Dibandingkan dengan baseline v1 pada data yang sama.
"""
import json, os, glob, sys
sys.path.insert(0, os.path.dirname(__file__))
from backtest import load_pairs, ema, efficiency_ratio, FEE_MAKER, FEE_TAKER

BUDGET = 2500.0

def backtest_dip_chop(cs, off_bps=30, tp_bps=60, sl_bps=120, max_hold=9):
    net_total, trades = 0.0, []
    i = 25
    while i < len(cs)-2:
        closes = [c["c"] for c in cs[max(0,i-60):i+1]]
        er = efficiency_ratio(closes)
        if er >= 0.25:
            i += 1; continue
        entry = cs[i]["c"]*(1-off_bps/10000.0)
        filled = None
        for j in range(i+1, min(i+6, len(cs))):
            if cs[j]["l"] < entry: filled = j; break
        if filled is None: i += 1; continue
        e = filled
        target, sl = entry*(1+tp_bps/10000.0), entry*(1-sl_bps/10000.0)
        exit_px, reason = None, None
        if cs[e]["l"] <= sl: exit_px, reason = sl, "sl"
        elif cs[e]["h"] >= target: exit_px, reason = target, "target"
        if exit_px is None:
            for j in range(e+1, min(e+max_hold, len(cs))):
                if cs[j]["l"] <= sl: exit_px, reason = sl, "sl"; break
                if cs[j]["h"] >= target: exit_px, reason = target, "target"; break
            if exit_px is None:
                j = min(e+max_hold, len(cs)-1); exit_px, reason = cs[j]["c"], "maxhold"
        qty = BUDGET/entry
        net = (exit_px-entry)*qty - entry*qty*FEE_MAKER - exit_px*qty*(FEE_TAKER if reason=="maxhold" else FEE_MAKER)
        net_total += net; trades.append(net)
        i = j+1
    return net_total, trades

def backtest_dip_confirm(cs, tp_bps=60, sl_bps=120, max_hold=9):
    net_total, trades = 0.0, []
    i = 25
    while i < len(cs)-2:
        c = cs[i]
        rng = c["h"]-c["l"]
        mean = sum(x["v"] for x in cs[max(0,i-20):i])/20 if i >= 20 else 0
        capit = rng > 0 and (c["c"]-c["l"]) >= 0.5*rng and mean > 0 and c["v"] >= 1.2*mean
        if not capit:
            i += 1; continue
        entry = c["l"]*(1-5/10000.0)
        filled = None
        for j in range(i+1, min(i+6, len(cs))):
            if cs[j]["l"] < entry: filled = j; break
        if filled is None: i += 1; continue
        e = filled
        target, sl = entry*(1+tp_bps/10000.0), entry*(1-sl_bps/10000.0)
        exit_px, reason = None, None
        if cs[e]["l"] <= sl: exit_px, reason = sl, "sl"
        elif cs[e]["h"] >= target: exit_px, reason = target, "target"
        if exit_px is None:
            for j in range(e+1, min(e+max_hold, len(cs))):
                if cs[j]["l"] <= sl: exit_px, reason = sl, "sl"; break
                if cs[j]["h"] >= target: exit_px, reason = target, "target"; break
            if exit_px is None:
                j = min(e+max_hold, len(cs)-1); exit_px, reason = cs[j]["c"], "maxhold"
        qty = BUDGET/entry
        net = (exit_px-entry)*qty - entry*qty*FEE_MAKER - exit_px*qty*(FEE_TAKER if reason=="maxhold" else FEE_MAKER)
        net_total += net; trades.append(net)
        i = j+1
    return net_total, trades

def backtest_mom_pullback(cs, off_bps=30, tp_bps=60, sl_bps=120, max_hold=9, budget=500.0):
    net_total, trades = 0.0, []
    i = 25
    while i < len(cs)-2:
        closes = [c["c"] for c in cs[max(0,i-60):i+1]]
        er = efficiency_ratio(closes)
        bull = closes[-1] > ema(closes[:-1], 15)
        if not (er > 0.35 and bull):
            i += 1; continue
        entry = cs[i]["c"]*(1-off_bps/10000.0)
        filled = None
        for j in range(i+1, min(i+4, len(cs))):
            if cs[j]["l"] < entry: filled = j; break
        if filled is None: i += 1; continue
        e = filled
        target, sl = entry*(1+tp_bps/10000.0), entry*(1-sl_bps/10000.0)
        exit_px, reason = None, None
        if cs[e]["l"] <= sl: exit_px, reason = sl, "sl"
        elif cs[e]["h"] >= target: exit_px, reason = target, "target"
        if exit_px is None:
            for j in range(e+1, min(e+max_hold, len(cs))):
                if cs[j]["l"] <= sl: exit_px, reason = sl, "sl"; break
                if cs[j]["h"] >= target: exit_px, reason = target, "target"; break
            if exit_px is None:
                j = min(e+max_hold, len(cs)-1); exit_px, reason = cs[j]["c"], "maxhold"
        qty = budget/entry
        net = (exit_px-entry)*qty - entry*qty*FEE_MAKER - exit_px*qty*(FEE_TAKER if reason=="maxhold" else FEE_MAKER)
        net_total += net; trades.append(net)
        i = j+1
    return net_total, trades

def agg(fn, pairs, **kw):
    tot, n, w = 0.0, 0, 0
    for sym, cs in pairs.items():
        if sym == "USDC-USDT": continue
        nt, tr = fn(cs, **kw)
        tot += nt; n += len(tr); w += sum(1 for t in tr if t > 0)
    return tot, n, (w/n if n else 0)

def main():
    pairs = load_pairs()
    print("=== LAB v2: hipotesis perbaikan (budget $2500 dip / $500 momentum) ===\n")
    rows = []
    tot, n, w = agg(backtest_dip_chop, pairs); rows.append(("A dip-chop (ER<0.25, 30/60/120)", tot, n, w))
    tot, n, w = agg(backtest_dip_confirm, pairs); rows.append(("B dip-confirm capitulation (60/120)", tot, n, w))
    tot, n, w = agg(backtest_mom_pullback, pairs); rows.append(("C mom-pullback maker (30/60/120, $500)", tot, n, w))
    for name, tot, n, w in rows:
        print(f"{name:44s} trades={n:5d} winrate={w*100:5.1f}% net=${tot:+10.2f} avg=${(tot/n if n else 0):+7.3f}/trade")
    print("\nReferensi v1: dip default 30/60/120 sangat negatif; momentum IOC -828.")
    print("Catatan: 9.5 hari data, jendela pasar trending kuat. Arah riset, bukan jaminan.")

if __name__ == "__main__":
    main()
