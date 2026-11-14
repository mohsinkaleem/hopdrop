package hop

import (
	"sort"
	"strings"
	"sync"
	"time"
)

// Peer is another device. For a browser tab (type "web"), Addr is the daemon hosting it and Via is that
// daemon's id.
type Peer struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Type     string    `json:"type"`
	Addr     string    `json:"addr,omitempty"`
	Via      string    `json:"via,omitempty"`
	Online   bool      `json:"online"`
	LastSeen time.Time `json:"-"`
}

// PeerTable tracks peers and whether they are online.
type PeerTable struct {
	mu sync.Mutex
	m  map[string]*Peer
}

func NewPeerTable() *PeerTable { return &PeerTable{m: map[string]*Peer{}} }

// Seen records that p was heard from. It reports whether the visible state changed (came online, renamed).
func (t *PeerTable) Seen(p Peer) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	old, ok := t.m[p.ID]
	changed := !ok || !old.Online || old.Name != p.Name || old.Addr != p.Addr
	p.Online, p.LastSeen = true, time.Now()
	t.m[p.ID] = &p
	return changed
}

// Gone marks a peer offline right away (it said goodbye). Reports whether it was online.
func (t *PeerTable) Gone(id string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	p, ok := t.m[id]
	if !ok || !p.Online {
		return false
	}
	p.Online = false
	return true
}

// Sweep marks peers silent for longer than after as offline and returns them.
func (t *PeerTable) Sweep(after time.Duration) []Peer {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []Peer
	for _, p := range t.m {
		if p.Online && time.Since(p.LastSeen) > after {
			p.Online = false
			out = append(out, *p)
		}
	}
	return out
}

// Quiet lists addresses of directly reachable peers silent for longer than quiet but heard from within
// maxAge: candidates for an HTTP probe.
func (t *PeerTable) Quiet(quiet, maxAge time.Duration) []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []string
	for _, p := range t.m {
		if age := time.Since(p.LastSeen); p.Via == "" && age > quiet && age < maxAge {
			out = append(out, p.Addr)
		}
	}
	return out
}

func (t *PeerTable) Get(id string) (Peer, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	p, ok := t.m[id]
	if !ok {
		return Peer{}, false
	}
	return *p, true
}

func (t *PeerTable) OnlineAt(addr string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, p := range t.m {
		if p.Online && p.Via == "" && p.Addr == addr {
			return true
		}
	}
	return false
}

// List returns all peers except offline browser tabs (those are only listed while open).
func (t *PeerTable) List() []Peer {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]Peer, 0, len(t.m))
	for _, p := range t.m {
		if p.Type == "web" && !p.Online {
			continue
		}
		out = append(out, *p)
	}
	return out
}

// sortPeers orders online first, then by name.
func sortPeers(ps []Peer) {
	sort.Slice(ps, func(i, j int) bool {
		if ps[i].Online != ps[j].Online {
			return ps[i].Online
		}
		return strings.ToLower(ps[i].Name) < strings.ToLower(ps[j].Name)
	})
}
