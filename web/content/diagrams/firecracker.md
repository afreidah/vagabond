---
title: "Firecracker MicroVMs"
linkTitle: "Firecracker MicroVMs"
description: "Interactive diagram of vagabond-firecracker running an agent's workload in a microVM: the request check, the jail, cgroup and tap, the boot, the guest, timeouts and cancellation, restarts and release."
weight: 6
---

How vagabond-firecracker runs a workload the agent hands it: the checks on a `Submit`, the jail, cgroup and tap it builds, the boot and the guest inside, the supervisor and timeout that follow the workload, what a restarted daemon takes back, and what `Release` removes. **Hover over any component** for implementation details.

{{< diagram >}}

<script>
window.vagabondDiagram = {
  source: [
    'flowchart TD',
    '    subgraph SUBMITTING [Submit]',
    '        SUBMIT([Submit from<br>the agent]):::entry --> CHECK{"ID, memory, drive,<br>fstype, cgroup?"}:::filter',
    '        CHECK -->|no| BAD[InvalidArgument]:::reject',
    '        CHECK -->|yes| HELD{Already<br>held?}:::decision',
    '        HELD -->|yes| STANDS[Its state<br>as it stands]:::process',
    '        HELD -->|no| ADMIT[Admit: a free slot,<br>the record saved first]:::process',
    '        ADMIT -->|no slot| FULL[ResourceExhausted]:::reject',
    '        ADMIT --> RECORD[(record.json)]:::storage',
    '        ADMIT --> JAIL[Jail: kernel, initramfs,<br>drive node, config]:::process',
    '        JAIL --> CGROUP[cgroup: cpu.max,<br>memory.max + overhead]:::process',
    '        CGROUP --> TAP[Tap vfcN<br>and its /30]:::process',
    '        TAP --> JAILER[The jailer becomes<br>Firecracker]:::process',
    '        JAILER --> BOOT{"Guest answers<br>within 30 s?"}:::decision',
    '        BOOT -->|"no, or Firecracker<br>exits"| BOOTFAIL[Boot failure:<br>removed, forgotten]:::reject',
    '        BOOT -->|yes| START[Start the workload<br>as the image user]:::process',
    '    end',
    '',
    '    subgraph RUNNING [The workload]',
    '        INIT[vagabond-init:<br>root drive, vsock 1024]:::success',
    '        START --> INIT',
    '        START --> SUPERVISE[Supervisor:<br>output, end, shutdown]:::background',
    '        START --> WATCH[Timeout<br>watch]:::background',
    '        SUPERVISE -->|Read| INIT',
    '        SUPERVISE --> OUTPUT[(output.log)]:::storage',
    '        SUPERVISE --> ENDED[End saved,<br>guest shut down]:::process',
    '        CANCEL([Cancel]):::entry --> STOP[Stop: SIGTERM,<br>SIGKILL after 10 s]:::process',
    '        WATCH -->|deadline| STOP',
    '        STOP -->|Signal| INIT',
    '        READLOGS([ReadLogs]):::entry --> OUTPUT',
    '    end',
    '',
    '    subgraph RESTART [Daemon start]',
    '        STARTUP([vagabond-firecracker<br>starts]):::entry --> NAT[Forwarding, NAT table,<br>Docker rules]:::process',
    '        NAT --> REATTACH{"Firecracker runs,<br>guest answers?"}:::decision',
    '        REATTACH -->|yes| SUPERVISE',
    '        REATTACH -->|no| LOSTVM[Removed, ended<br>without an exit]:::reject',
    '        NAT --> ENDEDVM[Ended, still<br>running: removed]:::process',
    '    end',
    '',
    '    subgraph RELEASING [Release]',
    '        RELEASE([Release]):::entry --> REMOVE[Remove Firecracker,<br>tap, jail, cgroup, state]:::process',
    '    end',
    '',
    '    RECORD -->|read back| REATTACH',
    '    ENDED -.->|"once the server<br>has the result"| RELEASE',
  ].join('\n'),

  info: {
    SUBMIT: {
      title: 'Submit from the Agent',
      badge: 'entry', badgeText: 'executor protocol',
      body: '<p>ttRPC on <code>--socket</code>, mode <code>0600</code>, root&rsquo;s. The agent&rsquo;s firecracker executor sends the execution ID, the image&rsquo;s process (args, environment, working directory, uid, gid and groups), the declared CPU and memory, the timeout, the devmapper snapshot&rsquo;s block device and filesystem type, and the agent&rsquo;s cgroup.</p><p><code>Submit</code> returns <code>running</code> once the workload has started.</p><p><a href="../../docs/firecracker/#protocol">Executor protocol &rarr;</a></p>'
    },
    CHECK: {
      title: 'Request Check',
      badge: 'filter', badgeText: 'before anything is made',
      body: '<p>Refused, before anything is made: an execution ID that is not 1 to 64 letters, digits and hyphens, which also keeps a path out of the jail&rsquo;s; no memory to size the microVM by; a negative timeout; nothing to run; no drive or cgroup; a filesystem type that is not one plain word, which the guest kernel&rsquo;s command line would take for more parameters.</p><p>A jail path that leaves the vsock socket past 107 bytes is refused here too.</p>'
    },
    BAD: {
      title: 'InvalidArgument',
      badge: 'reject', badgeText: 'refused',
      body: '<p>The request cannot be acted on. The agent fails the submission as infrastructure, so the server may try another node.</p>'
    },
    HELD: {
      title: 'Already Held?',
      badge: 'decision', badgeText: 'idempotency',
      body: '<p>The execution ID is the idempotency key: a <code>Submit</code> for an execution already held starts nothing and answers its state as it stands. The agent&rsquo;s executor checks <code>Status</code> first for the same reason, before it pulls the image.</p>'
    },
    STANDS: {
      title: 'Its State as It Stands',
      badge: 'process', badgeText: 'answer',
      body: '<p><code>accepted</code> while it boots, <code>running</code>, or how it ended, from its record.</p>'
    },
    ADMIT: {
      title: 'Admit',
      badge: 'process', badgeText: 'under the lock',
      body: '<p>Under the records lock, so two submissions cannot take one slot: the lowest free slot, its <code>/30</code> of <code>--pool</code>, tap <code>vfcN</code> and vsock CID <code>3 + N</code>, and a record saved before anything it describes is made.</p><p>A <code>Submit</code> that fails after this removes what it made and forgets the execution, so the same ID can be submitted again.</p><p><a href="../../docs/firecracker/#networking">Slots &rarr;</a></p>'
    },
    FULL: {
      title: 'ResourceExhausted',
      badge: 'reject', badgeText: 'pool used up',
      body: '<p><code>the address pool is used up</code>: every slot is held, ended executions included until they are released.</p>'
    },
    RECORD: {
      title: 'record.json',
      badge: 'storage', badgeText: 'state dir',
      body: '<p><code>&lt;state-dir&gt;/&lt;execution-id&gt;/record.json</code>: the declared resources and timeout, the slot, the cgroup, Firecracker&rsquo;s pid and start time, the start and end, the exit code, and whether it was cancelled. Each change is written to a temporary file and renamed over the last, so a crash leaves the old record whole.</p><p><a href="../../docs/firecracker/#restarts">Restarts &rarr;</a></p>'
    },
    JAIL: {
      title: 'The Jail',
      badge: 'process', badgeText: 'chroot',
      body: '<p><code>&lt;chroot-base&gt;/firecracker/&lt;execution-id&gt;/root/</code>: the kernel and initramfs hard-linked, or copied across filesystems; the root drive as a device node only Firecracker&rsquo;s user may open; and <code>config.json</code>, which Firecracker boots from without API calls. The boot arguments name the drive&rsquo;s filesystem type the <code>Submit</code> gave.</p><p><a href="../../docs/firecracker/#jail">Jail &rarr;</a></p>'
    },
    CGROUP: {
      title: 'The cgroup',
      badge: 'process', badgeText: 'limits',
      body: '<p><code>&lt;cgroup&gt;/&lt;execution-id&gt;</code> under the agent&rsquo;s cgroup. <code>cpu.max</code> holds it to its declared millicores; <code>memory.max</code> is its declared memory plus <code>--vmm-overhead</code>, 32 MiB by default, for Firecracker itself. That sum is what <code>Held</code> reports and the server reserves.</p><p><a href="../../docs/firecracker/#sizing">Sizing &rarr;</a></p>'
    },
    TAP: {
      title: 'Tap and Its /30',
      badge: 'process', badgeText: 'network',
      body: '<p>Tap <code>vfcN</code>, owned by Firecracker&rsquo;s user, the host on the <code>/30</code>&rsquo;s first address and the microVM on its second. Traffic leaves through <code>--uplink</code> behind NAT; a microVM reaches no other microVM and none of the host&rsquo;s addresses.</p><p><a href="../../docs/firecracker/#nat-and-the-firewall">NAT and the firewall &rarr;</a></p>'
    },
    JAILER: {
      title: 'The Jailer Becomes Firecracker',
      badge: 'process', badgeText: 'jailer',
      body: '<p>The jailer creates the device nodes, moves into the cgroup, chroots into the jail, drops to <code>--uid</code> and <code>--gid</code>, and becomes Firecracker, which boots at once. Its pid and start time are saved together, since a pid is reused but never with the same start time.</p>'
    },
    BOOT: {
      title: 'Guest Answers within 30 s?',
      badge: 'decision', badgeText: 'boot race',
      body: '<p>The daemon connects to the guest through Firecracker&rsquo;s vsock socket, <code>CONNECT 1024</code>, while it watches Firecracker. A Firecracker that exits first fails the <code>Submit</code> at once, with <code>vagabond-init</code>&rsquo;s line when it wrote one, else the console&rsquo;s last lines.</p>'
    },
    BOOTFAIL: {
      title: 'Boot Failure',
      badge: 'reject', badgeText: 'Internal',
      body: '<p><code>Firecracker exited while booting: ...</code>. What was made is removed and the execution forgotten, so the agent&rsquo;s executor deletes its snapshot and the same ID can come again.</p><p><a href="../../docs/firecracker/#troubleshooting">Troubleshooting &rarr;</a></p>'
    },
    START: {
      title: 'Start the Workload',
      badge: 'process', badgeText: 'guest Start',
      body: '<p>The guest&rsquo;s <code>Start</code> runs the process once; a second call starts nothing, so a retry after a lost reply never runs it twice. The start is saved, and the timeout runs from it.</p>'
    },
    INIT: {
      title: 'vagabond-init',
      badge: 'success', badgeText: 'PID 1',
      body: '<p>Mounts the kernel&rsquo;s filesystems and the root drive, switches into it, writes <code>/etc/resolv.conf</code> from the kernel&rsquo;s <code>ip=</code> name server, and serves the guest protocol on vsock port 1024. It runs the workload with only its own environment, as the image&rsquo;s user and groups, standard output and error on one pipe.</p><p>The workload is not PID 1, so <code>SIGTERM</code> ends a plain <code>sh -c</code> at once, with 143.</p><p><a href="../../docs/firecracker/#inside-the-microvm">Inside the microVM &rarr;</a></p>'
    },
    SUPERVISE: {
      title: 'Supervisor',
      badge: 'background', badgeText: 'per microVM',
      body: '<p>One task per running workload: copies its output to <code>output.log</code> as it is read, then saves its end with the exit code, unset when the guest&rsquo;s connection broke as the microVM died, and tells the guest to shut down.</p><p>A workload already cancelled when supervision starts, as when <code>Cancel</code> came while it booted or just before a restart, is stopped first.</p>'
    },
    WATCH: {
      title: 'Timeout Watch',
      badge: 'background', badgeText: 'per microVM',
      body: '<p>From the saved start, so a restarted daemon keeps the same deadline; zero is none. At the deadline the workload is stopped, and ends <code>failed</code>.</p><p><a href="../../docs/firecracker/#timeouts">Timeouts &rarr;</a></p>'
    },
    CANCEL: {
      title: 'Cancel',
      badge: 'entry', badgeText: 'executor protocol',
      body: '<p>Marks the record cancelled, then stops the workload; it reports <code>cancelled</code> whatever its exit. An execution already ended is marked all the same.</p>'
    },
    STOP: {
      title: 'Stop',
      badge: 'process', badgeText: 'signals',
      body: '<p><code>SIGTERM</code> through the guest&rsquo;s <code>Signal</code>, then, 10 s later, <code>SIGKILL</code> if the workload is still supervised. A workload no longer supervised has nothing left to stop.</p>'
    },
    OUTPUT: {
      title: 'output.log',
      badge: 'storage', badgeText: 'state dir',
      body: '<p><code>&lt;state-dir&gt;/&lt;execution-id&gt;/output.log</code>, kept until <code>Release</code>, its front freed past <code>--max-log-mib</code> with its length kept. <code>ReadLogs</code> returns it from an offset, or from where the kept part begins, up to 64 KiB a call, with the <code>offset</code> its data begins at, and <code>ended</code> once the workload has ended and the offset has reached its end.</p>'
    },
    ENDED: {
      title: 'End Saved, Guest Shut Down',
      badge: 'process', badgeText: 'end',
      body: '<p>The guest flushes the root drive and reboots, which ends Firecracker. Until Firecracker has exited, <code>Held</code> still counts the microVM as running, since its memory is still charged.</p><p>The tap, jail and cgroup stay until <code>Release</code>.</p>'
    },
    READLOGS: {
      title: 'ReadLogs',
      badge: 'entry', badgeText: 'executor protocol',
      body: '<p>What the agent&rsquo;s executor polls while the server captures the output, from where the server&rsquo;s copy got to. An execution not started yet reads as empty.</p><p><a href="../live-output/">Live output diagram &rarr;</a></p>'
    },
    STARTUP: {
      title: 'vagabond-firecracker Starts',
      badge: 'entry', badgeText: 'daemon',
      body: '<p>Started by the agent with <code>-firecracker</code>, as its child. Parses its flags, sets up the network, reads every record in <code>--state-dir</code>, takes back the microVMs a previous daemon left, then serves. A record that cannot be read stops it, rather than lose the microVM it describes.</p><p>MicroVMs belong to their Firecracker processes, not to the daemon, so they keep running while it is stopped.</p><p><a href="../../docs/firecracker/#startup-sequence">Startup sequence &rarr;</a></p>'
    },
    NAT: {
      title: 'Forwarding, NAT Table, Docker Rules',
      badge: 'process', badgeText: 'network',
      body: '<p>Turns on IPv4 forwarding, replaces its own nftables table whole in one transaction, and, where Docker&rsquo;s <code>DOCKER-USER</code> chain exists, inserts the two rules microVM traffic needs past Docker&rsquo;s forward drop. None of it is undone at exit, since the microVMs outlive the daemon.</p><p><a href="../../docs/firecracker/#docker">Docker &rarr;</a></p>'
    },
    REATTACH: {
      title: 'Firecracker Runs, Guest Answers?',
      badge: 'decision', badgeText: 'recovery',
      body: '<p>For each execution that has not ended: one that started, whose Firecracker still runs and whose guest answers within 5 s, is supervised again, its output appended where the log ends and its timeout run to the saved deadline. It logs <code>recovered</code>.</p>'
    },
    LOSTVM: {
      title: 'Removed, Ended without an Exit',
      badge: 'reject', badgeText: 'lost the microVM',
      body: '<p>Any other has its microVM removed and is ended without an exit, so it reports <code>failed</code> and <code>Release</code> clears it. It logs <code>lost the microVM</code>.</p>'
    },
    ENDEDVM: {
      title: 'Ended, Still Running',
      badge: 'process', badgeText: 'recovery',
      body: '<p>An execution whose end was saved, but whose Firecracker still runs because the daemon stopped before shutting the guest down, has its microVM removed, so it holds no more of the node. Its record stays for <code>Result</code>, <code>ReadLogs</code> and <code>Release</code>. It logs <code>removing an ended microVM</code>.</p>'
    },
    RELEASE: {
      title: 'Release',
      badge: 'entry', badgeText: 'executor protocol',
      body: '<p>The last call for an execution, once the server has its result and output. An execution the daemon does not hold is released already. The agent&rsquo;s executor then deletes the image&rsquo;s snapshot.</p>'
    },
    REMOVE: {
      title: 'Remove Everything',
      badge: 'process', badgeText: 'release',
      body: '<p>Kills a Firecracker that still runs and waits up to 10 s for it to exit, then removes the tap, the jail, the cgroup and <code>&lt;state-dir&gt;/&lt;execution-id&gt;</code>, record and output included. Each step passes over what is already gone, so <code>Release</code> can be repeated, or finish one a crash cut short.</p><p><a href="../../docs/firecracker/#release">Release &rarr;</a></p>'
    }
  }
};
</script>

## Legend

| Node | Meaning |
|------|---------|
| <span class="dg-swatch dg-entry"></span> | Calls from the agent, and the daemon starting |
| <span class="dg-swatch dg-process"></span> | Processing step |
| <span class="dg-swatch dg-decision"></span> | Decision / branch |
| <span class="dg-swatch dg-filter"></span> | Request check |
| <span class="dg-swatch dg-storage"></span> | State: the record and the output |
| <span class="dg-swatch dg-success"></span> | The guest, inside the microVM |
| <span class="dg-swatch dg-background"></span> | Per-microVM tasks |
| <span class="dg-swatch dg-reject"></span> | Refusal / failure |
