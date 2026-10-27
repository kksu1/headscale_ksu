package state

import (
	"net/netip"
	"testing"
	"time"

	"github.com/juanfont/headscale/hscontrol/db"
	"github.com/juanfont/headscale/hscontrol/types"
	"github.com/juanfont/headscale/hscontrol/types/change"
	"github.com/stretchr/testify/require"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
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

func TestPreAuthKeyFixedIPs(t *testing.T) {
	fixed4 := netip.MustParseAddr("100.64.0.56")
	fixed6 := netip.MustParseAddr("fd7a:115c:a1e0::38")

	register := func(t *testing.T, s *State, authKey string, machineKey key.MachinePrivate) types.NodeView {
		t.Helper()

		nv, _, err := s.HandleNodeFromPreAuthKey(tailcfg.RegisterRequest{
			Auth:     &tailcfg.RegisterResponseAuth{AuthKey: authKey},
			NodeKey:  key.NewNode().Public(),
			Hostinfo: &tailcfg.Hostinfo{Hostname: "fixed-ip-node"},
			Expiry:   time.Now().Add(24 * time.Hour),
		}, machineKey.Public())
		require.NoError(t, err)

		return nv
	}

	t.Run("new node gets the key's addresses", func(t *testing.T) {
		s, _, node1, _ := setupSetIPState(t)

		pak, err := s.CreatePreAuthKeyWithIPs(new(types.UserID(*node1.UserID)), false, false, nil, nil, fixed4, netip.Addr{})
		require.NoError(t, err)
		require.Equal(t, fixed4.String(), pak.Proto().GetIpv4())

		nv := register(t, s, pak.Key, key.NewMachine())
		require.Equal(t, fixed4, nv.IPv4().Get())
		require.True(t, nv.IPv6().Valid(), "IPv6 not fixed, must be allocated")

		dbNode, err := s.DB().GetNodeByID(nv.ID())
		require.NoError(t, err)
		require.Equal(t, fixed4, *dbNode.IPv4)
		require.ErrorIs(t, s.ipAlloc.Claim(fixed4), db.ErrIPInUse)
	})

	t.Run("reserved address is not handed to anyone else", func(t *testing.T) {
		s, cfg, node1, node2 := setupSetIPState(t)

		_, err := s.CreatePreAuthKeyWithIPs(new(types.UserID(*node1.UserID)), false, false, nil, nil, fixed4, fixed6)
		require.NoError(t, err)

		_, _, err = s.SetNodeIPs(node2.ID, fixed4, netip.Addr{})
		require.ErrorIs(t, err, db.ErrIPReservedByKey)

		_, err = s.CreatePreAuthKeyWithIPs(new(types.UserID(*node1.UserID)), false, false, nil, nil, netip.Addr{}, fixed6)
		require.ErrorIs(t, err, db.ErrIPReservedByKey)

		// The reservation is rebuilt from the database after a restart.
		require.NoError(t, s.Close())

		s2, err := NewState(cfg)
		require.NoError(t, err)
		t.Cleanup(func() { _ = s2.Close() })

		_, _, err = s2.SetNodeIPs(node2.ID, fixed4, netip.Addr{})
		require.ErrorIs(t, err, db.ErrIPReservedByKey)
	})

	t.Run("expiring the key releases the address", func(t *testing.T) {
		s, _, node1, node2 := setupSetIPState(t)

		pak, err := s.CreatePreAuthKeyWithIPs(new(types.UserID(*node1.UserID)), false, false, nil, nil, fixed4, netip.Addr{})
		require.NoError(t, err)
		require.NoError(t, s.ExpirePreAuthKey(pak.ID))

		_, _, err = s.SetNodeIPs(node2.ID, fixed4, netip.Addr{})
		require.NoError(t, err)
	})

	t.Run("rejected keys are not created", func(t *testing.T) {
		s, _, node1, node2 := setupSetIPState(t)
		uid := new(types.UserID(*node1.UserID))

		before, err := s.ListPreAuthKeys()
		require.NoError(t, err)

		_, err = s.CreatePreAuthKeyWithIPs(uid, true, false, nil, nil, fixed4, netip.Addr{})
		require.ErrorIs(t, err, ErrFixedIPReusableKey)

		_, err = s.CreatePreAuthKeyWithIPs(uid, false, false, nil, nil, *node2.IPv4, netip.Addr{})
		require.ErrorIs(t, err, db.ErrIPInUse)

		_, err = s.CreatePreAuthKeyWithIPs(uid, false, false, nil, nil, fixed6, netip.Addr{})
		require.ErrorIs(t, err, ErrSetNodeIPsInvalid)

		after, err := s.ListPreAuthKeys()
		require.NoError(t, err)
		require.Len(t, after, len(before), "no key may be left behind")
	})

	t.Run("existing node re-registering with a new key moves to its addresses", func(t *testing.T) {
		s, _, node1, _ := setupSetIPState(t)
		uid := new(types.UserID(*node1.UserID))
		machineKey := key.NewMachine()

		first, err := s.CreatePreAuthKey(uid, false, false, nil, nil)
		require.NoError(t, err)

		nv := register(t, s, first.Key, machineKey)
		require.NotEqual(t, fixed4, nv.IPv4().Get())

		second, err := s.CreatePreAuthKeyWithIPs(uid, false, false, nil, nil, fixed4, netip.Addr{})
		require.NoError(t, err)

		moved := register(t, s, second.Key, machineKey)
		require.Equal(t, nv.ID(), moved.ID(), "must update the existing node")
		require.Equal(t, fixed4, moved.IPv4().Get())
	})
}
