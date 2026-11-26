// -------------------------------------------------------------------------------
// Container Networking Tests
//
// Author: Alex Freidah
//
// The parts of container networking that need no root: finding the CNI
// plugins, the resolv.conf and names newNetwork writes, the nftables script,
// and when the Docker rules are added. Attaching real containers is in the
// containerd executor tests.
// -------------------------------------------------------------------------------

package executor

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	cni "github.com/containerd/go-cni"
)

// -------------------------------------------------------------------------
// FIXTURES
// -------------------------------------------------------------------------

// pluginDir is a directory holding a stand-in for each CNI plugin, which
// newNetwork only looks for.
func pluginDir(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	for _, plugin := range cniPlugins {
		if err := os.WriteFile(filepath.Join(dir, plugin), nil, 0o755); err != nil { //nolint:gosec // a stand-in plugin
			t.Fatal(err)
		}
	}

	return dir
}

// commands records every command a network runs, answering each from fail:
// the command line it is given fails when fail returns true.
type commands struct {
	ran  []string
	fail func(line string) bool
}

// run records the command line and fails it when fail says so.
func (c *commands) run(_ context.Context, _, name string, args ...string) error {
	line := strings.Join(append([]string{name}, args...), " ")
	c.ran = append(c.ran, line)

	if c.fail != nil && c.fail(line) {
		return errors.New("exit status 1")
	}

	return nil
}

// testNetwork is a network over the default pool and name server whose
// commands are recorded by cmds.
func testNetwork(cmds *commands) *network {
	return &network{
		cfg:  NetworkConfig{Pool: DefaultContainerPool, DNS: DefaultDNS},
		name: "vagabond_containers",
		run:  cmds.run,
	}
}

// fakeCNI answers Setup with result and setupErr, and Remove with removeErr,
// for the failures a real plugin does not give on demand. Its other methods
// are unset.
type fakeCNI struct {
	cni.CNI

	result    *cni.Result
	setupErr  error
	removeErr error
}

// Setup returns the result and error it was given.
func (f *fakeCNI) Setup(context.Context, string, string, ...cni.NamespaceOpts) (*cni.Result, error) {
	return f.result, f.setupErr
}

// Remove returns the error it was given.
func (f *fakeCNI) Remove(context.Context, string, string, ...cni.NamespaceOpts) error {
	return f.removeErr
}

// resultWith is a CNI result giving the container interface addr.
func resultWith(addr string) *cni.Result {
	return &cni.Result{Interfaces: map[string]*cni.Config{"eth0": {IPConfigs: []*cni.IPConfig{{IP: net.ParseIP(addr)}}}}}
}

// -------------------------------------------------------------------------
// CONSTRUCTOR
// -------------------------------------------------------------------------

// The defaults fill an empty config, the names come from the containerd
// namespace, and the resolv.conf names the name server.
func TestNewNetwork(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()

	n, err := newNetwork(&NetworkConfig{CNIPath: pluginDir(t)}, dataDir, "vagabond-test")
	if err != nil {
		t.Fatalf("newNetwork() = %v", err)
	}

	if n.cfg.Pool != DefaultContainerPool || n.cfg.DNS != DefaultDNS || n.name != "vagabond_test_containers" {
		t.Errorf("network = %+v, %s; want the defaults under vagabond_test_containers", n.cfg, n.name)
	}

	resolv, err := os.ReadFile(n.resolvConf)
	if err != nil || string(resolv) != "nameserver 1.1.1.1\n" || filepath.Dir(n.resolvConf) != filepath.Join(dataDir, "cni") {
		t.Errorf("resolv.conf at %s = %q, %v; want the name server under the data dir", n.resolvConf, resolv, err)
	}
}

// A missing plugin is an error naming it, before anything is written.
func TestNewNetwork_MissingPlugin(t *testing.T) {
	t.Parallel()

	dir := pluginDir(t)
	if err := os.Remove(filepath.Join(dir, "host-local")); err != nil {
		t.Fatal(err)
	}

	if _, err := newNetwork(&NetworkConfig{CNIPath: dir}, t.TempDir(), "vagabond"); err == nil ||
		!strings.Contains(err.Error(), "the CNI plugin host-local") {
		t.Errorf("newNetwork() = %v, want the missing plugin named", err)
	}
}

// A data directory the network's files cannot be written under is an error
// naming what could not be made.
func TestNewNetwork_UnwritableDataDir(t *testing.T) {
	t.Parallel()

	// A file where the data directory should be, then a directory where resolv.conf should be
	notADir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notADir, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	blocked := t.TempDir()
	if err := os.MkdirAll(filepath.Join(blocked, "cni", "resolv.conf"), 0o750); err != nil {
		t.Fatal(err)
	}

	for dataDir, want := range map[string]string{notADir: "creating the network directory", blocked: "writing"} {
		if _, err := newNetwork(&NetworkConfig{CNIPath: pluginDir(t)}, dataDir, "vagabond"); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("newNetwork(%s) = %v, want %q", dataDir, err, want)
		}
	}
}

// -------------------------------------------------------------------------
// CONTAINERS
// -------------------------------------------------------------------------

// attach returns the address CNI gave the container's interface, and fails
// when CNI fails or gives it none.
func TestNetwork_Attach(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		fake *fakeCNI
		want string
	}{
		"attached":   {fake: &fakeCNI{result: resultWith("172.30.0.2")}, want: "172.30.0.2"},
		"refused":    {fake: &fakeCNI{setupErr: errors.New("refused")}, want: "refused"},
		"no address": {fake: &fakeCNI{result: &cni.Result{Interfaces: map[string]*cni.Config{}}}, want: "CNI gave it no address"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			n := testNetwork(&commands{})
			n.cni = tc.fake

			// The address when it attached, the error when it did not
			addr, err := n.attach(t.Context(), "exec-1", 42)

			got := addr.String()
			if err != nil {
				got = err.Error()
			}

			if !strings.Contains(got, tc.want) {
				t.Errorf("attach() = %q, want %q", got, tc.want)
			}
		})
	}
}

// detach passes CNI's failure to free an address on.
func TestNetwork_Detach(t *testing.T) {
	t.Parallel()

	n := testNetwork(&commands{})

	n.cni = &fakeCNI{}
	if err := n.detach(t.Context(), "exec-1"); err != nil {
		t.Errorf("detach() = %v", err)
	}

	n.cni = &fakeCNI{removeErr: errors.New("refused")}
	if err := n.detach(t.Context(), "exec-1"); err == nil || !strings.Contains(err.Error(), "freeing the address of exec-1") {
		t.Errorf("detach() = %v, want the failure", err)
	}
}

// -------------------------------------------------------------------------
// RULES
// -------------------------------------------------------------------------

// The script replaces the table whole, NATs the pool, drops private ranges
// and the node, and lists the private containers as exceptions.
func TestNetwork_Ruleset(t *testing.T) {
	t.Parallel()

	n := testNetwork(&commands{})

	for _, want := range []string{
		"table ip vagabond_containers\ndelete table ip vagabond_containers\ntable ip vagabond_containers {",
		"ip saddr 172.30.0.0/16 ip daddr != 172.30.0.0/16 masquerade",
		"ip daddr 172.30.0.0/16 drop",
		"ip saddr 172.30.0.0/16 ip daddr 1.1.1.1 meta l4proto { tcp, udp } th dport 53 accept",
		"ip saddr 172.30.0.0/16 ip daddr { " + privateRanges + " } drop",
		"ip saddr 172.30.0.0/16 drop",
		"elements = { 172.30.0.2, 172.30.0.6 }",
	} {
		if got := n.ruleset([]netip.Addr{netip.MustParseAddr("172.30.0.2"), netip.MustParseAddr("172.30.0.6")}); !strings.Contains(got, want) {
			t.Errorf("ruleset() lacks %q:\n%s", want, got)
		}
	}

	if got := n.ruleset(nil); strings.Contains(got, "elements") {
		t.Errorf("ruleset(nil) lists elements:\n%s", got)
	}
}

// The table goes through nft, and a failure names it.
func TestNetwork_Rules(t *testing.T) {
	t.Parallel()

	cmds := &commands{fail: func(line string) bool { return strings.HasPrefix(line, "iptables") }}

	if err := testNetwork(cmds).rules(t.Context(), nil); err != nil || cmds.ran[0] != "nft -f -" {
		t.Errorf("rules() = %v after %q, want the table written by nft", err, cmds.ran)
	}

	failing := &commands{fail: func(line string) bool { return line == "nft -f -" }}
	if err := testNetwork(failing).rules(t.Context(), nil); err == nil || !strings.Contains(err.Error(), "vagabond_containers table") {
		t.Errorf("rules() = %v, want the table's failure", err)
	}
}

// The Docker rules go in only where Docker's chain exists, each only when it
// is missing.
func TestNetwork_DockerUser(t *testing.T) {
	t.Parallel()

	const (
		list   = "iptables -w -n -L DOCKER-USER"
		accept = "-s 172.30.0.0/16 -j ACCEPT"
		reply  = "-d 172.30.0.0/16 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT"
	)

	cases := map[string]struct {
		fail func(string) bool
		want []string
		err  bool
	}{
		"no Docker": {
			fail: func(line string) bool { return line == list },
			want: []string{list},
		},
		"one rule there": {
			fail: func(line string) bool { return line == "iptables -w -C DOCKER-USER "+reply },
			want: []string{list, "iptables -w -C DOCKER-USER " + accept, "iptables -w -C DOCKER-USER " + reply, "iptables -w -I DOCKER-USER " + reply},
		},
		"insert refused": {
			fail: func(line string) bool { return line != list },
			want: []string{list, "iptables -w -C DOCKER-USER " + accept, "iptables -w -I DOCKER-USER " + accept},
			err:  true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cmds := &commands{fail: tc.fail}

			err := testNetwork(cmds).dockerUser(t.Context())
			if (err != nil) != tc.err || !slices.Equal(cmds.ran, tc.want) {
				t.Errorf("dockerUser() = %v after %q, want %q", err, cmds.ran, tc.want)
			}
		})
	}
}

// A command's input reaches it, and a failure carries what it printed.
func TestRunCommand(t *testing.T) {
	t.Parallel()

	if err := runCommand(t.Context(), "in", "grep", "-q", "in"); err != nil {
		t.Errorf("runCommand() = %v, want its input read", err)
	}

	if err := runCommand(t.Context(), "", "sh", "-c", "echo refused >&2; exit 3"); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Errorf("runCommand() = %v, want what it printed", err)
	}
}
