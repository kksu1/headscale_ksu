# headscale v0.29.5 + `nodes set-ip`

A personal fork of headscale v0.29.5 that adds one feature: assigning a fixed
IPv4 and/or IPv6 address to an existing node, live, without restarting
headscale. Everything else is upstream v0.29.5. No database schema change.

## Usage

```sh
headscale nodes set-ip 8 100.64.0.56                       # IPv4 only, IPv6 kept
headscale nodes set-ip 8 100.64.0.56 fd7a:115c:a1e0::38    # both
headscale nodes set-ip -i 8 fd7a:115c:a1e0::38             # IPv6 only, upstream-style -i
```

REST (same API key as the rest of the API):

```sh
curl -X POST -H "Authorization: Bearer $KEY" \
  -d '{"ipv4":"100.64.0.56"}' https://headscale.example/api/v1/node/8/ip
```

gRPC: `HeadscaleService.SetNodeIPs(SetNodeIPsRequest{node_id, ipv4, ipv6})`.
An empty field keeps the node's current address for that family.

## What it does

1. Parses each address and checks its family.
2. Claims the new address(es) in the IP allocator, in one atomic step. The
   claim fails, and nothing changes, if an address is
   - outside `prefixes.v4` / `prefixes.v6` from the config,
   - the prefix network or broadcast address, `100.100.100.100`,
     `fd7a:115c:a1e0::53` or inside the ChromeOS VM range `100.115.92.0/23`,
   - already used by another node, or already handed out by the allocator.
3. Updates the node in memory (NodeStore), then writes `nodes.ipv4` /
   `nodes.ipv6` to the database. If the database write fails the in-memory
   change and the claim are rolled back.
4. Releases the node's old address(es) so the allocator can reuse them.
5. Refreshes the policy (filters reference node IPs) and sends a full update
   to every connected node, so peers get the new addresses/AllowedIPs and the
   node itself gets its new self addresses.

Setting a node's current address again is a no-op. An offline or expired
node can be changed; it picks up the address when it next connects. A re-auth
as the same user keeps the node and therefore the address; registering the
machine under a different user creates a new node with a new address.

Things to know:

- ACL `hosts` entries or rules written as raw IPs follow the IP, not the node.
- Existing connections to the old address on peers break; clients reconnect.
- `headscale version` reports `dev`. Headscale then skips its database version
  check and keeps the stored version, so rolling back is clean.

## Changes vs v0.29.5

| File | Change |
| --- | --- |
| `proto/headscale/v1/{headscale,node}.proto`, `gen/` | `SetNodeIPs` RPC, `POST /api/v1/node/{node_id}/ip` |
| `hscontrol/db/ip.go` | `IPAllocator.Claim` (validate + reserve) |
| `hscontrol/state/state.go` | `State.SetNodeIPs` |
| `hscontrol/grpcv1.go` | gRPC handler, maps errors to gRPC codes |
| `cmd/headscale/cli/nodes.go` | `headscale nodes set-ip` |
| `*_test.go` | allocator, state (memory/DB/restart/rollback), live propagation via `servertest` |
| `Dockerfile.setip` | image build |

## Build

```sh
docker build -f Dockerfile.setip -t headscale-local:v0.29.5-setip .
```

The image mirrors the official one (distroless, `/ko-app/headscale`
entrypoint), so it is a drop-in swap of the `image:` line in compose.

## Deploy and roll back

```sh
cd ~/headscale-admin
docker compose stop headscale
cp <data-dir>/db.sqlite <data-dir>/db.sqlite.bak-$(date +%F)   # backup
# compose: image: headscale-local:v0.29.5-setip
docker compose up -d headscale
docker compose exec headscale headscale nodes set-ip 8 100.64.0.56
docker compose exec headscale headscale nodes list
```

Rollback: set `image: headscale/headscale:v0.29.5` again and
`docker compose up -d headscale`. Assigned addresses stay in the database and
keep working with the official image.
