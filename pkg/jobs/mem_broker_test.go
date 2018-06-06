package jobs

import (
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cozy/cozy-stack/pkg/prefixer"
	"github.com/stretchr/testify/assert"
)

var localDB = prefixer.NewPrefixer("cozy.local", "cozy.local")

func TestProperSerial(t *testing.T) {
	job := NewJob(prefixer.NewPrefixer("cozy.tools:8080", "cozy.tools:8080"),
		&JobRequest{
			WorkerType: "",
		})
	assert.NoError(t, job.Create())
	assert.NoError(t, job.AckConsumed())
	job2, err := Get(job, job.ID())
	assert.NoError(t, err)
	assert.Equal(t, State(Running), job2.State)
}

func TestMessageMarshalling(t *testing.T) {
	data := []byte(`{"Data": "InZhbHVlIgo=", "Type": "json"}`)
	var m Message
	assert.NoError(t, json.Unmarshal(data, &m))
	var s string
	assert.NoError(t, m.Unmarshal(&s))
	assert.Equal(t, "value", s)

	data = []byte(`"value2"`)
	assert.NoError(t, json.Unmarshal(data, &m))
	assert.NoError(t, m.Unmarshal(&s))
	assert.Equal(t, "value2", s)

	data = []byte(`{
		"domain": "cozy.local",
		"worker": "foo",
		"message": {"Data": "InZhbHVlIgo=", "Type": "json"}
}`)

	var j Job
	assert.NoError(t, json.Unmarshal(data, &j))
	assert.Equal(t, "cozy.local", j.Domain)
	assert.Equal(t, "foo", j.WorkerType)
	assert.EqualValues(t, []byte(`"value"`), j.Message)

	var err error
	var j2 Job
	data, err = json.Marshal(j)
	assert.NoError(t, err)
	assert.NoError(t, json.Unmarshal(data, &j2))
	assert.Equal(t, "cozy.local", j2.Domain)
	assert.Equal(t, "foo", j2.WorkerType)
	assert.EqualValues(t, []byte(`"value"`), j2.Message)

	assert.EqualValues(t, &j2, j2.Clone())
}

func TestInMemoryJobs(t *testing.T) {
	n := 10
	v := 100

	var w sync.WaitGroup

	var workersTestList = WorkersList{
		{
			WorkerType:  "test",
			Concurrency: 4,
			WorkerFunc: func(ctx *WorkerContext) error {
				var msg string
				err := ctx.UnmarshalMessage(&msg)
				if !assert.NoError(t, err) {
					return err
				}
				if strings.HasPrefix(msg, "a-") {
					_, err := strconv.Atoi(msg[len("a-"):])
					assert.NoError(t, err)
				} else if strings.HasPrefix(msg, "b-") {
					_, err := strconv.Atoi(msg[len("b-"):])
					assert.NoError(t, err)
				} else {
					t.Fatal()
				}
				w.Done()
				return nil
			},
		},
	}

	broker1 := NewMemBroker()
	broker2 := NewMemBroker()
	broker1.StartWorkers(workersTestList)
	broker2.StartWorkers(workersTestList)
	w.Add(2)

	go func() {
		for i := 0; i < n; i++ {
			w.Add(1)
			msg, _ := NewMessage("a-" + strconv.Itoa(i+1))
			_, err := broker1.PushJob(localDB, &JobRequest{
				WorkerType: "test",
				Message:    msg,
			})
			assert.NoError(t, err)
			time.Sleep(randomMicro(0, v))
		}
		w.Done()
	}()

	go func() {
		for i := 0; i < n; i++ {
			w.Add(1)
			msg, _ := NewMessage("b-" + strconv.Itoa(i+1))
			_, err := broker2.PushJob(localDB, &JobRequest{
				WorkerType: "test",
				Message:    msg,
			})
			assert.NoError(t, err)
			time.Sleep(randomMicro(0, v))
		}
		w.Done()
	}()

	w.Wait()
}

func TestUnknownWorkerError(t *testing.T) {
	broker := NewMemBroker()
	broker.StartWorkers(WorkersList{})
	_, err := broker.PushJob(localDB, &JobRequest{
		WorkerType: "nope",
		Message:    nil,
	})
	assert.Error(t, err)
	assert.Equal(t, ErrUnknownWorker, err)
}

func TestUnknownMessageType(t *testing.T) {
	var w sync.WaitGroup

	broker := NewMemBroker()
	broker.StartWorkers(WorkersList{
		{
			WorkerType:  "test",
			Concurrency: 4,
			WorkerFunc: func(ctx *WorkerContext) error {
				var msg string
				err := ctx.UnmarshalMessage(&msg)
				assert.Error(t, err)
				assert.Equal(t, ErrMessageNil, err)
				w.Done()
				return nil
			},
		},
	})

	w.Add(1)
	_, err := broker.PushJob(localDB, &JobRequest{
		WorkerType: "test",
		Message:    nil,
	})

	assert.NoError(t, err)
	w.Wait()
}

func TestTimeout(t *testing.T) {
	var w sync.WaitGroup

	broker := NewMemBroker()
	broker.StartWorkers(WorkersList{
		{
			WorkerType:   "timeout",
			Concurrency:  1,
			MaxExecCount: 1,
			Timeout:      1 * time.Millisecond,
			WorkerFunc: func(ctx *WorkerContext) error {
				<-ctx.Done()
				w.Done()
				return ctx.Err()
			},
		},
	})

	w.Add(1)
	_, err := broker.PushJob(localDB, &JobRequest{
		WorkerType: "timeout",
		Message:    nil,
	})

	assert.NoError(t, err)
	w.Wait()
}

func TestRetry(t *testing.T) {
	var w sync.WaitGroup

	maxExecCount := 4

	var count int
	broker := NewMemBroker()
	broker.StartWorkers(WorkersList{
		{
			WorkerType:   "test",
			Concurrency:  1,
			MaxExecCount: maxExecCount,
			Timeout:      1 * time.Millisecond,
			RetryDelay:   1 * time.Millisecond,
			WorkerFunc: func(ctx *WorkerContext) error {
				<-ctx.Done()
				w.Done()
				count++
				if count < maxExecCount {
					return ctx.Err()
				}
				return nil
			},
		},
	})

	w.Add(maxExecCount)
	_, err := broker.PushJob(localDB, &JobRequest{
		WorkerType: "test",
		Message:    nil,
	})

	assert.NoError(t, err)
	w.Wait()
}

func TestPanicRetried(t *testing.T) {
	var w sync.WaitGroup

	maxExecCount := 4

	broker := NewMemBroker()
	broker.StartWorkers(WorkersList{
		{
			WorkerType:   "panic",
			Concurrency:  1,
			MaxExecCount: maxExecCount,
			RetryDelay:   1 * time.Millisecond,
			WorkerFunc: func(ctx *WorkerContext) error {
				w.Done()
				panic("oops")
			},
		},
	})

	w.Add(maxExecCount)
	_, err := broker.PushJob(localDB, &JobRequest{
		WorkerType: "panic",
		Message:    nil,
	})

	assert.NoError(t, err)
	w.Wait()
}

func TestPanic(t *testing.T) {
	var w sync.WaitGroup

	even, _ := NewMessage(0)
	odd, _ := NewMessage(1)

	broker := NewMemBroker()
	broker.StartWorkers(WorkersList{
		{
			WorkerType:   "panic2",
			Concurrency:  1,
			MaxExecCount: 1,
			RetryDelay:   1 * time.Millisecond,
			WorkerFunc: func(ctx *WorkerContext) error {
				var i int
				if err := ctx.UnmarshalMessage(&i); err != nil {
					return err
				}
				if i%2 != 0 {
					panic("oops")
				}
				w.Done()
				return nil
			},
		},
	})
	w.Add(2)
	var err error
	_, err = broker.PushJob(localDB, &JobRequest{WorkerType: "panic2", Message: odd})
	assert.NoError(t, err)
	_, err = broker.PushJob(localDB, &JobRequest{WorkerType: "panic2", Message: even})
	assert.NoError(t, err)
	_, err = broker.PushJob(localDB, &JobRequest{WorkerType: "panic2", Message: odd})
	assert.NoError(t, err)
	_, err = broker.PushJob(localDB, &JobRequest{WorkerType: "panic2", Message: even})
	assert.NoError(t, err)
	w.Wait()
}
