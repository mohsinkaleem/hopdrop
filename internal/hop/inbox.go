package hop

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	maxSelfItems = 100            // this device's received list (in memory)
	tabItemTTL   = 24 * time.Hour // a browser tab's items are dropped after this long
	tabForget    = time.Hour      // a closed tab with an empty inbox is forgotten after this long
)

// item is something received: a file (or several), an image, a text or a link.
type item struct {
	ID   string `json:"id"`
	From Sender `json:"from"`
	Kind string `json:"kind"`           // file, image, files, text, url
	Name string `json:"name,omitempty"` // file name, or "3 files"
	Size int64  `json:"size"`
	Text string `json:"text,omitempty"`
	At   int64  `json:"at"`
	path string // file to serve or reveal (the first one, for several)
}

// tab is a browser (phone) that opened our page and acts as a device while it is open.
type tab struct {
	ID, Name string
	conns    int
	items    []*item
	seen     time.Time
}

func spoolRoot() string          { return filepath.Join(CacheDir(), "spool") }
func spoolDir(tab string) string { return filepath.Join(spoolRoot(), tab) }

func validID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func fileItem(from Sender, paths []string) *item {
	it := &item{ID: newID(), From: from, Kind: "files", Name: fmt.Sprintf("%d files", len(paths)), At: time.Now().Unix()}
	for _, p := range paths {
		if st, err := os.Stat(p); err == nil {
			it.Size += st.Size()
		}
	}
	if len(paths) > 0 {
		it.path = paths[0]
	}
	if len(paths) == 1 {
		it.Name, it.Kind = filepath.Base(paths[0]), "file"
		if isImage(it.Name) {
			it.Kind = "image"
		}
	}
	return it
}

func isImage(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".heic", ".heif", ".avif", ".bmp":
		return true
	}
	return false
}

func parentDir(p string) string { return filepath.Dir(p) }

// ---------------------------------------------------------------- tabs

func (n *Node) hostsTab(id string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.tabs[id] != nil
}

func (n *Node) tabOnline(id string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	t := n.tabs[id]
	return t != nil && t.conns > 0
}

func (n *Node) connectedTabs() []tab {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []tab
	for _, t := range n.tabs {
		if t.conns > 0 {
			out = append(out, tab{ID: t.ID, Name: t.Name})
		}
	}
	return out
}

func (n *Node) tabConnect(id, name string) {
	n.mu.Lock()
	t := n.tabs[id]
	if t == nil {
		t = &tab{ID: id}
		n.tabs[id] = t
	}
	changed := t.conns == 0 || t.Name != name
	t.Name, t.seen = name, time.Now()
	t.conns++
	n.mu.Unlock()
	if changed {
		log.Printf("+ browser %q connected", name)
		n.broadcast(n.tabHello(id, name))
		n.peersChanged()
	}
}

func (n *Node) tabDisconnect(id string) {
	n.mu.Lock()
	t := n.tabs[id]
	t.conns--
	t.seen = time.Now()
	gone, name := t.conns == 0, t.Name
	n.mu.Unlock()
	if gone {
		// Give a reloading tab a moment to reconnect before telling everyone it left.
		time.AfterFunc(2*time.Second, func() {
			if !n.tabOnline(id) {
				log.Printf("- browser %q closed", name)
				n.broadcast(hello{Hop: helloVersion, ID: id, Bye: true})
				n.peersChanged()
			}
		})
	}
}

func (n *Node) pruneTabs() {
	n.mu.Lock()
	defer n.mu.Unlock()
	for id, t := range n.tabs {
		keep := t.items[:0]
		for _, it := range t.items {
			if time.Since(time.Unix(it.At, 0)) < tabItemTTL {
				keep = append(keep, it)
			} else if it.path != "" {
				_ = os.Remove(it.path)
			}
		}
		t.items = keep
		if t.conns == 0 && len(t.items) == 0 && time.Since(t.seen) > tabForget {
			delete(n.tabs, id)
			_ = os.RemoveAll(spoolDir(id))
		}
	}
}

// ---------------------------------------------------------------- delivery and events

func (n *Node) deliverSelf(it *item) {
	n.mu.Lock()
	n.inbox = append(n.inbox, it)
	if len(n.inbox) > maxSelfItems {
		n.inbox = n.inbox[len(n.inbox)-maxSelfItems:]
	}
	n.mu.Unlock()
	n.publish("", "inbox", it)
}

func (n *Node) deliverTab(id string, it *item) {
	n.mu.Lock()
	if t := n.tabs[id]; t != nil {
		t.items = append(t.items, it)
	}
	n.mu.Unlock()
	n.publish(id, "inbox", it)
}

// sub is one open event stream: this device's own page (key "") or a browser tab (key = its id).
type sub struct {
	key string
	ch  chan []byte
}

// publish sends an event to the streams with this key; key "*" means all of them.
func (n *Node) publish(key, event string, data any) {
	b, _ := json.Marshal(data)
	msg := []byte(fmt.Sprintf("event: %s\ndata: %s\n\n", event, b))
	n.mu.Lock()
	defer n.mu.Unlock()
	for s := range n.subs {
		if key == "*" || s.key == key {
			select {
			case s.ch <- msg:
			default: // slow reader: drop rather than block
			}
		}
	}
}

func (n *Node) peersChanged() { n.publish("*", "peers", "changed") }

// events: GET /api/events[?id=&name=] — server-sent events. With id, a browser tab registers as a device
// for as long as the stream is open. Without, it is this device's own page (local only).
func (n *Node) events(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" && !isLocalReq(r) {
		writeErr(w, errorf(403, "only available on this device"))
		return
	}
	if id != "" && !validID(id) {
		writeErr(w, errorf(400, "bad id"))
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, errorf(500, "streaming unsupported"))
		return
	}
	s := &sub{key: id, ch: make(chan []byte, 32)}
	n.mu.Lock()
	n.subs[s] = true
	n.mu.Unlock()
	defer func() {
		n.mu.Lock()
		delete(n.subs, s)
		n.mu.Unlock()
	}()
	if id != "" {
		name := cleanName(r.URL.Query().Get("name"), 40)
		if name == "" {
			name = "Phone"
		}
		n.tabConnect(id, name)
		defer n.tabDisconnect(id)
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	fmt.Fprintf(w, "event: hello\ndata: %q\n\n", n.id)
	flusher.Flush()
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case msg := <-s.ch:
			_, _ = w.Write(msg)
		case <-ping.C:
			_, _ = w.Write([]byte(": ping\n\n"))
		}
		flusher.Flush()
	}
}

// ---------------------------------------------------------------- inbox endpoints

// items returns the inbox selected by ?id= (a browser tab), or this device's (local requests only).
func (n *Node) items(r *http.Request) ([]*item, error) {
	id := r.URL.Query().Get("id")
	if id == "" {
		if !isLocalReq(r) {
			return nil, errorf(403, "only available on this device")
		}
		return n.inbox, nil
	}
	if t := n.tabs[id]; t != nil {
		return t.items, nil
	}
	return nil, nil
}

func (n *Node) findItem(r *http.Request) (item, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	list, err := n.items(r)
	if err != nil {
		return item{}, err
	}
	for _, it := range list {
		if it.ID == r.PathValue("item") {
			return *it, nil
		}
	}
	return item{}, errorf(404, "no such item")
}

// inboxList: GET /api/inbox[?id=] — oldest first.
func (n *Node) inboxList(w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	list, err := n.items(r)
	list = append([]*item{}, list...)
	n.mu.Unlock()
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, list)
}

// inboxFile: GET /api/inbox/{item}[?id=&inline=1] — download, or view in the browser.
func (n *Node) inboxFile(w http.ResponseWriter, r *http.Request) {
	it, err := n.findItem(r)
	if err == nil && it.path == "" {
		err = errorf(404, "not a file")
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	f, err := os.Open(it.path)
	if err != nil {
		writeErr(w, errorf(404, "the file is gone (moved or deleted?)"))
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		writeErr(w, err)
		return
	}
	name := filepath.Base(it.path)
	disp := "attachment"
	if r.URL.Query().Has("inline") {
		disp = "inline"
	}
	ascii := strings.Map(func(c rune) rune {
		if c < 0x20 || c > 0x7e || c == '"' || c == '\\' {
			return '_'
		}
		return c
	}, name)
	h := w.Header()
	h.Set("Content-Disposition", fmt.Sprintf(`%s; filename="%s"; filename*=UTF-8''%s`, disp, ascii, pctEncode(name)))
	// Received files are untrusted: never let one run script on this origin.
	h.Set("Content-Security-Policy", "sandbox")
	h.Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, name, st.ModTime(), f)
}

// inboxDelete: DELETE /api/inbox/{item}[?id=] — dismiss. A tab's spooled file is deleted too; this
// device's files stay in the download folder.
func (n *Node) inboxDelete(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	n.mu.Lock()
	list, err := n.items(r)
	var removed *item
	for i, it := range list {
		if it.ID == r.PathValue("item") {
			removed = it
			list = append(list[:i:i], list[i+1:]...)
			break
		}
	}
	if removed != nil {
		if id == "" {
			n.inbox = list
		} else {
			n.tabs[id].items = list
		}
	}
	n.mu.Unlock()
	switch {
	case err != nil:
		writeErr(w, err)
	case removed == nil:
		writeErr(w, errorf(404, "no such item"))
	default:
		if id != "" && removed.path != "" {
			_ = os.Remove(removed.path)
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// reveal: POST /api/inbox/{item}/reveal — show the file in Finder (local only).
func (n *Node) reveal(w http.ResponseWriter, r *http.Request) {
	it, err := n.findItem(r)
	if err == nil {
		if _, serr := os.Stat(it.path); it.path == "" || serr != nil {
			err = errorf(404, "the file is gone (moved or deleted?)")
		}
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	open(it.path, true)
	w.WriteHeader(http.StatusNoContent)
}

// openDownloads: POST /api/downloads — open the download folder (local only).
func (n *Node) openDownloads(w http.ResponseWriter, _ *http.Request) {
	dir := n.downloadDir()
	_ = os.MkdirAll(dir, 0o755)
	open(dir, false)
	w.WriteHeader(http.StatusNoContent)
}

func pctEncode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.IndexByte("-._~", c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}
