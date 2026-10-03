# extractable-components.md — polyester-momentum-bot

## OfficeHUD
- Source: `cmd/office/index.html` (#ui, #feed, #help)
- Category: layout
- Description: top-left PnL HUD + top-right event feed + bottom hint bar
- Extractable props: totalNet, cycles, wins, losses, events[]
- Hardcoded: Indonesian copy, emoji, colors

## CharacterFactory
- Source: `cmd/office/index.html` (character())
- Category: basic
- Description: voxel character w/ name tag sprite + idle animation hooks
- Extractable props: position, skinColor, shirtColor, name
- Hardcoded: body proportions, tag canvas 256x64

## DeskFactory
- Source: `cmd/office/index.html` (desk())
- Category: basic
- Description: desk + glowing monitor
- Extractable props: position, rotation
- Hardcoded: wood colors, screen color

## PnlBillboard
- Source: `cmd/office/index.html` (drawBillboard)
- Category: basic
- Description: wall-mounted canvas sprite showing strategy PnL
- Extractable props: totalNet, cycles, wins, losses
- Hardcoded: 512x160 canvas, fonts

## EventReactionMap
- Source: `cmd/office/index.html` (KINDMAP/react/floatEmoji/burst)
- Category: basic
- Description: maps bot event kinds to character reactions, emojis, confetti
- Extractable props: events[]
- Hardcoded: emoji per kind, character roles, colors
