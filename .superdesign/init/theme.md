# theme.md — polyester-momentum-bot (office page tokens)

## Part 1 — compact tokens
- Background page/scene: `#141824` (dark navy), scene fog `#141824` 60→140
- Floor: `#2b3350`, work carpet `#3a4470`
- Wood props: `#8a5a33` / `#a06a3d`; desk top `#d9c9a8`, legs `#b8a683`
- Wall `#f4f0e6` with screens `#9fd8ff`
- Accents: green `#43e97b` (profit), red `#ff5d5d` (loss/SL), yellow `#ffd54a` (momentum), blue `#4facfe` (sell), muted `#9fb0e8`
- HUD: white text `#e8ecff`, translucent cards `#ffffff14` + blur(4px), radius 8px
- Fonts: system stack (`-apple-system, 'Segoe UI', sans-serif`); numbers monospace on billboard
- Character palette: skins `#ffc48a/#f3d1b0/#c98850/#ffd9b0/#b07b4f/#f2c19b`, shirts `#3f6df0/#e2574c/#2fa864/#9a5ce0/#d9a13b/#1f2a44`
- No breakpoints (fullscreen desktop), no shadows configured, MeshLambertMaterial + 1 directional + 1 ambient light

## Part 2 — raw source
No tailwind config / globals.css exist. Scene constants live inline in
`cmd/office/index.html` (see components.md excerpts). UI CSS is a small
inline <style> block in the same file.
