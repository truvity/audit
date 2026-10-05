// Package health serves a process's readiness: whether it can do its work now,
// as against /healthz, which says only that the process is up and answering.
//
// A pod that is up and cannot reach its database is not one to send traffic to
// and not one to restart (a restart does not bring the database back), so the
// two are different questions with different probes: liveness reads /healthz,
// readiness reads /readyz. Readiness never reports why beyond the name of what
// failed: the endpoint is reachable by anything that can reach the pod, and an
// error from a dependency can carry an address.
package health

import (
	"context"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// Check is one thing readiness depends on: its name is what the response says
// when it fails, and Fn is nil-error when it holds.
type Check struct {
	Name string
	Fn   func(context.Context) error
}

// Timeout is how long all the checks together may take. A probe waits for the
// answer for a second or so, and a dependency that does not answer in time is
// not ready.
const Timeout = 2 * time.Second

// Ready answers 200 when every check passes and 503, naming the checks that did
// not, when any fails. The checks run in parallel, at every probe: they must be
// cheap (a ping, a one-object list). With no checks it is as ready as the
// process is up, which is /healthz's answer.
func Ready(log *slog.Logger, checks ...Check) http.Handler {
	if log == nil {
		log = slog.Default()
	}
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), Timeout)
		defer cancel()
		var (
			mu     sync.Mutex
			failed []string
			wg     sync.WaitGroup
		)
		for _, c := range checks {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := c.Fn(ctx); err != nil {
					// The reason is for the log: the response carries no address.
					log.Warn("not ready", "check", c.Name, "error", err)
					mu.Lock()
					failed = append(failed, c.Name)
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		if len(failed) > 0 {
			sort.Strings(failed)
			http.Error(rw, "not ready: "+strings.Join(failed, ", "), http.StatusServiceUnavailable)
			return
		}
		rw.WriteHeader(http.StatusOK)
	})
}
