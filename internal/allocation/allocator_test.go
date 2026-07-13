package allocation

import (
	"net/netip"
	"testing"
)

func TestNextIsStableAcrossUnrelatedMembershipChanges(t *testing.T) {
	used := map[netip.Addr]struct{}{netip.MustParseAddr("172.30.99.2"): {}}
	got, err := Next("172.30.99.0/29", used)
	if err != nil {
		t.Fatal(err)
	}
	if want := netip.MustParseAddr("172.30.99.3"); got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
	used[netip.MustParseAddr("172.30.99.5")] = struct{}{}
	got2, _ := Next("172.30.99.0/29", used)
	if got2 != got {
		t.Fatalf("unrelated allocation renumbered candidate: %s -> %s", got, got2)
	}
}

func TestNextReservesNetworkGatewayAndBroadcast(t *testing.T) {
	got, err := Next("10.0.0.0/30", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != "10.0.0.2" {
		t.Fatalf("got %s", got)
	}
	if _, err = Next("10.0.0.0/31", nil); err == nil {
		t.Fatal("expected unusable pool error")
	}
}
