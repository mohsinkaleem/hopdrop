package hop

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode"

	"rsc.io/qr"
)

//go:embed web/index.html
var webFS embed.FS

// fwdHeader marks a request one daemon proxied to another; it is never proxied again.
const fwdHeader = "X-Hop-Fwd"

// Node is a running HopDrop device.
type Node struct {
	id, typ string
	port    int
	peers   *PeerTable
	client  *http.Client // probes
	fwd     http.RoundTripper
	udp     *net.UDPConn
	stop    context.CancelFunc

	mu        sync.Mutex // guards everything below
	cfg       Config
	transfers map[string]*transfer
	busy      map[string]bool // .part files being written right now
	tabs      map[string]*tab
	inbox     []*item // what this device received (newest last)
	subs      map[*sub]bool
}

// Info is the answer to GET /api/info.
type Info struct {
	ID    string   `json:"id"`
	Name  string   `json:"name"`
	Type  string   `json:"type"`
	Port  int      `json:"port"`
	Ver   string   `json:"ver"`
	Local bool     `json:"local"` // the request comes from this machine
	URLs  []string `json:"urls,omitempty"`
}

// Sender identifies who sent an offer or text.
type Sender struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

func NewNode(cfg Config) *Node {
	dial := &net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}
	return &Node{
		id: cfg.ID, typ: DeviceType(), port: cfg.Port, cfg: cfg,
		peers:     NewPeerTable(),
		client:    &http.Client{Timeout: 3 * time.Second},
		fwd:       &http.Transport{DialContext: dial.DialContext, MaxIdleConnsPerHost: 4, IdleConnTimeout: time.Minute},
		transfers: map[string]*transfer{},
		busy:      map[string]bool{},
		tabs:      map[string]*tab{},
		subs:      map[*sub]bool{},
	}
}

func (n *Node) Name() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.cfg.Name
}

func (n *Node) downloadDir() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.cfg.DownloadDir
}

func (n *Node) manualPeers() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.cfg.Peers...)
}

// Run serves HTTP and runs discovery until ctx is cancelled or /api/quit is called.
func (n *Node) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", n.port))
	if err != nil {
		return fmt.Errorf("port %d is busy (is HopDrop already running?): %w", n.port, err)
	}
	ctx, n.stop = context.WithCancel(ctx)
	_ = os.RemoveAll(spoolRoot()) // browser tabs' inboxes do not survive a restart

	if n.udp, err = listenUDP(DiscoveryPort); err != nil {
		log.Printf("UDP discovery unavailable (%v); only devices added by address will be found", err)
	} else {
		go n.heartbeatLoop(ctx)
		go n.listenLoop(ctx)
	}
	go n.livenessLoop(ctx)

	srv := &http.Server{
		Handler:           n.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx }, // ends SSE streams on shutdown
	}
	go func() {
		<-ctx.Done()
		n.broadcast(hello{Hop: helloVersion, ID: n.id, Bye: true})
		for _, t := range n.connectedTabs() {
			n.broadcast(hello{Hop: helloVersion, ID: t.ID, Bye: true})
		}
		if n.udp != nil {
			n.udp.Close()
		}
		sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()

	log.Printf("HopDrop %s: %s (%s) on port %d, saving to %s", Version, n.Name(), n.typ, n.port, n.downloadDir())
	for _, u := range lanURLs(n.port) {
		log.Printf("open %s on a phone", u)
	}
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	log.Printf("stopped")
	return nil
}

// Handler is the HTTP API and the page.
func (n *Node) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", servePage)
	mux.HandleFunc("GET /api/info", n.getInfo)
	mux.HandleFunc("PATCH /api/info", localOnly(n.patchInfo))
	mux.HandleFunc("GET /api/peers", n.listPeers)
	mux.HandleFunc("POST /api/peers", n.addPeer)
	mux.HandleFunc("POST /api/offer", n.routed(n.offer))
	mux.HandleFunc("PUT /api/offer/{id}/{index}", n.routed(n.upload))
	mux.HandleFunc("DELETE /api/offer/{id}", n.routed(n.cancel))
	mux.HandleFunc("POST /api/text", n.routed(n.text))
	mux.HandleFunc("GET /api/events", n.events)
	mux.HandleFunc("GET /api/inbox", n.inboxList)
	mux.HandleFunc("GET /api/inbox/{item}", n.inboxFile)
	mux.HandleFunc("DELETE /api/inbox/{item}", n.inboxDelete)
	mux.HandleFunc("POST /api/inbox/{item}/reveal", localOnly(n.reveal))
	mux.HandleFunc("POST /api/downloads", localOnly(n.openDownloads))
	mux.HandleFunc("GET /api/qr.svg", qrSVG)
	mux.HandleFunc("POST /api/quit", localOnly(n.quit))
	return mux
}

func servePage(w http.ResponseWriter, _ *http.Request) {
	page, _ := webFS.ReadFile("web/index.html")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(page)
}

// ---------------------------------------------------------------- helpers

type httpError struct {
	code int
	msg  string
}

func (e *httpError) Error() string { return e.msg }

func errorf(code int, format string, a ...any) error {
	return &httpError{code, fmt.Sprintf(format, a...)}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	var he *httpError
	if errors.As(err, &he) {
		code = he.code
	}
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

func readJSON(r *http.Request, limit int64, v any) error {
	if err := json.NewDecoder(io.LimitReader(r.Body, limit)).Decode(v); err != nil {
		return errorf(400, "bad request body: %v", err)
	}
	return nil
}

func localOnly(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !isLocalReq(r) {
			writeErr(w, errorf(403, "only available on this device"))
			return
		}
		h(w, r)
	}
}

// cleanName keeps a display name short and printable.
func cleanName(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	if r := []rune(s); len(r) > max {
		s = string(r[:max])
	}
	return strings.TrimSpace(s)
}

// ---------------------------------------------------------------- routing

// routed applies the one routing rule to a delivery endpoint, based on ?to=<device id>:
// this device (absent or our id) → h(…, ""); a browser tab we host → h(…, tabID); any other device →
// proxied to its address, at most once.
func (n *Node) routed(h func(http.ResponseWriter, *http.Request, string) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		to := r.URL.Query().Get("to")
		var err error
		switch {
		case to == "" || to == n.id:
			err = h(w, r, "")
		case n.hostsTab(to):
			err = h(w, r, to)
		case r.Header.Get(fwdHeader) != "":
			err = errorf(404, "device %s is not reachable here", to)
		default:
			p, ok := n.peers.Get(to)
			switch {
			case !ok:
				err = errorf(404, "unknown device %s", to)
			case !p.Online:
				err = errorf(404, "%s is offline", p.Name)
			default:
				n.proxy(w, r, p)
			}
		}
		if err != nil {
			writeErr(w, err)
		}
	}
}

func (n *Node) proxy(w http.ResponseWriter, r *http.Request, p Peer) {
	target := &url.URL{Scheme: "http", Host: p.Addr}
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Header.Set(fwdHeader, n.id)
		},
		Transport: n.fwd,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if r.Context().Err() == nil {
				writeErr(w, errorf(502, "%s is unreachable: %v", p.Name, err))
			}
		},
	}
	rp.ServeHTTP(w, r)
}

// ---------------------------------------------------------------- info, peers, qr, quit

func (n *Node) getInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, Info{ID: n.id, Name: n.Name(), Type: n.typ, Port: n.port, Ver: Version,
		Local: isLocalReq(r), URLs: lanURLs(n.port)})
}

func (n *Node) patchInfo(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := readJSON(r, 4096, &req); err != nil {
		writeErr(w, err)
		return
	}
	name := cleanName(req.Name, 60)
	if name == "" {
		writeErr(w, errorf(400, "name is empty"))
		return
	}
	n.mu.Lock()
	n.cfg.Name = name
	err := n.cfg.Save()
	n.mu.Unlock()
	if err != nil {
		writeErr(w, err)
		return
	}
	n.announce(false)
	n.peersChanged()
	n.getInfo(w, r)
}

func (n *Node) listPeers(w http.ResponseWriter, _ *http.Request) {
	ps := n.peers.List()
	for _, t := range n.connectedTabs() {
		ps = append(ps, Peer{ID: t.ID, Name: t.Name, Type: "web", Via: n.id, Online: true})
	}
	sortPeers(ps)
	writeJSON(w, 200, ps)
}

func (n *Node) addPeer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Addr string `json:"addr"`
	}
	if err := readJSON(r, 4096, &req); err != nil {
		writeErr(w, err)
		return
	}
	addr, err := parseAddr(strings.TrimSpace(req.Addr))
	if err != nil {
		writeErr(w, errorf(400, "%v", err))
		return
	}
	p, err := n.probe(r.Context(), addr)
	if err != nil {
		writeErr(w, errorf(400, "%v", err))
		return
	}
	n.mu.Lock()
	known := false
	for _, a := range n.cfg.Peers {
		known = known || a == addr
	}
	if !known {
		n.cfg.Peers = append(n.cfg.Peers, addr)
		err = n.cfg.Save()
	}
	n.mu.Unlock()
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, p)
}

func qrSVG(w http.ResponseWriter, r *http.Request) {
	text := r.URL.Query().Get("text")
	if text == "" || len(text) > 512 {
		writeErr(w, errorf(400, "text must be 1 to 512 bytes"))
		return
	}
	code, err := qr.Encode(text, qr.M)
	if err != nil {
		writeErr(w, errorf(400, "%v", err))
		return
	}
	const quiet = 2
	size := code.Size + 2*quiet
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" shape-rendering="crispEdges">`, size, size)
	fmt.Fprintf(&b, `<rect width="%d" height="%d" fill="#fff"/><path fill="#000" d="`, size, size)
	for y := 0; y < code.Size; y++ {
		for x := 0; x < code.Size; x++ {
			if code.Black(x, y) {
				fmt.Fprintf(&b, "M%d %dh1v1h-1z", x+quiet, y+quiet)
			}
		}
	}
	b.WriteString(`"/></svg>`)
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "max-age=3600")
	_, _ = io.WriteString(w, b.String())
}

func (n *Node) quit(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNoContent)
	if n.stop != nil {
		log.Printf("turning off (hop off)")
		go n.stop()
	}
}

// open shows path in the file manager; with reveal, selects it (macOS) or opens its folder.
func open(path string, reveal bool) {
	var cmd *exec.Cmd
	switch {
	case runtime.GOOS == "darwin" && reveal:
		cmd = exec.Command("open", "-R", path)
	case runtime.GOOS == "darwin":
		cmd = exec.Command("open", path)
	default:
		if reveal {
			path = parentDir(path)
		}
		cmd = exec.Command("xdg-open", path)
	}
	if err := cmd.Start(); err == nil {
		go func() { _ = cmd.Wait() }()
	}
}
