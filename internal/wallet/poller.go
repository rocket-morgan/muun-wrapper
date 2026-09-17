package wallet

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"
)

// RunPoller synchronizes incoming Lightning swaps immediately and then at interval.
func (m *Manager) RunPoller(ctx context.Context, interval time.Duration, reportError func(error)) {
	go runPoller(ctx, interval, func(ctx context.Context) error {
		_, err := m.Sync(ctx)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}, reportError)
}

func runPoller(
	ctx context.Context,
	interval time.Duration,
	sync func(context.Context) error,
	reportError func(error),
) {
	if interval <= 0 {
		if reportError != nil {
			reportError(fmt.Errorf("poll interval must be positive: %s", interval))
		}
		return
	}
	if reportError == nil {
		reportError = func(error) {}
	}

	run := func() bool {
		if err := sync(ctx); err != nil {
			reportError(err)
		}
		return ctx.Err() == nil
	}
	if !run() {
		return
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !run() {
				return
			}
		}
	}
}
