// Package hub provides a WebSocket broadcast hub and network trust utilities.
package hub

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
)

// Hub manages WebSocket connections and broadcasts messages to all clients.
type Hub struct {
	mu          sync.Mutex
	clients     map[*websocket.Conn]struct{}
	broadcast   chan []byte
	trustedNets []*net.IPNet
	upgrader    websocket.Upgrader
}

// New creates a Hub. trustedNets restricts which remote addresses may connect;
// pass nil to allow all.
func New(trustedNets []*net.IPNet) *Hub {
	h := &Hub{
		clients:   make(map[*websocket.Conn]struct{}),
		broadcast: make(chan []byte, 256),
		trustedNets: trustedNets,
		upgrader: websocket.Upgrader{
			// Origin checking is handled by isTrusted; accept all origins here.
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}
	go h.run()
	return h
}

// Broadcast queues data to be sent to all connected clients.
// It is safe to call from any goroutine. Drops silently if the buffer is full.
func (h *Hub) Broadcast(data []byte) {
	select {
	case h.broadcast <- data:
	default:
	}
}

// ServeWS upgrades the HTTP connection to a WebSocket and registers the client.
// Connections from untrusted addresses receive 403 Forbidden.
func (h *Hub) ServeWS(w http.ResponseWriter, r *http.Request) {
	if !h.isTrusted(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[hub] upgrade: %v", err)
		return
	}
	h.mu.Lock()
	h.clients[conn] = struct{}{}
	h.mu.Unlock()

	// Read pump — needed to detect disconnections and process control frames.
	go func() {
		defer func() {
			h.mu.Lock()
			delete(h.clients, conn)
			h.mu.Unlock()
			conn.Close()
		}()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				break
			}
		}
	}()
}

// run is the send loop; it runs in a dedicated goroutine.
func (h *Hub) run() {
	for data := range h.broadcast {
		h.mu.Lock()
		for conn := range h.clients {
			if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
				delete(h.clients, conn)
				conn.Close()
			}
		}
		h.mu.Unlock()
	}
}

// isTrusted returns true if the request's remote address falls within one of
// the hub's trusted networks, or if no networks are configured.
func (h *Hub) isTrusted(r *http.Request) bool {
	if len(h.trustedNets) == 0 {
		return true
	}
	host := r.RemoteAddr
	if h2, _, err := net.SplitHostPort(host); err == nil {
		host = h2
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, n := range h.trustedNets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// ParseTrustedCIDRs parses a comma-separated list of CIDR strings.
func ParseTrustedCIDRs(s string) ([]*net.IPNet, error) {
	var out []*net.IPNet
	for _, raw := range strings.Split(s, ",") {
		cidr := strings.TrimSpace(raw)
		if cidr == "" {
			continue
		}
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			return nil, fmt.Errorf("invalid CIDR %q: %w", cidr, err)
		}
		out = append(out, ipNet)
	}
	return out, nil
}

// DetectLocalSubnets returns the CIDRs of all local network interfaces.
func DetectLocalSubnets() []*net.IPNet {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []*net.IPNet
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			switch v := addr.(type) {
			case *net.IPNet:
				out = append(out, v)
			case *net.IPAddr:
				if mask := v.IP.DefaultMask(); mask != nil {
					out = append(out, &net.IPNet{IP: v.IP, Mask: mask})
				}
			}
		}
	}
	return out
}
