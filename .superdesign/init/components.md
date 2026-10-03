# components.md — polyester-momentum-bot

Framework: NONE (Go backend + one self-contained vanilla-JS Three.js page).
No package.json, no component library, no Tailwind. The only web UI is
`cmd/office/index.html` (3D isometric office) served by `cmd/office/main.go`.

## OfficePage (the only "component")
- File: `cmd/office/index.html`
- Description: full-screen Three.js isometric office with HUD overlay; reads `/api/state` every 2s.
- Key building blocks (actual code excerpts):

```js
// voxel box factory
const mat=(c)=>new THREE.MeshLambertMaterial({color:c});
const box=(w,h,d,c,x,y,z,parent)=>{const m=new THREE.Mesh(new THREE.BoxGeometry(w,h,d),mat(c));m.position.set(x,y,z);(parent||scene).add(m);return m};

// character factory: head+body+arms+legs + canvas-sprite name tag
function character(x,z,color,shirt,name){ /* head 0.9^3, body 1.0x1.3x0.65, armL/armR named, SpriteMaterial tag from canvas 256x64 */ }

// desk factory
function desk(x,z,rot){ /* table top 4.6x0.3x2.2, legs, monitor 1.6x1 with glowing screen 0x7fe3ff */ }

// billboard (canvas texture sprite on back wall)
function drawBillboard(pnl,cy,w,l){ /* 512x160 canvas: title, colored PnL monospace, cycle stats */ }

// HUD overlay (plain DOM)
#ui{position:fixed;left:16px;top:14px;color:#fff}          // title + PnL
#feed{position:fixed;right:16px;top:14px;width:330px}       // event feed .fitem
#help{position:fixed;bottom:10px;left:16px}                 // hint line

// reactions
const KINDMAP={filled:['trader','🛒 FILL!','#43e97b'],momentum:['momentum','🚀 GANBATTE!','#ffd54a'],...}
function react(kind,sym,txt){ floatEmoji + character jump/faint/run + confetti burst on profit }
```

State animation loop: idle bob `Math.abs(sin(t*2))*0.12`, typing arms `rotation.x=-1.1±sin(t*9)*0.5`, jump/faint/run timers in `userData`.

Data contract consumed (see routes.md): `{total_net, cycles, wins, losses, events:[{ts,kind,sym,txt}], trades:[]}` with kind ∈ filled|momentum|sl|skip|sell|profit|loss.
