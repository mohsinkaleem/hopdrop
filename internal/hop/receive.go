package hop

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// transferTTL: idle transfers are forgotten after this long (their .part files stay, for resume).
const transferTTL = time.Hour

type transfer struct {
	id      string
	from    Sender
	tab     string // "" = for this device; else a browser tab we host
	files   []*incoming
	touched time.Time
}

type incoming struct {
	rel   string // as offered, for display
	dest  string
	size  int64
	done  bool
	saved string // final path once done
}

type offerReq struct {
	From  Sender `json:"from"`
	Files []struct {
		Path string `json:"path"`
		Size int64  `json:"size"`
	} `json:"files"`
}

type offerResp struct {
	ID      string  `json:"id"`
	Offsets []int64 `json:"offsets"` // bytes already here per file: resume from there
}

// offer: POST /api/offer — announce files; the answer says where to resume each one.
func (n *Node) offer(w http.ResponseWriter, r *http.Request, tab string) error {
	var req offerReq
	if err := readJSON(r, 4<<20, &req); err != nil {
		return err
	}
	if len(req.Files) == 0 {
		return errorf(400, "offer has no files")
	}
	if tab != "" && !n.tabOnline(tab) {
		return errorf(404, "that browser tab is closed")
	}
	req.From.Name = cleanName(req.From.Name, 60)
	root := filepath.Join(n.downloadDir(), sanitizeComponent(req.From.Name))
	if tab != "" {
		root = filepath.Join(spoolDir(tab), sanitizeComponent(req.From.Name))
	}
	t := &transfer{id: newID(), from: req.From, tab: tab, touched: time.Now()}
	resp := offerResp{ID: t.id, Offsets: make([]int64, len(req.Files))}
	var total int64
	for i, f := range req.Files {
		rel, ok := sanitizeRel(f.Path)
		if !ok || f.Size < 0 {
			return errorf(400, "unsafe path %q", f.Path)
		}
		dest := filepath.Join(root, rel)
		if st, err := os.Stat(partPath(dest)); err == nil {
			if st.Size() <= f.Size {
				resp.Offsets[i] = st.Size()
			} else {
				_ = os.Remove(partPath(dest))
			}
		}
		t.files = append(t.files, &incoming{rel: f.Path, dest: dest, size: f.Size})
		total += f.Size
	}
	n.mu.Lock()
	n.transfers[t.id] = t
	n.mu.Unlock()
	log.Printf("<- %s offers %d file(s), %s%s", req.From.Name, len(t.files), fmtBytes(total), n.forTab(tab))
	writeJSON(w, 200, resp)
	return nil
}

// upload: PUT /api/offer/{id}/{index}?offset= — the file's bytes from offset. When all bytes are here the
// .part file becomes the real file.
func (n *Node) upload(w http.ResponseWriter, r *http.Request, tab string) error {
	offset, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	if r.URL.Query().Get("offset") == "" {
		offset, err = 0, nil
	}
	if err != nil || offset < 0 {
		return errorf(400, "bad offset")
	}
	index, _ := strconv.Atoi(r.PathValue("index"))

	n.mu.Lock()
	t, ok := n.transfers[r.PathValue("id")]
	if !ok || t.tab != tab || index < 0 || index >= len(t.files) {
		n.mu.Unlock()
		return errorf(404, "no such transfer")
	}
	f := t.files[index]
	part := partPath(f.dest)
	switch {
	case f.done:
		n.mu.Unlock()
		return errorf(409, "file already received")
	case n.busy[part]:
		n.mu.Unlock()
		return errorf(409, "file is already being received")
	}
	n.busy[part] = true
	t.touched = time.Now()
	n.mu.Unlock()
	defer func() {
		n.mu.Lock()
		delete(n.busy, part)
		n.mu.Unlock()
	}()

	saved, err := receiveFile(r.Body, f.dest, offset, f.size)
	if err != nil {
		return err
	}
	n.mu.Lock()
	f.done, f.saved = true, saved
	t.touched = time.Now()
	complete := true
	for _, g := range t.files {
		complete = complete && g.done
	}
	n.mu.Unlock()

	if tab != "" {
		n.deliverTab(tab, fileItem(t.from, []string{saved}))
	}
	if complete {
		n.completed(t)
	}
	writeJSON(w, 200, map[string]string{"name": filepath.Base(saved)})
	return nil
}

// receiveFile appends body to dest's .part file from offset and, once it holds size bytes, renames it to a
// free name next to dest. It returns that final path.
func receiveFile(body io.Reader, dest string, offset, size int64) (string, error) {
	part := partPath(dest)
	if offset > size {
		return "", errorf(416, "offset %d is past the end (%d bytes)", offset, size)
	}
	if err := os.MkdirAll(filepath.Dir(part), 0o755); err != nil {
		return "", err
	}
	var out *os.File
	var err error
	if offset == 0 {
		out, err = os.Create(part)
	} else {
		var have int64
		if st, serr := os.Stat(part); serr == nil {
			have = st.Size()
		}
		if have != offset {
			return "", errorf(416, "resume at %d, but %d bytes are here", offset, have)
		}
		out, err = os.OpenFile(part, os.O_WRONLY|os.O_APPEND, 0)
	}
	if err != nil {
		return "", err
	}
	want := size - offset
	got, err := io.CopyBuffer(out, io.LimitReader(body, want+1), make([]byte, 1<<20))
	if cerr := out.Close(); err == nil && cerr != nil {
		return "", cerr
	}
	switch {
	case err != nil:
		return "", errorf(400, "upload interrupted after %d bytes: %v", offset+got, err)
	case got > want:
		_ = os.Remove(part)
		return "", errorf(400, "more bytes than offered")
	case got < want:
		return "", errorf(400, "incomplete upload: %d of %d bytes", offset+got, size)
	}
	final := uniquePath(dest)
	if err := os.Rename(part, final); err != nil {
		return "", err
	}
	return final, nil
}

// completed runs once every file of t has arrived.
func (n *Node) completed(t *transfer) {
	n.mu.Lock()
	delete(n.transfers, t.id)
	saved := make([]string, len(t.files))
	var total int64
	for i, f := range t.files {
		saved[i] = f.saved
		total += f.size
	}
	n.mu.Unlock()
	log.Printf("<- got %d file(s), %s from %s%s", len(saved), fmtBytes(total), t.from.Name, n.forTab(t.tab))
	if t.tab == "" {
		n.deliverSelf(fileItem(t.from, saved))
	}
}

// cancel: DELETE /api/offer/{id} — forget the transfer and its partial files.
func (n *Node) cancel(w http.ResponseWriter, r *http.Request, tab string) error {
	n.mu.Lock()
	t, ok := n.transfers[r.PathValue("id")]
	if ok && t.tab == tab {
		delete(n.transfers, t.id)
	}
	n.mu.Unlock()
	if !ok || t.tab != tab {
		return errorf(404, "no such transfer")
	}
	for _, f := range t.files {
		if !f.done {
			_ = os.Remove(partPath(f.dest))
		}
	}
	log.Printf("<- %s cancelled a transfer", t.from.Name)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// text: POST /api/text — a text or a link.
func (n *Node) text(w http.ResponseWriter, r *http.Request, tab string) error {
	var req struct {
		From Sender `json:"from"`
		Text string `json:"text"`
	}
	if err := readJSON(r, 1<<20, &req); err != nil {
		return err
	}
	if strings.TrimSpace(req.Text) == "" {
		return errorf(400, "text is empty")
	}
	req.From.Name = cleanName(req.From.Name, 60)
	it := &item{ID: newID(), From: req.From, Kind: "text", Text: req.Text, Size: int64(len(req.Text)), At: time.Now().Unix()}
	if isURL(req.Text) {
		it.Kind = "url"
	}
	if tab != "" {
		if !n.tabOnline(tab) {
			return errorf(404, "that browser tab is closed")
		}
		n.deliverTab(tab, it)
	} else {
		n.deliverSelf(it)
	}
	log.Printf("<- %s from %s%s", it.Kind, req.From.Name, n.forTab(tab))
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// prune forgets idle transfers and expired tab inboxes.
func (n *Node) prune() {
	n.mu.Lock()
	for id, t := range n.transfers {
		if time.Since(t.touched) > transferTTL {
			delete(n.transfers, id)
		}
	}
	n.mu.Unlock()
	n.pruneTabs()
}

func (n *Node) forTab(tab string) string {
	if tab == "" {
		return ""
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if t := n.tabs[tab]; t != nil {
		return " for " + t.Name
	}
	return ""
}

func isURL(s string) bool {
	s = strings.TrimSpace(s)
	return (strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")) && !strings.ContainsAny(s, " \t\r\n")
}

func fmtBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for m := b / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
