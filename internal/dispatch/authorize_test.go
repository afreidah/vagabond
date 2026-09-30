// -------------------------------------------------------------------------------
// Rejected Credential Tests
//
// Author: Alex Freidah
//
// A call the platform rejects as unauthorized has its credential resolved
// again and is made once more; a provider that cannot take a new credential
// fails as it did.
// -------------------------------------------------------------------------------

package dispatch

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/plugin"
)

// expiringProvider rejects submissions as unauthorized until it is handed a
// new credential, as a provider holding an expired session token does.
type expiringProvider struct {
	scriptedProvider

	fresh           bool
	recredentialErr error // refused as a new credential that is unusable is
}

// Submit rejects the call while the credential is stale.
func (p *expiringProvider) Submit(ctx context.Context, id execution.ID, task *job.Task) (plugin.Submission, error) {
	if !p.fresh {
		p.submits++

		return plugin.Submission{}, plugin.ClassifyHTTP(http.StatusForbidden, 0, errors.New("expired token"))
	}

	return p.scriptedProvider.Submit(ctx, id, task)
}

// Recredential takes the new credential.
func (p *expiringProvider) Recredential(context.Context, []byte) error {
	if p.recredentialErr != nil {
		return p.recredentialErr
	}

	p.fresh = true

	return nil
}

// A rejected submission is made again once the credential is refreshed, and
// the task runs.
func TestAuthorized_RefreshesAndRetries(t *testing.T) {
	t.Parallel()

	p := &expiringProvider{scriptedProvider: scriptedProvider{
		name: "a", pollsToFinish: 1, finalState: execution.StateSucceeded,
	}}

	reg := newRegistry(&p.scriptedProvider)
	reg.providers["a"] = p

	outcome, err := newDispatcher(t, reg).RunTask(t.Context(), origin, containerTask(t, nil), nil, nil)
	if err != nil || !outcome.Succeeded() {
		t.Fatalf("RunTask() = %+v, %v; want a success after the refresh", outcome, err)
	}

	if reg.recredentialed != 1 || p.submits != 2 {
		t.Errorf("refreshed %d times over %d submissions, want 1 over 2", reg.recredentialed, p.submits)
	}
}

// A refresh that fails is reported beside the rejection it was answering, and
// the call is not made again on a credential known to be bad.
func TestAuthorized_RefreshFails(t *testing.T) {
	t.Parallel()

	unusable := errors.New("unusable key")
	p := &expiringProvider{
		scriptedProvider: scriptedProvider{name: "a"},
		recredentialErr:  unusable,
	}

	reg := newRegistry(&p.scriptedProvider)
	reg.providers["a"] = p

	_, err := newDispatcher(t, reg).RunTask(t.Context(), origin, containerTask(t, nil), nil, nil)
	if !errors.Is(err, plugin.ErrUnauthorized) || !errors.Is(err, unusable) {
		t.Errorf("RunTask() = %v, want the rejection and the refresh failure", err)
	}

	if p.submits != 1 {
		t.Errorf("submitted %d times, want 1", p.submits)
	}
}

// A provider that cannot take a new credential fails as it did, with no
// retry.
func TestAuthorized_NothingToRefresh(t *testing.T) {
	t.Parallel()

	p := &scriptedProvider{
		name:      "a",
		submitErr: plugin.ClassifyHTTP(http.StatusForbidden, 0, errors.New("denied")),
	}

	reg := newRegistry(p)

	if _, err := newDispatcher(t, reg).RunTask(t.Context(), origin, containerTask(t, nil), nil, nil); !errors.Is(err, plugin.ErrUnauthorized) {
		t.Errorf("RunTask() = %v, want the rejection", err)
	}

	if reg.recredentialed != 0 || p.submits != 1 {
		t.Errorf("refreshed %d times over %d submissions, want 0 over 1", reg.recredentialed, p.submits)
	}
}
