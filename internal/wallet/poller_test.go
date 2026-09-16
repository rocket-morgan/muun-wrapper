package wallet

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestRunPollerSyncsImmediatelyAndStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	calls := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		runPoller(ctx, time.Hour, func(context.Context) error {
			calls <- struct{}{}
			return nil
		}, func(error) {})
		close(done)
	}()

	select {
	case <-calls:
	case <-time.After(time.Second):
		t.Fatal("poller did not run its initial sync")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("poller did not stop after cancellation")
	}
}

func TestRunPollerReportsSyncErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	want := errors.New("sync failed")
	reported := make(chan error, 1)
	go runPoller(ctx, time.Hour, func(context.Context) error {
		return want
	}, func(err error) {
		reported <- err
	})

	select {
	case got := <-reported:
		if !errors.Is(got, want) {
			t.Fatalf("reported error = %v, want %v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("poller did not report the sync error")
	}
}

func TestManagerPollerIgnoresWalletNotCreatedYet(t *testing.T) {
	manager, err := NewManager(Config{
		DataDir:    t.TempDir(),
		Passphrase: "test passphrase",
		HoustonURL: "https://example.test/houston/",
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reported := make(chan error, 1)
	manager.RunPoller(ctx, time.Hour, func(err error) { reported <- err })

	select {
	case err := <-reported:
		if errors.Is(err, os.ErrNotExist) {
			t.Fatalf("wallet-not-created error was reported: %v", err)
		}
	case <-time.After(100 * time.Millisecond):
	}
}
