#!/usr/bin/env bash
# -------------------------------------------------------------------------------
# Executor Test Environment
#
# Author: Alex Freidah
#
# Starts and stops what the executor tests run against, apart from anything the
# host runs: a containerd of its own, with the devmapper snapshotter on a thin
# pool over loop devices, and a vagabond-firecracker booting the pinned kernel
# and initramfs. down clears everything up made, and the firewall rules the
# tests leave for microVMs and containers, and passes over whatever is already
# gone. Run as root, by make.
#
# Usage: KVM_DIR=<dir> DAEMON=<vagabond-firecracker> executor-env.sh up|down
# -------------------------------------------------------------------------------

set -euo pipefail

: "${KVM_DIR:?the directory holding firecracker, jailer, vmlinux and initramfs.cpio.gz}"
: "${DAEMON:?the vagabond-firecracker binary}"

# Where the environment keeps its state, the sockets the tests reach it on, and
# the cgroup the tests run workloads under, as an agent's own. The jails are
# short of path, since a microVM's vsock socket lives in its jail.
DIR="${KVM_DIR}/executor-env"
RUN=/run/vagabond-test
JAILS=/var/tmp/vagabond-test-jails
POOL=vagabond-test-pool
CGROUP=/sys/fs/cgroup/vagabond-test

# The address range the containerd executor tests give containers, the agent's
# default.
CONTAINER_POOL=172.30.0.0/16

# uplink is the interface the default route leaves through.
uplink() {
	ip -o route show default | awk '{ print $5; exit }'
}

# wait_for waits up to ten seconds for the socket $1, and fails with the log $2
# when it does not appear.
wait_for() {
	local socket=$1 log=$2

	for _ in $(seq 100); do
		[[ -S "${socket}" ]] && return 0
		sleep 0.1
	done

	echo "${socket} did not appear; its log:" >&2
	cat "${log}" >&2
	return 1
}

# stop ends the process named in the pid file $1 and waits for it to exit.
stop() {
	local pidfile=$1
	[[ -f "${pidfile}" ]] || return 0

	local pid
	pid=$(cat "${pidfile}")
	kill "${pid}" 2>/dev/null || true

	while kill -0 "${pid}" 2>/dev/null; do
		sleep 0.1
	done

	rm -f "${pidfile}"
}

# up makes the thin pool, starts containerd over it, and starts the daemon,
# first clearing whatever a run that died before down left.
up() {
	down
	mkdir -p "${DIR}" "${RUN}"

	# Sparse, so the pool takes only the disk its snapshots use.
	truncate -s 8G "${DIR}/data"
	truncate -s 256M "${DIR}/meta"

	local data meta
	data=$(losetup --find --show "${DIR}/data")
	meta=$(losetup --find --show "${DIR}/meta")
	dmsetup create "${POOL}" --table "0 $(blockdev --getsz "${data}") thin-pool ${meta} ${data} 128 32768"

	cat > "${DIR}/containerd.toml" <<EOF
version = 2
root = "${DIR}/containerd"
state = "${RUN}/containerd"

[grpc]
  address = "${RUN}/containerd.sock"

[plugins."io.containerd.snapshotter.v1.devmapper"]
  root_path = "${DIR}/devmapper"
  pool_name = "${POOL}"
  base_image_size = "1GB"
  fs_type = "ext4"
  discard_blocks = true
EOF

	containerd --config "${DIR}/containerd.toml" > "${DIR}/containerd.log" 2>&1 &
	echo $! > "${RUN}/containerd.pid"
	wait_for "${RUN}/containerd.sock" "${DIR}/containerd.log"

	# Each workload's cgroup is made under it, limited by these controllers.
	mkdir -p "${CGROUP}"
	echo "+cpu +memory" > "${CGROUP}/cgroup.subtree_control"

	"${DAEMON}" \
		--socket "${RUN}/firecracker.sock" \
		--state-dir "${DIR}/firecracker" \
		--chroot-base "${JAILS}" \
		--kernel "${KVM_DIR}/vmlinux" \
		--initramfs "${KVM_DIR}/initramfs.cpio.gz" \
		--firecracker "${KVM_DIR}/firecracker" \
		--jailer "${KVM_DIR}/jailer" \
		--uid 65534 --gid 65534 \
		--uplink "$(uplink)" \
		> "${DIR}/firecracker.log" 2>&1 &
	echo $! > "${RUN}/firecracker.pid"
	wait_for "${RUN}/firecracker.sock" "${DIR}/firecracker.log"
}

# down stops the daemon, clears what its microVMs left, stops containerd and
# takes the pool apart.
down() {
	stop "${RUN}/firecracker.pid"

	# Whatever still runs under the tests' cgroup is killed through it at once.
	if [[ -d "${CGROUP}" ]]; then
		echo 1 > "${CGROUP}/cgroup.kill"

		while grep -q "populated 1" "${CGROUP}/cgroup.events"; do
			sleep 0.1
		done

		find "${CGROUP}" -depth -type d -exec rmdir {} +
	fi

	# The taps, NAT table and Docker rules the daemon made, which outlive it by design.
	local tap
	for tap in /sys/class/net/vfc*; do
		[[ -e "${tap}" ]] && ip link del "$(basename "${tap}")"
	done

	nft delete table ip vagabond_firecracker 2>/dev/null || true

	local out
	out=$(uplink)
	iptables -w -D DOCKER-USER -i vfc+ -o "${out}" -j ACCEPT 2>/dev/null || true
	iptables -w -D DOCKER-USER -i "${out}" -o vfc+ -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT 2>/dev/null || true
	rm -rf "${JAILS}"

	# The containers' table and Docker rules, which the containerd executor tests leave.
	nft delete table ip vagabond_test_containers 2>/dev/null || true
	iptables -w -D DOCKER-USER -s "${CONTAINER_POOL}" -j ACCEPT 2>/dev/null || true
	iptables -w -D DOCKER-USER -d "${CONTAINER_POOL}" -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT 2>/dev/null || true

	stop "${RUN}/containerd.pid"

	# The pool's thin devices before the pool itself, which their names start with.
	local device
	for device in $(dmsetup ls 2>/dev/null | awk -v pool="${POOL}" 'index($1, pool) == 1 { print $1 }' | sort -r); do
		dmsetup remove "${device}"
	done

	local loop
	for loop in $(losetup --list --noheadings --output NAME --associated "${DIR}/data" 2>/dev/null) \
		$(losetup --list --noheadings --output NAME --associated "${DIR}/meta" 2>/dev/null); do
		losetup --detach "${loop}"
	done

	rm -rf "${DIR}" "${RUN}"
}

case "${1:-}" in
	up) up ;;
	down) down ;;
	*)
		echo "usage: $0 up|down" >&2
		exit 2
		;;
esac
