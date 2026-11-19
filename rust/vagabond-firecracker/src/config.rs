// -------------------------------------------------------------------------------
// Daemon Configuration
//
// Author: Alex Freidah
//
// The daemon's flags: where it serves and keeps state, what each microVM boots,
// who Firecracker runs as, and how microVMs reach the network.
// -------------------------------------------------------------------------------

use std::net::Ipv4Addr;
use std::path::PathBuf;

use clap::Parser;
use ipnet::Ipv4Net;
use tracing::level_filters::LevelFilter;

// Config is the daemon's command line, parsed into typed values. The field
// comments are the flags' --help text.
#[derive(Parser)]
#[command(about = "Runs workloads in Firecracker microVMs for the vagabond agent")]
pub struct Config {
    /// Unix socket the executor protocol is served on, openable by root only
    #[arg(long, default_value = "/run/vagabond-firecracker/executor.sock")]
    pub socket: PathBuf,

    /// Directory holding each microVM's record and output
    #[arg(long, default_value = "/var/lib/vagabond-firecracker")]
    pub state_dir: PathBuf,

    /// Guest kernel every microVM boots
    #[arg(long)]
    pub kernel: PathBuf,

    /// Initramfs holding vagabond-init, which every microVM boots into
    #[arg(long)]
    pub initramfs: PathBuf,

    /// Firecracker binary the jailer runs
    #[arg(long, default_value = "/usr/bin/firecracker")]
    pub firecracker: PathBuf,

    /// Jailer binary each microVM starts through
    #[arg(long, default_value = "/usr/bin/jailer")]
    pub jailer: PathBuf,

    /// User Firecracker runs as inside its jail
    #[arg(long)]
    pub uid: u32,

    /// Group Firecracker runs as inside its jail
    #[arg(long)]
    pub gid: u32,

    /// Directory the jailer builds each microVM's jail under
    #[arg(long, default_value = "/srv/jailer")]
    pub chroot_base: PathBuf,

    /// MiB added to each microVM's memory for Firecracker itself in its cgroup
    #[arg(long, default_value_t = 32)]
    pub vmm_overhead: u64,

    /// Addresses each microVM's /30 is taken from
    #[arg(long, default_value = "172.16.0.0/16")]
    pub pool: Ipv4Net,

    /// Host interface microVM traffic leaves through, behind NAT
    #[arg(long)]
    pub uplink: String,

    /// Nameserver each microVM resolves through
    #[arg(long, default_value = "1.1.1.1")]
    pub dns: Ipv4Addr,

    /// Least severe level logged: error, warn, info, debug or trace
    #[arg(long, default_value = "info")]
    pub log_level: LevelFilter,
}

#[cfg(test)]
impl Config {
    // sample is the daemon's flags with only the required ones given, as an
    // operator who takes every default starts it, with its state under dir and
    // its jails under dir/jails, for the tests of every module.
    pub(crate) fn sample(dir: &std::path::Path) -> Config {
        let state_dir = dir.display().to_string();
        let jails = dir.join("jails").display().to_string();
        Config::parse_from([
            "vagabond-firecracker",
            "--kernel",
            "/boot/vmlinux",
            "--initramfs",
            "/boot/initramfs",
            "--uid",
            "10000",
            "--gid",
            "10000",
            "--uplink",
            "eth0",
            "--state-dir",
            &state_dir,
            "--chroot-base",
            &jails,
        ])
    }
}
