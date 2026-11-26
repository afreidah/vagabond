// -------------------------------------------------------------------------------
// MicroVM Networking
//
// Author: Alex Freidah
//
// Gives each microVM a tap device and a /30 from the pool, behind NAT out of
// the uplink, so microVMs reach the internet and nothing reaches them. Set up
// with the ip and nft commands, as Firecracker's network setup describes, and
// with iptables on a host where Docker's forward drop is in the way.
// -------------------------------------------------------------------------------

use std::io;
use std::net::Ipv4Addr;
use std::path::Path;
use std::process::Stdio;

use ipnet::Ipv4Net;
use serde::{Deserialize, Serialize};
use tokio::io::AsyncWriteExt;
use tokio::process::Command;

use crate::context;

// FIRST_CID is the lowest vsock CID a guest may have: 0 to 2 are reserved, 2
// being the host.
const FIRST_CID: u32 = 3;

// PRIVATE_RANGES are what a microVM reaches only when its workload asks: the
// RFC 1918 ranges, shared address space and link-local.
const PRIVATE_RANGES: &str =
    "10.0.0.0/8, 100.64.0.0/10, 169.254.0.0/16, 172.16.0.0/12, 192.168.0.0/16";

// Slot is where one microVM sits: its vsock CID, its tap device, and the /30
// the tap and the guest share.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct Slot {
    pub cid: u32,
    pub tap: String,
    pub subnet: Ipv4Net,
}

impl Slot {
    // free returns the lowest slot no microVM holds, given the CIDs in use, or
    // None when the pool has no /30 left. A private microVM's tap is named
    // vfcp<n> rather than vfc<n>, which is how the firewall rules tell it may
    // reach the node and its private networks.
    pub fn free(pool: Ipv4Net, used_cids: &[u32], private: bool) -> Option<Slot> {
        let prefix = if private { "vfcp" } else { "vfc" };
        let subnets = pool.subnets(30).ok()?;
        (0..)
            .zip(subnets)
            .find(|(n, _)| !used_cids.contains(&(FIRST_CID + n)))
            .map(|(n, subnet)| Slot {
                cid: FIRST_CID + n,
                tap: format!("{prefix}{n}"),
                subnet,
            })
    }

    // host is the tap's address, the first of the /30, which the guest routes
    // through.
    pub fn host(&self) -> Ipv4Addr {
        Ipv4Addr::from(u32::from(self.subnet.network()) + 1)
    }

    // guest is the microVM's address, the second of the /30.
    pub fn guest(&self) -> Ipv4Addr {
        Ipv4Addr::from(u32::from(self.subnet.network()) + 2)
    }

    // create_tap makes the microVM's tap device, owned by the user and group
    // Firecracker runs as so the jailed process can open it, with the host's
    // address on it and up.
    pub async fn create_tap(&self, uid: u32, gid: u32) -> io::Result<()> {
        let (uid, gid) = (uid.to_string(), gid.to_string());
        let host = format!("{}/{}", self.host(), self.subnet.prefix_len());
        let tap = self.tap.as_str();
        run(
            "ip",
            &[
                "tuntap", "add", "dev", tap, "mode", "tap", "user", &uid, "group", &gid,
            ],
            "",
        )
        .await?;
        run("ip", &["addr", "add", &host, "dev", tap], "").await?;
        run("ip", &["link", "set", tap, "up"], "").await
    }

    // delete_tap removes the microVM's tap device, doing nothing when it is
    // already gone, so a release can be repeated.
    pub async fn delete_tap(&self) -> io::Result<()> {
        if !Path::new("/sys/class/net").join(&self.tap).exists() {
            return Ok(());
        }
        run("ip", &["link", "del", &self.tap], "").await
    }
}

// setup_nat lets microVMs reach the internet through the uplink and keeps
// everything else from reaching them: forwarding on, the daemon's nftables
// table replaced whole in one transaction, so a restart never doubles a rule,
// and the microVMs' traffic let through Docker's forward drop. A microVM
// cannot reach private ranges or the node, except DNS to the name server,
// unless its tap is a private one.
pub async fn setup_nat(pool: Ipv4Net, uplink: &str, dns: Ipv4Addr) -> io::Result<()> {
    tokio::fs::write("/proc/sys/net/ipv4/ip_forward", "1")
        .await
        .map_err(context("enabling IPv4 forwarding"))?;
    let rules = format!(
        r#"
add table ip vagabond_firecracker
delete table ip vagabond_firecracker
table ip vagabond_firecracker {{
    chain postrouting {{
        type nat hook postrouting priority srcnat; policy accept;
        ip saddr {pool} oifname "{uplink}" masquerade
    }}
    chain forward {{
        type filter hook forward priority filter; policy accept;
        oifname "vfc*" ct state established,related accept
        oifname "vfc*" drop
        iifname "vfcp*" oifname "{uplink}" accept
        iifname "vfc*" ip daddr {dns} meta l4proto {{ tcp, udp }} th dport 53 accept
        iifname "vfc*" ip daddr {{ {PRIVATE_RANGES} }} drop
        iifname "vfc*" oifname "{uplink}" accept
        iifname "vfc*" drop
    }}
    chain input {{
        type filter hook input priority filter; policy accept;
        iifname "vfc*" ct state established,related accept
        iifname "vfcp*" accept
        iifname "vfc*" ip daddr {dns} meta l4proto {{ tcp, udp }} th dport 53 accept
        iifname "vfc*" drop
    }}
}}
"#
    );
    run("nft", &["-f", "-"], &rules).await?;
    allow_through_docker(uplink).await
}

// docker_rules are the DOCKER-USER rules the microVMs' traffic needs: out of
// the uplink, and only replies back in.
pub fn docker_rules(uplink: &str) -> [Vec<&str>; 2] {
    [
        vec!["-i", "vfc+", "-o", uplink, "-j", "ACCEPT"],
        vec![
            "-i",
            uplink,
            "-o",
            "vfc+",
            "-m",
            "conntrack",
            "--ctstate",
            "RELATED,ESTABLISHED",
            "-j",
            "ACCEPT",
        ],
    ]
}

// allow_through_docker adds docker_rules to the DOCKER-USER chain, the one
// Docker leaves for rules of others, since every forwarded packet also passes
// the drop Docker sets on a host it turns forwarding on for. Each rule goes in
// once; a host without the chain has no such drop.
async fn allow_through_docker(uplink: &str) -> io::Result<()> {
    if !succeeds("iptables", &["-w", "-n", "-L", "DOCKER-USER"]).await {
        return Ok(());
    }
    for rule in docker_rules(uplink) {
        if !succeeds(
            "iptables",
            &[&["-w", "-C", "DOCKER-USER"], rule.as_slice()].concat(),
        )
        .await
        {
            run(
                "iptables",
                &[&["-w", "-I", "DOCKER-USER"], rule.as_slice()].concat(),
                "",
            )
            .await?;
        }
    }
    Ok(())
}

// succeeds runs a check, whose failure is an answer rather than an error, and
// is false when the command fails or cannot run.
async fn succeeds(program: &str, args: &[&str]) -> bool {
    Command::new(program)
        .args(args)
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .status()
        .await
        .is_ok_and(|status| status.success())
}

// run runs a command to completion with input on its stdin, turning a failure
// into an error that names the command and carries what it printed on stderr.
async fn run(program: &str, args: &[&str], input: &str) -> io::Result<()> {
    let mut child = Command::new(program)
        .args(args)
        .stdin(Stdio::piped())
        .stdout(Stdio::null())
        .stderr(Stdio::piped())
        .spawn()
        .map_err(context(program))?;
    // Dropping stdin after the write closes it, so the program sees its input end.
    if let Some(mut stdin) = child.stdin.take() {
        stdin.write_all(input.as_bytes()).await?;
    }
    let output = child.wait_with_output().await?;
    if output.status.success() {
        return Ok(());
    }
    let stderr = String::from_utf8_lossy(&output.stderr);
    Err(io::Error::other(format!(
        "{program} {}: {}",
        args.join(" "),
        stderr.trim()
    )))
}

#[cfg(test)]
mod tests {
    use super::*;

    // pool is the default pool the daemon takes /30s from.
    fn pool() -> Ipv4Net {
        "172.16.0.0/16".parse().unwrap()
    }

    // With no microVM holding one, the first slot is the pool's first /30 and
    // the lowest guest CID.
    #[test]
    fn free_starts_at_the_first_slot() {
        let slot = Slot::free(pool(), &[], false).unwrap();
        let first = Slot {
            cid: 3,
            tap: "vfc0".into(),
            subnet: "172.16.0.0/30".parse().unwrap(),
        };
        assert_eq!(slot, first);
    }

    // A slot a released microVM left is taken again before a new one, so the
    // numbers stay low.
    #[test]
    fn free_reuses_a_gap() {
        let slot = Slot::free(pool(), &[3, 5], false).unwrap();
        assert_eq!(slot.cid, 4);
        assert_eq!(slot.tap, "vfc1");
        assert_eq!(slot.subnet, "172.16.0.4/30".parse::<Ipv4Net>().unwrap());
    }

    // A private microVM's tap is named vfcp<n>, which the firewall rules let
    // reach the node and its private networks.
    #[test]
    fn free_names_a_private_tap() {
        let slot = Slot::free(pool(), &[3], true).unwrap();
        assert_eq!(slot.cid, 4);
        assert_eq!(slot.tap, "vfcp1");
    }

    // A pool whose every /30 is held has no slot left: a /29 holds two.
    #[test]
    fn free_is_none_when_the_pool_is_full() {
        let pool = "172.16.0.0/29".parse().unwrap();
        assert_eq!(Slot::free(pool, &[3, 4], false), None);
    }

    // The tap takes the first address of the /30 and the guest the second.
    #[test]
    fn host_and_guest_are_the_first_two_addresses() {
        let slot = Slot::free(pool(), &[3], false).unwrap();
        assert_eq!(slot.host(), Ipv4Addr::new(172, 16, 0, 5));
        assert_eq!(slot.guest(), Ipv4Addr::new(172, 16, 0, 6));
    }
}
