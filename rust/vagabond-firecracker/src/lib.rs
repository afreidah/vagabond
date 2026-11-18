// -------------------------------------------------------------------------------
// vagabond-firecracker Library
//
// Author: Alex Freidah
//
// What the daemon and vagabond-init both use, the generated protocol code and
// the port the guest protocol is served on, and the daemon's own modules.
// -------------------------------------------------------------------------------

pub mod config;
pub mod executor;
pub mod guest;
pub mod network;
pub mod record;
pub mod vm;

// GUEST_PORT is the vsock port vagabond-init serves the guest protocol on, and
// the one the daemon asks Firecracker's vsock socket to connect it to.
pub const GUEST_PORT: u32 = 1024;

// INIT_FAILED begins the console line vagabond-init writes when it cannot set
// a microVM up, which the daemon looks for to say why a boot failed.
pub const INIT_FAILED: &str = "vagabond-init: ";

// context prefixes an error with what was being done, since an io::Error names
// no file, keeping its kind, which decides the status a caller is given.
pub fn context(what: impl std::fmt::Display) -> impl FnOnce(std::io::Error) -> std::io::Error {
    move |err| std::io::Error::new(err.kind(), format!("{what}: {err}"))
}

// proto is the generated code: a module of messages per proto file (agent,
// executor, guest) and one of ttRPC client and server code per service
// (agent_ttrpc, executor_ttrpc, guest_ttrpc).
pub mod proto {
    include!(concat!(env!("OUT_DIR"), "/mod.rs"));
}
