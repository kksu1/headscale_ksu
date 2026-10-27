# headscale v0.29.6 + `nodes set-ip`

A personal fork of headscale v0.29.6 that adds fixed node addresses:

- `nodes set-ip` assigns a fixed IPv4 and/or IPv6 address to an existing node,
  live, without restarting headscale.
- A pre-auth key can carry a fixed address, which the node registering with it
  gets from the start (see [Fixed IP pre-auth keys](#fixed-ip-pre-auth-keys)).

Everything else is upstream v0.29.6. The pre-auth key feature adds two columns
to `pre_auth_keys`, so rolling back to an image without it needs one SQL step
first (see [Deploy and roll back](#deploy-and-roll-back)).

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

## Fixed IP pre-auth keys

```sh
headscale preauthkeys create -u 1 --ipv4 100.64.0.56
headscale preauthkeys create -u 1 --ipv4 100.64.0.56 --ipv6 fd7a:115c:a1e0::38
```

REST: `POST /api/v1/preauthkey` with `"ipv4"` and/or `"ipv6"` next to the usual
fields. headscale-admin's Deploy page has a Fixed IP option that does this.

- The addresses are validated like `set-ip` (inside the prefix, not reserved,
  not in use) when the key is created, and the key is refused if they fail.
- They are then reserved for the key: the allocator does not hand them to any
  other node, `set-ip` and other keys cannot take them. The reservation is kept
  in `pre_auth_keys.ipv4` / `ipv6` and rebuilt on restart.
- The node that registers with the key gets those addresses; a family the key
  leaves empty is allocated as usual. If the machine already has a node for
  that user, the node is moved to the addresses (like `set-ip`).
- The reservation ends when the key is used, expired (by time or
  `preauthkeys expire`) or deleted.
- Reusable keys cannot carry fixed addresses, one address cannot go to several
  nodes.

## What `set-ip` does

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
- `headscale version` reports `v0.29.6+setip`, stamped by `Dockerfile.setip`
  through `types.Version`. The database version check and the update check
  ignore the `+setip` build metadata and treat it as v0.29.6, and the stored
  database version becomes `v0.29.6+setip`. Rolling back to the official
  v0.29.5 or v0.29.6 image is allowed (same minor version).

## Changes vs v0.29.6

| File | Change |
| --- | --- |
| `proto/headscale/v1/{headscale,node}.proto`, `gen/` | `SetNodeIPs` RPC, `POST /api/v1/node/{node_id}/ip` |
| `hscontrol/db/ip.go` | `IPAllocator.Claim` (validate + reserve) |
| `hscontrol/state/state.go` | `State.SetNodeIPs` |
| `hscontrol/grpcv1.go` | gRPC handler, maps errors to gRPC codes |
| `cmd/headscale/cli/nodes.go` | `headscale nodes set-ip` |
| `*_test.go` | allocator, state (memory/DB/restart/rollback), live propagation via `servertest` |
| `hscontrol/types/version.go` | `types.Version`, set at build time with `-ldflags -X` |
| `proto/headscale/v1/preauthkey.proto`, `gen/` | `ipv4` / `ipv6` on `PreAuthKey` and `CreatePreAuthKeyRequest` |
| `hscontrol/db/db.go`, `schema.sql` | migration `202610271200-pre-auth-key-fixed-ips` (adds `pre_auth_keys.ipv4`, `ipv6`) |
| `hscontrol/db/ip.go` | `IPAllocator.Reserve` / `Unreserve` / `NextFor` |
| `hscontrol/state/state.go` | `State.CreatePreAuthKeyWithIPs`, fixed addresses at registration |
| `cmd/headscale/cli/preauthkeys.go` | `preauthkeys create --ipv4 --ipv6` |
| `Dockerfile.setip` | image build |

## Build

```sh
docker build -f Dockerfile.setip -t headscale-local:v0.29.6-setip .
```

Upstream 0.29.6 requires Go 1.27.3 or newer for custom builds
(CVE-2026-111943). `Dockerfile.setip` builds with `golang:1.27.3-trixie` and
uses the same digest-pinned `distroless/base-debian13` base as the official
0.29.6 image; `headscale version` shows `v0.29.6+setip` and
`built with: go1.27.3`. Docker builds have no `.git`, so Go cannot stamp the
version itself; for another release pass `--build-arg VERSION=vX.Y.Z+setip`
or change the `ARG` default.

The image mirrors the official one (distroless, `/ko-app/headscale`
entrypoint), so it is a drop-in swap of the `image:` line in compose.

## Deploy and roll back

```sh
cd ~/headscale-admin
docker compose stop headscale
cp <data-dir>/db.sqlite <data-dir>/db.sqlite.bak-$(date +%F)   # backup
# compose: image: headscale-local:v0.29.6-setip
docker compose up -d headscale
docker compose exec headscale headscale nodes set-ip 8 100.64.0.56
docker compose exec headscale headscale nodes list
```

Rollback: set the previous `image:` line again (`headscale-local:v0.29.5-setip`
or `headscale/headscale:v0.29.6`) and
`docker compose up -d headscale`. Assigned addresses stay in the database and
keep working with the official image.

Once a build with fixed IP pre-auth keys has run, the database has two extra
columns, and on SQLite every image without them (official or an older setip
build) refuses to start with `validating schema`. Remove them first, with
headscale stopped:

```sh
cd ~/headscale-admin
docker compose stop headscale
C=$(docker compose ps -aq headscale)
D=~/headscale-rollback-$(date +%F-%H%M); mkdir -p "$D"
docker cp "$C":/var/lib/headscale/. "$D/"
ls "$D"   # must not list db.sqlite-wal; if it does, stop here
cp "$D/db.sqlite" "$D/db.sqlite.before-rollback"
docker run --rm -v "$D":/d alpine:3 sh -c 'apk add -q sqlite && sqlite3 /d/db.sqlite "
  ALTER TABLE pre_auth_keys DROP COLUMN ipv4;
  ALTER TABLE pre_auth_keys DROP COLUMN ipv6;
  DELETE FROM migrations WHERE id = '\''202610271200-pre-auth-key-fixed-ips'\'';"'
docker cp "$D/db.sqlite" "$C":/var/lib/headscale/db.sqlite
# now switch the image: line and docker compose up -d headscale
```

Pending fixed addresses on unused keys are dropped; node addresses are kept.
Upgrading again re-adds the columns.

## Branches

| Branch | Base |
| --- | --- |
| `setip-v0.29.6` | upstream tag `v0.29.6` (current) |
| `setip-v0.29.5` | upstream tag `v0.29.5` (previous, kept for rollback) |

Each branch is the set-ip commits rebased onto the upstream tag. For the next
release: `git checkout -b setip-vX.Y.Z setip-v0.29.6 && git rebase --onto vX.Y.Z v0.29.6`, bump the Go and base
image pins in `Dockerfile.setip` to match upstream's `Dockerfile.integration`
and `.goreleaser.yml`, then `go test ./hscontrol/...` and rebuild.

## headscale-admin "Set IP"

A companion patch for headscale-admin (`dev` branch, branch `setip`) adds a
pencil button next to a node's IP addresses. It calls the endpoint above, sends
only the family you changed, and shows headscale's error (in use, outside
prefix, reserved) as a toast. Against an official headscale image the button
just shows an error, nothing else breaks. Build it with the repo's own
Dockerfile.
