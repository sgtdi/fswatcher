package fswatcher

import "context"

// startCancellationWakeup signals a blocked native loop when ctx is canceled.
// Stop and join the signal goroutine before releasing its native descriptor,
// including when the loop stops on an error before the context is canceled.
func startCancellationWakeup(ctx context.Context, wake func()) (stop func()) {
	stopping, exited := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(exited)
		select {
		case <-ctx.Done():
			wake()
		case <-stopping:
		}
	}()
	return func() {
		close(stopping)
		<-exited
	}
}
