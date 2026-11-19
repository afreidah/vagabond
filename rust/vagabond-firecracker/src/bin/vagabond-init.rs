// -------------------------------------------------------------------------------
// vagabond-init
//
// Author: Alex Freidah
//
// PID 1 inside each microVM. Mounts the kernel's filesystems and the image's
// root drive, runs the workload inside the image's filesystem, and serves the
// guest protocol to vagabond-firecracker over vsock until told to shut down.
// -------------------------------------------------------------------------------

use std::os::unix::process::CommandExt;
use std::process::{Command, ExitCode, Stdio};
use std::sync::{Arc, Mutex, OnceLock};

use async_trait::async_trait;
use nix::mount::{MsFlags, mount};
use nix::sys::reboot::{RebootMode, reboot};
use nix::sys::signal::{Signal, kill};
use nix::sys::termios::tcdrain;
use nix::sys::wait::{WaitPidFlag, WaitStatus, waitpid};
use nix::unistd::{Gid, Pid, Uid, chdir, chroot, setgid, setgroups, setuid, sync};
use tokio::io::AsyncReadExt;
use tokio::net::unix::pipe;
use tokio::signal::unix::{SignalKind, signal};
use tokio::sync::watch;
use ttrpc::r#async::{Server, TtrpcContext};
use vagabond_firecracker::proto::agent::Empty;
use vagabond_firecracker::proto::executor::{Output, Process};
use vagabond_firecracker::proto::guest::{Exit, ReadRequest, SignalRequest};
use vagabond_firecracker::proto::guest_ttrpc::{Guest, create_guest};
use vagabond_firecracker::{GUEST_PORT, INIT_FAILED, context};

// mount_kernel_filesystems mounts processes, system information, devices and
// shared memory, creating each mount point first, since an initramfs holds only
// what was packed into it.
fn mount_kernel_filesystems() -> std::io::Result<()> {
    let hardened = MsFlags::MS_NOSUID | MsFlags::MS_NODEV | MsFlags::MS_NOEXEC;
    let mounts = [
        ("proc", "/proc", hardened),
        ("sysfs", "/sys", hardened),
        ("devtmpfs", "/dev", MsFlags::MS_NOSUID),
        ("tmpfs", "/dev/shm", hardened),
    ];
    for (fstype, target, flags) in mounts {
        let mounted = std::fs::create_dir_all(target).and_then(|()| {
            Ok(mount(
                Some(fstype),
                target,
                Some(fstype),
                flags,
                None::<&str>,
            )?)
        });
        mounted.map_err(context(format!("mounting {fstype} at {target}")))?;
    }
    Ok(())
}

// NEW_ROOT is where the image's root drive is mounted before init switches
// into it.
const NEW_ROOT: &str = "/mnt/root";

// kernel_parameter returns the value of key=value on the kernel command line,
// which the daemon sets when it boots the microVM.
fn kernel_parameter<'a>(cmdline: &'a str, key: &str) -> std::io::Result<&'a str> {
    cmdline
        .split_whitespace()
        .find_map(|word| word.strip_prefix(key)?.strip_prefix('='))
        .ok_or_else(|| std::io::Error::other(format!("no {key}= on the kernel command line")))
}

// mount_root_drive mounts the image's root drive at NEW_ROOT: the device and
// filesystem named by root= and rootfstype= on the kernel command line.
fn mount_root_drive() -> std::io::Result<()> {
    let cmdline =
        std::fs::read_to_string("/proc/cmdline").map_err(context("reading /proc/cmdline"))?;
    let device = kernel_parameter(&cmdline, "root")?;
    let fstype = kernel_parameter(&cmdline, "rootfstype")?;
    let mounted = std::fs::create_dir_all(NEW_ROOT).and_then(|()| {
        Ok(mount(
            Some(device),
            NEW_ROOT,
            Some(fstype),
            MsFlags::empty(),
            None::<&str>,
        )?)
    });
    mounted.map_err(context(format!(
        "mounting {device} at {NEW_ROOT} as {fstype}"
    )))
}

// switch_root makes NEW_ROOT the root of the filesystem, carrying the kernel
// filesystems into it, so init and the workload it starts see only the image.
fn switch_root() -> std::io::Result<()> {
    for target in ["/proc", "/sys", "/dev"] {
        let moved = format!("{NEW_ROOT}{target}");
        std::fs::create_dir_all(&moved)?;
        mount(
            Some(target),
            moved.as_str(),
            None::<&str>,
            MsFlags::MS_MOVE,
            None::<&str>,
        )?;
    }
    chdir(NEW_ROOT)?;
    mount(Some("."), "/", None::<&str>, MsFlags::MS_MOVE, None::<&str>)?;
    // The image now sits at /, and the working directory still points at it.
    chroot(".")?;
    chdir("/")?;
    Ok(())
}

// write_resolv_conf gives the workload the nameservers the daemon set in ip=,
// which the kernel publishes in /proc/net/pnp as lines a resolver reads.
fn write_resolv_conf() -> std::io::Result<()> {
    let nameservers = std::fs::read("/proc/net/pnp").map_err(context("reading /proc/net/pnp"))?;
    std::fs::create_dir_all("/etc")?;
    // Renaming replaces the image's own resolv.conf even when it is a symlink
    // into /run, which writing through would follow.
    std::fs::write("/etc/resolv.conf.vagabond", nameservers)?;
    std::fs::rename("/etc/resolv.conf.vagabond", "/etc/resolv.conf")
}

// GuestService serves the guest protocol for the one workload a microVM runs.
struct GuestService {
    // pid is the workload's once started. The reaper holds this lock while it
    // collects exits, so the workload cannot exit before its pid is recorded.
    pid: Mutex<Option<Pid>>,
    // output is the read end of the pipe the workload's stdout and stderr share.
    output: OnceLock<tokio::sync::Mutex<pipe::Receiver>>,
    // exit is the workload's exit status once the reaper has collected it.
    exit: watch::Sender<Option<i32>>,
}

impl GuestService {
    // new is a service with no workload started and no exit, ready for the
    // reaper and the first Start.
    fn new() -> Self {
        Self {
            pid: Mutex::new(None),
            output: OnceLock::new(),
            exit: watch::Sender::new(None),
        }
    }

    // reap collects every process that exits, as PID 1 must: processes whose
    // parent has exited become init's, and an uncollected exit stays a zombie.
    async fn reap(&self) -> std::io::Result<()> {
        let mut exited = signal(SignalKind::child())?;
        loop {
            self.collect();
            exited.recv().await;
        }
    }

    // collect takes every exit waiting and publishes the workload's, as its
    // code or 128 plus the signal that killed it, under the pid lock so a
    // workload that exits right after starting still matches its pid.
    fn collect(&self) {
        let pid = self.pid.lock().expect("pid lock poisoned");
        // One SIGCHLD can stand for several exits, so take them until none is left.
        loop {
            let (exited, code) = match waitpid(None::<Pid>, Some(WaitPidFlag::WNOHANG)) {
                Ok(WaitStatus::Exited(exited, code)) => (exited, code),
                Ok(WaitStatus::Signaled(exited, killed_by, _)) => (exited, 128 + killed_by as i32),
                _ => return,
            };
            if Some(exited) == *pid {
                self.exit.send_replace(Some(code));
            }
        }
    }
}

// spawn starts the workload with only its own environment, stdout and stderr
// sharing one pipe, as the image's user, and returns its pid and the pipe's
// read end. The groups are set before the group, and the group before the
// user, as each needs the root the next gives up.
fn spawn(process: &Process) -> std::io::Result<(Pid, pipe::Receiver)> {
    let (program, args) = process.args.split_first().ok_or_else(|| {
        std::io::Error::new(std::io::ErrorKind::InvalidInput, "no program to run")
    })?;
    let (sender, receiver) = pipe::pipe()?;
    let output = sender.into_blocking_fd()?;
    let mut command = Command::new(program);
    command
        .args(args)
        .env_clear()
        .envs(&process.env)
        .stdin(Stdio::null())
        .stdout(output.try_clone()?)
        .stderr(output);

    // A working directory the image lacks is made, owned by root and 0755 under
    // init's umask, so the task starts in it as it would in a container.
    if !process.working_dir.is_empty() {
        std::fs::create_dir_all(&process.working_dir)
            .map_err(context(format!("creating {}", process.working_dir)))?;
        command.current_dir(&process.working_dir);
    }

    // Only root can take on another user, as init always is inside a microVM.
    if Uid::effective().is_root() {
        let (uid, gid) = (Uid::from_raw(process.uid), Gid::from_raw(process.gid));
        let groups: Vec<Gid> = process
            .additional_gids
            .iter()
            .map(|&group| Gid::from_raw(group))
            .collect();

        // SAFETY: between fork and exec the closure makes only the setgroups,
        // setgid and setuid system calls, which are async-signal-safe, on values
        // made before the fork, so it allocates and locks nothing.
        unsafe {
            command.pre_exec(move || {
                setgroups(&groups)?;
                setgid(gid)?;
                setuid(uid)?;
                Ok(())
            });
        }
    }
    let child = command.spawn()?;
    Ok((Pid::from_raw(child.id() as i32), receiver))
}

// internal turns a failure inside init into the status the daemon receives,
// so the daemon can tell it from a dropped connection.
fn internal(err: impl ToString) -> ttrpc::Error {
    ttrpc::Error::RpcStatus(ttrpc::get_status(ttrpc::Code::INTERNAL, err))
}

// invalid_argument is the status for a request init cannot act on, so the
// daemon knows the mistake is in what it sent.
fn invalid_argument(err: impl ToString) -> ttrpc::Error {
    ttrpc::Error::RpcStatus(ttrpc::get_status(ttrpc::Code::INVALID_ARGUMENT, err))
}

// not_started is the status for a call that needs the workload before Start
// has run it.
fn not_started() -> ttrpc::Error {
    let message = "the workload has not started";
    ttrpc::Error::RpcStatus(ttrpc::get_status(ttrpc::Code::FAILED_PRECONDITION, message))
}

// MAX_READ caps one Read at what a pipe holds by default, which also keeps a
// large max from allocating that much inside the guest.
const MAX_READ: usize = 64 * 1024;

#[async_trait]
impl Guest for GuestService {
    // start runs the workload, once: a second call starts nothing, so a daemon
    // retrying after a lost reply does not run it twice.
    async fn start(&self, _ctx: &TtrpcContext, process: Process) -> ttrpc::Result<Empty> {
        let mut pid = self.pid.lock().expect("pid lock poisoned");
        if pid.is_none() {
            let (started, output) = spawn(&process).map_err(|err| match err.kind() {
                // An empty command, or one with a zero byte, is the request's fault.
                std::io::ErrorKind::InvalidInput => invalid_argument(err),
                _ => internal(err),
            })?;
            *pid = Some(started);
            // Set only here, under the pid lock, so it cannot already be set.
            let _ = self.output.set(tokio::sync::Mutex::new(output));
        }
        Ok(Empty::new())
    }

    // read returns the next output the workload wrote, up to max bytes (0 for
    // MAX_READ), waiting until there is some; ended once every writer closed.
    async fn read(&self, _ctx: &TtrpcContext, request: ReadRequest) -> ttrpc::Result<Output> {
        let output = self.output.get().ok_or_else(not_started)?;
        let size = match request.max as usize {
            0 => MAX_READ,
            max => max.min(MAX_READ),
        };
        let mut buf = vec![0; size];
        let n = output.lock().await.read(&mut buf).await.map_err(internal)?;
        buf.truncate(n);
        Ok(Output {
            data: buf,
            ended: n == 0,
            ..Default::default()
        })
    }

    // wait returns the workload's exit status, waiting until the reaper has
    // collected it.
    async fn wait(&self, _ctx: &TtrpcContext, _: Empty) -> ttrpc::Result<Exit> {
        if self.output.get().is_none() {
            return Err(not_started());
        }
        let mut exit = self.exit.subscribe();
        let code = exit
            .wait_for(Option::is_some)
            .await
            .map_err(internal)?
            .unwrap_or_default();
        Ok(Exit {
            code,
            ..Default::default()
        })
    }

    // signal sends the requested signal to the workload while it runs; once it
    // has exited, there is nothing to signal.
    async fn signal(&self, _ctx: &TtrpcContext, request: SignalRequest) -> ttrpc::Result<Empty> {
        let requested = Signal::try_from(request.signal).map_err(invalid_argument)?;
        // The pid lock keeps the reaper from collecting the exit, which frees the
        // pid for reuse, between the check and the kill.
        let pid = self.pid.lock().expect("pid lock poisoned");
        let workload = pid.ok_or_else(not_started)?;
        if self.exit.borrow().is_none() {
            kill(workload, requested).map_err(internal)?;
        }
        Ok(Empty::new())
    }

    // shutdown flushes the root drive's writes and restarts the microVM, which
    // ends Firecracker, so the call never returns and the daemon sees the
    // connection close.
    async fn shutdown(&self, _ctx: &TtrpcContext, _: Empty) -> ttrpc::Result<Empty> {
        sync();
        let Err(err) = reboot(RebootMode::RB_AUTOBOOT);
        Err(internal(err))
    }
}

// main runs init and returns only when setup fails, saying why on the console
// in one line the daemon looks for. PID 1 exiting panics the kernel, which ends
// the microVM.
#[tokio::main]
async fn main() -> ExitCode {
    if let Err(err) = run().await {
        eprintln!("{INIT_FAILED}{err}");
        // The console is still sending the line when the write returns, and the
        // kernel's panic would cut into it.
        let _ = tcdrain(std::io::stderr());
    }
    ExitCode::FAILURE
}

// run sets up the microVM, serves the guest protocol over vsock and reaps
// exited processes for as long as it runs.
async fn run() -> std::io::Result<()> {
    mount_kernel_filesystems()?;
    mount_root_drive()?;
    switch_root().map_err(context(format!("switching root to {NEW_ROOT}")))?;
    write_resolv_conf()?;
    let service = Arc::new(GuestService::new());
    // A CID of -1 accepts connections at whatever address Firecracker gave the guest.
    let mut server = Server::new()
        .bind(&format!("vsock://-1:{GUEST_PORT}"))
        .map_err(std::io::Error::other)
        .map_err(context(format!("serving vsock port {GUEST_PORT}")))?
        .register_service(create_guest(service.clone()));
    server.start().await.map_err(std::io::Error::other)?;
    service.reap().await
}

#[cfg(test)]
mod tests {
    use super::*;
    use tempfile::TempDir;
    use vagabond_firecracker::proto::guest_ttrpc::GuestClient;

    // SERVING is taken by every test that serves the guest protocol: its reaper
    // collects any child of the test process, so two at once could take each
    // other's exits.
    static SERVING: tokio::sync::Mutex<()> = tokio::sync::Mutex::const_new(());

    // Served is a service under test with a client for it. Dropping it stops the
    // server, removes its socket's directory and lets the next test serve.
    struct Served {
        client: GuestClient,
        _server: Server,
        _dir: TempDir,
        _serving: tokio::sync::MutexGuard<'static, ()>,
    }

    // serve starts a fresh service and its reaper on a Unix socket in a new
    // temporary directory and connects a client to it, as the daemon does over
    // vsock.
    async fn serve() -> Served {
        let serving = SERVING.lock().await;
        let dir = tempfile::tempdir().unwrap();
        let address = format!("unix://{}/guest.sock", dir.path().display());
        let service = Arc::new(GuestService::new());
        let mut server = Server::new()
            .bind(&address)
            .unwrap()
            .register_service(create_guest(service.clone()));
        server.start().await.unwrap();
        tokio::spawn(async move { service.reap().await });
        let client = ttrpc::r#async::Client::connect(&address).await.unwrap();
        Served {
            client: GuestClient::new(client),
            _server: server,
            _dir: dir,
            _serving: serving,
        }
    }

    // context gives each call a deadline, so a test that would hang fails
    // instead.
    fn context() -> ttrpc::context::Context {
        ttrpc::context::with_duration(std::time::Duration::from_secs(10))
    }

    // read_all reads the workload's output until Read reports it ended, as the
    // daemon copies it to the host.
    async fn read_all(client: &GuestClient) -> Vec<u8> {
        let mut all = Vec::new();
        loop {
            let output = client.read(context(), &ReadRequest::new()).await.unwrap();
            all.extend(output.data);
            if output.ended {
                return all;
            }
        }
    }

    // start runs script under /bin/sh as the workload. The path is absolute
    // because the workload's environment, PATH included, is only its own.
    async fn start(served: &Served, script: &str) {
        let process = Process {
            args: vec!["/bin/sh".into(), "-c".into(), script.into()],
            ..Default::default()
        };
        served.client.start(context(), &process).await.unwrap();
    }

    // code is the status a call failed with, failing the test when the call
    // succeeded or failed without one.
    fn code<T: std::fmt::Debug>(result: ttrpc::Result<T>) -> ttrpc::Code {
        match result.unwrap_err() {
            ttrpc::Error::RpcStatus(status) => status.code(),
            other => panic!("not a status: {other:?}"),
        }
    }

    // kernel_parameter matches the whole key, so a lookup for root skips the
    // rootfstype= before it.
    #[test]
    fn kernel_parameter_matches_whole_key() {
        let cmdline = "console=ttyS0 rootfstype=ext4 root=/dev/vda\n";
        assert_eq!(kernel_parameter(cmdline, "root").unwrap(), "/dev/vda");
        assert_eq!(kernel_parameter(cmdline, "rootfstype").unwrap(), "ext4");
    }

    // kernel_parameter fails, naming the key, when the command line lacks it,
    // so a misconfigured boot says what is missing.
    #[test]
    fn kernel_parameter_reports_missing_key() {
        let err = kernel_parameter("console=ttyS0\n", "root").unwrap_err();
        assert_eq!(err.to_string(), "no root= on the kernel command line");
    }

    // A started workload's output reads back until it ends, and Wait returns
    // the code the program exited with.
    #[tokio::test]
    async fn start_runs_the_workload_to_its_exit() {
        let served = serve().await;
        start(&served, "echo hi; exit 3").await;
        assert_eq!(read_all(&served.client).await, b"hi\n");
        let exit = served.client.wait(context(), &Empty::new()).await.unwrap();
        assert_eq!(exit.code, 3);
    }

    // Signal reaches a running workload, one killed by a signal exits with 128
    // plus its number, and a signal after the exit sends nothing.
    #[tokio::test]
    async fn signal_kills_the_workload() {
        let served = serve().await;
        start(&served, "exec sleep 30").await;
        let kill = SignalRequest {
            signal: Signal::SIGKILL as i32,
            ..Default::default()
        };
        served.client.signal(context(), &kill).await.unwrap();
        let exit = served.client.wait(context(), &Empty::new()).await.unwrap();
        assert_eq!(exit.code, 137);
        served.client.signal(context(), &kill).await.unwrap();
    }

    // A second Start runs nothing, so a daemon retrying Start cannot run the
    // workload twice.
    #[tokio::test]
    async fn second_start_runs_nothing() {
        let served = serve().await;
        start(&served, "echo once").await;
        start(&served, "echo twice").await;
        assert_eq!(read_all(&served.client).await, b"once\n");
    }

    // Read, Wait and Signal fail as a precondition before Start, since there is
    // no workload to act on.
    #[tokio::test]
    async fn calls_before_start_fail() {
        let served = serve().await;
        let client = &served.client;
        let precondition = ttrpc::Code::FAILED_PRECONDITION;
        assert_eq!(
            code(client.read(context(), &ReadRequest::new()).await),
            precondition
        );
        assert_eq!(
            code(client.wait(context(), &Empty::new()).await),
            precondition
        );
        let term = SignalRequest {
            signal: Signal::SIGTERM as i32,
            ..Default::default()
        };
        assert_eq!(code(client.signal(context(), &term).await), precondition);
    }

    // A number that is no signal, and a Start with nothing to run, are refused
    // as the request's fault.
    #[tokio::test]
    async fn bad_requests_are_invalid_arguments() {
        let served = serve().await;
        let client = &served.client;
        let invalid = ttrpc::Code::INVALID_ARGUMENT;
        let bogus = SignalRequest {
            signal: 999,
            ..Default::default()
        };
        assert_eq!(code(client.signal(context(), &bogus).await), invalid);
        assert_eq!(
            code(client.start(context(), &Process::new()).await),
            invalid
        );
    }
}
