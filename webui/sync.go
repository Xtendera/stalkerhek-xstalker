package webui

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Catalogue sync schedule: the movie/series listings are rebuilt only at the
// times chosen on the dashboard (local time), never on a timer, so a refresh
// can't land in the middle of a viewing session. Default: 03:00 daily.

var defaultSyncTimes = []string{"03:00"}

var (
	syncMu      sync.Mutex // guards the fields below
	syncTimes   = defaultSyncTimes
	syncLoaded  bool
	syncRunning bool
	syncLast    time.Time
	syncLastMsg string
	syncWake    = make(chan struct{}, 1)
	syncStart   sync.Once
)

func syncPath() string {
	dir := filepath.Dir(profilesFile)
	if dir == "" {
		dir = "."
	}
	return filepath.Join(dir, "sync.json")
}

func loadSyncTimesLocked() {
	if syncLoaded {
		return
	}
	syncLoaded = true
	b, err := os.ReadFile(syncPath())
	if err != nil {
		return
	}
	var v struct {
		Times []string `json:"times"`
	}
	if json.Unmarshal(b, &v) == nil {
		if t, err := normalizeSyncTimes(v.Times); err == nil {
			syncTimes = t
		}
	}
}

// normalizeSyncTimes validates "HH:MM" entries, dedupes and sorts them.
// An empty list is allowed (automatic sync off).
func normalizeSyncTimes(in []string) ([]string, error) {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		t, err := time.Parse("15:04", s)
		if err != nil {
			return nil, fmt.Errorf("%q is not a time like 03:00", s)
		}
		k := t.Format("15:04")
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out, nil
}

func getSyncTimes() []string {
	syncMu.Lock()
	defer syncMu.Unlock()
	loadSyncTimesLocked()
	return append([]string{}, syncTimes...)
}

func setSyncTimes(times []string) error {
	t, err := normalizeSyncTimes(times)
	if err != nil {
		return err
	}
	syncMu.Lock()
	loadSyncTimesLocked()
	syncTimes = t
	syncMu.Unlock()
	if b, err := json.Marshal(map[string]any{"times": t}); err == nil {
		_ = os.MkdirAll(filepath.Dir(syncPath()), 0o755)
		_ = os.WriteFile(syncPath(), b, 0o644)
	}
	LogInfo("SETTINGS", "catalogue sync times=%v", t)
	select {
	case syncWake <- struct{}{}:
	default:
	}
	return nil
}

// nextSyncTime returns the next scheduled time after now, or zero when none.
func nextSyncTime(now time.Time, times []string) time.Time {
	var best time.Time
	for _, s := range times {
		t, err := time.Parse("15:04", s)
		if err != nil {
			continue
		}
		c := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, now.Location())
		if !c.After(now) {
			c = c.AddDate(0, 0, 1)
		}
		if best.IsZero() || c.Before(best) {
			best = c
		}
	}
	return best
}

// refreshCatalogue rebuilds every running profile's movie and series listing.
// Old data keeps being served until the new data is ready.
func refreshCatalogue(reason string) bool {
	syncMu.Lock()
	if syncRunning {
		syncMu.Unlock()
		return false
	}
	syncRunning = true
	syncMu.Unlock()

	started := time.Now()
	log.Printf("[SYNC] catalogue refresh started (%s)", reason)
	total := 0
	for _, pr := range ListProfiles() {
		portal, ok := GetProfilePortal(pr.ID)
		if !ok {
			continue
		}
		if cats, err := portal.GetVODCategories(); err == nil {
			total += xtreamRefresh(fmt.Sprintf("vod%d", pr.ID), filterVODCategories(cats), vodFetcher(pr.ID, portal))
		} else {
			log.Printf("[SYNC] profile=%d vod categories: %v", pr.ID, err)
		}
		if cats, err := portal.GetSeriesCategories(); err == nil {
			total += xtreamRefresh(fmt.Sprintf("series%d", pr.ID), filterVODCategories(cats), seriesFetcher(portal))
		} else {
			log.Printf("[SYNC] profile=%d series categories: %v", pr.ID, err)
		}
	}
	msg := fmt.Sprintf("%d items in %s (%s)", total, time.Since(started).Round(time.Second), reason)
	log.Printf("[SYNC] catalogue refresh done: %s", msg)
	syncMu.Lock()
	syncRunning = false
	syncLast = time.Now()
	syncLastMsg = msg
	syncMu.Unlock()
	return true
}

// warmCatalogue builds listings that don't exist yet (cold start) so the first
// client import is fast. It never replaces data that is already cached.
func warmCatalogue() {
	for _, pr := range ListProfiles() {
		portal, ok := GetProfilePortal(pr.ID)
		if !ok {
			continue
		}
		if cats, err := portal.GetVODCategories(); err == nil {
			xtreamAll(fmt.Sprintf("vod%d", pr.ID), filterVODCategories(cats), vodFetcher(pr.ID, portal))
		}
		if cats, err := portal.GetSeriesCategories(); err == nil {
			xtreamAll(fmt.Sprintf("series%d", pr.ID), filterVODCategories(cats), seriesFetcher(portal))
		}
	}
}

func syncLoop() {
	time.Sleep(30 * time.Second)
	warmCatalogue()
	for {
		next := nextSyncTime(time.Now(), getSyncTimes())
		var timer <-chan time.Time
		if next.IsZero() {
			log.Printf("[SYNC] no sync times configured; automatic refresh is off")
		} else {
			log.Printf("[SYNC] next catalogue refresh at %s", next.Format("2006-01-02 15:04"))
			timer = time.After(time.Until(next))
		}
		select {
		case <-syncWake:
		case <-timer:
			refreshCatalogue("scheduled " + next.Format("15:04"))
		}
	}
}

func RegisterSyncHandlers(mux *http.ServeMux) {
	syncStart.Do(func() { go syncLoop() })
	mux.HandleFunc("/api/sync", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
		case http.MethodPost:
			_ = r.ParseForm()
			if r.FormValue("action") == "now" {
				if !refreshStartAsync() {
					http.Error(w, `{"ok":false,"error":"a sync is already running"}`, http.StatusConflict)
					return
				}
			} else {
				raw := r.FormValue("times")
				var list []string
				for _, f := range strings.FieldsFunc(raw, func(c rune) bool { return c == ',' || c == ' ' || c == ';' || c == '\n' }) {
					list = append(list, f)
				}
				if err := setSyncTimes(list); err != nil {
					w.WriteHeader(http.StatusBadRequest)
					_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
					return
				}
			}
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		times := getSyncTimes()
		syncMu.Lock()
		running, last, lastMsg := syncRunning, syncLast, syncLastMsg
		syncMu.Unlock()
		resp := map[string]any{"ok": true, "times": times, "running": running, "last_message": lastMsg}
		if !last.IsZero() {
			resp["last"] = last.Format("2006-01-02 15:04")
		}
		if n := nextSyncTime(time.Now(), times); !n.IsZero() {
			resp["next"] = n.Format("2006-01-02 15:04")
		}
		_ = json.NewEncoder(w).Encode(resp)
	})
}

func refreshStartAsync() bool {
	syncMu.Lock()
	busy := syncRunning
	syncMu.Unlock()
	if busy {
		return false
	}
	go refreshCatalogue("manual")
	return true
}
