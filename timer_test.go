package main

import (
	"fmt"
	"testing"
	"time"
)

func TestTimer(t *testing.T) {
	timer := NewTimer()
	time.Sleep(time.Millisecond * 200)
	fmt.Printf("%v\n", timer.Now())
	now := timer.Now()
	if !(now >= time.Millisecond*200 && now < time.Millisecond*201) {
		t.Fail()
	}

	timer.Pause()
	time.Sleep(time.Millisecond * 200)
	fmt.Printf("%v\n", timer.Now())
	now = timer.Now()
	if !(now >= time.Millisecond*200 && now < time.Millisecond*201) {
		t.Fail()
	}

	timer.Start()
	time.Sleep(time.Millisecond * 300)
	fmt.Printf("%v\n", timer.Now())
	now = timer.Now()
	if !(now >= time.Millisecond*500 && now < time.Millisecond*501) {
		t.Fail()
	}

	timer.Jump(time.Millisecond * 500)
	fmt.Printf("%v\n", timer.Now())
	now = timer.Now()
	if !(now >= time.Second && now < time.Second + time.Millisecond) {
		t.Fail()
	}
}

func BenchmarkTimer(b *testing.B) {
	timer := NewTimer()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		timer.Now()
	}
}
