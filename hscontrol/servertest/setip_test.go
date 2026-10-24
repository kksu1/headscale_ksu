package servertest_test

import (
	"net/netip"
	"slices"
	"testing"
	"time"

	"github.com/juanfont/headscale/hscontrol/servertest"
	"github.com/stretchr/testify/require"
	"tailscale.com/types/netmap"
)

// TestSetNodeIPsPropagates proves an admin address change reaches the node
// itself and its peers live, and that the allocator never hands the address
// to a node that registers afterwards.
func TestSetNodeIPsPropagates(t *testing.T) {
	t.Parallel()

	for _, delta := range []bool{false, true} {
		name := "full_netmap"
		if delta {
			name = "delta_updates"
		}

		t.Run(name, func(t *testing.T) {
			t.Parallel()

			srv := servertest.NewServer(t)
			user := srv.CreateUser(t, "setip-user")

			var opts []servertest.ClientOption
			if delta {
				opts = append(opts, servertest.WithDeltaUpdates())
			}

			c1 := servertest.NewClient(t, srv, "setip-node1", append(opts, servertest.WithUser(user))...)
			c2 := servertest.NewClient(t, srv, "setip-node2", append(opts, servertest.WithUser(user))...)

			c1.WaitForPeers(t, 1, 10*time.Second)
			c2.WaitForPeers(t, 1, 10*time.Second)

			// With the sequential allocator the next registration would get
			// .3; take it first so the test also proves allocator awareness.
			newV4 := netip.MustParseAddr("100.64.0.3")
			newV6 := netip.MustParseAddr("fd7a:115c:a1e0::3")

			nodeID := findNodeID(t, srv, "setip-node1")
			_, c, err := srv.State().SetNodeIPs(nodeID, newV4, newV6)
			require.NoError(t, err)
			srv.App.Change(c)

			want := []netip.Prefix{netip.PrefixFrom(newV4, 32), netip.PrefixFrom(newV6, 128)}

			c1.WaitForCondition(t, "self has new addresses", 10*time.Second,
				func(nm *netmap.NetworkMap) bool {
					return nm.SelfNode.Valid() && slices.Equal(nm.SelfNode.Addresses().AsSlice(), want)
				})

			c2.WaitForCondition(t, "peer has new addresses", 10*time.Second,
				func(nm *netmap.NetworkMap) bool {
					for _, p := range nm.Peers {
						if p.Hostinfo().Hostname() == "setip-node1" {
							return slices.Equal(p.Addresses().AsSlice(), want)
						}
					}

					return false
				})
			servertest.AssertPeerHasAllowedIPs(t, c2, "setip-node1", want)

			c3 := servertest.NewClient(t, srv, "setip-node3", append(opts, servertest.WithUser(user))...)
			c3.WaitForPeers(t, 2, 10*time.Second)

			for _, addr := range c3.Netmap().SelfNode.Addresses().All() {
				require.NotEqual(t, newV4, addr.Addr(), "allocator handed out a claimed address")
				require.NotEqual(t, newV6, addr.Addr(), "allocator handed out a claimed address")
			}
		})
	}
}
