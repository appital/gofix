package internal

import (
	"sync"
	"time"
)

type EventTimer struct {
	f     func()
	timer *time.Timer
	done  chan struct{}
	wg    sync.WaitGroup
	once  sync.Once
}

func NewEventTimer(task func()) *EventTimer {
	t := &EventTimer{
		f:     task,
		timer: newStoppedTimer(),
		done:  make(chan struct{}),
	}

	t.wg.Go(func() {

		for {
			select {

			case <-t.timer.C:
				t.f()

			case <-t.done:
				t.timer.Stop()
				return

			}
		}
	})

	return t
}

func (t *EventTimer) Stop() {
	if t == nil {
		return
	}

	t.once.Do(func() {
		close(t.done)
	})

	t.wg.Wait()
}

// Reset arranges for the task to run after timeout, or stops the timer if
// timeout is not positive.
func (t *EventTimer) Reset(timeout time.Duration) {
	if t == nil {
		return
	}

	if timeout <= 0 {
		t.timer.Stop()
		return
	}
	t.timer.Reset(timeout)
}

func newStoppedTimer() *time.Timer {
	timer := time.NewTimer(time.Second)
	if !timer.Stop() {
		<-timer.C
	}
	return timer
}
