package hop

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testNode starts a node's HTTP API on a test server, with its own download folder.
func testNode(t *testing.T, name string) (*Node, *httptest.Server) {
	t.Helper()
	t.Setenv("HOPDROP_HOME", t.TempDir())
	n := NewNode(Config{ID: newID(), Name: name, Port: DefaultPort, DownloadDir: t.TempDir()})
	srv := httptest.NewServer(n.Handler())
	t.Cleanup(srv.Close)
	return n, srv
}

func call(t *testing.T, method, url string, body any, out any) int {
	t.Helper()
	var r io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		r = strings.NewReader(b)
	default:
		j, _ := json.Marshal(b)
		r = strings.NewReader(string(j))
	}
	req, _ := http.NewRequest(method, url, r)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if out != nil && resp.StatusCode < 300 {
		if err := json.Unmarshal(data, out); err != nil {
			t.Fatalf("%s %s: %v (%s)", method, url, err, data)
		}
	}
	return resp.StatusCode
}

var me = Sender{ID: "sender1", Name: "Phone", Type: "web"}

func offerFiles(files ...any) offerReq {
	req := offerReq{From: me}
	for i := 0; i < len(files); i += 2 {
		req.Files = append(req.Files, struct {
			Path string `json:"path"`
			Size int64  `json:"size"`
		}{files[i].(string), int64(files[i+1].(int))})
	}
	return req
}

func TestSendAndResume(t *testing.T) {
	n, srv := testNode(t, "Mac")
	dir := filepath.Join(n.downloadDir(), "Phone")

	var o offerResp
	if c := call(t, "POST", srv.URL+"/api/offer", offerFiles("a.txt", 5, "sub/b.txt", 3), &o); c != 200 || len(o.Offsets) != 2 {
		t.Fatalf("offer: %d %+v", c, o)
	}
	// Interrupted upload: 2 of 5 bytes stay in a.txt.part.
	if c := call(t, "PUT", srv.URL+"/api/offer/"+o.ID+"/0", "he", nil); c != 400 {
		t.Fatalf("short upload: %d", c)
	}
	// A new offer resumes at byte 2.
	if c := call(t, "POST", srv.URL+"/api/offer", offerFiles("a.txt", 5, "sub/b.txt", 3), &o); c != 200 || o.Offsets[0] != 2 {
		t.Fatalf("resume offer: %d %+v", c, o)
	}
	if c := call(t, "PUT", srv.URL+"/api/offer/"+o.ID+"/0?offset=1", "ello", nil); c != 416 {
		t.Fatalf("wrong offset: %d", c)
	}
	var res map[string]string
	if c := call(t, "PUT", srv.URL+"/api/offer/"+o.ID+"/0?offset=2", "llo", &res); c != 200 || res["name"] != "a.txt" {
		t.Fatalf("resume: %d %v", c, res)
	}
	if c := call(t, "PUT", srv.URL+"/api/offer/"+o.ID+"/0", "hello", nil); c != 409 {
		t.Fatalf("again: %d", c)
	}
	if c := call(t, "PUT", srv.URL+"/api/offer/"+o.ID+"/1", "toolong", nil); c != 400 {
		t.Fatalf("too long: %d", c)
	}
	if c := call(t, "PUT", srv.URL+"/api/offer/"+o.ID+"/1", "abc", nil); c != 200 {
		t.Fatalf("b: %d", c)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "a.txt")); string(got) != "hello" {
		t.Fatalf("a.txt = %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "sub", "b.txt")); string(got) != "abc" {
		t.Fatalf("b.txt = %q", got)
	}
	var items []item
	call(t, "GET", srv.URL+"/api/inbox", nil, &items)
	if len(items) != 1 || items[0].Kind != "files" || items[0].Size != 8 {
		t.Fatalf("inbox: %+v", items)
	}

	// Same name again → "a (1).txt".
	call(t, "POST", srv.URL+"/api/offer", offerFiles("a.txt", 2), &o)
	call(t, "PUT", srv.URL+"/api/offer/"+o.ID+"/0", "hi", &res)
	if res["name"] != "a (1).txt" {
		t.Fatalf("dup name: %v", res)
	}
}

func TestRejects(t *testing.T) {
	_, srv := testNode(t, "Mac")
	for _, p := range []string{"../x", "/etc/passwd", "a/../../b", ""} {
		if c := call(t, "POST", srv.URL+"/api/offer", offerFiles(p, 1), nil); c != 400 {
			t.Errorf("offer %q: %d", p, c)
		}
	}
	if c := call(t, "POST", srv.URL+"/api/offer?to=nobody", offerFiles("a", 1), nil); c != 404 {
		t.Errorf("unknown device: %d", c)
	}
}

func TestCancel(t *testing.T) {
	n, srv := testNode(t, "Mac")
	var o offerResp
	call(t, "POST", srv.URL+"/api/offer", offerFiles("big.bin", 10), &o)
	call(t, "PUT", srv.URL+"/api/offer/"+o.ID+"/0", "1234", nil)
	part := filepath.Join(n.downloadDir(), "Phone", "big.bin.part")
	if _, err := os.Stat(part); err != nil {
		t.Fatal("no .part after short upload")
	}
	if c := call(t, "DELETE", srv.URL+"/api/offer/"+o.ID, nil, nil); c != 204 {
		t.Fatalf("cancel: %d", c)
	}
	if _, err := os.Stat(part); err == nil {
		t.Fatal(".part kept after cancel")
	}
}

func TestText(t *testing.T) {
	_, srv := testNode(t, "Mac")
	call(t, "POST", srv.URL+"/api/text", map[string]any{"from": me, "text": "https://example.com/x"}, nil)
	call(t, "POST", srv.URL+"/api/text", map[string]any{"from": me, "text": "hello there"}, nil)
	var items []item
	call(t, "GET", srv.URL+"/api/inbox", nil, &items)
	if len(items) != 2 || items[0].Kind != "url" || items[1].Kind != "text" || items[1].Text != "hello there" {
		t.Fatalf("inbox: %+v", items)
	}
	if c := call(t, "DELETE", srv.URL+"/api/inbox/"+items[0].ID, nil, nil); c != 204 {
		t.Fatalf("dismiss: %d", c)
	}
}

// openTab connects a browser tab's event stream and returns its events.
func openTab(t *testing.T, srv *httptest.Server, id, name string) (<-chan string, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/events?id="+id+"&name="+name, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	ch := make(chan string, 16)
	go func() {
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if ev, ok := strings.CutPrefix(sc.Text(), "event: "); ok {
				ch <- ev
			}
		}
	}()
	if ev := <-ch; ev != "hello" {
		t.Fatalf("first event %q", ev)
	}
	return ch, cancel
}

func waitEvent(t *testing.T, ch <-chan string, want string) {
	t.Helper()
	timeout := time.After(2 * time.Second)
	for {
		select {
		case ev := <-ch:
			if ev == want {
				return
			}
		case <-timeout:
			t.Fatalf("no %q event", want)
		}
	}
}

// A Mac sends to a phone hosted by a Pi: the Mac's page posts to the Mac, which proxies to the Pi, which
// spools the file in the phone's inbox.
func TestForwardToTab(t *testing.T) {
	mac, macSrv := testNode(t, "Mac")
	pi, piSrv := testNode(t, "Pi")
	events, closeTab := openTab(t, piSrv, "phone1", "Pixel")

	var peers []Peer
	call(t, "GET", piSrv.URL+"/api/peers", nil, &peers)
	if len(peers) != 1 || peers[0].ID != "phone1" || !peers[0].Online {
		t.Fatalf("pi peers: %+v", peers)
	}
	// The Mac learned about the phone from the Pi's hello.
	mac.peers.Seen(Peer{ID: "phone1", Name: "Pixel", Type: "web", Via: pi.id, Addr: strings.TrimPrefix(piSrv.URL, "http://")})

	var o offerResp
	if c := call(t, "POST", macSrv.URL+"/api/offer?to=phone1", offerFiles("pic.png", 4), &o); c != 200 {
		t.Fatalf("forwarded offer: %d", c)
	}
	if c := call(t, "PUT", macSrv.URL+"/api/offer/"+o.ID+"/0?to=phone1", "\x89PNG", nil); c != 200 {
		t.Fatalf("forwarded upload: %d", c)
	}
	waitEvent(t, events, "inbox")
	var items []item
	call(t, "GET", piSrv.URL+"/api/inbox?id=phone1", nil, &items)
	if len(items) != 1 || items[0].Kind != "image" || items[0].Name != "pic.png" {
		t.Fatalf("phone inbox: %+v", items)
	}
	resp, err := http.Get(piSrv.URL + "/api/inbox/" + items[0].ID + "?id=phone1")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "\x89PNG" || !strings.HasPrefix(resp.Header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("download: %q %v", body, resp.Header)
	}
	// Nothing landed in the Pi's own download folder.
	if entries, _ := os.ReadDir(pi.downloadDir()); len(entries) != 0 {
		t.Fatalf("pi download dir not empty: %v", entries)
	}

	// Text to the phone, through the Mac.
	if c := call(t, "POST", macSrv.URL+"/api/text?to=phone1", map[string]any{"from": me, "text": "hi"}, nil); c != 204 {
		t.Fatalf("forwarded text: %d", c)
	}
	waitEvent(t, events, "inbox")

	// Once the tab closes, it is no longer listed and cannot receive.
	closeTab()
	deadline := time.Now().Add(5 * time.Second)
	for pi.tabOnline("phone1") {
		if time.Now().After(deadline) {
			t.Fatal("tab still online")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if c := call(t, "POST", piSrv.URL+"/api/text?to=phone1", map[string]any{"from": me, "text": "hi"}, nil); c != 404 {
		t.Fatalf("text to closed tab: %d", c)
	}
}

func TestNoDoubleForward(t *testing.T) {
	_, srv := testNode(t, "Mac")
	req, _ := http.NewRequest("POST", srv.URL+"/api/text?to=elsewhere", strings.NewReader(`{"text":"x"}`))
	req.Header.Set(fwdHeader, "other")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("got %d", resp.StatusCode)
	}
}

func TestQR(t *testing.T) {
	_, srv := testNode(t, "Mac")
	resp, err := http.Get(srv.URL + "/api/qr.svg?text=" + "http://192.168.1.4:47100/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasPrefix(string(body), "<svg") {
		t.Fatalf("qr: %d %s", resp.StatusCode, fmt.Sprint(len(body)))
	}
}
