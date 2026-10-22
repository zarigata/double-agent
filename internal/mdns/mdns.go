// Package mdns implements the local discovery advertisement of the Double
// Agent control plane.
//
// A node announces itself on the LAN so that a phone on the same network can
// open the Web UI without typing an address. The advertisement carries no
// secret: the node still requires a login.
package mdns

import (
	"context"
	"fmt"
	"net"
	"time"

	"golang.org/x/net/ipv4"
)

// ServiceType is the mDNS service type the node advertises.
const ServiceType = "_double-agent._tcp"

// Advertiser announces a node on the local network.
type Advertiser struct {
	// Name is the instance name, e.g. "double-agent".
	Name string
	// Port is the TCP port of the Web UI.
	Port int
	// TXT holds the additional key/value pairs of the announcement.
	TXT []string

	conn *ipv4.PacketConn
}

// New creates an Advertiser.
func New(name string, port int) *Advertiser {
	return &Advertiser{Name: name, Port: port}
}

// Start begins advertising. It returns when the advertisement is live; the
// advertisement continues until Stop is called.
func (a *Advertiser) Start(ctx context.Context) error {
	if a.Name == "" {
		return fmt.Errorf("mdns: the instance name is required")
	}
	if a.Port <= 0 {
		return fmt.Errorf("mdns: the port is required")
	}
	conn, err := net.ListenPacket("udp4", ":5353")
	if err != nil {
		return fmt.Errorf("mdns: listen for mdns: %w", err)
	}
	pconn := ipv4.NewPacketConn(conn)
	// The multicast TTL keeps the announcement on the local segment.
	if err := pconn.SetMulticastTTL(1); err != nil {
		conn.Close()
		return fmt.Errorf("mdns: set multicast ttl: %w", err)
	}
	if err := pconn.SetMulticastLoopback(true); err != nil {
		conn.Close()
		return fmt.Errorf("mdns: set loopback: %w", err)
	}
	a.conn = pconn

	go a.loop(ctx)
	return nil
}

// Stop ends the advertisement.
func (a *Advertiser) Stop() error {
	if a.conn == nil {
		return nil
	}
	err := a.conn.Close()
	a.conn = nil
	if err != nil {
		return fmt.Errorf("mdns: close: %w", err)
	}
	return nil
}

// loop sends the announcement at the standard mDNS interval.
func (a *Advertiser) loop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := a.announce(ctx); err != nil {
				// A transient network error must not stop the daemon; the
				// next tick tries again.
				continue
			}
		}
	}
}

// announce sends one announcement packet.
//
// The node builds the DNS-SD resource records by hand: the announcement
// format is small and stable, and pulling in a full mDNS library for it
// would add a dependency that the rest of the project does not need.
func (a *Advertiser) announce(ctx context.Context) error {
	if a.conn == nil {
		return fmt.Errorf("mdns: the advertiser is not running")
	}
	host := fmt.Sprintf("%s.local.", a.Name)
	txt := append([]string{}, a.TXT...)

	packet := buildAnnouncement(a.Name, host, a.Port, txt)
	dst := &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353}
	// The interface index lets the kernel pick the outgoing NIC instead of
	// routing the multicast packet through the default gateway.
	cm := &ipv4.ControlMessage{}
	if iface, err := a.multicastInterface(); err == nil && iface != nil {
		cm.IfIndex = iface.Index
	}
	if _, err := a.conn.WriteTo(packet, cm, dst); err != nil {
		return fmt.Errorf("mdns: announce: %w", err)
	}
	return nil
}

// multicastInterface returns the interface that carries the local multicast
// traffic, if one can be determined.
func (a *Advertiser) multicastInterface() (*net.Interface, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("mdns: list interfaces: %w", err)
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagMulticast == 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			if ipNet.IP.IsLoopback() {
				continue
			}
			return &iface, nil
		}
	}
	return nil, fmt.Errorf("mdns: no multicast interface found")
}

// buildAnnouncement assembles the DNS-SD response packet.
func buildAnnouncement(instance, host string, port int, txt []string) []byte {
	var b []byte
	// Header: id, flags (response + authoritative), questions, answers.
	b = append(b, 0x00, 0x00) // transaction id
	b = append(b, 0x84, 0x00) // response, authoritative
	b = append(b, 0x00, 0x00) // questions
	b = append(b, 0x00, 0x00) // answers
	b = append(b, 0x00, 0x00) // authority
	b = append(b, 0x00, 0x00) // additional

	// PTR record: service type -> instance.
	b = append(b, encodeName(ServiceType)...)
	b = append(b, 0x00, 0x0c)             // type PTR
	b = append(b, 0x00, 0x01)             // class IN
	b = append(b, 0x00, 0x00, 0x00, 0x78) // ttl 120
	ptr := encodeName(instance + "." + ServiceType)
	b = append(b, byte(len(ptr)>>8), byte(len(ptr)))
	b = append(b, ptr...)

	// SRV record: instance -> host and port.
	b = append(b, encodeName(instance+"."+ServiceType)...)
	b = append(b, 0x00, 0x21)             // type SRV
	b = append(b, 0x00, 0x01)             // class IN
	b = append(b, 0x00, 0x00, 0x00, 0x78) // ttl 120
	rdlen := 6 + len(encodeName(host))
	b = append(b, byte(rdlen>>8), byte(rdlen))
	b = append(b, 0x00, 0x00) // priority
	b = append(b, 0x00, 0x00) // weight
	b = append(b, byte(port>>8), byte(port))
	b = append(b, encodeName(host)...)

	// TXT record with the advertised properties.
	b = append(b, encodeName(instance+"."+ServiceType)...)
	b = append(b, 0x00, 0x10)             // type TXT
	b = append(b, 0x00, 0x01)             // class IN
	b = append(b, 0x00, 0x00, 0x00, 0x78) // ttl 120
	var txtData []byte
	for _, item := range txt {
		txtData = append(txtData, byte(len(item)))
		txtData = append(txtData, item...)
	}
	if len(txtData) == 0 {
		txtData = []byte{0}
	}
	b = append(b, byte(len(txtData)>>8), byte(len(txtData)))
	b = append(b, txtData...)

	// A record for the host.
	b = append(b, encodeName(host)...)
	b = append(b, 0x00, 0x01)             // type A
	b = append(b, 0x00, 0x01)             // class IN
	b = append(b, 0x00, 0x00, 0x78, 0x00) // ttl 120
	b = append(b, 0x00, 0x04)
	b = append(b, 127, 0, 0, 1) // the loopback address is replaced by the receiver

	return b
}

// encodeName renders a DNS name in the wire format.
func encodeName(name string) []byte {
	var out []byte
	for _, label := range splitLabels(name) {
		out = append(out, byte(len(label)))
		out = append(out, label...)
	}
	out = append(out, 0x00)
	return out
}

// splitLabels splits a dotted name into its labels.
func splitLabels(name string) []string {
	var out []string
	current := ""
	for _, r := range name {
		if r == '.' {
			if current != "" {
				out = append(out, current)
			}
			current = ""
			continue
		}
		current += string(r)
	}
	if current != "" {
		out = append(out, current)
	}
	return out
}
