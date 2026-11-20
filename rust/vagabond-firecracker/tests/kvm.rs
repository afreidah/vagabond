// -------------------------------------------------------------------------------
// MicroVM Tests
//
// Author: Alex Freidah
//
// Real microVMs booted by the real daemon: Firecracker, its jailer, a kernel and
// the guest init on the host's KVM. Needs root, so every test is ignored; make
// firecracker-test builds them as the user, runs them with sudo, and names in
// VAGABOND_KVM_DIR where it put Firecracker, the kernel, the initramfs and the
// root drive.
// -------------------------------------------------------------------------------

use std::fs::File;
use std::io;
use std::net::{SocketAddr, TcpStream};
use std::path::{Path, PathBuf};
use std::process::{Child, Command};
use std::time::Duration;

use nix::sched::{CloneFlags, setns};
use nix::sys::signal::{Signal, kill};
use nix::unistd::Pid;
use protobuf::MessageField;
use tempfile::TempDir;
use tokio::sync::MutexGuard;
use ttrpc::r#async::Client;
use vagabond_firecracker::network::docker_rules;
use vagabond_firecracker::proto::agent::{Empty, ExecutionRequest, LogsRequest, Resources};
use vagabond_firecracker::proto::executor::{ExecutorSubmitRequest, Process};
use vagabond_firecracker::proto::executor_ttrpc::ExecutorClient;
use vagabond_firecracker::record::Record;

// ONE_DAEMON lets one test run a daemon at a time: every daemon hands out the
// same vsock CIDs and tap names, and those are the host's, not the daemon's.
static ONE_DAEMON: tokio::sync::Mutex<()> = tokio::sync::Mutex::const_new(());

// kvm_dir is where make firecracker-test put what the tests boot, failing the
// test when it was run some other way.
fn kvm_dir() -> PathBuf {
    std::env::var_os("VAGABOND_KVM_DIR")
        .map(PathBuf::from)
        .expect("VAGABOND_KVM_DIR is set; run make firecracker-test")
}

// uplink is the interface the host's default route leaves through, which the
// microVMs reach the internet by: the route whose destination and mask are both
// zero.
fn uplink() -> String {
    let routes = std::fs::read_to_string("/proc/net/route").unwrap();
    routes
        .lines()
        .skip(1)
        .map(|line| line.split_whitespace().collect::<Vec<_>>())
        .find(|fields| fields.get(1) == Some(&"00000000") && fields.get(7) == Some(&"00000000"))
        .map(|fields| fields[0].to_string())
        .expect("the host has a default route")
}

// Drive is a root drive of the test's own, an image file on a loop device,
// since the daemon takes a block device as the agent hands it one. Dropping it
// detaches the device, then removes the image.
struct Drive {
    _dir: TempDir,
    device: PathBuf,
}

impl Drive {
    // new is a copy of the Alpine image, so the microVM can write to it.
    fn new() -> Drive {
        Drive::attach(|image| {
            std::fs::copy(kvm_dir().join("rootfs.ext4"), image).unwrap();
        })
    }

    // blank is 64 MiB of zeros, a drive no filesystem can be mounted from.
    fn blank() -> Drive {
        Drive::attach(|image| File::create(image).unwrap().set_len(64 << 20).unwrap())
    }

    // attach has make write the image in a directory of the drive's own, and
    // attaches it to the first free loop device.
    fn attach(make: impl FnOnce(&Path)) -> Drive {
        let dir = tempfile::tempdir().unwrap();
        let image = dir.path().join("rootfs.ext4");
        make(&image);
        let output = Command::new("losetup")
            .args(["--find", "--show"])
            .arg(&image)
            .output()
            .unwrap();
        assert!(
            output.status.success(),
            "losetup: {}",
            String::from_utf8_lossy(&output.stderr)
        );
        let device = PathBuf::from(String::from_utf8(output.stdout).unwrap().trim());
        Drive { _dir: dir, device }
    }
}

impl Drop for Drive {
    // drop detaches the loop device, before the copy behind it is removed with
    // the directory.
    fn drop(&mut self) {
        let _ = Command::new("losetup")
            .arg("--detach")
            .arg(&self.device)
            .status();
    }
}

// CGROUP is the parent the tests' microVMs run under, as the agent's own cgroup
// is on a node, relative to the cgroup root.
const CGROUP: &str = "vagabond-firecracker-test";

// Daemon is vagabond-firecracker as a node runs it, with a client to talk to it
// as the agent does. It holds ONE_DAEMON for the test, and keeps its socket,
// state and jails in directories of its own and the kernel it boots.
struct Daemon {
    _one: MutexGuard<'static, ()>,
    dir: TempDir,
    jails: TempDir,
    kernel: PathBuf,
    process: Child,
    client: ExecutorClient,
}

impl Daemon {
    // start starts the daemon booting the kernel make firecracker-test put
    // beside Firecracker.
    async fn start() -> Daemon {
        Daemon::booting(kvm_dir().join("vmlinux")).await
    }

    // booting makes the cgroup the microVMs run under, with the CPU and memory
    // controllers handed down to theirs, and starts the daemon booting kernel.
    // The jails are in /var/tmp, on disk where /tmp may forbid device files, and
    // with a path short enough for the vsock socket in each.
    async fn booting(kernel: PathBuf) -> Daemon {
        let one = ONE_DAEMON.lock().await;
        let cgroup = Path::new("/sys/fs/cgroup").join(CGROUP);
        match std::fs::create_dir(&cgroup) {
            Err(err) if err.kind() != io::ErrorKind::AlreadyExists => {
                panic!("creating {CGROUP}: {err}")
            }
            _ => {}
        }
        std::fs::write(cgroup.join("cgroup.subtree_control"), "+cpu +memory").unwrap();

        let dir = tempfile::tempdir().unwrap();
        let jails = tempfile::tempdir_in("/var/tmp").unwrap();
        let (process, client) = spawn(dir.path(), jails.path(), &kernel).await;
        Daemon {
            _one: one,
            dir,
            jails,
            kernel,
            process,
            client,
        }
    }

    // stop sends the daemon SIGTERM and waits for it to exit, as a node's
    // service manager stops it. Its microVMs outlive it.
    fn stop(&mut self) {
        let _ = kill(Pid::from_raw(self.process.id() as i32), Signal::SIGTERM);
        let _ = self.process.wait();
    }

    // restart stops the daemon and starts another on the same socket, state and
    // jails, as a node does on an upgrade, its microVMs running on between.
    async fn restart(&mut self) {
        self.stop();
        (self.process, self.client) = spawn(self.dir.path(), self.jails.path(), &self.kernel).await;
    }

    // record is what the daemon saved about an execution: its slot, cgroup and
    // Firecracker.
    fn record(&self, id: &str) -> Record {
        Record::load(&self.dir.path().join("state"), id).unwrap()
    }

    // stopped waits for the execution's Firecracker to have exited, taking the
    // process from the record the daemon saved for it.
    async fn stopped(&self, id: &str) {
        let firecracker = self
            .record(id)
            .firecracker
            .expect("the microVM was launched");
        firecracker
            .stopped()
            .await
            .unwrap_or_else(|err| panic!("{id}: {err}"));
    }
}

impl Drop for Daemon {
    // drop stops the daemon and clears away what a failed test left of its
    // microVMs, so the next test starts clean: their Firecrackers, killed
    // through the cgroup they all run under, their cgroups, their taps, and the
    // daemon's nftables table and Docker rules.
    fn drop(&mut self) {
        self.stop();
        let cgroup = Path::new("/sys/fs/cgroup").join(CGROUP);
        let _ = std::fs::write(cgroup.join("cgroup.kill"), "1");
        cleared(&cgroup);
        remove_cgroups(&cgroup);
        if let Ok(links) = std::fs::read_dir("/sys/class/net") {
            for name in links.flatten().map(|link| link.file_name()) {
                if name.to_string_lossy().starts_with("vfc") {
                    let _ = Command::new("ip").args(["link", "del"]).arg(&name).status();
                }
            }
        }
        let _ = Command::new("nft")
            .args(["delete", "table", "ip", "vagabond_firecracker"])
            .status();
        for rule in docker_rules(&uplink()) {
            let _ = Command::new("iptables")
                .args(["-w", "-D", "DOCKER-USER"])
                .args(rule)
                .status();
        }
    }
}

// cleared waits up to five seconds for every process under cgroup to have
// exited, since cgroup.kill signals them without waiting.
fn cleared(cgroup: &Path) {
    for _ in 0..50 {
        match std::fs::read_to_string(cgroup.join("cgroup.events")) {
            Ok(events) if events.contains("populated 1") => {
                std::thread::sleep(Duration::from_millis(100))
            }
            _ => return,
        }
    }
}

// remove_cgroups removes cgroup and every cgroup under it, the deepest first,
// since a cgroup with children cannot be removed.
fn remove_cgroups(cgroup: &Path) {
    if let Ok(entries) = std::fs::read_dir(cgroup) {
        for entry in entries.flatten() {
            if entry.file_type().is_ok_and(|kind| kind.is_dir()) {
                remove_cgroups(&entry.path());
            }
        }
    }
    let _ = std::fs::remove_dir(cgroup);
}

// OUTSIDE is the network namespace that stands in for another machine.
const OUTSIDE: &str = "vfc-test-outside";

// OPEN are the DOCKER-USER rules that let everything to and from microVMs past
// Docker's forward drop, so the daemon's own rules are all that stop it.
const OPEN: [[&str; 4]; 2] = [
    ["-i", "vfc+", "-j", "ACCEPT"],
    ["-o", "vfc+", "-j", "ACCEPT"],
];

// Outside is another machine as far as the host's firewall can tell: a network
// namespace at the far end of a veth pair, routed through the host. While it
// stands, Docker's forward drop is opened to the microVMs.
struct Outside;

impl Outside {
    // new makes the namespace, with its end of the pair at 198.18.0.2 and the
    // host's at 198.18.0.1, from the range set aside for network tests, and
    // opens Docker's drop where the host has one.
    fn new() -> Outside {
        let setup = [
            format!("netns add {OUTSIDE}"),
            format!("link add vfo0 type veth peer name vfo1 netns {OUTSIDE}"),
            "addr add 198.18.0.1/30 dev vfo0".into(),
            "link set vfo0 up".into(),
            format!("-n {OUTSIDE} addr add 198.18.0.2/30 dev vfo1"),
            format!("-n {OUTSIDE} link set vfo1 up"),
            format!("-n {OUTSIDE} route add default via 198.18.0.1"),
        ];
        for args in setup {
            let status = Command::new("ip")
                .args(args.split_whitespace())
                .status()
                .unwrap();
            assert!(status.success(), "ip {args}");
        }
        for rule in OPEN {
            let _ = Command::new("iptables")
                .args(["-w", "-I", "DOCKER-USER"])
                .args(rule)
                .status();
        }
        Outside
    }

    // connect tries a TCP connection to address from the namespace, on a thread
    // of its own, since a thread joins a network namespace by itself.
    fn connect(&self, address: SocketAddr) -> io::Result<TcpStream> {
        std::thread::spawn(move || {
            setns(
                File::open(format!("/run/netns/{OUTSIDE}"))?,
                CloneFlags::CLONE_NEWNET,
            )?;
            TcpStream::connect_timeout(&address, Duration::from_secs(3))
        })
        .join()
        .unwrap()
    }
}

impl Drop for Outside {
    // drop deletes the namespace, which takes the veth pair with it, and closes
    // Docker's drop again.
    fn drop(&mut self) {
        let _ = Command::new("ip").args(["netns", "del", OUTSIDE]).status();
        for rule in OPEN {
            let _ = Command::new("iptables")
                .args(["-w", "-D", "DOCKER-USER"])
                .args(rule)
                .status();
        }
    }
}

// context gives a call a minute, room for a Submit to boot the microVM and wait
// for its guest to answer.
fn context() -> ttrpc::context::Context {
    ttrpc::context::with_duration(Duration::from_secs(60))
}

// workload is a Submit of args on the drive, with a core, 128 MiB and a minute
// to run, under the tests' cgroup.
fn workload(id: &str, drive: &Drive, args: &[&str]) -> ExecutorSubmitRequest {
    ExecutorSubmitRequest {
        execution_id: id.into(),
        process: MessageField::some(Process {
            args: args.iter().map(ToString::to_string).collect(),
            ..Default::default()
        }),
        resources: MessageField::some(Resources {
            cpu: 1000,
            memory: 128,
            ..Default::default()
        }),
        timeout: MessageField::some(Duration::from_secs(60).into()),
        root_drive: drive.device.display().to_string(),
        root_fstype: "ext4".into(),
        cgroup: CGROUP.into(),
        ..Default::default()
    }
}

// execution is the request naming one execution, which Status, Result, Cancel
// and Release take.
fn execution(id: &str) -> ExecutionRequest {
    ExecutionRequest {
        execution_id: id.into(),
        ..Default::default()
    }
}

// output reads the execution's whole output as the agent does, from the offset
// it has, until the daemon says the workload has ended with all of it read.
async fn output(client: &ExecutorClient, id: &str) -> String {
    let mut data = Vec::new();
    for _ in 0..300 {
        let request = LogsRequest {
            execution_id: id.into(),
            offset: data.len() as i64,
            ..Default::default()
        };
        let output = client.read_logs(context(), &request).await.unwrap();
        data.extend_from_slice(&output.data);
        if output.ended {
            return String::from_utf8(data).unwrap();
        }
        if output.data.is_empty() {
            tokio::time::sleep(Duration::from_millis(100)).await;
        }
    }
    panic!("{id} did not end in 30 seconds");
}

// listening waits up to ten seconds for address to take a TCP connection from
// the host.
async fn listening(address: SocketAddr) {
    for _ in 0..100 {
        if tokio::net::TcpStream::connect(address).await.is_ok() {
            return;
        }
        tokio::time::sleep(Duration::from_millis(100)).await;
    }
    panic!("nothing listens on {address}");
}

// A workload runs as the user and groups the agent resolved from its image.
#[tokio::test]
#[ignore = "boots microVMs, which needs root and /dev/kvm; run make firecracker-test"]
async fn a_workload_runs_as_its_images_user() {
    let daemon = Daemon::start().await;
    let drive = Drive::new();
    let mut run = workload("user", &drive, &["/bin/sh", "-c", "id -u; id -g; id -G"]);
    let process = run.process.mut_or_insert_default();
    (process.uid, process.gid, process.additional_gids) = (1000, 1000, vec![1001, 1002]);
    daemon.client.submit(context(), &run).await.unwrap();

    assert_eq!(
        output(&daemon.client, "user").await,
        "1000\n1000\n1000 1001 1002\n"
    );
}

// A working directory the image lacks is made, parents and all, owned by root,
// and the workload starts in it.
#[tokio::test]
#[ignore = "boots microVMs, which needs root and /dev/kvm; run make firecracker-test"]
async fn a_missing_working_directory_is_made() {
    let daemon = Daemon::start().await;
    let drive = Drive::new();
    let mut run = workload(
        "workdir",
        &drive,
        &["/bin/sh", "-c", "pwd; stat -c '%u %a' ."],
    );
    run.process.mut_or_insert_default().working_dir = "/work/space".into();
    daemon.client.submit(context(), &run).await.unwrap();

    assert_eq!(
        output(&daemon.client, "workdir").await,
        "/work/space\n0 755\n"
    );
}

// A command runs in a microVM booted from the root drive, and its output and
// exit code come back as the agent reads them.
#[tokio::test]
#[ignore = "boots microVMs, which needs root and /dev/kvm; run make firecracker-test"]
async fn a_command_runs_in_a_microvm() {
    let daemon = Daemon::start().await;
    let drive = Drive::new();
    let run = workload("runs", &drive, &["/bin/sh", "-c", "echo hello; exit 3"]);
    daemon.client.submit(context(), &run).await.unwrap();

    assert_eq!(output(&daemon.client, "runs").await, "hello\n");
    let result = daemon
        .client
        .result(context(), &execution("runs"))
        .await
        .unwrap();
    assert_eq!(result.exit_code, Some(3));
    let status = daemon
        .client
        .status(context(), &execution("runs"))
        .await
        .unwrap();
    assert_eq!(status.state, "failed");
    daemon
        .client
        .release(context(), &execution("runs"))
        .await
        .unwrap();
}

// A process the workload leaves running in a session of its own is killed when
// the workload exits, so the run still ends, with the workload's own code.
#[tokio::test]
#[ignore = "boots microVMs, which needs root and /dev/kvm; run make firecracker-test"]
async fn a_leftover_process_ends_with_the_workload() {
    let daemon = Daemon::start().await;
    let drive = Drive::new();
    let run = workload(
        "leftover",
        &drive,
        &["/bin/sh", "-c", "setsid sleep 300 & sleep 1; echo hi"],
    );
    daemon.client.submit(context(), &run).await.unwrap();

    assert_eq!(output(&daemon.client, "leftover").await, "hi\n");
    let result = daemon
        .client
        .result(context(), &execution("leftover"))
        .await
        .unwrap();
    assert_eq!(result.exit_code, Some(0));
    daemon
        .client
        .release(context(), &execution("leftover"))
        .await
        .unwrap();
}

// Cancel stops the workload, which ends cancelled with the exit SIGTERM gives
// it, and the microVM stops with it.
#[tokio::test]
#[ignore = "boots microVMs, which needs root and /dev/kvm; run make firecracker-test"]
async fn cancel_stops_the_microvm() {
    let daemon = Daemon::start().await;
    let drive = Drive::new();
    let run = workload("cancels", &drive, &["/bin/sleep", "300"]);
    daemon.client.submit(context(), &run).await.unwrap();
    daemon
        .client
        .cancel(context(), &execution("cancels"))
        .await
        .unwrap();

    assert_eq!(output(&daemon.client, "cancels").await, "");
    let result = daemon
        .client
        .result(context(), &execution("cancels"))
        .await
        .unwrap();
    assert_eq!(result.exit_code, Some(143));
    let status = daemon
        .client
        .status(context(), &execution("cancels"))
        .await
        .unwrap();
    assert_eq!(status.state, "cancelled");
    daemon.stopped("cancels").await;
    daemon
        .client
        .release(context(), &execution("cancels"))
        .await
        .unwrap();
}

// Release of a running microVM leaves nothing of it behind: no Firecracker, tap
// device, cgroup or jail, and no saved record or output.
#[tokio::test]
#[ignore = "boots microVMs, which needs root and /dev/kvm; run make firecracker-test"]
async fn release_leaves_nothing_behind() {
    let daemon = Daemon::start().await;
    let drive = Drive::new();
    let run = workload("releases", &drive, &["/bin/sleep", "300"]);
    daemon.client.submit(context(), &run).await.unwrap();
    let record = daemon.record("releases");

    daemon
        .client
        .release(context(), &execution("releases"))
        .await
        .unwrap();

    assert!(
        !record
            .firecracker
            .expect("the microVM was launched")
            .running()
    );
    let made = [
        Path::new("/sys/class/net").join(&record.slot.tap),
        Path::new("/sys/fs/cgroup").join(&record.cgroup),
        daemon.jails.path().join("firecracker/releases"),
        daemon.dir.path().join("state/releases"),
    ];
    for path in made {
        assert!(!path.exists(), "{} is left behind", path.display());
    }
}

// A restarted daemon takes back the microVMs it left: Held still lists them, one
// that ended before still reports its result, and one still running is followed
// to its end.
#[tokio::test]
#[ignore = "boots microVMs, which needs root and /dev/kvm; run make firecracker-test"]
async fn a_restarted_daemon_takes_back_its_microvms() {
    let mut daemon = Daemon::start().await;
    let (first, second) = (Drive::new(), Drive::new());
    let early = workload("ends-before", &first, &["/bin/sh", "-c", "echo early"]);
    let late = workload(
        "runs-across",
        &second,
        &["/bin/sh", "-c", "sleep 5; echo late; exit 4"],
    );
    daemon.client.submit(context(), &early).await.unwrap();
    daemon.client.submit(context(), &late).await.unwrap();
    assert_eq!(output(&daemon.client, "ends-before").await, "early\n");

    daemon.restart().await;

    let mut held = daemon
        .client
        .held(context(), &Empty::default())
        .await
        .unwrap()
        .held;
    held.sort_by(|a, b| a.execution_id.cmp(&b.execution_id));
    let listed: Vec<_> = held
        .iter()
        .map(|held| (held.execution_id.as_str(), held.running))
        .collect();
    assert_eq!(listed, [("ends-before", false), ("runs-across", true)]);
    assert_eq!(output(&daemon.client, "runs-across").await, "late\n");
    for (id, code) in [("ends-before", 0), ("runs-across", 4)] {
        let result = daemon
            .client
            .result(context(), &execution(id))
            .await
            .unwrap();
        assert_eq!(result.exit_code, Some(code), "{id}");
        daemon
            .client
            .release(context(), &execution(id))
            .await
            .unwrap();
    }
}

// A microVM's memory counts against the cgroup it was given: Firecracker runs in
// a cgroup under it capped at the declared memory and the VMM's allowance, and
// what the guest fills is charged there.
#[tokio::test]
#[ignore = "boots microVMs, which needs root and /dev/kvm; run make firecracker-test"]
async fn memory_counts_against_the_cgroup() {
    let daemon = Daemon::start().await;
    let drive = Drive::new();
    let fill = "dd if=/dev/zero of=/dev/shm/fill bs=1M count=32 && sleep 300";
    daemon
        .client
        .submit(
            context(),
            &workload("fills", &drive, &["/bin/sh", "-c", fill]),
        )
        .await
        .unwrap();
    let record = daemon.record("fills");
    let leaf = Path::new("/sys/fs/cgroup").join(&record.cgroup);

    let max = std::fs::read_to_string(leaf.join("memory.max")).unwrap();
    assert_eq!(max.trim(), ((128 + 32) << 20).to_string());
    let pid = record.firecracker.expect("the microVM was launched").pid;
    // The cgroup v2 line is the one numbered 0, beside any v1 hierarchy's.
    let membership = std::fs::read_to_string(format!("/proc/{pid}/cgroup")).unwrap();
    let under = format!("0::/{}", record.cgroup);
    assert!(
        membership.lines().any(|line| line.starts_with(&under)),
        "Firecracker is in {membership}"
    );

    let given = Path::new("/sys/fs/cgroup")
        .join(CGROUP)
        .join("memory.current");
    let charged = || {
        std::fs::read_to_string(&given)
            .unwrap()
            .trim()
            .parse::<u64>()
            .unwrap()
    };
    for _ in 0..100 {
        if charged() >= 32 << 20 {
            break;
        }
        tokio::time::sleep(Duration::from_millis(100)).await;
    }
    assert!(
        charged() >= 32 << 20,
        "{CGROUP} is charged {} bytes",
        charged()
    );
    daemon
        .client
        .release(context(), &execution("fills"))
        .await
        .unwrap();
}

// A microVM reaches the internet: it resolves a name through the name server
// the kernel's autoconfiguration gave it, and its traffic leaves through the
// NAT, past Docker's forward drop where the host has one.
#[tokio::test]
#[ignore = "boots microVMs, which needs root and /dev/kvm; run make firecracker-test"]
async fn a_microvm_reaches_the_internet() {
    let daemon = Daemon::start().await;
    let drive = Drive::new();
    let fetch = workload(
        "fetches",
        &drive,
        &[
            "/usr/bin/wget",
            "-q",
            "-O",
            "/dev/null",
            "http://example.com/",
        ],
    );
    daemon.client.submit(context(), &fetch).await.unwrap();

    let printed = output(&daemon.client, "fetches").await;
    let result = daemon
        .client
        .result(context(), &execution("fetches"))
        .await
        .unwrap();
    assert_eq!(result.exit_code, Some(0), "wget printed: {printed}");
    daemon
        .client
        .release(context(), &execution("fetches"))
        .await
        .unwrap();
}

// Nothing outside a microVM reaches it, past whatever Docker drops: not another
// microVM, not another machine routed through the host, and the microVM reaches
// none of the host. Each refusal has a control that the same path otherwise
// connects, and the host's own processes still reach the microVM.
#[tokio::test]
#[ignore = "boots microVMs, which needs root and /dev/kvm; run make firecracker-test"]
async fn nothing_outside_reaches_a_microvm() {
    let daemon = Daemon::start().await;
    let (first, second) = (Drive::new(), Drive::new());
    let listen = workload(
        "listens",
        &first,
        &["/usr/bin/nc", "-lk", "-p", "8080", "-e", "/bin/true"],
    );
    daemon.client.submit(context(), &listen).await.unwrap();
    let listens = SocketAddr::from((daemon.record("listens").slot.guest(), 8080));
    listening(listens).await;

    let outside = Outside::new();
    let host = std::net::TcpListener::bind("0.0.0.0:0").unwrap();
    let port = host.local_addr().unwrap().port();
    assert!(
        outside
            .connect(SocketAddr::from(([198, 18, 0, 1], port)))
            .is_ok(),
        "the namespace reaches no one"
    );

    let probe = format!(
        "nc -z -w 3 example.com 80 || echo cannot-connect
         nc -z -w 3 {} 8080 && echo reached-vm
         nc -z -w 3 $(ip route | awk '/default/ {{print $3}}') {port} && echo reached-host
         echo probed",
        listens.ip()
    );
    daemon
        .client
        .submit(
            context(),
            &workload("probes", &second, &["/bin/sh", "-c", &probe]),
        )
        .await
        .unwrap();
    // Asserted together, so a failure shows every way through at once.
    let printed = output(&daemon.client, "probes").await;
    let reached_from_outside = outside.connect(listens).is_ok();
    assert_eq!(
        (printed.as_str(), reached_from_outside),
        ("probed\n", false)
    );

    for id in ["listens", "probes"] {
        daemon
            .client
            .release(context(), &execution(id))
            .await
            .unwrap();
    }
}

// A microVM whose Firecracker cannot boot fails its Submit at once with what
// Firecracker said, and is forgotten, so it can be submitted again.
#[tokio::test]
#[ignore = "boots microVMs, which needs root and /dev/kvm; run make firecracker-test"]
async fn a_microvm_that_cannot_boot_fails_its_submit() {
    // The initramfs is no kernel, so Firecracker exits loading it.
    let daemon = Daemon::booting(kvm_dir().join("initramfs.cpio.gz")).await;
    let drive = Drive::new();
    let run = workload("unbootable", &drive, &["/bin/true"]);
    let submitted = std::time::Instant::now();

    let refused = daemon.client.submit(context(), &run).await.unwrap_err();
    assert!(
        submitted.elapsed() < Duration::from_secs(10),
        "took {:?}",
        submitted.elapsed()
    );
    assert!(
        refused
            .to_string()
            .contains("Firecracker exited while booting"),
        "{refused}"
    );
    let unknown = daemon
        .client
        .status(context(), &execution("unbootable"))
        .await
        .unwrap_err();
    let forgotten = matches!(&unknown, ttrpc::Error::RpcStatus(status) if status.code() == ttrpc::Code::NOT_FOUND);
    assert!(forgotten, "{unknown:?}");
}

// A microVM whose root drive cannot be mounted fails its Submit at once with
// what vagabond-init said, the step and the device named.
#[tokio::test]
#[ignore = "boots microVMs, which needs root and /dev/kvm; run make firecracker-test"]
async fn a_root_drive_that_cannot_mount_fails_its_submit() {
    let daemon = Daemon::start().await;
    let drive = Drive::blank();
    let run = workload("unmountable", &drive, &["/bin/true"]);

    let refused = daemon.client.submit(context(), &run).await.unwrap_err();
    let said = "vagabond-init: mounting /dev/vda at /mnt/root as ext4";
    assert!(refused.to_string().contains(said), "{refused}");
}

// spawn starts the daemon booting kernel, with its socket and state in dir and
// its jails in jails, and connects once it answers, failing the test when it
// exits first or does not answer in ten seconds.
async fn spawn(dir: &Path, jails: &Path, kernel: &Path) -> (Child, ExecutorClient) {
    let kvm = kvm_dir();
    let socket = dir.join("executor.sock");
    let mut process = Command::new(env!("CARGO_BIN_EXE_vagabond-firecracker"))
        .arg("--socket")
        .arg(&socket)
        .arg("--state-dir")
        .arg(dir.join("state"))
        .arg("--kernel")
        .arg(kernel)
        .arg("--initramfs")
        .arg(kvm.join("initramfs.cpio.gz"))
        .arg("--firecracker")
        .arg(kvm.join("firecracker"))
        .arg("--jailer")
        .arg(kvm.join("jailer"))
        .arg("--chroot-base")
        .arg(jails)
        .args(["--uid", "65534", "--gid", "65534", "--uplink", &uplink()])
        .spawn()
        .unwrap();

    let address = format!("unix://{}", socket.display());
    for _ in 0..100 {
        if let Some(status) = process.try_wait().unwrap() {
            panic!("vagabond-firecracker exited before answering: {status}");
        }
        if let Ok(client) = Client::connect(&address).await {
            return (process, ExecutorClient::new(client));
        }
        tokio::time::sleep(Duration::from_millis(100)).await;
    }
    let _ = process.kill();
    panic!("vagabond-firecracker did not answer on {address}");
}
