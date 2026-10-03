package main

import "time"

const accountRetryCeiling = time.Hour

type accountRetry struct {
	due  time.Time
	wait time.Duration
}

type accountRetries struct {
	firstWait time.Duration
	pending   map[int64]accountRetry
}

func newAccountRetries(firstWait time.Duration) *accountRetries {
	return &accountRetries{firstWait: firstWait, pending: map[int64]accountRetry{}}
}

func (r *accountRetries) due(runID int64, now time.Time) bool {
	retry, waiting := r.pending[runID]
	return !waiting || !now.Before(retry.due)
}

func (r *accountRetries) failed(runID int64, now time.Time) time.Duration {
	wait := r.firstWait
	if previous, waiting := r.pending[runID]; waiting {
		wait = min(previous.wait*2, accountRetryCeiling)
	}
	r.pending[runID] = accountRetry{due: now.Add(wait), wait: wait}
	return wait
}

func (r *accountRetries) succeeded(runID int64) {
	delete(r.pending, runID)
}
