#!/usr/bin/env python3
"""Expectancy bruto vs neto per strategi / era / exit_reason / symbol (/ regime bila ada kolomnya).

Pakai:  python3 expectancy.py trades.csv [--fee 0.0015] [--with-estimates] [--tp 1.5 --sl 0.75]
Hanya posisi long. Baris bernilai '~' atau berlabel ESTIMASI dikeluarkan kecuali --with-estimates.
Gross/fee/net DIHITUNG ULANG dari entry, exit, qty, lalu dicocokkan dengan net di log.
"""
import argparse, csv, math
from collections import defaultdict


def num(s):
    s = (s or "").strip()
    return float(s.lstrip("~").strip()), s.startswith("~")


def load(path, fee):
    out = []
    with open(path, newline="", encoding="utf-8") as f:
        for r in csv.DictReader(f):
            entry, e1 = num(r["entry"])
            exit_, e2 = num(r["exit"])
            qty, _ = num(r["qty"])
            est = e1 or e2 or "ESTIMASI" in (r.get("keterangan") or "").upper()
            gross = (exit_ - entry) * qty
            fees = fee * (entry + exit_) * qty
            row = dict(r)
            row.update(est=est, ALL="semua", gross=gross, fees=fees, net=gross - fees,
                       gross_bps=(exit_ / entry - 1) * 1e4,
                       fee_bps=fees / (entry * qty) * 1e4)
            row["net_bps"] = row["gross_bps"] - row["fee_bps"]
            try:
                row["logged_net"] = float(r["net_pnl_usdt"])
            except (KeyError, ValueError):
                row["logged_net"] = None
            out.append(row)
    return out


def stats(rows):
    n = len(rows)
    mean = lambda x: sum(x) / len(x)
    nt = [r["net_bps"] for r in rows]
    sd = math.sqrt(sum((v - mean(nt)) ** 2 for v in nt) / (n - 1)) if n > 1 else float("nan")
    return dict(n=n,
                win_g=sum(r["gross_bps"] > 0 for r in rows),
                win_n=sum(v > 0 for v in nt),
                gross=mean([r["gross_bps"] for r in rows]),
                fee=mean([r["fee_bps"] for r in rows]),
                net=mean(nt),
                se=sd / math.sqrt(n) if n > 1 else float("nan"),
                usdt=sum(r["net"] for r in rows))


def table(rows, key):
    groups = defaultdict(list)
    for r in rows:
        groups[r.get(key) or "?"].append(r)
    print(f"\n== per {key} ==")
    print(f"{'grup':<20}{'n':>3}{'winG':>5}{'winN':>5}{'gross_bps':>10}{'fee_bps':>8}{'net_bps':>8}{'±se':>7}{'net_USDT':>10}")
    for k, g in sorted(groups.items()):
        s = stats(g)
        print(f"{k:<20}{s['n']:>3}{s['win_g']:>5}{s['win_n']:>5}{s['gross']:>10.1f}"
              f"{s['fee']:>8.1f}{s['net']:>8.1f}{s['se']:>7.1f}{s['usdt']:>10.2f}")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("csv")
    ap.add_argument("--fee", type=float, default=0.0015, help="fee taker per leg (0.0015 = 0.15%%)")
    ap.add_argument("--with-estimates", action="store_true")
    ap.add_argument("--tp", type=float, help="target profit %% (untuk break-even win rate)")
    ap.add_argument("--sl", type=float, help="stop loss %%")
    a = ap.parse_args()

    rows = load(a.csv, a.fee)
    used = [r for r in rows if a.with_estimates or not r["est"]]
    print(f"Trade dipakai: {len(used)} dari {len(rows)} (estimasi dikeluarkan: {len(rows) - len(used)})")
    if not used:
        return

    print("\n== rekonsiliasi net (hitung ulang vs log) ==")
    for r in used:
        if r["logged_net"] is None:
            continue
        d = r["net"] - r["logged_net"]
        flag = "OK" if abs(d) <= 0.02 else "BEDA"
        print(f"trade {r['trade']:>2} {r['symbol']:<10} hitung {r['net']:>8.4f}  log {r['logged_net']:>8.4f}  selisih {d:>7.4f} {flag}")

    for key in ["ALL", "strategy", "era", "exit_reason", "symbol"] + (["regime"] if "regime" in used[0] else []):
        table(used, key)

    s = stats(used)
    print(f"\nFee round trip rata-rata: {s['fee']:.1f} bps. Gerak harga bruto minimal untuk impas: {s['fee']:.1f} bps.")
    print(f"Gerak bruto rata-rata yang teramati: {s['gross']:.1f} bps.")
    if s["n"] < 30:
        print(f"PERINGATAN: n={s['n']} < 30. Ini anekdot, bukan expectancy.")

    if a.tp and a.sl:
        fee_pct = s["fee"] / 100
        be_net = (a.sl + fee_pct) / (a.tp + a.sl)
        be_gross = a.sl / (a.tp + a.sl)
        print(f"\nTP +{a.tp}% / SL -{a.sl}%  (fee {fee_pct:.2f}%)")
        print(f"  win rate impas tanpa fee : {be_gross:.1%}")
        print(f"  win rate impas dengan fee: {be_net:.1%}")
        print(f"  net kalau TP kena: {a.tp - fee_pct:+.2f}%   net kalau SL kena: {-(a.sl + fee_pct):+.2f}%")


if __name__ == "__main__":
    main()
