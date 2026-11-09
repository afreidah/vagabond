---
title: "Diagrams"
seoTitle: "Vagabond Diagrams"
description: "Interactive diagrams of Vagabond's architecture, task path, scheduling, quotas, agents, live output, execution states, background services and database schema, with implementation notes on every component."
cascade:
  type: docs
---

Interactive diagrams of Vagabond's internals. **Hover over any component** for
what it does and which package owns it. A component that another diagram covers
in detail links to it, and its panel stays in place so the link can be clicked.

{{< cards >}}
  {{< card link="architecture/" title="System Architecture" icon="cube" subtitle="The CLI, the server's internals, the store, providers and agent nodes." >}}
  {{< card link="task-path/" title="Task Path" icon="arrow-right" subtitle="One run of a job, from admission and ranking to release, settle and reroute." >}}
  {{< card link="admission/" title="Admission and Ranking" icon="filter" subtitle="13 checks in four tiers, scorers, strict and weighted tiers, and selection." >}}
  {{< card link="quotas/" title="Quota Accounting" icon="scale" subtitle="Pools and namespace shares, headroom, reservation, settlement and the reaper." >}}
  {{< card link="agents/" title="Agents and Pools" icon="chip" subtitle="Agent startup, the mTLS session, placement, containerd and the grace period." >}}
  {{< card link="live-output/" title="Live Output" icon="status-online" subtitle="The CLI follower, the follow route, pool and Cloud Run streams, resume and 416." >}}
  {{< card link="execution-states/" title="Execution States" icon="check-circle" subtitle="Every execution and dispatch state, each legal transition, and what writes it." >}}
  {{< card link="background-services/" title="Background Services" icon="refresh" subtitle="Every server and agent loop, its interval, what it touches and how it fails." >}}
  {{< card link="database-schema/" title="Database Schema" icon="database" subtitle="Tables, keys and indexes of the Postgres store, and the queries that read and write each." >}}
{{< /cards >}}
