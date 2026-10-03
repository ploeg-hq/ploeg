package main

import (
	"testing"
	"time"
)

func TestAccountRetries_AnUntriedAccountIsDue(t *testing.T) {
	retries := newAccountRetries(30 * time.Second)
	if !retries.due(7, time.Now()) {
		t.Fatal("an account that never failed was held back")
	}
}

func TestAccountRetries_AFailureHoldsTheAccountBackUntilItsWaitPasses(t *testing.T) {
	start := time.Now()
	retries := newAccountRetries(30 * time.Second)

	if wait := retries.failed(7, start); wait != 30*time.Second {
		t.Fatalf("first wait = %s, want 30s", wait)
	}
	if retries.due(7, start.Add(29*time.Second)) {
		t.Fatal("the account was retried before its wait passed")
	}
	if !retries.due(7, start.Add(30*time.Second)) {
		t.Fatal("the account was not retried once its wait passed")
	}
	if !retries.due(8, start) {
		t.Fatal("one account's failure held back another account")
	}
}

func TestAccountRetries_RepeatedFailuresDoubleTheWaitUpToAnHour(t *testing.T) {
	now := time.Now()
	retries := newAccountRetries(30 * time.Second)
	var waits []time.Duration
	for range 10 {
		waits = append(waits, retries.failed(7, now))
	}
	want := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute,
		16 * time.Minute, 32 * time.Minute, time.Hour, time.Hour, time.Hour}
	for i := range want {
		if waits[i] != want[i] {
			t.Fatalf("waits = %v, want %v", waits, want)
		}
	}
}

func TestAccountRetries_ASuccessClearsTheBackoff(t *testing.T) {
	now := time.Now()
	retries := newAccountRetries(30 * time.Second)
	retries.failed(7, now)
	retries.failed(7, now)
	retries.succeeded(7)

	if !retries.due(7, now) {
		t.Fatal("a recovered account stayed held back")
	}
	if wait := retries.failed(7, now); wait != 30*time.Second {
		t.Fatalf("wait after recovery = %s, want the first wait again", wait)
	}
}
