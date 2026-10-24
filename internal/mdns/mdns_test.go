package mdns

import (
	"bytes"
	"context"
	"testing"
	"time"
)

// contains reports whether the packet holds the needle.
func contains(packet, needle []byte) bool {
	return bytes.Contains(packet, needle)
}

// TestTheServiceNameIsBuilt checks the wire format of a DNS name.
func TestTheServiceNameIsBuilt(t *testing.T) {
	wire := encodeName(ServiceType)
	want := []byte{
		13, '_', 'd', 'o', 'u', 'b', 'l', 'e', '-', 'a', 'g', 'e', 'n', 't',
		4, '_', 't', 'c', 'p',
		0,
	}
	if !bytes.Equal(wire, want) {
		t.Errorf("the name is encoded as %v, want %v", wire, want)
	}
}

// TestSplitLabels checks the splitting of a dotted name.
func TestSplitLabels(t *testing.T) {
	cases := []struct {
		name string
		want []string
	}{
		{name: "_double-agent._tcp", want: []string{"_double-agent", "_tcp"}},
		{name: "double-agent.local.", want: []string{"double-agent", "local"}},
		{name: "single", want: []string{"single"}},
		{name: "", want: nil},
		{name: "..", want: nil},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got := splitLabels(test.name)
			if len(got) != len(test.want) {
				t.Fatalf("the labels are %v, want %v", got, test.want)
			}
			for i, label := range got {
				if label != test.want[i] {
					t.Errorf("the label %d is %q, want %q", i, label, test.want[i])
				}
			}
		})
	}
}

// TestTheAnnouncementCarriesTheService checks the records of the
// announcement.
func TestTheAnnouncementCarriesTheService(t *testing.T) {
	const name = "double-agent"
	host := name + ".local."
	packet := buildAnnouncement(name, host, 7443, nil)

	if !contains(packet, encodeName(ServiceType)) {
		t.Error("the announcement does not carry the service type")
	}
	if !contains(packet, encodeName(name+"."+ServiceType)) {
		t.Error("the announcement does not carry the instance name")
	}
	if !contains(packet, encodeName(host)) {
		t.Error("the announcement does not carry the host name")
	}
	// The port is announced in the service record.
	port := []byte{0x1d, 0x13}
	if !contains(packet, port) {
		t.Errorf("the announcement does not carry the port %d", 7443)
	}
	// The record types of the announcement.
	for _, record := range [][]byte{{0x00, 0x0c}, {0x00, 0x21}, {0x00, 0x10}, {0x00, 0x01}} {
		if !contains(packet, record) {
			t.Errorf("the announcement does not carry the record type %v", record)
		}
	}
}

// TestTheAnnouncementCarriesTheTextRecords checks the additional properties
// of the announcement.
func TestTheAnnouncementCarriesTheTextRecords(t *testing.T) {
	txt := []string{"ver=0.1.0-dev", "api=1", "tls=1"}
	packet := buildAnnouncement("double-agent", "double-agent.local.", 7443, txt)
	for _, item := range txt {
		record := append([]byte{byte(len(item))}, []byte(item)...)
		if !contains(packet, record) {
			t.Errorf("the announcement does not carry the text record %q", item)
		}
	}
}

// TestAnAnnouncementWithoutTextRecordsIsValid checks that an announcement
// without properties is still well formed.
func TestAnAnnouncementWithoutTextRecordsIsValid(t *testing.T) {
	packet := buildAnnouncement("double-agent", "double-agent.local.", 7443, nil)
	if len(packet) == 0 {
		t.Fatal("the announcement is empty")
	}
	// The text record is empty but present.
	if !contains(packet, []byte{0x00, 0x10}) {
		t.Error("the announcement does not carry a text record")
	}
}

// TestTheAdvertiserRefusesAnIncompleteNode checks the arguments of the
// advertiser.
func TestTheAdvertiserRefusesAnIncompleteNode(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := New("", 7443).Start(ctx); err == nil {
		t.Error("an advertiser without a name was started")
	}
	if err := New("double-agent", 0).Start(ctx); err == nil {
		t.Error("an advertiser without a port was started")
	}
	if err := New("double-agent", -1).Start(ctx); err == nil {
		t.Error("an advertiser with a negative port was started")
	}
}

// TestTheAdvertisementDoesNotNeedAMulticastNetwork checks that the
// advertiser can be built and stopped without a multicast network. A
// sandbox often refuses to join a multicast group, so the start may fail:
// the test only insists that the failure is reported and not hidden.
func TestTheAdvertisementDoesNotNeedAMulticastNetwork(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	advertiser := New("double-agent", 7443)
	advertiser.TXT = []string{"ver=0.1.0-dev"}
	if err := advertiser.Start(ctx); err != nil {
		t.Logf("the advertisement could not be started: %v", err)
	} else {
		if err := advertiser.Stop(); err != nil {
			t.Errorf("stop the advertisement: %v", err)
		}
	}
	// Stopping an advertiser that never started is not an error.
	if err := advertiser.Stop(); err != nil {
		t.Errorf("stop the advertisement: %v", err)
	}
}

// TestTheAdvertiserKeepsItsProperties checks the state of an advertiser that
// was never started.
func TestTheAdvertiserKeepsItsProperties(t *testing.T) {
	advertiser := New("studio-node", 7443)
	if advertiser.Name != "studio-node" {
		t.Errorf("the advertiser is named %q", advertiser.Name)
	}
	if advertiser.Port != 7443 {
		t.Errorf("the advertiser announces the port %d", advertiser.Port)
	}
	// The announcement of an advertiser that is not running is refused.
	if err := advertiser.announce(context.Background()); err == nil {
		t.Error("an advertiser that is not running announced itself")
	}
	// The loop returns as soon as the context is done.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		advertiser.loop(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("the loop did not return")
	}
}
