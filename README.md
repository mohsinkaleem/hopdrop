# HopDrop

Peer-to-peer file and text transfer between your devices on the home LAN: Macs, a Raspberry Pi, and any phone with a
browser. No hub, no cloud, no accounts, no app to install. One static Go binary. See [DESIGN.md](DESIGN.md).

## Mac

```bash
go install ./cmd/hop     # installs `hop` into ~/go/bin
hop open                 # start HopDrop in the background (if needed) and open its page
hop off                  # stop it
hop                      # or: run it in this terminal (Ctrl-C stops it)
```

Nothing starts at login. Received files go to `~/Downloads/HopDrop/<sender>/`; texts and links are listed on the page
with **Copy**.

**The page** (`hop open`, i.e. `http://localhost:47100/`) is the Mac's window. Select devices (several is fine),
then drop files (onto a device card to send just there), choose files or folders, type text, paste (⌘V; images are
sent straight away as `clipboard-….png`), or click *Send clipboard*. Uploads show speed and time left and can be
cancelled, and a failed one can be retried: it resumes where it stopped. *Received on <Mac>* lists what arrived,
with *Copy*, *Open* and *Show in Finder*. *Open on a phone* shows a QR code. Click your device name to rename it, and
use *Add by IP…* for a device that isn't found automatically. Closing the tab doesn't turn HopDrop off; `hop off`
does.

## Raspberry Pi (64-bit Pi OS)

```bash
scripts/build-pi.sh pi5     # cross-build, copy, install and start the systemd service (any ssh host works)
```

On the Pi it runs as the `hopdrop` service: `sudo systemctl stop|start hopdrop`, logs with
`journalctl -u hopdrop -f`.

## Phone (or any browser)

Open `http://<mac-or-pi-ip>:47100/`, or scan the QR code under *Open on a phone* on the Mac's page.

- Pick devices, then send files or text. Paste an image to send it. Uploads can be cancelled and retried.
- While the tab is open, the phone is a device too: the others list it and can send to it. Things arrive in the
  page's *Received* list with **Save** / **Copy** / **Open**.
- Tap your device name (top right) to rename it.
- The page talks only to the device that served it, and that device forwards to the others.

## Config

`~/Library/Application Support/hopdrop/config.json` (Mac) or `~/.config/hopdrop/config.json` (Linux): `name`,
`port`, `download_dir`, and `peers` (added by IP). `HOPDROP_HOME=/some/dir` uses a different folder, which is handy
for running two copies on one machine:

```bash
HOPDROP_HOME=/tmp/hop-b hop -name Test -port 47101 -dir /tmp/hop-b/dl
```

## Ports

TCP 47100 (HTTP and the page) and UDP 47100 (discovery).

## Development

```bash
go test -race ./...
go build ./cmd/hop
```
