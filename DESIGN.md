# HopDrop — design

Peer-to-peer transfer of files and text between devices on a home LAN. No hub, no cloud, no accounts, no pairing
(trusted network: everything is auto-accepted). One static `hop` binary (Go standard library plus `rsc.io/qr`), and
the browser page as the only UI.

## Devices

| Device | Form |
|---|---|
| Mac | `hop` in a terminal, or `hop open` (background) / `hop off`. The page at `http://localhost:47100/` is its window |
| Raspberry Pi | `hop` as a systemd service |
| Phone / any browser | The page served by any device, at `http://<device>:47100/`. A device while the tab is open, relayed by that device |

## Decisions (and what changed from the earlier Rust version)

| Topic | Rust version | Now |
|---|---|---|
| Discovery | mDNS + UDP heartbeat + HTTP probes + manual add | **UDP heartbeat + HTTP probes + manual add** (no mDNS, no port 5353) |
| UI | CLI (send, ls, clip, history, queue…) + page | **Page only.** CLI is just `hop`, `hop open`, `hop off` |
| Mac run mode | launchd job (`hop on` / `hop off`) | **Foreground**: `hop` runs in a terminal; `hop open` starts it detached if needed; `hop off` stops it |
| Pi run mode | systemd | systemd (unchanged), cross-built with plain `GOOS=linux GOARCH=arm64` — no zig |
| Clipboard | daemon writes the system clipboard (arboard) | **Page only**: copy/paste in the browser (localhost is a secure context) |
| Notifications | osascript | Browser notification while the page is open; none otherwise |
| Integrity | streaming blake3 + `/done` step | **Size check**: the file is finalized when exactly the offered bytes arrive. Resume by `.part` length |
| Image clips | base64 PNG in JSON | **Just a file** (`clipboard-<time>.png`) sent like any other |
| Phones | 24 h inbox after the tab closes | **Online only**: listed while the tab is open; items wait on the host until saved/dismissed (pruned after 24 h or on restart) |
| History, retry queue | `history.jsonl`, `queue.json` | **Dropped** |
| Config | TOML | JSON (`config.json`, no dependency); `HOPDROP_HOME` still overrides the folder |
| Security | none (trusted LAN) | none (trusted LAN) |
| Dependencies | ~25 crates | stdlib + `rsc.io/qr` (QR code for phones) |

## One routing rule

Every endpoint that delivers something takes `?to=<device id>`. The daemon that receives the request decides:

1. `to` is me (or absent) → handle it here.
2. `to` is a browser tab I host → spool it into that tab's inbox.
3. `to` is another device → proxy the request, unchanged, to that device's address (for a phone: its host).
   Proxied requests carry `X-Hop-Fwd: 1` and are never forwarded a second time.

The page therefore only ever talks to the daemon that served it, which keeps it clear of CORS and Chrome's
local-network restrictions. This rule replaces both the separate `/fwd/{peer}/…` relay and the special
`?to=` handling.

## HTTP API (`/api`)

| Method | Path | Purpose |
|---|---|---|
| GET | `/` | The page (embedded with `go:embed`) |
| GET | `/api/info` | `{id, name, type, port, local}`; `local` = the request comes from this machine |
| PATCH | `/api/info` | `{name}`: rename this device (local only) |
| GET / POST | `/api/peers` | Peer list (online first) / `{addr}` add by IP, saved to config |
| POST | `/api/offer?to=` | `{from, files:[{path,size}]}` → `{id, offsets:[…]}`; offsets > 0 resume `.part` files |
| PUT | `/api/offer/{id}/{index}?to=&offset=` | File bytes from `offset`; finalized (`.part` → unique name) when complete → `{name}` |
| DELETE | `/api/offer/{id}?to=` | Cancel; removes `.part` files |
| POST | `/api/text?to=` | `{from, text}`: a text or link |
| GET | `/api/events?id=&name=` | SSE. With `id`: a browser tab registering as a peer. Without (local only): this device's own page. Events `inbox`, `peers` |
| GET | `/api/inbox?id=` | Received items for a hosted tab (or, local only and no `id`, for this device) |
| GET / DELETE | `/api/inbox/{item}?id=[&inline=1]` | Download or view a received file / dismiss an item |
| POST | `/api/inbox/{item}/reveal` | Show in Finder (local only) |
| POST | `/api/downloads` | Open the download folder (local only) |
| GET | `/api/qr.svg?text=` | QR code for a page URL; `/api/info` also lists this device's LAN URLs |
| POST | `/api/quit` | Stop the daemon (local only; used by `hop off`) |

Removed: `/whoami` (folded into `/info`), `/done`, `GET /transfer/{id}`, `/clip`, `/fwd`, `/history`,
`/queue`, `/local/*` (merged into `/inbox` and `/events`).

## Receiving

- Destination: `<download_dir>/<sender name>/<relative path>` (default `~/Downloads/HopDrop`).
- Paths sanitized as today: reject empty, `.`, `..`, absolute paths, `\`, `:`, NUL.
- Write to `name.part`; when the byte count equals the offered size, rename to `name`, `name (1)`, ….
- On the device itself, files and texts are listed in a recent-items list (last 100, in memory) that the local
  page shows with Copy / Open / Show in Finder.

## Discovery

- UDP port 47100, broadcast to each IPv4 interface's directed broadcast address every 5 s:
  `{"hop":2,"id","name","type","port","via"?,"probe"?}`. A `probe` hello gets an immediate unicast reply, and a
  `{"hop":2,"id","bye":true}` hello (sent on shutdown, and when a browser tab closes) marks a device offline at once.
- A peer is offline after 15 s of silence; peers silent for over 7 s, and manually added ones, are probed with
  `GET /api/info`.
- A host announces each connected browser tab as `type: web, via: <host id>`, and stops as soon as the tab
  disconnects (after 2 s, so a reloading tab doesn't flicker).

## Code layout

```
go.mod                    module hopdrop
cmd/hop/main.go           hop | hop open | hop off   (flags: -name -port -dir)
internal/hop/
  config.go               config.json, dirs, device name/type
  peers.go                peer table: online/offline, sweep, quiet list
  discovery.go            UDP heartbeat, probe replies, HTTP probing of quiet/manual peers
  server.go               mux, routing rule (self / hosted tab / proxy), info, peers, qr, quit
  receive.go              offer, upload with resume, finalize, cancel, text
  inbox.go                inboxes (this device + hosted tabs), SSE, file serving, reveal
  paths.go                sanitizing, .part, unique names (+ tests)
  web/index.html          the page, adapted
deploy/hopdrop.service    ExecStart=/usr/local/bin/hop
scripts/build-pi.sh       GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build + scp + systemctl
```

## The page

- Talks only to `/api/...` on its own host with `?to=` (one code path for every target).
- Uploads use XHR `PUT` with `?offset=`, keeping cancel, retry/resume, speed and ETA.
- Pasted or copied images are uploaded as `clipboard-<time>.png` files.
- *Add by IP…* adds a device that discovery can't see; the device name (top right) renames this device or tab.
- Received items offer Copy (text/link/image), Save, Open, Show in Finder (local page only).

## Platform notes

- Chrome on plain `http://` LAN origins has no async clipboard and no WebCrypto. `localhost` is a secure context,
  so the Mac's own page can read the clipboard (*Send clipboard*) and copy images; phones copy text only.
- Files received by a browser tab are served with `Content-Security-Policy: sandbox`, so an uploaded HTML or SVG
  file can't run script on the device's origin.
