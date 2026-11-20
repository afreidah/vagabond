// -------------------------------------------------------------------------------
// MicroVM Records
//
// Author: Alex Freidah
//
// What the daemon saves about each microVM, one JSON file per execution under
// the state directory, so a restarted daemon finds its microVMs where it left
// them and can still answer for one that has stopped.
// -------------------------------------------------------------------------------

use std::io::{self, Write};
use std::path::Path;
use std::time::{Duration, SystemTime};

use protobuf::MessageField;
use protobuf::well_known_types::timestamp::Timestamp;
use serde::{Deserialize, Serialize};

use crate::context;
use crate::network::Slot;
use crate::proto::agent::{ExecutionResult, ExecutionStatus, Held, Resources};
use crate::vm::Firecracker;

// Record is one microVM: what the agent asked for, what the daemon gave it, and
// how its workload went.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct Record {
    // What Submit asked for: CPU in millicores, memory in MiB, and the timeout,
    // zero for none.
    pub execution_id: String,
    pub cpu: i64,
    pub memory: i64,
    pub timeout: Duration,

    // Where the daemon put the microVM: its network slot and its cgroup, relative
    // to the cgroup root, and Firecracker's process once launched.
    pub slot: Slot,
    pub cgroup: String,
    pub firecracker: Option<Firecracker>,

    // How the workload went: started and ended once each happened, and the exit
    // code unset when it never exited, as when the microVM died first.
    pub started: Option<SystemTime>,
    pub ended: Option<SystemTime>,
    pub exit_code: Option<i32>,
    pub cancelled: bool,
}

impl Record {
    // save writes the record under state_dir, replacing the last one through a
    // rename, the file and both directories synced, so a crash or a power loss
    // mid-write leaves the old record whole.
    pub fn save(&self, state_dir: &Path) -> io::Result<()> {
        let dir = state_dir.join(&self.execution_id);
        std::fs::create_dir_all(&dir)?;

        // Synced before the rename, so the record it replaces is never swapped for a partial one.
        let temp = dir.join("record.json.tmp");
        let mut file = std::fs::File::create(&temp)?;
        file.write_all(&serde_json::to_vec_pretty(self)?)?;
        file.sync_all()?;
        std::fs::rename(temp, dir.join("record.json"))?;

        // The rename, and a directory just made, last only once their directories are synced.
        std::fs::File::open(&dir)?.sync_all()?;
        std::fs::File::open(state_dir)?.sync_all()
    }

    // load reads the record of one execution back from under state_dir, an
    // error naming the file and keeping its kind, so a missing one is NotFound.
    pub fn load(state_dir: &Path, execution_id: &str) -> io::Result<Record> {
        let path = state_dir.join(execution_id).join("record.json");
        let read = || -> io::Result<Record> { Ok(serde_json::from_slice(&std::fs::read(&path)?)?) };
        read().map_err(context(format!("reading {}", path.display())))
    }

    // all reads every record under state_dir, one per execution's directory,
    // passing over a directory a crash left before its first save. A record that
    // cannot be read stops the daemon rather than lose its microVM.
    pub fn all(state_dir: &Path) -> io::Result<Vec<Record>> {
        let mut records = Vec::new();
        for entry in std::fs::read_dir(state_dir)? {
            let name = entry?.file_name();
            match Record::load(state_dir, &name.to_string_lossy()) {
                Ok(record) => records.push(record),
                Err(err) if err.kind() == io::ErrorKind::NotFound => {}
                Err(err) => return Err(err),
            }
        }
        Ok(records)
    }

    // state is where the workload stands, in the agent's terms: accepted until
    // it starts, running until it ends, then cancelled when Cancel stopped it,
    // succeeded on exit zero, and failed otherwise, a microVM that ended
    // without an exit included.
    pub fn state(&self) -> &'static str {
        match self {
            Record {
                ended: None,
                started: None,
                ..
            } => "accepted",
            Record { ended: None, .. } => "running",
            Record {
                cancelled: true, ..
            } => "cancelled",
            Record {
                exit_code: Some(0), ..
            } => "succeeded",
            _ => "failed",
        }
    }

    // status is the execution's status as Status reports it. The provider's ID
    // is the execution's own, the microVM having no other.
    pub fn status(&self) -> ExecutionStatus {
        ExecutionStatus {
            state: self.state().into(),
            provider_id: self.execution_id.clone(),
            started_at: MessageField::from_option(self.started.map(Timestamp::from)),
            ended_at: MessageField::from_option(self.ended.map(Timestamp::from)),
            ..Default::default()
        }
    }

    // result is how the workload ended: its exit code, unset when it never
    // exited, and how long it ran.
    pub fn result(&self) -> ExecutionResult {
        let ran = match (self.started, self.ended) {
            (Some(started), Some(ended)) => ended.duration_since(started).unwrap_or_default(),
            _ => Duration::ZERO,
        };
        ExecutionResult {
            exit_code: self.exit_code,
            duration: MessageField::some(ran.into()),
            ..Default::default()
        }
    }

    // held is the execution as Held lists it: what it takes of the node, what
    // it declared and each microVM's overhead, and whether it still does, as an
    // accepted one is about to, and an ended one does until its Firecracker has
    // exited.
    pub fn held(&self, overhead: &Resources) -> Held {
        let resources = Resources {
            cpu: self.cpu + overhead.cpu,
            memory: self.memory + overhead.memory,
            ..Default::default()
        };
        let firecracker = self.firecracker.as_ref().is_some_and(Firecracker::running);
        Held {
            execution_id: self.execution_id.clone(),
            resources: MessageField::some(resources),
            running: self.ended.is_none() || firecracker,
            ..Default::default()
        }
    }
}

#[cfg(test)]
impl Record {
    // sample is a record as Submit saves one, before the microVM is launched or
    // its workload has started, for the tests of every module.
    pub(crate) fn sample() -> Record {
        Record {
            execution_id: "exec-1".into(),
            cpu: 1000,
            memory: 512,
            timeout: Duration::from_secs(60),
            slot: Slot {
                cid: 3,
                tap: "vfc0".into(),
                subnet: "172.16.0.0/30".parse().unwrap(),
            },
            cgroup: "system.slice/vagabond-agent.service/exec-1".into(),
            firecracker: None,
            started: None,
            ended: None,
            exit_code: None,
            cancelled: false,
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    // A saved record loads back exactly as it was saved.
    #[test]
    fn save_then_load_round_trips() {
        let dir = tempfile::tempdir().unwrap();
        let record = Record::sample();
        record.save(dir.path()).unwrap();
        assert_eq!(Record::load(dir.path(), "exec-1").unwrap(), record);
    }

    // A second save replaces the first and leaves no temporary file behind, as
    // Submit saves again once the microVM is launched.
    #[test]
    fn save_replaces_the_last_record() {
        let dir = tempfile::tempdir().unwrap();
        let mut record = Record::sample();
        record.save(dir.path()).unwrap();
        record.firecracker = Some(Firecracker {
            pid: 42,
            started: 7,
        });
        record.started = Some(SystemTime::now());
        record.save(dir.path()).unwrap();
        assert_eq!(Record::load(dir.path(), "exec-1").unwrap(), record);
        assert!(!dir.path().join("exec-1/record.json.tmp").exists());
    }

    // The state follows the workload through its life: accepted, running, then
    // how it ended, cancellation first, and a microVM that ended without an
    // exit as failed.
    #[test]
    fn state_follows_the_workload() {
        let mut record = Record::sample();
        assert_eq!(record.state(), "accepted");
        record.started = Some(SystemTime::now());
        assert_eq!(record.state(), "running");
        record.ended = Some(SystemTime::now());
        assert_eq!(record.state(), "failed");
        record.exit_code = Some(1);
        assert_eq!(record.state(), "failed");
        record.exit_code = Some(0);
        assert_eq!(record.state(), "succeeded");
        record.cancelled = true;
        assert_eq!(record.state(), "cancelled");
    }

    // A finished execution reports its times with itself as the provider's ID,
    // its exit code and how long it ran, and, held, what it declared and that
    // it no longer runs.
    #[test]
    fn a_finished_execution_reports_how_it_went() {
        assert!(Record::sample().held(&Resources::default()).running);

        let mut record = Record::sample();
        let started = SystemTime::UNIX_EPOCH + Duration::from_secs(1_000);
        record.started = Some(started);
        record.ended = Some(started + Duration::from_secs(90));
        record.exit_code = Some(3);

        let status = record.status();
        assert_eq!(status.provider_id, "exec-1");
        assert_eq!(status.started_at.seconds, 1_000);
        assert_eq!(status.ended_at.seconds, 1_090);

        let result = record.result();
        assert_eq!(result.exit_code, Some(3));
        assert_eq!(result.duration.seconds, 90);

        let held = record.held(&Resources::default());
        assert_eq!((held.resources.cpu, held.resources.memory), (1000, 512));
        assert!(!held.running);
    }

    // Held charges each microVM its overhead, and an ended one until its
    // Firecracker has exited, as its memory is the cgroup's until then.
    #[test]
    fn held_counts_the_overhead_until_firecracker_exits() {
        let overhead = Resources {
            memory: 32,
            ..Default::default()
        };
        let mut record = Record::sample();
        record.ended = Some(SystemTime::now());
        record.firecracker = Firecracker::of(std::process::id() as i32);

        let held = record.held(&overhead);
        assert_eq!((held.resources.cpu, held.resources.memory), (1000, 544));
        assert!(held.running);
    }

    // all returns every saved record and passes over a directory holding none.
    #[test]
    fn all_reads_every_saved_record() {
        let dir = tempfile::tempdir().unwrap();
        let mut second = Record::sample();
        second.execution_id = "exec-2".into();
        Record::sample().save(dir.path()).unwrap();
        second.save(dir.path()).unwrap();
        std::fs::create_dir(dir.path().join("exec-3")).unwrap();

        let mut ids: Vec<_> = Record::all(dir.path())
            .unwrap()
            .into_iter()
            .map(|r| r.execution_id)
            .collect();
        ids.sort();
        assert_eq!(ids, ["exec-1", "exec-2"]);
    }

    // An execution that was never saved has no record, which callers report as
    // an unknown execution.
    #[test]
    fn load_of_an_unknown_execution_is_not_found() {
        let dir = tempfile::tempdir().unwrap();
        let err = Record::load(dir.path(), "exec-1").unwrap_err();
        assert_eq!(err.kind(), io::ErrorKind::NotFound);
    }
}
