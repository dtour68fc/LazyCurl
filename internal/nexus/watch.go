package nexus

import (
	"context"
	"time"

	nexuscurlv1 "github.com/dtour68fc/LazyCurl/internal/nexus/pb/v1"
)

// maxConsecutiveSubscribeFailures is how many times in a row the
// Subscribe stream can fail to even connect before WatchWithReconnect
// gives up and calls onGiveUp - at that point something's actually wrong
// (server down, network gone), not just a blip, and the caller is
// expected to fall back to the local workspace rather than retry forever.
const maxConsecutiveSubscribeFailures = 3

// subscribeHealthyDuration is how long a stream has to stay open before a
// later failure resets the consecutive-failure counter back to zero -
// otherwise a connection that's been fine for hours and drops once would
// immediately count as 1 of only 3 allowed failures.
const subscribeHealthyDuration = 30 * time.Second

// WatchWithReconnect keeps a project's Subscribe stream open, calling
// onNotify for every ChangeNotification received, transparently
// reconnecting (with backoff) on drops. After
// maxConsecutiveSubscribeFailures reconnect attempts fail in a row, it
// stops retrying and calls onGiveUp exactly once. Returns a cancel func
// that stops everything early (e.g. on manual workspace switch).
func WatchWithReconnect(client *Client, project string, onNotify func(*nexuscurlv1.ChangeNotification), onGiveUp func(error)) (cancel func()) {
	ctx, cancelFn := context.WithCancel(context.Background())

	go func() {
		backoff := time.Second
		failures := 0

		for {
			if ctx.Err() != nil {
				return
			}

			connectedAt := time.Now()
			ch, err := client.Subscribe(ctx, project)
			if err != nil {
				failures++
				if failures >= maxConsecutiveSubscribeFailures {
					onGiveUp(err)
					return
				}
				if !sleepOrDone(ctx, backoff) {
					return
				}
				backoff = nextBackoff(backoff)
				continue
			}

			var streamErr error
			for notif := range ch {
				onNotify(notif)
			}
			// Channel closed - either ctx was canceled (graceful) or the
			// stream itself ended/errored. There's no error value surfaced
			// by the channel today, so treat any close as a drop unless we
			// were the ones who canceled it.
			if ctx.Err() != nil {
				return
			}

			if time.Since(connectedAt) >= subscribeHealthyDuration {
				failures = 0
				backoff = time.Second
			} else {
				failures++
				if failures >= maxConsecutiveSubscribeFailures {
					onGiveUp(streamErr)
					return
				}
			}
			if !sleepOrDone(ctx, backoff) {
				return
			}
			backoff = nextBackoff(backoff)
		}
	}()

	return cancelFn
}

func sleepOrDone(ctx context.Context, d time.Duration) bool {
	select {
	case <-time.After(d):
		return true
	case <-ctx.Done():
		return false
	}
}

func nextBackoff(d time.Duration) time.Duration {
	next := d * 2
	if next > 10*time.Second {
		return 10 * time.Second
	}
	return next
}
