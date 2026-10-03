# Design System — Polyester Trading Bot 3D Office

## Product context
A live "AI trading team office": an isometric 3D voxel office (Three.js) where
6 characters represent the trading bot's roles (Dip Trader, Momentum-kun,
Risk Manager, Brain Analyst, Guardian, Boss). The page is a real-time,
read-only monitor: it polls `/api/state` every 2s and characters react to REAL
trading events (fills, sells, stop-loss, momentum entries, skips). Audience:
the bot owner (casual, fun, wants to watch it like a game).

## Visual direction (v2 redesign target)
- Mood: premium dark "trading terminal meets cozy game" — think Bloomberg
  terminal art direction inside a Minecraft/Crossy-road voxel world.
- Scene: deep navy-to-black gradient environment (#0b0f1e → #141824), floor
  with subtle grid glow, glass partition walls with neon edge trim, warm
  interior lamps over desks.
- Accents: neon green #43e97b (profit) / neon red #ff5d5d (loss) / cyan
  #4facfe (info) / amber #ffd54a (momentum). Everything else neutral navy.
- HUD: glassmorphism panels (rgba white 6-10% + backdrop-blur 12px, 1px
  rgba-white border, 14px radius), Inter/system sans for labels, tabular
  monospace (SF Mono / JetBrains Mono fallback) for all numbers.
- Typography scale: title 20-24px semibold; PnL number 32px bold; feed 12px;
  name tags rendered as floating sprites with pill background.
- Motion: characters idle-bob; typing arms; jump on fill; faint on SL;
  confetti burst on profit; floating emoji tags that rise and fade; soft
  camera auto-drift when idle. No easing libraries; time-based sines.
- Language: ALL UI copy in ENGLISH (titles, tags, feed, help, quotes).

## Hard constraints
- Single self-contained HTML file; only external dep: three@0.160 from unpkg.
- Must keep data contract: GET /api/state → {total_net, cycles, wins, losses,
  events[{ts,kind,sym,txt}], trades[]} polled every 2s.
- Read-only monitor; drag-orbit camera + wheel zoom must remain.
- Keep the 6 roles and their reaction mapping; may restyle everything visual.
