package server

import (
	"context"
	"encoding/binary"
	"fmt"
	"log"
	"net"
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
	if p.IPVersion == 6 {
		hops = traceRouteICMPv6(ctx, targetIP, p.MaxHops)
	} else {
		hops = traceRouteICMPv4(ctx, targetIP, p.MaxHops)
	}

	sendRouteResult(conn, p, hops, "")
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

func traceRouteICMPv4(ctx context.Context, target string, maxHops int) []v2.RouteHop {
	targetIP := net.ParseIP(target)
	if targetIP == nil {
		return nil
	}

	recvSock, err := net.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		log.Printf("route: failed to listen ICMP: %v", err)
		return nil
	}
	defer recvSock.Close()
	_ = recvSock.SetDeadline(time.Now().Add(time.Duration(maxHops) * (routeHopTimeout + 200*time.Millisecond)))

	icmpID := uint16(time.Now().UnixNano() & 0xFFFF)
	hops := make([]v2.RouteHop, 0, maxHops)

	for ttl := 1; ttl <= maxHops; ttl++ {
		select {
		case <-ctx.Done():
			return hops
		default:
		}

		seq := uint16(ttl)
		sentAt := time.Now()
		if err := sendICMPv4Probe(targetIP, icmpID, seq, ttl); err != nil {
			hops = append(hops, v2.RouteHop{TTL: ttl, Timeout: true})
			continue
		}

		ip, rtt, matched := recvICMPv4Response(recvSock, icmpID, routeHopTimeout, sentAt)
		if matched {
			hops = append(hops, v2.RouteHop{TTL: ttl, IP: ip, LatencyMS: float64(rtt.Milliseconds())})
			if ip == target {
				return hops
			}
		} else {
			hops = append(hops, v2.RouteHop{TTL: ttl, Timeout: true})
		}
	}
	return hops
}

func sendICMPv4Probe(target net.IP, icmpID, seq uint16, ttl int) error {
	sock, err := net.Dial("ip4:icmp", target.String())
	if err != nil {
		return err
	}
	defer sock.Close()

	c := ipv4.NewConn(sock)
	if err := c.SetTTL(ttl); err != nil {
		return err
	}

	msg := icmp.Message{
		Type: ipv4.ICMPTypeEcho, Code: 0,
		Body: &icmp.Echo{ID: int(icmpID), Seq: int(seq), Data: []byte("KOMARI-ROUTE")},
	}
	data, err := msg.Marshal(nil)
	if err != nil {
		return err
	}
	_, err = sock.Write(data)
	return err
}

func recvICMPv4Response(sock net.PacketConn, icmpID uint16, timeout time.Duration, sentAt time.Time) (string, time.Duration, bool) {
	_ = sock.SetDeadline(time.Now().Add(timeout))
	buf := make([]byte, 1500)
	for {
		n, _, err := sock.ReadFrom(buf)
		if err != nil {
			return "", 0, false
		}
		rtt := time.Since(sentAt)
		msg, err := icmp.ParseMessage(1, buf[:n])
		if err != nil {
			continue
		}
		switch msg.Type {
		case ipv4.ICMPTypeTimeExceeded:
			if ip, ok := matchEmbeddedICMPID(msg.Body, icmpID); ok {
				return ip, rtt, true
			}
		case ipv4.ICMPTypeDestinationUnreachable:
			if ip, ok := matchEmbeddedICMPID(msg.Body, icmpID); ok {
				return ip, rtt, true
			}
		case ipv4.ICMPTypeEchoReply:
			if echo, ok := msg.Body.(*icmp.Echo); ok && echo.ID == int(icmpID) {
				return "", rtt, true
			}
		}
	}
}

func matchEmbeddedICMPID(body icmp.MessageBody, expectedID uint16) (string, bool) {
	rawBody, ok := body.(*icmp.RawBody)
	if !ok {
		return "", false
	}
	data := rawBody.Data
	// IPv4 header (>=20 bytes) + first 8 bytes of ICMP Echo (type, code, checksum, id, seq)
	if len(data) < 28 {
		return "", false
	}
	ihl := int(data[0]&0x0F) * 4
	if ihl < 20 || len(data) < ihl+8 {
		return "", false
	}
	embeddedID := binary.BigEndian.Uint16(data[ihl+4 : ihl+6])
	if embeddedID != expectedID {
		return "", false
	}
	srcIP := net.IP(make([]byte, 4))
	copy(srcIP, data[12:16])
	return srcIP.String(), true
}

// ---------- IPv6 traceroute ----------

func traceRouteICMPv6(ctx context.Context, target string, maxHops int) []v2.RouteHop {
	targetIP := net.ParseIP(target)
	if targetIP == nil {
		return nil
	}

	recvSock, err := net.ListenPacket("ip6:ipv6-icmp", "::")
	if err != nil {
		log.Printf("route: failed to listen ICMPv6: %v", err)
		return nil
	}
	defer recvSock.Close()
	_ = recvSock.SetDeadline(time.Now().Add(time.Duration(maxHops) * (routeHopTimeout + 200*time.Millisecond)))

	icmpID := uint16(time.Now().UnixNano() & 0xFFFF)
	hops := make([]v2.RouteHop, 0, maxHops)

	for ttl := 1; ttl <= maxHops; ttl++ {
		select {
		case <-ctx.Done():
			return hops
		default:
		}

		seq := uint16(ttl)
		sentAt := time.Now()
		if err := sendICMPv6Probe(targetIP, icmpID, seq, ttl); err != nil {
			hops = append(hops, v2.RouteHop{TTL: ttl, Timeout: true})
			continue
		}

		ip, rtt, matched := recvICMPv6Response(recvSock, icmpID, routeHopTimeout, sentAt)
		if matched {
			hops = append(hops, v2.RouteHop{TTL: ttl, IP: ip, LatencyMS: float64(rtt.Milliseconds())})
			if ip == target {
				return hops
			}
		} else {
			hops = append(hops, v2.RouteHop{TTL: ttl, Timeout: true})
		}
	}
	return hops
}

func sendICMPv6Probe(target net.IP, icmpID, seq uint16, ttl int) error {
	sock, err := net.Dial("ip6:ipv6-icmp", target.String())
	if err != nil {
		return err
	}
	defer sock.Close()

	c := ipv6.NewConn(sock)
	if err := c.SetHopLimit(ttl); err != nil {
		return err
	}

	msg := icmp.Message{
		Type: ipv6.ICMPTypeEchoRequest, Code: 0,
		Body: &icmp.Echo{ID: int(icmpID), Seq: int(seq), Data: []byte("KOMARI-ROUTE")},
	}
	data, err := msg.Marshal(nil)
	if err != nil {
		return err
	}
	_, err = sock.Write(data)
	return err
}

func recvICMPv6Response(sock net.PacketConn, icmpID uint16, timeout time.Duration, sentAt time.Time) (string, time.Duration, bool) {
	_ = sock.SetDeadline(time.Now().Add(timeout))
	buf := make([]byte, 1500)
	for {
		n, from, err := sock.ReadFrom(buf)
		if err != nil {
			return "", 0, false
		}
		rtt := time.Since(sentAt)
		msg, err := icmp.ParseMessage(58, buf[:n])
		if err != nil {
			continue
		}
		switch msg.Type {
		case ipv6.ICMPTypeTimeExceeded:
			if matchEmbeddedICMPv6ID(msg.Body, icmpID) {
				return from.String(), rtt, true
			}
		case ipv6.ICMPTypeDestinationUnreachable:
			if matchEmbeddedICMPv6ID(msg.Body, icmpID) {
				return from.String(), rtt, true
			}
		case ipv6.ICMPTypeEchoReply:
			if echo, ok := msg.Body.(*icmp.Echo); ok && echo.ID == int(icmpID) {
				return from.String(), rtt, true
			}
		}
	}
}

func matchEmbeddedICMPv6ID(body icmp.MessageBody, expectedID uint16) bool {
	rawBody, ok := body.(*icmp.RawBody)
	if !ok {
		return false
	}
	data := rawBody.Data
	// IPv6 header (40 bytes) + first 8 bytes of ICMPv6 Echo (type, code, checksum, id, seq)
	if len(data) < 48 {
		return false
	}
	embeddedID := binary.BigEndian.Uint16(data[44:46])
	return embeddedID == expectedID
}
