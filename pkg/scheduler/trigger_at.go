package scheduler

import (
	"time"

	"github.com/cozy/cozy-stack/pkg/consts"
	"github.com/cozy/cozy-stack/pkg/jobs"
)

// maxPastTriggerTime is the maximum duration in the past for which the at
// triggers are executed immediately instead of discarded.
var maxPastTriggerTime = 24 * time.Hour

// AtTrigger implements the @at trigger type. It schedules a job at a specified
// time in the future.
type AtTrigger struct {
	at   time.Time
	in   *TriggerInfos
	done chan struct{}
}

// NewAtTrigger returns a new instance of AtTrigger given the specified
// options.
func NewAtTrigger(infos *TriggerInfos) (*AtTrigger, error) {
	at, err := time.Parse(time.RFC3339, infos.Arguments)
	if err != nil {
		return nil, ErrMalformedTrigger
	}
	return &AtTrigger{
		at:   at,
		in:   infos,
		done: make(chan struct{}),
	}, nil
}

// NewInTrigger returns a new instance of AtTrigger given the specified
// options as @in.
func NewInTrigger(infos *TriggerInfos) (*AtTrigger, error) {
	d, err := time.ParseDuration(infos.Arguments)
	if err != nil {
		return nil, ErrMalformedTrigger
	}
	at := time.Now().Add(d)
	return &AtTrigger{
		at:   at,
		in:   infos,
		done: make(chan struct{}),
	}, nil
}

// Type implements the Type method of the Trigger interface.
func (a *AtTrigger) Type() string {
	return a.in.Type
}

// DocType implements the permissions.Validable interface
func (a *AtTrigger) DocType() string {
	return consts.Triggers
}

// ID implements the permissions.Validable interface
func (a *AtTrigger) ID() string {
	return a.in.TID
}

// Valid implements the permissions.Validable interface
func (a *AtTrigger) Valid(key, value string) bool {
	switch key {
	case jobs.WorkerType:
		return a.in.WorkerType == value
	}
	return false
}

// Schedule implements the Schedule method of the Trigger interface.
func (a *AtTrigger) Schedule() <-chan *jobs.JobRequest {
	ch := make(chan *jobs.JobRequest)
	go func() {
		duration := -time.Since(a.at)
		if duration < 0 {
			if duration > -maxPastTriggerTime {
				ch <- a.Trigger()
			}
			close(ch)
			return
		}
		select {
		case <-time.After(duration):
			ch <- a.Trigger()
		case <-a.done:
		}
		close(ch)
	}()
	return ch
}

// Trigger returns the triggered job request
func (a *AtTrigger) Trigger() *jobs.JobRequest {
	return &jobs.JobRequest{
		Domain:     a.in.Domain,
		WorkerType: a.in.WorkerType,
		Message:    a.in.Message,
		Options:    a.in.Options,
	}
}

// Unschedule implements the Unschedule method of the Trigger interface.
func (a *AtTrigger) Unschedule() {
	close(a.done)
}

// Infos implements the Infos method of the Trigger interface.
func (a *AtTrigger) Infos() *TriggerInfos {
	return a.in
}

var _ Trigger = &AtTrigger{}
