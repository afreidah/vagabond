// -------------------------------------------------------------------------------
// Container Networking
//
// Author: Alex Freidah
//
// Gives each container its own network namespace, connected to the node by
// CNI's ptp plugin with an address from the container pool. NAT, the Docker
// rules and the firewall drops are fixed rules for the whole pool, so the only
// thing to clean up per container is its address, which CNI releases by
// container ID after the namespace is gone.
// -------------------------------------------------------------------------------

package executor

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	cni "github.com/containerd/go-cni"
)

// -------------------------------------------------------------------------
// CONSTANTS
// -------------------------------------------------------------------------

// The default address range for containers, and the default name server for
// isolated containers and microVMs.
var (
	DefaultContainerPool = netip.MustParsePrefix("172.30.0.0/16")
	DefaultDNS           = netip.MustParseAddr("1.1.1.1")
)

// DefaultCNIPath is where the image puts the CNI plugins.
const DefaultCNIPath = "/opt/cni/bin"

// dockerChain is the iptables chain Docker leaves for others' rules.
const dockerChain = "DOCKER-USER"

// cniPlugins are the plugins a container's network takes.
var cniPlugins = []string{"ptp", "host-local", "loopback"}

// privateRanges are what a container reaches only when its task asks: the
// RFC 1918 ranges, shared address space and link-local.
const privateRanges = "10.0.0.0/8, 100.64.0.0/10, 169.254.0.0/16, 172.16.0.0/12, 192.168.0.0/16"

// conflist is the CNI network config, filled in with its name, the pool, and
// the directory where host-local records the addresses it hands out. NAT and
// the firewall are in the nftables table, not CNI.
const conflist = `{"cniVersion": "1.0.0", "name": %q, "plugins": [{"type": "ptp", "ipam": ` +
	`{"type": "host-local", "ranges": [[{"subnet": %q}]], "routes": [{"dst": "0.0.0.0/0"}], "dataDir": %q}}]}`

// -------------------------------------------------------------------------
// TYPES
// -------------------------------------------------------------------------

// NetworkConfig is how containers are networked. Host puts them on the node's
// own network. Otherwise each gets an address from Pool through the CNI
// plugins in CNIPath, and isolated containers resolve through DNS. Zero
// values take the defaults.
type NetworkConfig struct {
	Host    bool
	Pool    netip.Prefix
	DNS     netip.Addr
	CNIPath string
}

// network connects containers and keeps the pool's firewall rules. name is
// used for both the CNI network and the nftables table, and comes from the
// containerd namespace, so the executor tests never replace a real agent's.
type network struct {
	cfg        NetworkConfig
	cni        cni.CNI
	name       string
	resolvConf string
	run        func(ctx context.Context, stdin, name string, args ...string) error

	mu sync.Mutex // one rewrite of the rules at a time
}

// -------------------------------------------------------------------------
// CONSTRUCTOR
// -------------------------------------------------------------------------

// newNetwork checks that the CNI plugins are installed and writes the
// resolv.conf isolated containers use. Both it and the addresses CNI hands
// out live under dataDir.
func newNetwork(given *NetworkConfig, dataDir, namespace string) (*network, error) {
	cfg := *given
	cfg.Pool = cmp.Or(cfg.Pool, DefaultContainerPool)
	cfg.DNS = cmp.Or(cfg.DNS, DefaultDNS)
	cfg.CNIPath = cmp.Or(cfg.CNIPath, DefaultCNIPath)

	// A missing plugin fails here, before the node registers, not at a workload
	for _, plugin := range cniPlugins {
		if _, err := os.Stat(filepath.Join(cfg.CNIPath, plugin)); err != nil {
			return nil, fmt.Errorf("the CNI plugin %s: %w", plugin, err)
		}
	}

	name := strings.ReplaceAll(namespace, "-", "_") + "_containers"
	dir := filepath.Join(dataDir, "cni")

	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("creating the network directory: %w", err)
	}

	// Mounted read-only into containers, whose process may not be root
	resolvConf := filepath.Join(dir, "resolv.conf")
	if err := os.WriteFile(resolvConf, []byte("nameserver "+cfg.DNS.String()+"\n"), 0o644); err != nil { //nolint:gosec // read by containers
		return nil, fmt.Errorf("writing %s: %w", resolvConf, err)
	}

	c, err := cni.New(cni.WithPluginDir([]string{cfg.CNIPath}),
		cni.WithConfListBytes(fmt.Appendf(nil, conflist, name, cfg.Pool, filepath.Join(dir, "addresses"))),
		cni.WithLoNetwork)
	if err != nil {
		return nil, fmt.Errorf("loading the container network: %w", err)
	}

	return &network{cfg: cfg, cni: c, name: name, resolvConf: resolvConf, run: runCommand}, nil
}

// -------------------------------------------------------------------------
// CONTAINERS
// -------------------------------------------------------------------------

// attach connects a container to the node through the network namespace of
// its task's process, pid, and returns the address CNI gave it.
func (n *network) attach(ctx context.Context, id string, pid uint32) (netip.Addr, error) {
	res, err := n.cni.Setup(ctx, id, fmt.Sprintf("/proc/%d/ns/net", pid))
	if err != nil {
		return netip.Addr{}, fmt.Errorf("attaching %s to its network: %w", id, err)
	}

	if iface := res.Interfaces["eth0"]; iface != nil && len(iface.IPConfigs) > 0 {
		if addr, ok := netip.AddrFromSlice(iface.IPConfigs[0].IP.To4()); ok {
			return addr, nil
		}
	}

	return netip.Addr{}, fmt.Errorf("attaching %s to its network: CNI gave it no address", id)
}

// detach releases a container's address. Its link to the node was removed
// with its network namespace when the task ended, so no namespace is passed.
func (n *network) detach(ctx context.Context, id string) error {
	if err := n.cni.Remove(ctx, id, ""); err != nil {
		return fmt.Errorf("freeing the address of %s: %w", id, err)
	}

	return nil
}

// -------------------------------------------------------------------------
// RULES
// -------------------------------------------------------------------------

// rules replaces the nftables table, exempting the addresses in private from
// the private-network drops, and adds the Docker rules.
func (n *network) rules(ctx context.Context, private []netip.Addr) error {
	n.mu.Lock()
	defer n.mu.Unlock()

	if err := n.run(ctx, n.ruleset(private), "nft", "-f", "-"); err != nil {
		return fmt.Errorf("writing the %s table: %w", n.name, err)
	}

	return n.dockerUser(ctx)
}

// ruleset is the nftables script that replaces the table in one transaction.
// Containers get NAT out and replies back, but no new connections in. They
// cannot reach private ranges or the node, except DNS to the name server,
// unless their address is in private. The table only drops: a drop in any
// nftables table is final, so it needs no accepts to override others.
func (n *network) ruleset(private []netip.Addr) string {
	var elements string

	if len(private) > 0 {
		addrs := make([]string, len(private))
		for i, addr := range private {
			addrs[i] = addr.String()
		}

		elements = "elements = { " + strings.Join(addrs, ", ") + " }"
	}

	return fmt.Sprintf(`table ip %[1]s
delete table ip %[1]s
table ip %[1]s {
	set private {
		type ipv4_addr
		%[4]s
	}
	chain postrouting {
		type nat hook postrouting priority srcnat; policy accept;
		ip saddr %[2]s ip daddr != %[2]s masquerade
	}
	chain forward {
		type filter hook forward priority filter; policy accept;
		ip daddr %[2]s ct state established,related accept
		ip daddr %[2]s drop
		ip saddr @private accept
		ip saddr %[2]s ip daddr %[3]s meta l4proto { tcp, udp } th dport 53 accept
		ip saddr %[2]s ip daddr { %[5]s } drop
	}
	chain input {
		type filter hook input priority filter; policy accept;
		ip saddr %[2]s ct state established,related accept
		ip saddr @private accept
		ip saddr %[2]s ip daddr %[3]s meta l4proto { tcp, udp } th dport 53 accept
		ip saddr %[2]s drop
	}
}
`, n.name, n.cfg.Pool, n.cfg.DNS, elements, privateRanges)
}

// dockerUser adds accepts for the pool to Docker's DOCKER-USER chain, so
// Docker's forward drop does not block containers. Each rule is added once,
// and nothing is added on a node without the chain.
func (n *network) dockerUser(ctx context.Context) error {
	if n.run(ctx, "", "iptables", "-w", "-n", "-L", dockerChain) != nil {
		return nil
	}

	pool := n.cfg.Pool.String()

	for _, rule := range [][]string{
		{"-s", pool, "-j", "ACCEPT"},
		{"-d", pool, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"},
	} {
		if n.run(ctx, "", "iptables", append([]string{"-w", "-C", dockerChain}, rule...)...) == nil {
			continue
		}

		if err := n.run(ctx, "", "iptables", append([]string{"-w", "-I", dockerChain}, rule...)...); err != nil {
			return fmt.Errorf("letting the pool past Docker's forward drop: %w", err)
		}
	}

	return nil
}

// runCommand runs name with args and stdin on its standard input, failing
// with what it printed when it exits non-zero.
func runCommand(ctx context.Context, stdin, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // nft and iptables, with arguments from configuration
	cmd.Stdin = strings.NewReader(stdin)

	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %w: %s", name, err, bytes.TrimSpace(out))
	}

	return nil
}
