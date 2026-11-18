// -------------------------------------------------------------------------------
// Executor
//
// Author: Alex Freidah
//
// Serves the executor protocol to the agent, each execution's workload in its
// own microVM. Everything about an execution is in its record, saved as it
// changes, so a restarted daemon answers for every microVM it started.
// -------------------------------------------------------------------------------

use std::collections::HashMap;
use std::io;
use std::os::unix::net::SocketAddr;
use std::path::{Path, PathBuf};
use std::sync::{Arc, Mutex};
use std::time::{Duration, SystemTime};

use async_trait::async_trait;
use nix::sys::signal::Signal;
use tokio::io::{AsyncReadExt, AsyncSeekExt, AsyncWriteExt};
use tracing::{info, warn};
use ttrpc::Code;
use ttrpc::r#async::TtrpcContext;

use crate::INIT_FAILED;
use crate::config::Config;
use crate::guest;
use crate::network::Slot;
use crate::proto::agent::{
    Empty, ExecutionRequest, ExecutionResult, ExecutionStatus, LogsRequest, Submission,
};
use crate::proto::executor::{ExecutorSubmitRequest, HeldList, Output};
use crate::proto::executor_ttrpc::Executor as ExecutorService;
use crate::proto::guest::{ReadRequest, SignalRequest};
use crate::proto::guest_ttrpc::GuestClient;
use crate::record::Record;
use crate::vm::{self, leaf_cgroup};

// MAX_ID is the longest execution ID the jailer accepts.
const MAX_ID: usize = 64;

// BOOT_PATIENCE is how long a new microVM's guest may take to answer, and to
// start the workload.
const BOOT_PATIENCE: Duration = Duration::from_secs(30);

// GUEST_CALL_WAIT is how long a guest gets with Signal, and with Shutdown,
// which it answers by powering off.
const GUEST_CALL_WAIT: Duration = Duration::from_secs(10);

// STOP_GRACE is how long a stopped workload gets between SIGTERM and SIGKILL.
const STOP_GRACE: Duration = Duration::from_secs(10);

// MAX_LOG_READ caps how much output one ReadLogs returns.
const MAX_LOG_READ: u64 = 64 * 1024;

// RECONNECT_PATIENCE is how long a recovered microVM's guest, booted long ago,
// gets to answer before it is taken for dead.
const RECONNECT_PATIENCE: Duration = Duration::from_secs(5);

// Executor runs the agent's executions in microVMs. A clone shares the same
// state, so each microVM's supervisor task holds its own handle to it.
#[derive(Clone)]
pub struct Executor {
    config: Arc<Config>,
    // records is every execution's record as saved. Its lock is also held while
    // a Submit picks a slot, so two cannot take the same one.
    records: Arc<Mutex<HashMap<String, Record>>>,
    // guests is the connection to each supervised microVM's guest, which a
    // workload is stopped through.
    guests: Arc<Mutex<HashMap<String, GuestClient>>>,
    // grace is how long a stopped workload gets to exit before it is killed.
    grace: Duration,
}

impl Executor {
    // new is an executor over the records saved under the state directory,
    // which it creates on a node's first start.
    pub fn new(config: Config) -> io::Result<Executor> {
        std::fs::create_dir_all(&config.state_dir).map_err(crate::context(format!(
            "creating {}",
            config.state_dir.display()
        )))?;
        let records = Record::all(&config.state_dir)?
            .into_iter()
            .map(|record| (record.execution_id.clone(), record))
            .collect();
        Ok(Executor {
            config: Arc::new(config),
            records: Arc::new(Mutex::new(records)),
            guests: Arc::default(),
            grace: STOP_GRACE,
        })
    }

    // update changes an execution's record and saves it under the lock, keeping
    // the change only once it is saved, so memory and disk always agree.
    fn update(&self, execution_id: &str, change: impl FnOnce(&mut Record)) -> io::Result<()> {
        let mut records = self.records.lock().expect("records lock poisoned");
        let record = records
            .get_mut(execution_id)
            .ok_or(io::ErrorKind::NotFound)?;
        let mut changed = record.clone();
        change(&mut changed);
        changed.save(&self.config.state_dir)?;
        *record = changed;
        Ok(())
    }

    // recover takes back the executions a previous daemon left unfinished,
    // once at start. A running workload's supervisor and timeout resume; one
    // that never started, whose Firecracker is gone, or whose guest does not
    // answer has its microVM removed and is ended without an exit, so it
    // reports failed and Release can clear it.
    pub async fn recover(&self) -> io::Result<()> {
        let unfinished: Vec<Record> = {
            let records = self.records.lock().expect("records lock poisoned");
            records
                .values()
                .filter(|record| record.ended.is_none())
                .cloned()
                .collect()
        };
        for record in unfinished {
            let id = record.execution_id.clone();
            if let Some(guest) = self.reattach(&record).await {
                info!(execution = %id, "recovered");
                self.supervise(id, guest);
                continue;
            }
            warn!(execution = %id, "lost the microVM");
            vm::remove(&self.config, &record)
                .await
                .inspect_err(|err| warn!(execution = %id, error = %err, "removing the microVM"))?;
            self.update(&id, |record| record.ended = Some(SystemTime::now()))?;
        }
        Ok(())
    }

    // reattach reconnects to a started workload's guest, or gives None when it
    // never started, its Firecracker is gone, or the guest does not answer.
    async fn reattach(&self, record: &Record) -> Option<GuestClient> {
        let (Some(_), Some(firecracker)) = (record.started, record.firecracker) else {
            return None;
        };
        if !firecracker.running() {
            return None;
        }
        let vsock = vm::vsock_path(&self.config, &record.execution_id);
        guest::connect(&vsock, RECONNECT_PATIENCE)
            .await
            .inspect_err(|err| {
                warn!(execution = %record.execution_id, error = %err, "reconnecting to the guest")
            })
            .ok()
    }

    // admit takes a checked Submit's execution under the lock. One already held
    // is left as it stands, the ID being the idempotency key, and gives None. A
    // new one gets the lowest free slot and a record, saved before anything is
    // made, so a crash part way leaves a record for Release to clean up after.
    fn admit(&self, request: &ExecutorSubmitRequest) -> io::Result<Option<Record>> {
        let mut records = self.records.lock().expect("records lock poisoned");
        if records.contains_key(&request.execution_id) {
            return Ok(None);
        }
        // The guest is reached through a Unix socket in the jail, and the kernel
        // caps how long its path may be.
        let vsock = vm::vsock_path(&self.config, &request.execution_id);
        if SocketAddr::from_pathname(&vsock).is_err() {
            let message = format!(
                "{} is too long for a Unix socket; shorten --chroot-base",
                vsock.display()
            );
            return Err(io::Error::new(io::ErrorKind::InvalidInput, message));
        }
        let used: Vec<u32> = records.values().map(|record| record.slot.cid).collect();
        let slot = Slot::free(self.config.pool, &used).ok_or_else(|| {
            io::Error::new(io::ErrorKind::QuotaExceeded, "the address pool is used up")
        })?;
        let timeout = &request.timeout;
        let record = Record {
            execution_id: request.execution_id.clone(),
            cpu: request.resources.cpu,
            memory: request.resources.memory,
            timeout: Duration::new(timeout.seconds as u64, timeout.nanos as u32),
            slot,
            cgroup: leaf_cgroup(&request.cgroup, &request.execution_id),
            firecracker: None,
            started: None,
            ended: None,
            exit_code: None,
            cancelled: false,
        };
        record.save(&self.config.state_dir)?;
        records.insert(record.execution_id.clone(), record.clone());
        Ok(Some(record))
    }

    // boot launches an admitted execution's microVM and starts its workload,
    // returning the guest's connection for its supervisor. A failure removes
    // what was made and forgets the execution, so it can be submitted again;
    // when the removal fails, the record stays for Release to finish it.
    async fn boot(
        &self,
        record: &Record,
        request: &ExecutorSubmitRequest,
    ) -> io::Result<GuestClient> {
        let id = &record.execution_id;
        let started = self.start(record, request).await;
        if let Err(err) = &started {
            warn!(execution = %id, error = %err, "starting the microVM");
            // The record as it stands, so a Firecracker already launched is stopped.
            let current = self.record(id).unwrap_or_else(|| record.clone());
            match vm::remove(&self.config, &current).await {
                Ok(()) => {
                    if let Err(err) = self.forget(id) {
                        warn!(execution = %id, error = %err, "forgetting the execution");
                    }
                }
                Err(err) => warn!(execution = %id, error = %err, "removing the microVM"),
            }
        }
        started
    }

    // start launches the microVM, connects to its guest and starts the
    // workload, saving Firecracker's process and then the start as each
    // happens.
    async fn start(
        &self,
        record: &Record,
        request: &ExecutorSubmitRequest,
    ) -> io::Result<GuestClient> {
        let id = &record.execution_id;
        let drive = Path::new(&request.root_drive);
        let firecracker = vm::launch(Arc::clone(&self.config), record, drive).await?;
        self.update(id, |record| record.firecracker = Some(firecracker))?;
        let vsock = vm::vsock_path(&self.config, id);
        let guest = tokio::select! {
            connected = guest::connect(&vsock, BOOT_PATIENCE) => connected?,
            () = firecracker.exited() => return Err(self.boot_failure(id)),
        };
        let context = ttrpc::context::with_duration(BOOT_PATIENCE);
        guest
            .start(context, &request.process)
            .await
            .map_err(io::Error::other)?;
        self.update(id, |record| record.started = Some(SystemTime::now()))?;
        info!(execution = %id, cid = record.slot.cid, tap = %record.slot.tap, "started");
        Ok(guest)
    }

    // boot_failure is the error for a Firecracker that exited while booting,
    // carrying the last lines of its console, where the jailer and Firecracker
    // say why, as the console goes with the execution once it is forgotten.
    fn boot_failure(&self, id: &str) -> io::Error {
        let console = std::fs::read(vm::console_path(&self.config, id)).unwrap_or_default();
        let console = String::from_utf8_lossy(&console);
        let lines: Vec<&str> = console
            .lines()
            .filter(|line| !line.trim().is_empty())
            .collect();
        // vagabond-init says in one line why it could not set the microVM up;
        // without one, the reason is the jailer's or Firecracker's, at the end.
        let init = lines
            .iter()
            .rev()
            .find_map(|line| line.find(INIT_FAILED).map(|at| &line[at..]));
        let said = match init {
            Some(line) => line.to_string(),
            None => lines[lines.len().saturating_sub(3)..].join("; "),
        };
        io::Error::other(format!("Firecracker exited while booting: {said}"))
    }

    // supervise follows a started workload to its end in a task of its own: its
    // output copied to its log, its end saved with the exit code, unset when the
    // guest's connection broke as the microVM died, and the microVM shut down.
    // A workload released while it runs ends with nothing saved.
    fn supervise(&self, id: String, guest: GuestClient) {
        if let Some(record) = self.record(&id) {
            self.watch(&record);
        }
        self.guests
            .lock()
            .expect("guests lock poisoned")
            .insert(id.clone(), guest.clone());
        let executor = self.clone();
        tokio::spawn(async move {
            let followed = executor.follow(&id, &guest).await;
            // Let go of the guest first, so nothing seeing the end can signal it.
            // One already let go of was released, its microVM removed under the
            // supervisor, with nothing left to save or shut down.
            let held = executor
                .guests
                .lock()
                .expect("guests lock poisoned")
                .remove(&id);
            if held.is_none() {
                return;
            }
            let code = followed
                .inspect_err(|err| warn!(execution = %id, error = %err, "following the workload"))
                .ok();
            info!(execution = %id, exit_code = code, "ended");
            let saved = executor.update(&id, |record| {
                record.ended = Some(SystemTime::now());
                record.exit_code = code;
            });
            if let Err(err) = saved {
                warn!(execution = %id, error = %err, "saving the end");
            }
            // The guest powers off rather than answer.
            let context = ttrpc::context::with_duration(GUEST_CALL_WAIT);
            let _ = guest.shutdown(context, &Empty::new()).await;
        });
    }

    // stop ends a supervised workload: SIGTERM through its guest, then, from a
    // task of its own, SIGKILL once the grace has passed, unless it has exited
    // by then. A workload no longer supervised has nothing left to stop.
    async fn stop(&self, id: &str) -> io::Result<()> {
        let Some(guest) = self.guest(id) else {
            return Ok(());
        };
        signal(&guest, Signal::SIGTERM).await?;
        let (executor, id) = (self.clone(), id.to_string());
        tokio::spawn(async move {
            tokio::time::sleep(executor.grace).await;
            if let Some(guest) = executor.guest(&id) {
                info!(execution = %id, "killing the workload");
                if let Err(err) = signal(&guest, Signal::SIGKILL).await {
                    warn!(execution = %id, error = %err, "killing the workload");
                }
            }
        });
        Ok(())
    }

    // watch stops a workload that runs past its timeout, measured from its
    // saved start, so a restarted daemon keeps the same deadline. A workload
    // without a timeout is not watched.
    fn watch(&self, record: &Record) {
        let Some(started) = record.started else {
            return;
        };
        if record.timeout.is_zero() {
            return;
        }
        let left = record
            .timeout
            .saturating_sub(started.elapsed().unwrap_or_default());
        let (executor, id) = (self.clone(), record.execution_id.clone());
        tokio::spawn(async move {
            tokio::time::sleep(left).await;
            // A workload that ended in time has no supervisor left.
            if executor.guest(&id).is_none() {
                return;
            }
            info!(execution = %id, "timed out");
            if let Err(err) = executor.stop(&id).await {
                warn!(execution = %id, error = %err, "stopping the workload");
            }
        });
    }

    // guest is the connection to a supervised microVM's guest, or None once its
    // supervisor has let go of it.
    fn guest(&self, id: &str) -> Option<GuestClient> {
        self.guests
            .lock()
            .expect("guests lock poisoned")
            .get(id)
            .cloned()
    }

    // follow copies the workload's output to its log, each chunk written before
    // the next is read, appending so a restarted daemon carries on where the log
    // ends, then waits for the exit code.
    async fn follow(&self, id: &str, guest: &GuestClient) -> io::Result<i32> {
        let mut log = tokio::fs::OpenOptions::new()
            .create(true)
            .append(true)
            .open(self.output_path(id))
            .await?;
        loop {
            let context = ttrpc::context::with_timeout(0);
            let output = guest
                .read(context, &ReadRequest::new())
                .await
                .map_err(io::Error::other)?;
            log.write_all(&output.data).await?;
            if output.ended {
                break;
            }
        }
        let context = ttrpc::context::with_timeout(0);
        Ok(guest
            .wait(context, &Empty::new())
            .await
            .map_err(io::Error::other)?
            .code)
    }

    // output_path is where an execution's output is kept.
    fn output_path(&self, id: &str) -> PathBuf {
        self.config.state_dir.join(id).join("output.log")
    }

    // record is a copy of an execution's record as it stands, or None when the
    // executor holds none.
    fn record(&self, execution_id: &str) -> Option<Record> {
        self.records
            .lock()
            .expect("records lock poisoned")
            .get(execution_id)
            .cloned()
    }

    // forget drops an execution's record and deletes its directory, output and
    // console with it.
    fn forget(&self, execution_id: &str) -> io::Result<()> {
        self.records
            .lock()
            .expect("records lock poisoned")
            .remove(execution_id);
        match std::fs::remove_dir_all(self.config.state_dir.join(execution_id)) {
            Err(err) if err.kind() == io::ErrorKind::NotFound => Ok(()),
            result => result,
        }
    }
}

#[async_trait]
impl ExecutorService for Executor {
    // submit runs a new execution's workload in a microVM and returns once it
    // has started. An execution already held is reported as it stands.
    async fn submit(
        &self,
        _ctx: &TtrpcContext,
        request: ExecutorSubmitRequest,
    ) -> ttrpc::Result<Submission> {
        check(&request).map_err(|message| refusal(Code::INVALID_ARGUMENT, message))?;
        if let Some(record) = self.admit(&request).map_err(failure)? {
            let guest = self.boot(&record, &request).await.map_err(failure)?;
            self.supervise(record.execution_id.clone(), guest);
        }
        let record = self.known(&request.execution_id)?;
        Ok(Submission {
            state: record.state().into(),
            provider_id: record.execution_id,
            ..Default::default()
        })
    }

    // status reports where an execution stands.
    async fn status(
        &self,
        _ctx: &TtrpcContext,
        request: ExecutionRequest,
    ) -> ttrpc::Result<ExecutionStatus> {
        Ok(self.known(&request.execution_id)?.status())
    }

    // result reports how a finished execution's workload ended, and refuses one
    // that has not finished yet.
    async fn result(
        &self,
        _ctx: &TtrpcContext,
        request: ExecutionRequest,
    ) -> ttrpc::Result<ExecutionResult> {
        let record = self.known(&request.execution_id)?;
        if record.ended.is_none() {
            let message = format!("execution {} has not finished", record.execution_id);
            return Err(refusal(Code::FAILED_PRECONDITION, message));
        }
        Ok(record.result())
    }

    // cancel stops an execution's workload, which then reports cancelled. One
    // that has already ended is marked all the same, with nothing left to stop.
    async fn cancel(&self, _ctx: &TtrpcContext, request: ExecutionRequest) -> ttrpc::Result<Empty> {
        let id = &request.execution_id;
        self.known(id)?;
        info!(execution = %id, "cancelling");
        self.update(id, |record| record.cancelled = true)
            .map_err(failure)?;
        self.stop(id).await.map_err(failure)?;
        Ok(Empty::new())
    }

    // release removes an execution's microVM and everything kept for it, a
    // running one stopped with it. An execution not held is released already.
    async fn release(
        &self,
        _ctx: &TtrpcContext,
        request: ExecutionRequest,
    ) -> ttrpc::Result<Empty> {
        let Some(record) = self.record(&request.execution_id) else {
            return Ok(Empty::new());
        };
        let id = &record.execution_id;
        // Its supervisor, finding the guest let go of, ends quietly.
        self.guests.lock().expect("guests lock poisoned").remove(id);
        vm::remove(&self.config, &record)
            .await
            .inspect_err(|err| warn!(execution = %id, error = %err, "removing the microVM"))
            .map_err(failure)?;
        self.forget(id).map_err(failure)?;
        info!(execution = %id, "released");
        Ok(Empty::new())
    }

    // read_logs returns an execution's output from an offset, whatever is there
    // now up to MAX_LOG_READ, ended once the workload has ended and all of it
    // has been read.
    async fn read_logs(&self, _ctx: &TtrpcContext, request: LogsRequest) -> ttrpc::Result<Output> {
        let record = self.known(&request.execution_id)?;
        let offset = u64::try_from(request.offset)
            .map_err(|_| refusal(Code::INVALID_ARGUMENT, "an offset cannot be negative"))?;
        let path = self.output_path(&record.execution_id);
        let (data, size) = read_from(&path, offset).await.map_err(failure)?;
        // The supervisor saves the end after its last write, so an ended log is whole.
        let ended = record.ended.is_some() && offset + data.len() as u64 >= size;
        Ok(Output {
            data,
            ended,
            ..Default::default()
        })
    }

    // held lists every execution the executor holds, with what it declared and
    // whether it still runs.
    async fn held(&self, _ctx: &TtrpcContext, _: Empty) -> ttrpc::Result<HeldList> {
        let records = self.records.lock().expect("records lock poisoned");
        let held = records.values().map(Record::held).collect();
        Ok(HeldList {
            held,
            ..Default::default()
        })
    }
}

impl Executor {
    // known is an execution's record, or the status for one the executor does
    // not hold, the one place an execution is reported unknown.
    fn known(&self, id: &str) -> ttrpc::Result<Record> {
        self.record(id)
            .ok_or_else(|| refusal(Code::NOT_FOUND, format!("no execution {id}")))
    }
}

// refusal is the status a call fails with.
fn refusal(code: Code, message: impl ToString) -> ttrpc::Error {
    ttrpc::Error::RpcStatus(ttrpc::get_status(code, message))
}

// failure is the status for an error from below the protocol: the request's
// fault, a used-up pool, or else the daemon's own trouble. A missing file is
// the daemon's trouble too, as only known reports an execution unknown.
fn failure(err: io::Error) -> ttrpc::Error {
    let code = match err.kind() {
        io::ErrorKind::InvalidInput => Code::INVALID_ARGUMENT,
        io::ErrorKind::QuotaExceeded => Code::RESOURCE_EXHAUSTED,
        _ => Code::INTERNAL,
    };
    refusal(code, err)
}

// read_from reads up to MAX_LOG_READ of a log from offset, with the log's size.
// A log not written yet is empty.
async fn read_from(path: &Path, offset: u64) -> io::Result<(Vec<u8>, u64)> {
    let mut file = match tokio::fs::File::open(path).await {
        Ok(file) => file,
        Err(err) if err.kind() == io::ErrorKind::NotFound => return Ok((Vec::new(), 0)),
        Err(err) => return Err(err),
    };
    let size = file.metadata().await?.len();
    file.seek(io::SeekFrom::Start(offset)).await?;
    let mut data = Vec::new();
    file.take(MAX_LOG_READ).read_to_end(&mut data).await?;
    Ok((data, size))
}

// signal sends a signal to a workload through its guest.
async fn signal(guest: &GuestClient, sent: Signal) -> io::Result<()> {
    let request = SignalRequest {
        signal: sent as i32,
        ..Default::default()
    };
    let context = ttrpc::context::with_duration(GUEST_CALL_WAIT);
    guest
        .signal(context, &request)
        .await
        .map(|_| ())
        .map_err(io::Error::other)
}

// check refuses a Submit the daemon cannot act on, before anything is made: an
// ID the jailer would refuse, which, as it names directories, also keeps a path
// out of it; no memory to size the microVM by; a negative timeout; nothing to
// run; or no drive or cgroup.
fn check(request: &ExecutorSubmitRequest) -> Result<(), String> {
    let id = &request.execution_id;
    let allowed = |c: char| c.is_ascii_alphanumeric() || c == '-';
    if id.is_empty() || id.len() > MAX_ID || !id.chars().all(allowed) {
        return Err(format!(
            "execution ID {id:?} is not 1 to {MAX_ID} letters, digits and hyphens"
        ));
    }
    if request.resources.memory <= 0 {
        return Err("a microVM needs its memory declared".into());
    }
    if request.timeout.seconds < 0 || request.timeout.nanos < 0 {
        return Err("a timeout cannot be negative".into());
    }
    if request.process.args.is_empty() {
        return Err("no program to run".into());
    }
    if request.root_drive.is_empty() || request.cgroup.is_empty() {
        return Err("a root drive and a cgroup are both needed".into());
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::proto::agent::Resources;
    use crate::proto::executor::{Output, Process};
    use crate::proto::executor_ttrpc::{ExecutorClient, create_executor};
    use crate::proto::guest::Exit;
    use crate::proto::guest_ttrpc::{Guest, create_guest};
    use clap::Parser;
    use protobuf::MessageField;
    use std::sync::atomic::{AtomicBool, Ordering};
    use ttrpc::r#async::{Server, Service};

    // Workload is a test guest whose workload printed hello and exited with
    // code 3: its output reads once, then ends.
    #[derive(Default)]
    struct Workload {
        read: AtomicBool,
    }

    #[async_trait]
    impl Guest for Workload {
        // read returns the output the first time and its end after.
        async fn read(&self, _ctx: &TtrpcContext, _: ReadRequest) -> ttrpc::Result<Output> {
            let ended = self.read.swap(true, Ordering::SeqCst);
            let data = if ended {
                Vec::new()
            } else {
                b"hello\n".to_vec()
            };
            Ok(Output {
                data,
                ended,
                ..Default::default()
            })
        }

        // wait reports the exit at once.
        async fn wait(&self, _ctx: &TtrpcContext, _: Empty) -> ttrpc::Result<Exit> {
            Ok(Exit {
                code: 3,
                ..Default::default()
            })
        }
    }

    // Running is a test guest whose workload is running: it keeps the signals
    // it is sent, in a list the test holds too.
    struct Running {
        signals: Arc<Mutex<Vec<i32>>>,
    }

    #[async_trait]
    impl Guest for Running {
        // signal keeps the signal it was sent.
        async fn signal(
            &self,
            _ctx: &TtrpcContext,
            request: SignalRequest,
        ) -> ttrpc::Result<Empty> {
            self.signals.lock().unwrap().push(request.signal);
            Ok(Empty::new())
        }
    }

    // serve serves services on a Unix socket named name under dir and connects a
    // client to it, as the agent reaches the executor and the daemon a guest.
    // The server serves until it is dropped.
    async fn serve(
        dir: &Path,
        name: &str,
        services: HashMap<String, Service>,
    ) -> (ttrpc::r#async::Client, Server) {
        let address = format!("unix://{}", dir.join(name).display());
        let mut server = Server::new()
            .bind(&address)
            .unwrap()
            .register_service(services);
        server.start().await.unwrap();
        (
            ttrpc::r#async::Client::connect(&address).await.unwrap(),
            server,
        )
    }

    // serve_guest serves a test guest, as the daemon reaches one over vsock.
    async fn serve_guest(dir: &Path, guest: impl Guest + Send + 'static) -> (GuestClient, Server) {
        let (client, server) = serve(dir, "guest.sock", create_guest(Arc::new(guest))).await;
        (GuestClient::new(client), server)
    }

    // serve_executor serves the executor, as the agent reaches it.
    async fn serve_executor(dir: &Path, executor: Executor) -> (ExecutorClient, Server) {
        let (client, server) =
            serve(dir, "executor.sock", create_executor(Arc::new(executor))).await;
        (ExecutorClient::new(client), server)
    }

    // context gives each call a deadline, so a test that would hang fails
    // instead.
    fn context() -> ttrpc::context::Context {
        ttrpc::context::with_duration(Duration::from_secs(5))
    }

    // code is the status a call failed with, failing the test when the call
    // succeeded or failed without one.
    fn code<T: std::fmt::Debug>(result: ttrpc::Result<T>) -> Code {
        match result.unwrap_err() {
            ttrpc::Error::RpcStatus(status) => status.code(),
            other => panic!("not a status: {other:?}"),
        }
    }

    // finished admits an execution and gives it a start, an end with exit code
    // 0 and its output, as its supervisor would have, returning its ID.
    fn finished(executor: &Executor, output: &[u8]) -> String {
        let id = executor.admit(&request()).unwrap().unwrap().execution_id;
        let at = SystemTime::now();
        executor
            .update(&id, |record| {
                record.started = Some(at);
                record.ended = Some(at);
                record.exit_code = Some(0);
            })
            .unwrap();
        std::fs::write(executor.output_path(&id), output).unwrap();
        id
    }

    // ended waits for an execution's record to show its end, as its supervisor
    // saves it, failing the test after two seconds.
    async fn ended(executor: &Executor, id: &str) -> Record {
        for _ in 0..100 {
            if let Some(record) = executor.record(id)
                && record.ended.is_some()
            {
                return record;
            }
            tokio::time::sleep(Duration::from_millis(20)).await;
        }
        panic!("{id} did not end");
    }

    // config is the daemon's flags with only the required ones given, the state
    // directory at state_dir and the jails under it.
    fn config(state_dir: &Path) -> Config {
        let jails = state_dir.join("jails");
        let (state_dir, jails) = (state_dir.to_str().unwrap(), jails.to_str().unwrap());
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
            state_dir,
            "--chroot-base",
            jails,
        ])
    }

    // A new executor holds every record saved before it, and update changes a
    // record in memory and on disk alike.
    #[test]
    fn update_changes_the_saved_record() {
        let dir = tempfile::tempdir().unwrap();
        Record::sample().save(dir.path()).unwrap();
        let executor = Executor::new(config(dir.path())).unwrap();

        executor
            .update("exec-1", |record| record.cancelled = true)
            .unwrap();

        assert!(executor.records.lock().unwrap()["exec-1"].cancelled);
        assert!(Record::load(dir.path(), "exec-1").unwrap().cancelled);
    }

    // A new execution gets the first slot and a record of what it asked for,
    // saved before anything is made.
    #[test]
    fn admit_records_a_new_execution() {
        let dir = tempfile::tempdir().unwrap();
        let executor = Executor::new(config(dir.path())).unwrap();
        let request = request();

        let record = executor.admit(&request).unwrap().unwrap();

        assert_eq!(record.slot.cid, 3);
        assert_eq!((record.cpu, record.memory), (500, 256));
        let cgroup = format!(
            "system.slice/vagabond-agent.service/{}",
            request.execution_id
        );
        assert_eq!(record.cgroup, cgroup);
        assert_eq!(
            Record::load(dir.path(), &request.execution_id).unwrap(),
            record
        );
    }

    // An execution already held is left as it stands, so a retried Submit
    // makes nothing new.
    #[test]
    fn admit_leaves_a_held_execution() {
        let dir = tempfile::tempdir().unwrap();
        let executor = Executor::new(config(dir.path())).unwrap();
        executor.admit(&request()).unwrap();
        assert_eq!(executor.admit(&request()).unwrap(), None);
        assert_eq!(executor.records.lock().unwrap().len(), 1);
    }

    // With every /30 of the pool held, a new execution is refused as over
    // quota.
    #[test]
    fn admit_refuses_when_the_pool_is_used_up() {
        let dir = tempfile::tempdir().unwrap();
        let mut config = config(dir.path());
        config.pool = "172.16.0.0/30".parse().unwrap();
        let executor = Executor::new(config).unwrap();
        executor.admit(&request()).unwrap();

        let mut second = request();
        second.execution_id = "exec-2".into();
        let err = executor.admit(&second).unwrap_err();
        assert_eq!(err.kind(), io::ErrorKind::QuotaExceeded);
    }

    // An execution whose vsock socket would lie too deep for a Unix socket's
    // path is refused as the caller's fault, with nothing saved.
    #[test]
    fn admit_refuses_a_socket_path_too_long() {
        let dir = tempfile::tempdir().unwrap();
        let mut config = config(dir.path());
        config.chroot_base = dir.path().join("j".repeat(100));
        let executor = Executor::new(config).unwrap();

        let err = executor.admit(&request()).unwrap_err();
        assert_eq!(err.kind(), io::ErrorKind::InvalidInput);
        assert!(executor.records.lock().unwrap().is_empty());
    }

    // The supervisor copies the workload's output to its log, lets go of its
    // guest, and saves its end with the exit code.
    #[tokio::test]
    async fn supervise_follows_the_workload_to_its_end() {
        let dir = tempfile::tempdir().unwrap();
        let executor = Executor::new(config(dir.path())).unwrap();
        let request = request();
        let id = request.execution_id.clone();
        executor.admit(&request).unwrap();
        let (guest, _server) = serve_guest(dir.path(), Workload::default()).await;

        executor.supervise(id.clone(), guest);
        let record = ended(&executor, &id).await;

        assert_eq!(record.exit_code, Some(3));
        assert_eq!(
            std::fs::read(executor.output_path(&id)).unwrap(),
            b"hello\n"
        );
        assert!(executor.guests.lock().unwrap().is_empty());
    }

    // stop sends a supervised workload SIGTERM at once and SIGKILL once the
    // grace has passed.
    #[tokio::test]
    async fn stop_terms_then_kills() {
        let dir = tempfile::tempdir().unwrap();
        let mut executor = Executor::new(config(dir.path())).unwrap();
        executor.grace = Duration::from_millis(50);
        let signals: Arc<Mutex<Vec<i32>>> = Arc::default();
        let (guest, _server) = serve_guest(
            dir.path(),
            Running {
                signals: Arc::clone(&signals),
            },
        )
        .await;
        executor
            .guests
            .lock()
            .unwrap()
            .insert("exec-1".into(), guest);

        executor.stop("exec-1").await.unwrap();
        assert_eq!(*signals.lock().unwrap(), [15]);
        tokio::time::sleep(Duration::from_millis(200)).await;
        assert_eq!(*signals.lock().unwrap(), [15, 9]);
    }

    // A workload past its timeout, measured from its saved start, is stopped
    // at once.
    #[tokio::test]
    async fn watch_stops_a_workload_past_its_timeout() {
        let dir = tempfile::tempdir().unwrap();
        let executor = Executor::new(config(dir.path())).unwrap();
        let signals: Arc<Mutex<Vec<i32>>> = Arc::default();
        let (guest, _server) = serve_guest(
            dir.path(),
            Running {
                signals: Arc::clone(&signals),
            },
        )
        .await;
        executor
            .guests
            .lock()
            .unwrap()
            .insert("exec-1".into(), guest);
        let mut record = Record::sample();
        record.started = Some(SystemTime::now() - Duration::from_secs(61));

        executor.watch(&record);
        tokio::time::sleep(Duration::from_millis(100)).await;
        assert_eq!(*signals.lock().unwrap(), [15]);
    }

    // Calls for an execution the executor does not hold report it unknown,
    // except Release, for which it is released already, and a Submit missing
    // what it needs is refused.
    #[tokio::test]
    async fn unknown_executions_and_bad_requests_are_refused() {
        let dir = tempfile::tempdir().unwrap();
        let (client, _server) =
            serve_executor(dir.path(), Executor::new(config(dir.path())).unwrap()).await;
        let unknown = ExecutionRequest {
            execution_id: "exec-9".into(),
            ..Default::default()
        };
        let logs = LogsRequest {
            execution_id: "exec-9".into(),
            ..Default::default()
        };

        assert_eq!(
            code(client.status(context(), &unknown).await),
            Code::NOT_FOUND
        );
        assert_eq!(
            code(client.result(context(), &unknown).await),
            Code::NOT_FOUND
        );
        assert_eq!(
            code(client.cancel(context(), &unknown).await),
            Code::NOT_FOUND
        );
        assert_eq!(
            code(client.read_logs(context(), &logs).await),
            Code::NOT_FOUND
        );
        client.release(context(), &unknown).await.unwrap();
        let empty = ExecutorSubmitRequest::new();
        assert_eq!(
            code(client.submit(context(), &empty).await),
            Code::INVALID_ARGUMENT
        );
    }

    // A finished execution reports how it ended, is held no longer running,
    // and reads its output from any offset to its end.
    #[tokio::test]
    async fn a_finished_execution_reports_its_result_and_output() {
        let dir = tempfile::tempdir().unwrap();
        let executor = Executor::new(config(dir.path())).unwrap();
        let id = finished(&executor, b"hello world");
        let (client, _server) = serve_executor(dir.path(), executor).await;
        let request = ExecutionRequest {
            execution_id: id.clone(),
            ..Default::default()
        };

        assert_eq!(
            client.status(context(), &request).await.unwrap().state,
            "succeeded"
        );
        assert_eq!(
            client.result(context(), &request).await.unwrap().exit_code,
            Some(0)
        );
        let held = client.held(context(), &Empty::new()).await.unwrap().held;
        assert_eq!((held.len(), held[0].running), (1, false));
        for (offset, want) in [(0, &b"hello world"[..]), (6, &b"world"[..])] {
            let logs = LogsRequest {
                execution_id: id.clone(),
                offset,
                ..Default::default()
            };
            let output = client.read_logs(context(), &logs).await.unwrap();
            assert_eq!((output.data.as_slice(), output.ended), (want, true));
        }
    }

    // A running execution has no result yet, reads the output it has so far as
    // not ended, refuses a negative offset, and is marked when cancelled.
    #[tokio::test]
    async fn a_running_execution_has_no_result_yet() {
        let dir = tempfile::tempdir().unwrap();
        let executor = Executor::new(config(dir.path())).unwrap();
        let id = executor.admit(&request()).unwrap().unwrap().execution_id;
        executor
            .update(&id, |record| record.started = Some(SystemTime::now()))
            .unwrap();
        std::fs::write(executor.output_path(&id), "partial").unwrap();
        let (client, _server) = serve_executor(dir.path(), executor.clone()).await;
        let request = ExecutionRequest {
            execution_id: id.clone(),
            ..Default::default()
        };

        assert_eq!(
            code(client.result(context(), &request).await),
            Code::FAILED_PRECONDITION
        );
        let logs = LogsRequest {
            execution_id: id.clone(),
            ..Default::default()
        };
        let output = client.read_logs(context(), &logs).await.unwrap();
        assert_eq!(
            (output.data.as_slice(), output.ended),
            (&b"partial"[..], false)
        );
        let backwards = LogsRequest { offset: -1, ..logs };
        assert_eq!(
            code(client.read_logs(context(), &backwards).await),
            Code::INVALID_ARGUMENT
        );
        client.cancel(context(), &request).await.unwrap();
        assert!(executor.record(&id).unwrap().cancelled);
    }

    // Release forgets an execution: its record and directory go, and it is
    // unknown after.
    #[tokio::test]
    async fn release_forgets_the_execution() {
        let dir = tempfile::tempdir().unwrap();
        let executor = Executor::new(config(dir.path())).unwrap();
        let id = finished(&executor, b"done");
        let (client, _server) = serve_executor(dir.path(), executor).await;
        let request = ExecutionRequest {
            execution_id: id.clone(),
            ..Default::default()
        };

        client.release(context(), &request).await.unwrap();

        assert_eq!(
            code(client.status(context(), &request).await),
            Code::NOT_FOUND
        );
        assert!(!dir.path().join(&id).exists());
    }

    // Recovery ends, without an exit, an execution a crash left before its
    // workload started and one whose Firecracker has gone, so both report
    // failed.
    #[tokio::test]
    async fn recover_ends_what_it_cannot_take_back() {
        let dir = tempfile::tempdir().unwrap();
        let unstarted = Record::sample();
        let own = vm::Firecracker {
            pid: std::process::id() as i32,
            started: 0,
        };
        let gone = Record {
            execution_id: "exec-2".into(),
            started: Some(SystemTime::now()),
            firecracker: Some(own),
            ..Record::sample()
        };
        unstarted.save(dir.path()).unwrap();
        gone.save(dir.path()).unwrap();
        let executor = Executor::new(config(dir.path())).unwrap();

        executor.recover().await.unwrap();

        for id in ["exec-1", "exec-2"] {
            let record = executor.record(id).unwrap();
            assert_eq!((record.state(), record.exit_code), ("failed", None), "{id}");
        }
    }

    // A microVM that fails to launch is removed and its execution forgotten, so
    // the agent can submit it again.
    #[tokio::test]
    async fn boot_forgets_an_execution_that_fails() {
        let dir = tempfile::tempdir().unwrap();
        let mut config = config(dir.path());
        config.chroot_base = dir.path().join("jails");
        config.kernel = dir.path().join("missing-vmlinux");
        let executor = Executor::new(config).unwrap();
        let request = request();
        let record = executor.admit(&request).unwrap().unwrap();

        assert!(executor.boot(&record, &request).await.is_err());

        assert_eq!(executor.record(&request.execution_id), None);
        assert!(!dir.path().join(&request.execution_id).exists());
    }

    // An execution the executor holds no record of cannot be updated.
    #[test]
    fn update_of_an_unknown_execution_is_not_found() {
        let dir = tempfile::tempdir().unwrap();
        let executor = Executor::new(config(dir.path())).unwrap();
        let err = executor.update("exec-1", |_| {}).unwrap_err();
        assert_eq!(err.kind(), io::ErrorKind::NotFound);
    }

    // request is a Submit check accepts: an ID as the server makes them, a
    // program, the workload's size, a drive and a cgroup.
    fn request() -> ExecutorSubmitRequest {
        ExecutorSubmitRequest {
            execution_id: "0b6f6c1e-5d7a-4c1e-9f3a-2d4b8c6e1a90".into(),
            process: MessageField::some(Process {
                args: vec!["echo".into()],
                ..Default::default()
            }),
            resources: MessageField::some(Resources {
                cpu: 500,
                memory: 256,
                ..Default::default()
            }),
            root_drive: "/dev/dm-3".into(),
            cgroup: "/system.slice/vagabond-agent.service".into(),
            ..Default::default()
        }
    }

    // A whole request passes.
    #[test]
    fn check_accepts_a_whole_request() {
        assert_eq!(check(&request()), Ok(()));
    }

    // Each thing the daemon cannot act on is refused: a path or an oversized or
    // empty ID, no memory, a negative timeout, nothing to run, and no drive.
    #[test]
    fn check_refuses_what_the_daemon_cannot_act_on() {
        let refusals: [fn(&mut ExecutorSubmitRequest); 7] = [
            |r| r.execution_id = "../etc".into(),
            |r| r.execution_id = "a".repeat(65),
            |r| r.execution_id.clear(),
            |r| r.resources.mut_or_insert_default().memory = 0,
            |r| r.timeout.mut_or_insert_default().seconds = -1,
            |r| r.process.mut_or_insert_default().args.clear(),
            |r| r.root_drive.clear(),
        ];
        for refuse in refusals {
            let mut request = request();
            refuse(&mut request);
            assert!(check(&request).is_err(), "{request:?}");
        }
    }
}
