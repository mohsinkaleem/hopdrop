# HopDrop

Send files and text between your devices on your home network: Macs, Linux boxes, a Raspberry Pi, and any phone
with a browser.

- **No cloud, no accounts, no pairing.** Devices find each other on the LAN automatically.
- **Peer to peer.** There is no central server, so any device can send to any other.
- **One small binary.** Written in Go using the standard library plus one package, for QR codes.
- **Phones need no app.** Open the page in a browser and the phone can send and receive.

> [!WARNING]
> HopDrop assumes **a network you trust**. It has no authentication or encryption, and it accepts every
> incoming transfer. Don't run it on public or shared Wi-Fi.

## Install

Requires Go 1.24 or later.

```bash
git clone https://github.com/mohsinkaleem/hopdrop.git
cd hopdrop
go install ./cmd/hop        # installs `hop` into $(go env GOPATH)/bin
```

## Usage

```bash
hop open     # start HopDrop in the background (if it isn't running) and open its page
hop off      # stop it
hop          # or run it in the foreground in this terminal (Ctrl-C stops it)
```

Everything happens on the page at `http://localhost:47100/`:

- **Send:** pick one or more devices, then drop files or folders, type or paste text, or paste an image.
  Dropping onto a device card sends only to that device.
- **Transfers:** each upload shows its speed and time left. You can cancel an upload, and a failed one resumes
  from where it stopped when you retry it.
- **Receive:** received items are listed with *Copy*, *Open* and *Show in Finder*. Files are saved to
  `~/Downloads/HopDrop/<sender>/`.
- **Devices:** click your device name to rename it. Use *Add by IP…* for a device that isn't found
  automatically.

HopDrop doesn't start at login. Closing the browser tab leaves it running; `hop off` stops it.

### Phones (or any browser)

Open `http://<device-ip>:47100/`, or scan the QR code under *Open on a phone* on another device's page.

While the tab is open, the phone counts as a device: it can send to the others, and the others can send to it.
Received items show up on the page with *Save*, *Copy* and *Open*. The phone only talks to the device that
served the page, and that device relays its transfers.

### Raspberry Pi

Run this on your Mac or Linux machine. It cross-compiles for 64-bit Pi OS and installs HopDrop on the Pi as a
systemd service:

```bash
scripts/build-pi.sh pi@raspberrypi   # any ssh host works; omit it to build only (dist/hop-linux-arm64)
```

Manage it on the Pi with `sudo systemctl start|stop|restart hopdrop`, and follow its logs with
`journalctl -u hopdrop -f`. For 32-bit Pi OS, build with `GOARCH=arm GOARM=7`.

## Configuration

The config file is `config.json` in:

| OS | Folder |
|---|---|
| macOS | `~/Library/Application Support/hopdrop/` |
| Linux | `~/.config/hopdrop/` |

It holds `name`, `port`, `download_dir`, and `peers` (devices added by IP). Set `HOPDROP_HOME` to use a
different folder. Flags override the config for one run:

```bash
hop -name Studio -port 47101 -dir ~/Incoming
```

The flags and `HOPDROP_HOME` together let you run a second copy on the same machine for testing:

```bash
HOPDROP_HOME=/tmp/hop-b hop -name Test -port 47101 -dir /tmp/hop-b/dl
```

## Network

| Port | Protocol | Purpose |
|---|---|---|
| 47100 | TCP | HTTP API and the web page |
| 47100 | UDP | Discovery (broadcast heartbeats) |

If devices don't see each other, check that your firewall allows these ports and that client isolation is off
on your router. You can also use *Add by IP…*.

## Development

```bash
go test -race ./...
go build ./cmd/hop
```

See [DESIGN.md](DESIGN.md) for the architecture, routing, HTTP API and discovery protocol.
