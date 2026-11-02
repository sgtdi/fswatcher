package fswatcher

import (
	"context"
	"testing"
	"testing/synctest"

	"github.com/stretchr/testify/require"
)

func TestCancellationWakeupCannotSignalAfterStop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		called := false
		stop := startCancellationWakeup(ctx, func() { called = true })
		stop()
		cancel()
		synctest.Wait()
		require.False(t, called, "late cancellation must not signal a released descriptor")
	})
}

func TestCancellationWakeupStopWaitsForInFlightSignal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		entered, release, stopped := make(chan struct{}), make(chan struct{}), make(chan struct{})
		stop := startCancellationWakeup(ctx, func() { close(entered); <-release })
		cancel()
		<-entered
		go func() { stop(); close(stopped) }()
		synctest.Wait()
		select {
		case <-stopped:
			close(release)
			t.Fatal("stop returned while the descriptor was still being signaled")
		default:
		}
		close(release)
		synctest.Wait()
		select {
		case <-stopped:
		default:
			t.Fatal("stop did not join the completed signal")
		}
	})
}
