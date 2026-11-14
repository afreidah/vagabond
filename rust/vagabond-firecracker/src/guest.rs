// -------------------------------------------------------------------------------
// Guest Connection
//
// Author: Alex Freidah
//
// Reaches vagabond-init inside a microVM through the Unix socket Firecracker
// proxies the guest's vsock on: a CONNECT line naming the guest's port,
// answered by an OK line, then the guest protocol over the same connection.
// -------------------------------------------------------------------------------

use std::io;
use std::path::Path;
use std::time::Duration;

use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::UnixStream;
use tokio::time::Instant;
use ttrpc::r#async::Client;
use ttrpc::r#async::transport::Socket;

use crate::GUEST_PORT;
use crate::proto::guest_ttrpc::GuestClient;

// RETRY is how long connect waits between attempts while the guest boots.
const RETRY: Duration = Duration::from_millis(100);

// connect opens the guest protocol to the microVM behind the vsock socket at
// path, retrying until patience runs out: until the guest listens, the socket
// is missing or Firecracker closes the connection unanswered. An attempt still
// waiting on its answer at the deadline fails as timed out.
pub async fn connect(path: &Path, patience: Duration) -> io::Result<GuestClient> {
    let deadline = Instant::now() + patience;
    loop {
        match tokio::time::timeout_at(deadline, handshake(path)).await {
            Ok(Ok(stream)) => return Ok(GuestClient::new(Client::new(Socket::from(stream)))),
            // No retry that would start at the deadline, with no time to answer.
            Ok(Err(err)) if Instant::now() + RETRY >= deadline => return Err(err),
            Ok(Err(_)) => tokio::time::sleep(RETRY).await,
            Err(_) => {
                let message = "the guest's vsock did not answer in time";
                return Err(io::Error::new(io::ErrorKind::TimedOut, message));
            }
        }
    }
}

// handshake asks Firecracker for a connection to the guest's port and reads its
// answer, OK and the host side's port, leaving the stream carrying the guest
// protocol.
async fn handshake(path: &Path) -> io::Result<UnixStream> {
    let mut stream = UnixStream::connect(path).await?;
    stream
        .write_all(format!("CONNECT {GUEST_PORT}\n").as_bytes())
        .await?;
    // One byte at a time, so nothing after the line is read away from ttrpc.
    let mut line = Vec::new();
    loop {
        match stream.read_u8().await? {
            b'\n' => break,
            byte => line.push(byte),
        }
    }
    if !line.starts_with(b"OK ") {
        let answer = String::from_utf8_lossy(&line);
        return Err(io::Error::new(
            io::ErrorKind::ConnectionRefused,
            format!("vsock answered {answer:?}"),
        ));
    }
    Ok(stream)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::proto::agent::Empty;
    use crate::proto::guest::Exit;
    use crate::proto::guest_ttrpc::{Guest, create_guest};
    use async_trait::async_trait;
    use std::path::PathBuf;
    use std::sync::Arc;
    use tokio::net::UnixListener;
    use ttrpc::r#async::{Server, TtrpcContext};

    // ExitsSeven is a guest whose workload has exited with code 7.
    struct ExitsSeven;

    #[async_trait]
    impl Guest for ExitsSeven {
        // wait reports the exit at once.
        async fn wait(&self, _ctx: &TtrpcContext, _: Empty) -> ttrpc::Result<Exit> {
            Ok(Exit {
                code: 7,
                ..Default::default()
            })
        }
    }

    // proxy stands in for Firecracker's vsock socket at path: for each
    // connection it reads the CONNECT line and sends answer, and after an OK
    // carries the connection to the guest's server at inside, as Firecracker
    // carries it into the microVM.
    fn proxy(path: &Path, answer: &'static str, inside: PathBuf) {
        let listener = UnixListener::bind(path).unwrap();
        tokio::spawn(async move {
            loop {
                let (mut host, _) = listener.accept().await.unwrap();
                while host.read_u8().await.unwrap() != b'\n' {}
                host.write_all(answer.as_bytes()).await.unwrap();
                if answer.starts_with("OK") {
                    let mut guest = UnixStream::connect(&inside).await.unwrap();
                    let _ = tokio::io::copy_bidirectional(&mut host, &mut guest).await;
                }
            }
        });
    }

    // Through an answered CONNECT, the client reaches the guest's server.
    #[tokio::test]
    async fn connect_reaches_the_guest_through_the_vsock_socket() {
        let dir = tempfile::tempdir().unwrap();
        let (vsock, inside) = (dir.path().join("v.sock"), dir.path().join("guest.sock"));
        let mut server = Server::new()
            .bind(&format!("unix://{}", inside.display()))
            .unwrap()
            .register_service(create_guest(Arc::new(ExitsSeven)));
        server.start().await.unwrap();
        proxy(&vsock, "OK 1073741824\n", inside);

        let guest = connect(&vsock, Duration::from_secs(5)).await.unwrap();
        let context = ttrpc::context::with_duration(Duration::from_secs(5));
        assert_eq!(guest.wait(context, &Empty::new()).await.unwrap().code, 7);
    }

    // A CONNECT answered with anything but OK is refused, once patience runs
    // out on the retries.
    #[tokio::test]
    async fn connect_fails_when_the_vsock_refuses() {
        let dir = tempfile::tempdir().unwrap();
        let vsock = dir.path().join("v.sock");
        proxy(&vsock, "NO\n", PathBuf::new());
        let err = connect(&vsock, Duration::from_millis(300))
            .await
            .err()
            .expect("connected");
        assert_eq!(err.kind(), io::ErrorKind::ConnectionRefused);
    }

    // A vsock socket that never appears, as when Firecracker never started,
    // fails once patience runs out.
    #[tokio::test]
    async fn connect_fails_when_the_vsock_is_missing() {
        let dir = tempfile::tempdir().unwrap();
        let vsock = dir.path().join("v.sock");
        let err = connect(&vsock, Duration::from_millis(300))
            .await
            .err()
            .expect("connected");
        assert_eq!(err.kind(), io::ErrorKind::NotFound);
    }

    // A connection left unanswered times out at the deadline rather than
    // waiting forever.
    #[tokio::test]
    async fn connect_times_out_when_the_vsock_is_silent() {
        let dir = tempfile::tempdir().unwrap();
        let vsock = dir.path().join("v.sock");
        let _silent = UnixListener::bind(&vsock).unwrap();
        let err = connect(&vsock, Duration::from_millis(300))
            .await
            .err()
            .expect("connected");
        assert_eq!(err.kind(), io::ErrorKind::TimedOut);
    }
}
