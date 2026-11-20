---
title: "Guides"
seoTitle: "Vagabond Guides"
description: "Step-by-step walkthroughs for setting Vagabond up and using it: the server on Nomad, containers and Firecracker microVMs on agent nodes, writing jobs, CI on free cloud quota, and following and finding runs."
weight: 20
cascade:
  type: docs
---

Step-by-step walkthroughs, each from a working setup to a verified run, with
the configuration and command output at every step.

{{< cards >}}
  {{< card link="nomad-server/" title="Deploying the Server on Nomad" icon="cube" subtitle="The server as a Nomad job: state in Postgres, configuration and credentials from Vault, health checked through Consul." >}}
  {{< card link="containers-on-agent-nodes/" title="Running Jobs in Containers on Agent Nodes" icon="server" subtitle="An agent listener with mutual TLS from its own CA, the agent as a Nomad job with a delegated cgroup, and a containerd provider." >}}
  {{< card link="firecracker-microvms/" title="Running Jobs in Firecracker MicroVMs" icon="shield-check" subtitle="Prepare an agent node with KVM and a devmapper thin pool, start the agent with -firecracker, and run a job in a microVM." >}}
  {{< card link="writing-jobs/" title="Writing Jobs" icon="document-text" subtitle="A first job, resources and timeouts, parameters and metadata, routing, and constraints and affinities." >}}
  {{< card link="ci-on-free-quota/" title="Running CI on Free Cloud Quota" icon="cash" subtitle="Cloud Run and Code Engine with quota pools under their free allowances, a parameterized CI job, and a workflow that dispatches it." >}}
  {{< card link="following-runs/" title="Following and Finding Runs" icon="search" subtitle="Follow a run as it happens, find it again by job, state or provider, and read its dispatch, executions and output." >}}
{{< /cards >}}
