package state

import (
	"net/netip"
	"testing"

	"github.com/juanfont/headscale/hscontrol/db"
	"github.com/juanfont/headscale/hscontrol/types"
	"github.com/juanfont/headscale/hscontrol/types/change"
	"github.com/stretchr/testify/require"
)

func setupSetIPState(t *testing.T) (*State, *types.Config, *types.Node, *types.Node) {
	t.Helper()

	dbPath := t.TempDir() + "/headscale.db"
	cfg := persistTestConfig(dbPath)

	database, err := db.NewHeadscaleDatabase(cfg)
	require.NoError(t, err)

	user := database.CreateUserForTest("setip-user")
	node1 := database.CreateRegisteredNodeForTest(user, "setip-node1")
	node2 := database.CreateRegisteredNodeForTest(user, "setip-node2")
	require.NoError(t, database.Close())

	s, err := NewState(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	return s, cfg, node1, node2
}

func TestSetNodeIPs(t *testing.T) {
	newV4 := netip.MustParseAddr("100.64.0.56")
	newV6 := netip.MustParseAddr("fd7a:115c:a1e0::38")

	t.Run("updates memory, database and allocator", func(t *testing.T) {
		s, cfg, node1, _ := setupSetIPState(t)
		oldV4 := *node1.IPv4
		oldV6 := *node1.IPv6

		nv, c, err := s.SetNodeIPs(node1.ID, newV4, netip.Addr{})
		require.NoError(t, err)
		require.Equal(t, newV4, nv.IPv4().Get())
		require.Equal(t, oldV6, nv.IPv6().Get(), "IPv6 not given, must be kept")
		require.Equal(t, change.FullUpdate(), c)

		inMem, ok := s.GetNodeByID(node1.ID)
		require.True(t, ok)
		require.Equal(t, newV4, inMem.IPv4().Get())

		dbNode, err := s.DB().GetNodeByID(node1.ID)
		require.NoError(t, err)
		require.Equal(t, newV4, *dbNode.IPv4)

		require.ErrorIs(t, s.ipAlloc.Claim(newV4), db.ErrIPInUse, "new address must be marked used")
		require.NoError(t, s.ipAlloc.Claim(oldV4), "old address must be released")

		// The address survives a restart and is not handed out again.
		require.NoError(t, s.Close())

		s2, err := NewState(cfg)
		require.NoError(t, err)
		t.Cleanup(func() { _ = s2.Close() })

		restored, ok := s2.GetNodeByID(node1.ID)
		require.True(t, ok)
		require.Equal(t, newV4, restored.IPv4().Get())
		require.ErrorIs(t, s2.ipAlloc.Claim(newV4), db.ErrIPInUse)
	})

	t.Run("both families", func(t *testing.T) {
		s, _, node1, _ := setupSetIPState(t)

		nv, _, err := s.SetNodeIPs(node1.ID, newV4, newV6)
		require.NoError(t, err)
		require.Equal(t, newV4, nv.IPv4().Get())
		require.Equal(t, newV6, nv.IPv6().Get())
	})

	t.Run("address of another node is rejected", func(t *testing.T) {
		s, _, node1, node2 := setupSetIPState(t)

		_, _, err := s.SetNodeIPs(node1.ID, *node2.IPv4, netip.Addr{})
		require.ErrorIs(t, err, db.ErrIPInUse)

		inMem, _ := s.GetNodeByID(node1.ID)
		require.Equal(t, *node1.IPv4, inMem.IPv4().Get(), "rejected change must not touch the node")
	})

	t.Run("one bad family rejects the whole change", func(t *testing.T) {
		s, _, node1, node2 := setupSetIPState(t)

		_, _, err := s.SetNodeIPs(node1.ID, newV4, *node2.IPv6)
		require.ErrorIs(t, err, db.ErrIPInUse)
		require.NoError(t, s.ipAlloc.Claim(newV4), "claim of the valid address must be rolled back")
	})

	t.Run("setting the current address is a no-op", func(t *testing.T) {
		s, _, node1, _ := setupSetIPState(t)

		nv, c, err := s.SetNodeIPs(node1.ID, *node1.IPv4, netip.Addr{})
		require.NoError(t, err)
		require.True(t, c.IsEmpty())
		require.Equal(t, *node1.IPv4, nv.IPv4().Get())
	})

	t.Run("validation", func(t *testing.T) {
		s, _, node1, _ := setupSetIPState(t)

		tests := []struct {
			name   string
			v4, v6 string
			want   error
		}{
			{"nothing given", "", "", ErrSetNodeIPsInvalid},
			{"v6 in v4 slot", "fd7a:115c:a1e0::38", "", ErrSetNodeIPsInvalid},
			{"v4 in v6 slot", "", "100.64.0.56", ErrSetNodeIPsInvalid},
			{"4in6 in v6 slot", "", "::ffff:100.64.0.56", ErrSetNodeIPsInvalid},
			{"outside prefix", "192.168.1.10", "", db.ErrIPNotInPrefix},
			{"magicdns address", "100.100.100.100", "", db.ErrIPReserved},
			{"chromeos range", "100.115.92.10", "", db.ErrIPReserved},
			{"network address", "100.64.0.0", "", db.ErrIPReserved},
			{"outside v6 prefix", "", "fd00::1", db.ErrIPNotInPrefix},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				var v4, v6 netip.Addr
				if tt.v4 != "" {
					v4 = netip.MustParseAddr(tt.v4)
				}

				if tt.v6 != "" {
					v6 = netip.MustParseAddr(tt.v6)
				}

				_, _, err := s.SetNodeIPs(node1.ID, v4, v6)
				require.ErrorIs(t, err, tt.want)
			})
		}
	})

	t.Run("unknown node", func(t *testing.T) {
		s, _, _, _ := setupSetIPState(t)

		_, _, err := s.SetNodeIPs(999, newV4, netip.Addr{})
		require.ErrorIs(t, err, ErrNodeNotFound)
		require.NoError(t, s.ipAlloc.Claim(newV4), "nothing may be claimed for an unknown node")
	})
}
