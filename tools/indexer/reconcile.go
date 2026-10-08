package indexer

import (
	"context"
	"fmt"
	"time"
)

// Reconcile runs Sync every interval until ctx is cancelled. It recovers anything the watcher
// missed, such as events dropped by the operating system or files changed while the watcher
// was not running. A failed Sync is logged and the next tick tries again.
func (x *Indexer) Reconcile(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		return fmt.Errorf("reconcile interval must be positive")
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if _, err := x.Sync(ctx); err != nil && ctx.Err() == nil {
				x.log.Error("reconcile failed", "err", err)
			}
		}
	}
}
