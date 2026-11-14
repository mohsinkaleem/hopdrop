package hop

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"
)

const (
	heartbeat    = 5 * time.Second
	offlineAfter = 15 * time.Second // no hello for this long → offline
	probeAfter   = 7 * time.Second  // no hello for this long → probe over HTTP
	helloVersion = 2
)

// hello is the UDP discovery datagram, broadcast every few seconds.
type hello struct {
	Hop   int    `json:"hop"`
	ID    string `json:"id"`
	Name  string `json:"name,omitempty"`
	Type  string `json:"type,omitempty"`
	Port  int    `json:"port,omitempty"`
	Via   string `json:"via,omitempty"`   // browser tab: id of the daemon hosting it
	Probe bool   `json:"probe,omitempty"` // please reply right away (unicast)
	Bye   bool   `json:"bye,omitempty"`   // going away: mark offline now
}

func listenUDP(port int) (*net.UDPConn, error) {
	lc := net.ListenConfig{Control: reuseControl}
	pc, err := lc.ListenPacket(context.Background(), "udp4", fmt.Sprintf(":%d", port))
	if err != nil {
		return nil, err
	}
	return pc.(*net.UDPConn), nil
}

// ipv4Nets lists this machine's usable IPv4 addresses (not loopback or link-local) with their networks.
func ipv4Nets(broadcastOnly bool) []*net.IPNet {
	var out []*net.IPNet
	ifaces, _ := net.Interfaces()
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		if broadcastOnly && ifc.Flags&net.FlagBroadcast == 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok {
				if ip := n.IP.To4(); ip != nil && !ip.IsLinkLocalUnicast() {
					out = append(out, &net.IPNet{IP: ip, Mask: n.Mask[len(n.Mask)-4:]})
				}
			}
		}
	}
	return out
}

// broadcastAddrs is the directed broadcast address of each IPv4 network, or 255.255.255.255 if none.
func broadcastAddrs() []*net.UDPAddr {
	seen := map[string]bool{}
	var out []*net.UDPAddr
	for _, n := range ipv4Nets(true) {
		b := make(net.IP, 4)
		for i := range b {
			b[i] = n.IP[i] | ^n.Mask[i]
		}
		if !seen[b.String()] {
			seen[b.String()] = true
			out = append(out, &net.UDPAddr{IP: b, Port: DiscoveryPort})
		}
	}
	if len(out) == 0 {
		out = append(out, &net.UDPAddr{IP: net.IPv4bcast, Port: DiscoveryPort})
	}
	return out
}

// lanURLs is the page address for each LAN IP of this machine, private ranges first.
func lanURLs(port int) []string {
	var ips []net.IP
	for _, n := range ipv4Nets(false) {
		ips = append(ips, n.IP)
	}
	sort.SliceStable(ips, func(i, j int) bool { return ips[i].IsPrivate() && !ips[j].IsPrivate() })
	var out []string
	seen := map[string]bool{}
	for _, ip := range ips {
		u := fmt.Sprintf("http://%s:%d/", ip, port)
		if !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	return out
}

// isLocal reports whether ip belongs to this machine.
func isLocal(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.Equal(ip) {
			return true
		}
	}
	return false
}

func isLocalReq(r *http.Request) bool {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	return isLocal(net.ParseIP(host))
}

func (n *Node) selfHello(probe bool) hello {
	return hello{Hop: helloVersion, ID: n.id, Name: n.Name(), Type: n.typ, Port: n.port, Probe: probe}
}

func (n *Node) tabHello(id, name string) hello {
	return hello{Hop: helloVersion, ID: id, Name: name, Type: "web", Port: n.port, Via: n.id}
}

func (n *Node) broadcast(h hello) {
	if n.udp == nil {
		return
	}
	b, _ := json.Marshal(h)
	for _, a := range broadcastAddrs() {
		_, _ = n.udp.WriteToUDP(b, a)
	}
}

// announce broadcasts our own hello and one for each browser tab we host.
func (n *Node) announce(probe bool) {
	n.broadcast(n.selfHello(probe))
	for _, t := range n.connectedTabs() {
		n.broadcast(n.tabHello(t.ID, t.Name))
	}
}

func (n *Node) heartbeatLoop(ctx context.Context) {
	tick := time.NewTicker(heartbeat)
	defer tick.Stop()
	for probe := true; ; probe = false {
		n.announce(probe)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (n *Node) listenLoop(ctx context.Context) {
	buf := make([]byte, 2048)
	for {
		nb, src, err := n.udp.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			time.Sleep(time.Second)
			continue
		}
		var h hello
		if json.Unmarshal(buf[:nb], &h) != nil || h.Hop != helloVersion || h.ID == "" || h.ID == n.id || h.Via == n.id {
			continue
		}
		if h.Bye {
			if n.peers.Gone(h.ID) {
				n.peersChanged()
			}
			continue
		}
		if h.Probe {
			b, _ := json.Marshal(n.selfHello(false))
			_, _ = n.udp.WriteToUDP(b, src)
		}
		n.seen(Peer{ID: h.ID, Name: h.Name, Type: h.Type, Via: h.Via,
			Addr: net.JoinHostPort(src.IP.String(), strconv.Itoa(h.Port))})
	}
}

func (n *Node) seen(p Peer) {
	if n.peers.Seen(p) {
		log.Printf("+ %s (%s) at %s", p.Name, p.Type, p.Addr)
		n.peersChanged()
	}
}

// livenessLoop marks silent peers offline and probes quiet and manually added ones over HTTP.
func (n *Node) livenessLoop(ctx context.Context) {
	tick := time.NewTicker(3 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		n.prune()
		if gone := n.peers.Sweep(offlineAfter); len(gone) > 0 {
			for _, p := range gone {
				log.Printf("- %s offline", p.Name)
			}
			n.peersChanged()
		}
		targets := n.peers.Quiet(probeAfter, 10*time.Minute)
		for _, a := range n.manualPeers() {
			if !n.peers.OnlineAt(a) {
				targets = append(targets, a)
			}
		}
		var wg sync.WaitGroup
		for _, a := range targets {
			wg.Add(1)
			go func() { defer wg.Done(); _, _ = n.probe(ctx, a) }()
		}
		wg.Wait()
	}
}

// probe fetches /api/info from addr and records the device.
func (n *Node) probe(ctx context.Context, addr string) (Peer, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://"+addr+"/api/info", nil)
	resp, err := n.client.Do(req)
	if err != nil {
		return Peer{}, err
	}
	defer resp.Body.Close()
	var info Info
	if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&info) != nil || info.ID == "" {
		return Peer{}, fmt.Errorf("%s is not a HopDrop device", addr)
	}
	if info.ID == n.id {
		return Peer{}, fmt.Errorf("%s is this device", addr)
	}
	n.seen(Peer{ID: info.ID, Name: info.Name, Type: info.Type, Addr: addr})
	p, _ := n.peers.Get(info.ID)
	return p, nil
}

// parseAddr accepts host or host:port and returns host:port.
func parseAddr(s string) (string, error) {
	if host, port, err := net.SplitHostPort(s); err == nil {
		if _, err := strconv.ParseUint(port, 10, 16); host == "" || err != nil {
			return "", fmt.Errorf("%q is not host:port", s)
		}
		return s, nil
	}
	if s == "" {
		return "", fmt.Errorf("no address")
	}
	return net.JoinHostPort(s, strconv.Itoa(DefaultPort)), nil
}
