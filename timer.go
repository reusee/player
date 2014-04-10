package main

import (
	"math"
	"sync"
	"time"
)

type Timer struct {
	control    chan int64
	now        time.Duration
	startTime  time.Time
	frozenTime time.Time
	paused     bool
	dirty      bool
	cond       *sync.Cond
}

const (
	TIMER_START = math.MaxInt64
	TIMER_PAUSE = math.MaxInt64 - 1
	TIMER_GET   = math.MaxInt64 - 2
)

func NewTimer() *Timer {
	timer := &Timer{
		control:   make(chan int64),
		startTime: time.Now(),
		cond:      sync.NewCond(new(sync.Mutex)),
	}
	go func() {
		for {
			req := <-timer.control
			switch req {
			case TIMER_START:
				if timer.paused {
					timer.startTime = timer.startTime.Add(time.Now().Sub(timer.frozenTime))
					timer.paused = false
				}
			case TIMER_PAUSE:
				if !timer.paused {
					timer.frozenTime = time.Now()
					timer.paused = true
				}
			case TIMER_GET:
				if timer.paused {
					timer.now = timer.frozenTime.Sub(timer.startTime)
				} else {
					timer.now = time.Now().Sub(timer.startTime)
				}
			default:
				timer.startTime = timer.startTime.Add(-time.Duration(req))
			}
			timer.cond.L.Lock()
			timer.dirty = false
			timer.cond.Broadcast()
			timer.cond.L.Unlock()
		}
	}()
	return timer
}

func (self *Timer) Now() time.Duration {
	self.dirty = true
	self.control <- TIMER_GET

	self.cond.L.Lock()
	for self.dirty {
		self.cond.Wait()
	}
	self.cond.L.Unlock()

	return self.now
}

func (self *Timer) Start() {
	self.control <- TIMER_START

	self.cond.L.Lock()
	for self.dirty {
		self.cond.Wait()
	}
	self.cond.L.Unlock()
}

func (self *Timer) Pause() {
	self.control <- TIMER_PAUSE

	self.cond.L.Lock()
	for self.dirty {
		self.cond.Wait()
	}
	self.cond.L.Unlock()
}

func (self *Timer) Jump(d time.Duration) {
	self.control <- int64(d)

	self.cond.L.Lock()
	for self.dirty {
		self.cond.Wait()
	}
	self.cond.L.Unlock()
}
