package server

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	v2 "github.com/komari-monitor/komari-agent/protocol/v2"
	"github.com/komari-monitor/komari-agent/ws"
	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

const routeHopTimeout = 900 * time.Millisecond

var routeMu sync.Mutex

// handleAgentRoute validates incoming route parameters and dispatches the probe.
func handleAgentRoute(conn *ws.SafeConn, params interface{}) {
	var p v2.RouteParams
	if err := v2.BindParams(params, &p); err != nil {
		log.Printf("bad v2 route params: %v", err)
		return
	}
	if p.TaskID == 0 || p.Target == "" {
		log.Printf("route: invalid task_id or target")
		return
	}
	if p.Protocol == "" {
		p.Protocol = "icmp"
	}
	if p.Protocol != "icmp" {
		log.Printf("route: unsupported protocol %q", p.Protocol)
		return
	}
	if p.IPVersion != 4 && p.IPVersion != 6 {
		p.IPVersion = 4
	}
	if p.MaxHops <= 0 || p.MaxHops > 30 {
		p.MaxHops = 30
	}
	if !routeMu.TryLock() {
		log.Printf("route: probe already in progress, skipping task %d", p.TaskID)
		return
	}
	go func() {
		defer routeMu.Unlock()
		executeRouteProbe(conn, p)
	}()
}

func executeRouteProbe(conn *ws.SafeConn, p v2.RouteParams) {
	deadline := time.Duration(p.MaxHops) * (routeHopTimeout + 200*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()

	targetIP, err := resolveRouteTarget(p.Target, p.IPVersion)
	if err != nil {
		sendRouteResult(conn, p, nil, err.Error())
		return
	}

	var hops []v2.RouteHop
	var probeErr error
	if p.IPVersion == 6 {
		hops, probeErr = traceRouteICMPv6(ctx, targetIP, p.MaxHops)
	} else {
		hops, probeErr = traceRouteICMPv4(ctx, targetIP, p.MaxHops)
	}

	errText := ""
	if probeErr != nil {
		errText = probeErr.Error()
	}
	sendRouteResult(conn, p, hops, errText)
}

func sendRouteResult(conn *ws.SafeConn, p v2.RouteParams, hops []v2.RouteHop, probeErr string) {
	result := v2.RouteResultParams{
		TaskID:     p.TaskID,
		Protocol:   p.Protocol,
		Target:     p.Target,
		IPVersion:  p.IPVersion,
		Hops:       hops,
		Error:      probeErr,
		FinishedAt: time.Now().UTC(),
	}
	if conn != nil {
		payload := v2.BuildRouteResultPayload(result)
		if err := conn.WriteMessage(1, payload); err != nil {
			log.Printf("route: failed to send result via WebSocket: %v", err)
		}
		return
	}
	// POST fallback: wrap in a JSON-RPC request.
	req := v2.Request{
		JSONRPC: v2.Version,
		Method:  v2.MethodAgentRouteResult,
		Params:  result,
	}
	if err := postV2RPC(req); err != nil {
		log.Printf("route: failed to POST result: %v", err)
	}
}

// resolveRouteTarget resolves a hostname to the requested IP version.
func resolveRouteTarget(target string, ipVersion int) (string, error) {
	target = strings.TrimSpace(target)
	if ip := net.ParseIP(target); ip != nil {
		if ipVersion == 6 && ip.To4() != nil {
			return "", fmt.Errorf("target %s is not an IPv6 address", target)
		}
		if ipVersion == 4 && ip.To4() == nil {
			return "", fmt.Errorf("target %s is not an IPv4 address", target)
		}
		return target, nil
	}
	ips, err := net.LookupIP(target)
	if err != nil || len(ips) == 0 {
		return "", fmt.Errorf("failed to resolve %s", target)
	}
	for _, ip := range ips {
		if ipVersion == 6 && ip.To4() == nil {
			return ip.String(), nil
		}
		if ipVersion == 4 && ip.To4() != nil {
			return ip.To4().String(), nil
		}
	}
	return "", fmt.Errorf("no IPv%d address found for %s", ipVersion, target)
}

// ---------- IPv4 traceroute ----------

func traceRouteICMPv4(ctx context.Context, target string, maxHops int) ([]v2.RouteHop, error) {
	targetIP := net.ParseIP(target)
	if targetIP == nil {
		return nil, fmt.Errorf("invalid IPv4 target %q", target)
	}

	conn, err := icmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		return nil, fmt.Errorf("open built-in ICMP route probe (root/CAP_NET_RAW may be required): %w", err)
	}
	defer conn.Close()
	packet := conn.IPv4PacketConn()
	if packet == nil {
		return nil, fmt.Errorf("open built-in IPv4 route probe: packet connection is unavailable")
	}

	hops := make([]v2.RouteHop, 0, maxHops)
	id := os.Getpid() & 0xffff
	for ttl := 1; ttl <= maxHops; ttl++ {
		select {
		case <-ctx.Done():
			return hops, nil
		default:
		}

		if err := packet.SetTTL(ttl); err != nil {
			return hops, fmt.Errorf("set IPv4 TTL: %w", err)
		}
		message := icmp.Message{Type: ipv4.ICMPTypeEcho, Code: 0, Body: &icmp.Echo{ID: id, Seq: ttl, Data: []byte("KOMARI-ROUTE")}}
		payload, err := message.Marshal(nil)
		if err != nil {
			return hops, err
		}
		start := time.Now()
		_ = conn.SetDeadline(start.Add(routeHopTimeout))
		if _, err := conn.WriteTo(payload, &net.IPAddr{IP: targetIP}); err != nil {
			return hops, fmt.Errorf("send IPv4 route probe: %w", err)
		}
		buffer := make([]byte, 1500)
		n, peer, err := conn.ReadFrom(buffer)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				hops = append(hops, v2.RouteHop{TTL: ttl, Timeout: true})
				continue
			}
			return hops, fmt.Errorf("read IPv4 route probe: %w", err)
		}
		reply, err := icmp.ParseMessage(1, buffer[:n])
		if err != nil {
			hops = append(hops, v2.RouteHop{TTL: ttl, Timeout: true})
			continue
		}
		ip := routePeerIP(peer)
		hops = append(hops, v2.RouteHop{TTL: ttl, IP: ip, LatencyMS: float64(time.Since(start).Microseconds()) / 1000})
		if reply.Type == ipv4.ICMPTypeEchoReply || net.ParseIP(ip).Equal(targetIP) {
			break
		}
	}
	return hops, nil
}

func routePeerIP(addr net.Addr) string {
	switch value := addr.(type) {
	case *net.IPAddr:
		return value.IP.String()
	case *net.UDPAddr:
		return value.IP.String()
	default:
		return strings.Split(addr.String(), "%")[0]
	}
}

// ---------- IPv6 traceroute ----------

func traceRouteICMPv6(ctx context.Context, target string, maxHops int) ([]v2.RouteHop, error) {
	targetIP := net.ParseIP(target)
	if targetIP == nil {
		return nil, fmt.Errorf("invalid IPv6 target %q", target)
	}

	conn, err := icmp.ListenPacket("ip6:ipv6-icmp", "::")
	if err != nil {
		return nil, fmt.Errorf("open built-in IPv6 ICMP route probe (root/CAP_NET_RAW may be required): %w", err)
	}
	defer conn.Close()
	packet := conn.IPv6PacketConn()
	if packet == nil {
		return nil, fmt.Errorf("open built-in IPv6 route probe: packet connection is unavailable")
	}

	hops := make([]v2.RouteHop, 0, maxHops)
	id := os.Getpid() & 0xffff
	for ttl := 1; ttl <= maxHops; ttl++ {
		select {
		case <-ctx.Done():
			return hops, nil
		default:
		}

		if err := packet.SetHopLimit(ttl); err != nil {
			return hops, fmt.Errorf("set IPv6 hop limit: %w", err)
		}
		message := icmp.Message{Type: ipv6.ICMPTypeEchoRequest, Code: 0, Body: &icmp.Echo{ID: id, Seq: ttl, Data: []byte("KOMARI-ROUTE")}}
		payload, err := message.Marshal(nil)
		if err != nil {
			return hops, err
		}
		start := time.Now()
		_ = conn.SetDeadline(start.Add(routeHopTimeout))
		if _, err := conn.WriteTo(payload, &net.IPAddr{IP: targetIP}); err != nil {
			return hops, fmt.Errorf("send IPv6 route probe: %w", err)
		}
		buffer := make([]byte, 1500)
		n, peer, err := conn.ReadFrom(buffer)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				hops = append(hops, v2.RouteHop{TTL: ttl, Timeout: true})
				continue
			}
			return hops, fmt.Errorf("read IPv6 route probe: %w", err)
		}
		reply, err := icmp.ParseMessage(58, buffer[:n])
		if err != nil {
			hops = append(hops, v2.RouteHop{TTL: ttl, Timeout: true})
			continue
		}
		ip := routePeerIP(peer)
		hops = append(hops, v2.RouteHop{TTL: ttl, IP: ip, LatencyMS: float64(time.Since(start).Microseconds()) / 1000})
		if reply.Type == ipv6.ICMPTypeEchoReply || net.ParseIP(ip).Equal(targetIP) {
			break
		}
	}
	return hops, nil
}
