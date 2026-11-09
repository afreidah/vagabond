// Package scheduler decides which providers can run a task and which one
// should.
//
// Admission answers the first question and is a pure function: it reads a
// capability snapshot and a quota snapshot, both gathered beforehand, and
// reaches over no network. That is what keeps job plan fast, free, and free of
// side effects, and it makes admission testable from literals alone.
//
// Nothing here knows what a cloud is. Admission produces normalized candidates
// and rejections; the scheduler scores candidates. If either ever needs to know
// that a candidate is IBM, the capability model is missing something and that
// is where the fix belongs. depguard enforces the boundary.
//
// Admit runs the checkers in checks_*.go, Rank scores and orders the
// candidates admitted, and PlanTask runs both for job plan.
package scheduler
