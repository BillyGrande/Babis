# Test Environments & Services

Recommendations for building test environments for Babis — from free local
simulation to physical edge fleets and rented infrastructure. Prices are
approximate as of mid-2026.

## Premise: an air gap is a network condition, not a hardware property

Everything Babis cares about — no internet on day 0, outbound-only WAN,
flaky links, silent edges — is a *network topology*. Software can simulate
all of it precisely and for free. Spend money only on what software cannot
fake: real arm64 silicon, real resource constraints, and the feel of a
physical fleet on a shelf.

## Tier 0 — free, local (where most testing belongs)

### True air gap: VMs with no default route

Two or three Ubuntu VMs (multipass, virt-manager, VirtualBox) on host-only
networking with **no gateway configured**, running k3s or MicroK8s. A local
OCI registry (Hauler store or plain `registry:2`) runs inside the VM.
`babis bootstrap --chart oci://localhost:5000/... --plain-http` either
works fully offline or it doesn't — this is the honest test.

### CI-friendly air gap: kind on an internal Docker network

```bash
docker network create --internal airgap
# attach kind nodes to it; containers on --internal networks have no egress
```

This is the pattern for the roadmap's kind-based e2e (v0.1): operator in
one kind cluster, "edges" in others, egress removed selectively.

### Semi-connected simulation: `tc netem` + an iptables toggle

The flaky outbound-only WAN — Babis's actual niche — is a one-liner:

```bash
# degrade the link: 400ms latency, 15% packet loss
sudo tc qdisc add dev eth0 root netem delay 400ms loss 15%
# restore
sudo tc qdisc del dev eth0 root
```

And "edge goes silent for hours, does Reachable flip and recover?" is a
cron/systemd timer flipping a DROP rule:

```bash
iptables -A OUTPUT -d <hub-ip> -j DROP    # silence begins
iptables -D OUTPUT -d <hub-ip> -j DROP    # edge comes back
```

These tests are *more* rigorous than physical hardware because the failure
is precise and repeatable.

## Tier 1 — cheap physical fleet (~€100–250 total, one-time)

| Device | Approx. price | Why |
|---|---|---|
| Used thin clients / 1-liter mini PCs (Dell Wyse 5070, HP t630, Lenovo ThinkCentre Tiny, Fujitsu Futro) | €30–80 each (eBay, refurbishers; in Greece also Skroutz) | Best value-per-edge. x86, quiet, low power, RAM-upgradable, runs k3s/MicroK8s happily. Three of them is a believable fleet. |
| Raspberry Pi 5, 8 GB | ~€90 | Buy **one**, not three. Its job is validating the `linux-arm64` release binary and low-memory behavior — not being the fleet. A spare Pi 4 does the job too. |
| New N100 mini PCs (Beelink, GMKtec) | €130–180 | Lovely but overkill for this purpose. |

## Tier 2 — rented infrastructure (for always-on / real-WAN tests)

| Service | Approx. price | Role |
|---|---|---|
| **Hetzner Cloud** (CAX11 arm64 or CX22) | €4–8/month | Best pick from the EU. Killer feature: nodes on a **private network with no public IP**, routed through a NAT-gateway VM you control — firewall the gateway to get outbound-only, throttled, or fully-gapped topologies on demand. Also hosts a realistic central cluster. |
| **Oracle Cloud Always Free** (4 arm cores / 24 GB) | free | Permanent home for the central operator, so heartbeats cross a real WAN. Grab it. |
| **Scaleway Stardust** | €1–2/month | Disposable fake edges. |

Renting fake edges is the **least** useful spend: cloud VMs are too
healthy, too connected, and too x86 to teach anything the free tier
doesn't. Rent for the central side and for real-WAN crossing, not for
edge realism.

## Recommended sequence

1. **Spend €0 now.** Build the VM + netem harness and the kind e2e — it's
   roadmap v0.1 work anyway, and it's the setup CI runs forever.
2. **~€150 later:** two used thin clients + one Pi when a demo-able
   physical fleet and arm64 confidence matter.
3. **One rented central node** (Oracle free tier first, Hetzner if it
   outgrows it) when the operator should live across a real WAN from the
   edges.
