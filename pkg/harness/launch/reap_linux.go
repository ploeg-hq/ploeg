package launch

import (
	"bytes"
	"context"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

const reapEvery = 5 * time.Second

func (s *server) reapOrphans(ctx context.Context) {
	exits := make(chan os.Signal, 1)
	signal.Notify(exits, syscall.SIGCHLD)
	defer signal.Stop(exits)
	tick := time.NewTicker(reapEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-exits:
		case <-tick.C:
		}
		for _, pid := range s.orphanZombies() {
			var status syscall.WaitStatus
			_, _ = syscall.Wait4(pid, &status, syscall.WNOHANG, nil)
		}
	}
}

func (s *server) orphanZombies() []int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var zombies []int
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid == s.pid {
			continue
		}
		stat, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "stat"))
		if err != nil {
			continue
		}
		state, parent, ok := stateAndParent(stat)
		if !ok || state != 'Z' || parent != s.pid {
			continue
		}
		s.mu.Lock()
		own := s.children[pid]
		s.mu.Unlock()
		if !own {
			zombies = append(zombies, pid)
		}
	}
	return zombies
}

func stateAndParent(stat []byte) (byte, int, bool) {
	end := bytes.LastIndexByte(stat, ')')
	if end < 0 {
		return 0, 0, false
	}
	fields := bytes.Fields(stat[end+1:])
	if len(fields) < 2 || len(fields[0]) != 1 {
		return 0, 0, false
	}
	parent, err := strconv.Atoi(string(fields[1]))
	if err != nil {
		return 0, 0, false
	}
	return fields[0][0], parent, true
}
