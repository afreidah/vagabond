// -------------------------------------------------------------------------------
// MicroVMs
//
// Author: Alex Freidah
//
// Launches and removes one Firecracker microVM: its jail, the cgroup that holds
// it to what it declared, the configuration it boots from, and the jailer that
// starts it.
// -------------------------------------------------------------------------------

use std::fs;
use std::io;
use std::os::unix::fs::{FileTypeExt, MetadataExt, chown};
use std::path::{Path, PathBuf};
use std::process::Stdio;
use std::sync::Arc;
use std::time::Duration;

use nix::sys::signal::{Signal, kill};
use nix::sys::stat::{Mode, SFlag, mknod};
use nix::unistd::Pid;
use serde::{Deserialize, Serialize};
use serde_json::{Value, json};
use tokio::process::Command;

use crate::config::Config;
use crate::context;
use crate::record::Record;

// CGROUP_ROOT is where cgroup v2 is mounted, which microVM cgroups are relative
// to.
const CGROUP_ROOT: &str = "/sys/fs/cgroup";

// CPU_PERIOD is the cgroup CPU period, in microseconds, that cpu.max grants
// time within.
const CPU_PERIOD: i64 = 100_000;

// STOP_WAIT is how long remove waits for a killed Firecracker to exit.
const STOP_WAIT: Duration = Duration::from_secs(10);

// Paths inside the jail, which Firecracker sees as its root: the kernel, the
// initramfs, the image's drive, the socket the guest's vsock is reached on, and
// the configuration Firecracker boots from.
const KERNEL: &str = "/vmlinux";
const INITRAMFS: &str = "/initramfs";
const ROOT_DRIVE: &str = "/rootfs";
const VSOCK: &str = "/v.sock";
const CONFIG_FILE: &str = "/config.json";

// launch starts the microVM a record describes, its root on root_drive, of
// filesystem root_fstype: its jail, cgroup and tap, then the jailer, which
// moves into the cgroup, drops to Firecracker's user and becomes Firecracker,
// booting at once. It returns Firecracker's process. The jailer's errors and
// the guest's console go to console.log beside the record.
pub async fn launch(
    config: Arc<Config>,
    record: &Record,
    root_drive: &Path,
    root_fstype: &str,
) -> io::Result<Firecracker> {
    // The jail, a kernel copied across filesystems among it, the cgroup and the
    // console are written on a blocking thread, keeping the runtime's free.
    let (owned, drive, fstype) = (
        record.clone(),
        root_drive.to_path_buf(),
        root_fstype.to_string(),
    );
    let written = Arc::clone(&config);
    let console = tokio::task::spawn_blocking(move || {
        prepare_jail(&written, &owned, &drive, &fstype)?;
        create_cgroup(&written, &owned)?;
        let console = console_path(&written, &owned.execution_id);
        fs::File::create(&console).map_err(context(format!("creating {}", console.display())))
    })
    .await
    .map_err(io::Error::other)??;
    record.slot.create_tap(config.uid, config.gid).await?;

    let mut jailer = Command::new(&config.jailer);
    jailer
        .arg("--id")
        .arg(&record.execution_id)
        .arg("--exec-file")
        .arg(&config.firecracker)
        .arg("--uid")
        .arg(config.uid.to_string())
        .arg("--gid")
        .arg(config.gid.to_string())
        .arg("--chroot-base-dir")
        .arg(&config.chroot_base)
        .arg("--cgroup-version")
        .arg("2")
        .arg("--parent-cgroup")
        .arg(&record.cgroup)
        .args(["--", "--config-file", CONFIG_FILE])
        .stdin(Stdio::null())
        .stdout(console.try_clone()?)
        .stderr(console);
    let mut child = jailer.spawn().map_err(context(config.jailer.display()))?;
    let exited = || io::Error::other("the jailer exited as it started");
    let pid = child.id().ok_or_else(exited)?;
    let firecracker = Firecracker::of(pid as i32).ok_or_else(exited)?;
    // Waiting here collects Firecracker's exit, so a stopped one is no zombie.
    tokio::spawn(async move {
        let _ = child.wait().await;
    });
    Ok(firecracker)
}

// remove stops a microVM whose Firecracker still runs and deletes what the
// daemon gave it: the tap, the jail and the cgroup. Each step passes over what
// is already gone, so a release can be repeated, or finish one a crash cut
// short.
pub async fn remove(config: &Config, record: &Record) -> io::Result<()> {
    if let Some(firecracker) = record.firecracker
        && firecracker.running()
    {
        kill(Pid::from_raw(firecracker.pid), Signal::SIGKILL)?;
        firecracker.stopped().await?;
    }
    record.slot.delete_tap().await?;
    match tokio::fs::remove_dir_all(jail_dir(config, &record.execution_id)).await {
        Err(err) if err.kind() == io::ErrorKind::NotFound => {}
        result => result?,
    }
    remove_cgroup(record)
}

// Firecracker is one microVM's Firecracker process: its pid and when it
// started, in clock ticks since boot. The pair names one process, through the
// jailer becoming Firecracker and after, since a pid is reused once its
// process is gone but never with the same start time.
#[derive(Clone, Copy, Debug, PartialEq, Serialize, Deserialize)]
pub struct Firecracker {
    pub pid: i32,
    pub started: u64,
}

impl Firecracker {
    // of is the process with pid as it is now, or None when there is none.
    pub(crate) fn of(pid: i32) -> Option<Firecracker> {
        start_time(pid).map(|started| Firecracker { pid, started })
    }

    // running reports whether the process still runs: not gone, not exited and
    // waiting to be collected, and not another that took its pid.
    pub fn running(&self) -> bool {
        start_time(self.pid) == Some(self.started)
    }

    // stopped waits for the process to exit, as a killed one does before its
    // cgroup can be removed, failing after STOP_WAIT.
    pub async fn stopped(&self) -> io::Result<()> {
        tokio::time::timeout(STOP_WAIT, self.exited())
            .await
            .map_err(|_| io::Error::other(format!("Firecracker {} did not exit", self.pid)))
    }

    // exited waits, however long it takes, for the process to have exited,
    // checking every 50ms.
    pub async fn exited(&self) {
        while self.running() {
            tokio::time::sleep(Duration::from_millis(50)).await;
        }
    }
}

// start_time is when a process started, in clock ticks since boot, from
// /proc/<pid>/stat, or None when it is gone or has exited.
fn start_time(pid: i32) -> Option<u64> {
    let stat = fs::read_to_string(format!("/proc/{pid}/stat")).ok()?;
    // The fields follow the program's name, which is in parentheses and may hold
    // spaces or a parenthesis: the state first, the start time twentieth.
    let fields: Vec<&str> = stat.rsplit_once(')')?.1.split_whitespace().collect();
    match fields.first() {
        Some(&"Z" | &"X") | None => None,
        _ => fields.get(19)?.parse().ok(),
    }
}

// jail_dir is the directory the jailer builds a microVM's jail in, named for
// the Firecracker binary and the execution. The jail's root is its root
// subdirectory.
fn jail_dir(config: &Config, execution_id: &str) -> PathBuf {
    let binary = config.firecracker.file_name().unwrap_or_default();
    config.chroot_base.join(binary).join(execution_id)
}

// console_path is where the jailer's errors and Firecracker's output, the
// guest's console included, are written, beside the execution's record.
pub fn console_path(config: &Config, execution_id: &str) -> PathBuf {
    config.state_dir.join(execution_id).join("console.log")
}

// vsock_path is where on the host Firecracker serves a microVM's vsock, which
// the daemon reaches its guest through.
pub fn vsock_path(config: &Config, execution_id: &str) -> PathBuf {
    jailed(&jail_dir(config, execution_id).join("root"), VSOCK)
}

// jailed is where a path Firecracker sees inside its jail lies on the host. The
// leading slash goes first, since joining an absolute path replaces the base.
fn jailed(root: &Path, path: &str) -> PathBuf {
    root.join(path.trim_start_matches('/'))
}

// link_or_copy puts a boot file into the jail: a hard link where the jail is on
// the file's filesystem, a copy where it is not.
fn link_or_copy(from: &Path, to: &Path) -> io::Result<()> {
    match fs::hard_link(from, to) {
        Err(err) if err.kind() == io::ErrorKind::CrossesDevices => fs::copy(from, to).map(|_| ()),
        result => result,
    }
}

// prepare_jail fills a microVM's jail before the jailer starts it, since
// Firecracker boots from its configuration as soon as it runs: the kernel and
// initramfs, the root drive as a device node only Firecracker's user may open,
// and the configuration, naming the drive's filesystem.
fn prepare_jail(
    config: &Config,
    record: &Record,
    root_drive: &Path,
    root_fstype: &str,
) -> io::Result<()> {
    let root = jail_dir(config, &record.execution_id).join("root");
    fs::create_dir_all(&root).map_err(context(format!("creating {}", root.display())))?;
    link_or_copy(&config.kernel, &jailed(&root, KERNEL)).map_err(context(format!(
        "linking the kernel {}",
        config.kernel.display()
    )))?;
    link_or_copy(&config.initramfs, &jailed(&root, INITRAMFS)).map_err(context(format!(
        "linking the initramfs {}",
        config.initramfs.display()
    )))?;

    let drive = fs::metadata(root_drive).map_err(context(format!(
        "reading the root drive {}",
        root_drive.display()
    )))?;
    if !drive.file_type().is_block_device() {
        let message = format!("root drive {} is not a block device", root_drive.display());
        return Err(io::Error::new(io::ErrorKind::InvalidInput, message));
    }
    let node = jailed(&root, ROOT_DRIVE);
    mknod(
        &node,
        SFlag::S_IFBLK,
        Mode::S_IRUSR | Mode::S_IWUSR,
        drive.rdev(),
    )?;
    chown(&node, Some(config.uid), Some(config.gid))?;

    let firecracker = serde_json::to_vec_pretty(&firecracker_config(config, record, root_fstype))?;
    fs::write(jailed(&root, CONFIG_FILE), firecracker)
}

// firecracker_config is the configuration a microVM boots from. The boot
// arguments name the image's drive and its filesystem for vagabond-init, give
// the kernel the guest's address and nameserver, and turn a guest reboot or
// kernel panic into Firecracker exiting.
fn firecracker_config(config: &Config, record: &Record, root_fstype: &str) -> Value {
    let slot = &record.slot;
    let ip = format!(
        "{}::{}:{}::eth0:off:{}",
        slot.guest(),
        slot.host(),
        slot.subnet.netmask(),
        config.dns
    );
    let boot_args =
        format!("console=ttyS0 reboot=k panic=1 root=/dev/vda rootfstype={root_fstype} ip={ip}");
    json!({
        "boot-source": {
            "kernel_image_path": KERNEL,
            "initrd_path": INITRAMFS,
            "boot_args": boot_args,
        },
        "drives": [{
            "drive_id": "rootfs",
            "path_on_host": ROOT_DRIVE,
            "is_root_device": false,
            "is_read_only": false,
        }],
        "machine-config": {
            "vcpu_count": vcpus(record.cpu),
            "mem_size_mib": record.memory,
        },
        "network-interfaces": [{
            "iface_id": "eth0",
            "host_dev_name": slot.tap,
        }],
        "vsock": {
            "guest_cid": slot.cid,
            "uds_path": VSOCK,
        },
    })
}

// leaf_cgroup is a microVM's cgroup under the one the agent gave, relative to
// the cgroup root, as the jailer's --parent-cgroup takes it.
pub fn leaf_cgroup(parent: &str, execution_id: &str) -> String {
    format!("{}/{execution_id}", parent.trim_matches('/'))
}

// create_cgroup makes a microVM's cgroup and holds it to what the microVM
// declared: its memory with Firecracker's allowance, and its share of CPU.
fn create_cgroup(config: &Config, record: &Record) -> io::Result<()> {
    let dir = Path::new(CGROUP_ROOT).join(&record.cgroup);
    fs::create_dir(&dir).map_err(context(format!("creating the cgroup {}", dir.display())))?;
    let (memory, cpu) = (dir.join("memory.max"), dir.join("cpu.max"));
    fs::write(&memory, memory_max(record.memory, config.vmm_overhead))
        .map_err(context(format!("writing {}", memory.display())))?;
    fs::write(&cpu, cpu_max(record.cpu)).map_err(context(format!("writing {}", cpu.display())))
}

// remove_cgroup removes a microVM's cgroup once Firecracker has left it, doing
// nothing when it is already gone, so a release can be repeated.
fn remove_cgroup(record: &Record) -> io::Result<()> {
    match fs::remove_dir(Path::new(CGROUP_ROOT).join(&record.cgroup)) {
        Err(err) if err.kind() == io::ErrorKind::NotFound => Ok(()),
        result => result,
    }
}

// vcpus is the vCPU count for millicores: whole CPUs rounded up, then up to an
// even count above one, the only counts Firecracker accepts. The cgroup holds
// the microVM to the exact millicores.
fn vcpus(millicores: i64) -> i64 {
    let whole = (millicores + 999) / 1000;
    match whole {
        ..=1 => 1,
        n => n + n % 2,
    }
}

// cpu_max is the cgroup's cpu.max for millicores: that share of one CPU's time
// in each period, or no limit when the workload declared none.
fn cpu_max(millicores: i64) -> String {
    if millicores <= 0 {
        return format!("max {CPU_PERIOD}");
    }
    format!("{} {CPU_PERIOD}", millicores * CPU_PERIOD / 1000)
}

// memory_max is the cgroup's memory.max in bytes: the microVM's memory and the
// allowance for Firecracker itself, both in MiB.
fn memory_max(memory: i64, overhead: u64) -> String {
    ((memory as u64 + overhead) * 1024 * 1024).to_string()
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::network::Slot;

    // record is a microVM at 1500 millicores and 512 MiB in the pool's second
    // slot, as Submit saves it before launching.
    fn record(config: &Config) -> Record {
        Record {
            cpu: 1500,
            slot: Slot::free(config.pool, &[3], false).unwrap(),
            ..Record::sample()
        }
    }

    // A microVM's cgroup is the agent's with the execution under it, relative,
    // as the jailer refuses a path starting at the root.
    #[test]
    fn leaf_cgroup_is_relative_to_the_root() {
        let leaf = leaf_cgroup("/system.slice/vagabond-agent.service/", "exec-1");
        assert_eq!(leaf, "system.slice/vagabond-agent.service/exec-1");
    }

    // jailed maps a path Firecracker sees onto the jail's root, rather than
    // replacing the root with it.
    #[test]
    fn jailed_stays_under_the_jail_root() {
        let host = jailed(Path::new("/srv/jail/root"), "/vmlinux");
        assert_eq!(host, Path::new("/srv/jail/root/vmlinux"));
    }

    // A boot file on the jail's filesystem is hard linked, so the jail and the
    // flag's path share one file.
    #[test]
    fn link_or_copy_links_on_one_filesystem() {
        let dir = tempfile::tempdir().unwrap();
        let (from, to) = (dir.path().join("vmlinux"), dir.path().join("linked"));
        fs::write(&from, "kernel").unwrap();
        link_or_copy(&from, &to).unwrap();
        assert_eq!(
            fs::metadata(&to).unwrap().ino(),
            fs::metadata(&from).unwrap().ino()
        );
    }

    // A root drive that is no block device is refused as the request's fault,
    // after the boot files are in the jail.
    #[test]
    fn prepare_jail_refuses_a_drive_that_is_no_block_device() {
        let dir = tempfile::tempdir().unwrap();
        let mut config = Config::sample(dir.path());
        config.kernel = dir.path().join("vmlinux");
        config.initramfs = dir.path().join("initramfs");
        let drive = dir.path().join("rootfs.img");
        for file in [&config.kernel, &config.initramfs, &drive] {
            fs::write(file, "boot").unwrap();
        }
        let err = prepare_jail(&config, &record(&config), &drive, "ext4").unwrap_err();
        assert_eq!(err.kind(), io::ErrorKind::InvalidInput);
        let root = jail_dir(&config, "exec-1").join("root");
        assert!(root.join("vmlinux").exists());
        assert!(root.join("initramfs").exists());
    }

    // A process runs under its own start time only, as one that took a gone
    // process's pid would show another.
    #[test]
    fn running_needs_the_same_start_time() {
        let own = Firecracker::of(std::process::id() as i32).unwrap();
        assert!(own.running());
        assert!(
            !Firecracker {
                started: own.started + 1,
                ..own
            }
            .running()
        );
    }

    // remove kills a Firecracker that still runs, deletes the jail, and passes
    // over the tap and cgroup that were never made.
    #[tokio::test]
    async fn remove_stops_firecracker_and_deletes_the_jail() {
        let dir = tempfile::tempdir().unwrap();
        let config = Config::sample(dir.path());
        let mut stand_in = std::process::Command::new("sleep")
            .arg("30")
            .spawn()
            .unwrap();
        let firecracker = Firecracker::of(stand_in.id() as i32).unwrap();
        let mut record = record(&config);
        record.firecracker = Some(firecracker);
        let jail = jail_dir(&config, "exec-1");
        fs::create_dir_all(jail.join("root")).unwrap();

        remove(&config, &record).await.unwrap();

        assert!(!stand_in.wait().unwrap().success());
        assert!(!firecracker.running());
        assert!(!jail.exists());
    }

    // The configuration boots the kernel and initramfs inside the jail, names
    // the drive and the guest's network for vagabond-init and the kernel, and
    // sizes the microVM from its record.
    #[test]
    fn firecracker_config_boots_the_microvm_as_recorded() {
        let config = Config::sample(Path::new("/var/lib/vagabond-firecracker"));
        let fc = firecracker_config(&config, &record(&config), "ext4");
        assert_eq!(fc["boot-source"]["kernel_image_path"], "/vmlinux");
        assert_eq!(fc["boot-source"]["initrd_path"], "/initramfs");
        let boot_args = fc["boot-source"]["boot_args"].as_str().unwrap();
        let ending = "root=/dev/vda rootfstype=ext4 ip=172.16.0.6::172.16.0.5:255.255.255.252::eth0:off:1.1.1.1";
        assert!(boot_args.ends_with(ending), "{boot_args}");
        assert_eq!(fc["machine-config"]["vcpu_count"], 2);
        assert_eq!(fc["machine-config"]["mem_size_mib"], 512);
        assert_eq!(fc["network-interfaces"][0]["host_dev_name"], "vfc1");
        assert_eq!(fc["vsock"]["guest_cid"], 4);
    }

    // vCPUs round up to whole CPUs, then to an even count above one.
    #[test]
    fn vcpus_round_up_to_a_count_firecracker_accepts() {
        for (millicores, want) in [(0, 1), (500, 1), (1000, 1), (1500, 2), (2000, 2), (2500, 4)] {
            assert_eq!(vcpus(millicores), want, "{millicores} millicores");
        }
    }

    // cpu.max grants the declared share of each period, and no limit when none
    // was declared.
    #[test]
    fn cpu_max_grants_the_declared_share() {
        assert_eq!(cpu_max(1500), "150000 100000");
        assert_eq!(cpu_max(250), "25000 100000");
        assert_eq!(cpu_max(0), "max 100000");
    }

    // memory.max is the microVM's memory and Firecracker's allowance in bytes.
    #[test]
    fn memory_max_adds_the_allowance() {
        assert_eq!(memory_max(512, 32), (544 * 1024 * 1024).to_string());
    }
}
