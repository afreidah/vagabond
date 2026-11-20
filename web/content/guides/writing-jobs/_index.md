---
title: "Writing Jobs"
description: "Writing Vagabond job files: a first job, resources and timeouts, parameters and metadata, routing, and constraints and affinities, each checked with job validate and job plan."
weight: 35
---

A job file describes what to run; Vagabond decides where it runs. These pages
build a job up one block at a time, checking each with `job validate`, which
needs no server, and `job plan`, which shows where it would run without
running it. Every block and attribute is in the
[job specification](../../docs/job-specification.md).

![A job file holds one job. Its parameterized block is checked when a run is submitted, against the metadata the run supplies. Its routing block, with the providers list, constraints and affinities, and each task's resources and timeout are read by admission and ranking to choose a provider for the task. Each task's config and env are handed to the chosen provider, which runs the image sized by resources and stops it at timeout.](../../docs/assets/job-anatomy.svg)

{{< cards >}}
  {{< card link="first-job/" title="Your First Job" icon="document-text" subtitle="A container task on whatever providers the server has: validated, planned, run, and its output read back." >}}
  {{< card link="resources-and-timeouts/" title="Resources and Timeouts" icon="chip" subtitle="What a task asks for, how a plan checks it against each provider, and what it charges against quota." >}}
  {{< card link="parameters-and-metadata/" title="Parameters and Metadata" icon="variable" subtitle="Values a run must supply, how they reach the task, and what dispatch refuses." >}}
  {{< card link="routing/" title="Routing" icon="switch-horizontal" subtitle="Which providers a job may use, in what order, and at what cost." >}}
  {{< card link="constraints-and-affinities/" title="Constraints and Affinities" icon="adjustments" subtitle="Requirements that reject providers, and preferences that rank them." >}}
{{< /cards >}}
