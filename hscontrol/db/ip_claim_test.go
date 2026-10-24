package db

import (
	"net/netip"
	"testing"

	"github.com/juanfont/headscale/hscontrol/types"
	"github.com/stretchr/testify/require"
)

func TestIPAllocatorClaim(t *testing.T) {
	prefix4 := netip.MustParsePrefix("100.64.0.0/29")
	prefix6 := netip.MustParsePrefix("fd7a:115c:a1e0::/120")

	newAlloc := func(t *testing.T) *IPAllocator {
		t.Helper()

		alloc, err := NewIPAllocator(nil, &prefix4, &prefix6, types.IPAllocationStrategySequential)
		require.NoError(t, err)

		return alloc
	}

	t.Run("claimed address is skipped by Next", func(t *testing.T) {
		alloc := newAlloc(t)

		require.NoError(t, alloc.Claim(netip.MustParseAddr("100.64.0.1"), netip.MustParseAddr("fd7a:115c:a1e0::1")))

		v4, v6, err := alloc.Next()
		require.NoError(t, err)
		require.Equal(t, netip.MustParseAddr("100.64.0.2"), *v4)
		require.Equal(t, netip.MustParseAddr("fd7a:115c:a1e0::2"), *v6)
	})

	t.Run("address handed out by Next cannot be claimed", func(t *testing.T) {
		alloc := newAlloc(t)

		v4, _, err := alloc.Next()
		require.NoError(t, err)
		require.ErrorIs(t, alloc.Claim(*v4), ErrIPInUse)
	})

	t.Run("freed address can be claimed again", func(t *testing.T) {
		alloc := newAlloc(t)
		addr := netip.MustParseAddr("100.64.0.4")

		require.NoError(t, alloc.Claim(addr))
		require.ErrorIs(t, alloc.Claim(addr), ErrIPInUse)
		alloc.FreeIPs([]netip.Addr{addr})
		require.NoError(t, alloc.Claim(addr))
	})

	t.Run("rejections", func(t *testing.T) {
		tests := []struct {
			name string
			addr string
			want error
		}{
			{"outside v4 prefix", "100.64.1.1", ErrIPNotInPrefix},
			{"outside v6 prefix", "fd7a:115c:a1e0::1:1", ErrIPNotInPrefix},
			{"network address", "100.64.0.0", ErrIPReserved},
			{"broadcast address", "100.64.0.7", ErrIPReserved},
			{"v6 network address", "fd7a:115c:a1e0::", ErrIPReserved},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				require.ErrorIs(t, newAlloc(t).Claim(netip.MustParseAddr(tt.addr)), tt.want)
			})
		}
	})

	t.Run("tailscale service address is reserved", func(t *testing.T) {
		wide := netip.MustParsePrefix("100.64.0.0/10")
		alloc, err := NewIPAllocator(nil, &wide, nil, types.IPAllocationStrategySequential)
		require.NoError(t, err)

		require.ErrorIs(t, alloc.Claim(netip.MustParseAddr("100.100.100.100")), ErrIPReserved)
		require.ErrorIs(t, alloc.Claim(netip.MustParseAddr("100.115.92.1")), ErrIPReserved)
		require.ErrorIs(t, alloc.Claim(netip.MustParseAddr("fd7a:115c:a1e0::1")), ErrIPFamilyMissing)
	})

	t.Run("claim is all or nothing", func(t *testing.T) {
		alloc := newAlloc(t)
		good := netip.MustParseAddr("100.64.0.3")

		require.ErrorIs(t, alloc.Claim(good, netip.MustParseAddr("fd7a:115c:a1e0::1:1")), ErrIPNotInPrefix)
		require.NoError(t, alloc.Claim(good), "failed claim must not leave the first address marked used")
	})

	t.Run("duplicate in one claim", func(t *testing.T) {
		addr := netip.MustParseAddr("100.64.0.3")
		require.ErrorIs(t, newAlloc(t).Claim(addr, addr), ErrIPInUse)
	})
}
