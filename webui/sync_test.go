package webui

import (
	"testing"
	"time"
)

func TestNextSyncTime(t *testing.T) {
	loc := time.FixedZone("x", 0)
	now := time.Date(2026, 10, 17, 4, 0, 0, 0, loc)
	n := nextSyncTime(now, []string{"03:00"})
	if n.Format("2006-01-02 15:04") != "2026-10-18 03:00" {
		t.Fatal(n)
	}
	n = nextSyncTime(now, []string{"03:00", "15:30"})
	if n.Format("2006-01-02 15:04") != "2026-10-17 15:30" {
		t.Fatal(n)
	}
	if !nextSyncTime(now, nil).IsZero() {
		t.Fatal("expected zero")
	}
	got, err := normalizeSyncTimes([]string{"15:30", "3:00", "03:00"})
	if err == nil {
		t.Logf("got %v", got)
	}
	if _, err := normalizeSyncTimes([]string{"25:00"}); err == nil {
		t.Fatal("expected error")
	}
	if _, err := normalizeSyncTimes([]string{"abc"}); err == nil {
		t.Fatal("expected error")
	}
}
