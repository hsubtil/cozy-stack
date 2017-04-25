// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// GOMAXPROCS=10 go test

package lock

import (
	"fmt"
	"os"
	"runtime"
	"sync/atomic"
	"testing"

	"github.com/cozy/cozy-stack/pkg/config"
)

func reader(rwm ErrorRWLocker, iterations int, activity *int32, cdone chan bool) {
	for i := 0; i < iterations; i++ {
		err := rwm.RLock()
		if err != nil {
			panic(err)
		}
		n := atomic.AddInt32(activity, 1)
		if n < 1 || n >= 10000 {
			panic(fmt.Sprintf("wlock(%d)\n", n))
		}
		for i := 0; i < 100; i++ {
		}
		atomic.AddInt32(activity, -1)
		err = rwm.RUnlock()
		if err != nil {
			panic(err)
		}
	}
	cdone <- true
}

func writer(rwm ErrorRWLocker, iterations int, activity *int32, cdone chan bool) {
	for i := 0; i < iterations; i++ {
		err := rwm.Lock()
		if err != nil {
			panic(err)
		}
		n := atomic.AddInt32(activity, 10000)
		if n != 10000 {
			panic(fmt.Sprintf("wlock(%d)\n", n))
		}
		for i := 0; i < 100; i++ {
		}
		atomic.AddInt32(activity, -10000)
		err = rwm.Unlock()
		if err != nil {
			panic(err)
		}
	}
	cdone <- true
}

func HammerRWMutex(locker ErrorRWLocker, gomaxprocs, numReaders, iterations int) {
	runtime.GOMAXPROCS(gomaxprocs)
	// Number of active readers + 10000 * number of active writers.
	var activity int32
	cdone := make(chan bool)
	go writer(locker, iterations, &activity, cdone)
	var i int
	for i = 0; i < numReaders/2; i++ {
		go reader(locker, iterations, &activity, cdone)
	}
	go writer(locker, iterations, &activity, cdone)
	for ; i < numReaders; i++ {
		go reader(locker, iterations, &activity, cdone)
	}
	// Wait for the 2 writers and all readers to finish.
	for i := 0; i < 2+numReaders; i++ {
		<-cdone
	}
}

var n = 1000

func TestMemLock(t *testing.T) {
	c := config.GetConfig()
	backConfig := c.Lock
	c.Lock = config.Lock{URL: ""}
	globalRedisClient = nil
	defer func() { c.Lock = backConfig }()
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(-1))
	l := ReadWrite("test-mem")
	HammerRWMutex(l, 1, 1, n)
	HammerRWMutex(l, 1, 3, n)
	HammerRWMutex(l, 1, 10, n)
	HammerRWMutex(l, 4, 1, n)
	HammerRWMutex(l, 4, 3, n)
	HammerRWMutex(l, 4, 10, n)
	HammerRWMutex(l, 10, 1, n)
	HammerRWMutex(l, 10, 3, n)
	HammerRWMutex(l, 10, 10, n)
	HammerRWMutex(l, 10, 5, n)
}

func TestRedisLock(t *testing.T) {
	c := config.GetConfig()
	backConfig := c.Lock
	c.Lock = config.Lock{URL: "redis://localhost:6379/0"}
	globalRedisClient = nil
	defer func() { c.Lock = backConfig }()
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(-1))
	l := ReadWrite("test-redis")
	HammerRWMutex(l, 1, 1, n)
	HammerRWMutex(l, 1, 3, n)
	HammerRWMutex(l, 1, 10, n)
	HammerRWMutex(l, 4, 1, n)
	HammerRWMutex(l, 4, 3, n)
	HammerRWMutex(l, 4, 10, n)
	HammerRWMutex(l, 10, 1, n)
	HammerRWMutex(l, 10, 3, n)
	HammerRWMutex(l, 10, 10, n)
	HammerRWMutex(l, 10, 5, n)
}

func TestMain(m *testing.M) {
	config.UseTestFile()
	if testing.Short() {
		n = 5
	}
	os.Exit(m.Run())
}
