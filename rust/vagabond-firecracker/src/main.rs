// -------------------------------------------------------------------------------
// vagabond-firecracker
//
// Author: Alex Freidah
//
// The daemon beside the agent on a KVM node: runs the agent's workloads in
// Firecracker microVMs and serves the executor protocol to it.
// -------------------------------------------------------------------------------

use std::io;
use std::os::unix::fs::PermissionsExt;
use std::process::ExitCode;
use std::sync::Arc;

use clap::Parser;
use tokio::signal::unix::{SignalKind, signal};
use tracing::{error, info};
use ttrpc::r#async::Server;
use vagabond_firecracker::config::Config;
use vagabond_firecracker::context;
use vagabond_firecracker::executor::Executor;
use vagabond_firecracker::network;
use vagabond_firecracker::proto::executor_ttrpc::create_executor;

// main logs to stderr at the configured level, uncoloured for the journal, and
// runs the daemon until it is told to stop, logging what stopped it otherwise
// with a failing exit.
#[tokio::main]
async fn main() -> ExitCode {
    let config = Config::parse();
    tracing_subscriber::fmt()
        .with_max_level(config.log_level)
        .with_writer(io::stderr)
        .with_ansi(false)
        .with_target(false)
        .init();
    match run(config).await {
        Ok(()) => ExitCode::SUCCESS,
        Err(err) => {
            error!(error = %err, "failed");
            ExitCode::FAILURE
        }
    }
}

// run sets up the microVMs' network, takes back the microVMs a previous daemon
// left, and serves the executor protocol on a socket only root can open, until
// SIGTERM or SIGINT. The microVMs outlive it, for the next start to recover.
async fn run(config: Config) -> io::Result<()> {
    network::setup_nat(config.pool, &config.uplink).await?;
    let socket = config.socket.clone();
    let executor = Executor::new(config)?;
    executor.recover().await?;

    if let Some(dir) = socket.parent() {
        tokio::fs::DirBuilder::new()
            .recursive(true)
            .mode(0o700)
            .create(dir)
            .await?;
    }
    // A socket a crash left behind would stop the bind.
    match tokio::fs::remove_file(&socket).await {
        Err(err) if err.kind() != io::ErrorKind::NotFound => return Err(err),
        _ => {}
    }
    let mut server = Server::new()
        .bind(&format!("unix://{}", socket.display()))
        .map_err(io::Error::other)
        .map_err(context(format!("binding {}", socket.display())))?
        .register_service(create_executor(Arc::new(executor)));
    tokio::fs::set_permissions(&socket, std::fs::Permissions::from_mode(0o600)).await?;
    server.start().await.map_err(io::Error::other)?;
    info!(socket = %socket.display(), "serving");

    let (mut terminate, mut interrupt) = (
        signal(SignalKind::terminate())?,
        signal(SignalKind::interrupt())?,
    );
    tokio::select! {
        _ = terminate.recv() => {}
        _ = interrupt.recv() => {}
    }
    server.shutdown().await.map_err(io::Error::other)?;
    info!("stopped");
    Ok(())
}
