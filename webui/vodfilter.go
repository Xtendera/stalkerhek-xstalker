package webui

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/kidpoleon/stalkerhek/stalker"
)

// VOD language filter: restricts movie/series categories exposed over the
// Xtream API to English, Hindi and Gujarati ones. Toggled from the dashboard.
const (
	vodFilterAll       = "all"
	vodFilterLanguages = "languages"
)

var vodFilterKeywords = []string{"ENGLISH", "HINDI", "GUJARATI"}

var (
	vodFilterMu   sync.RWMutex
	vodFilterMode = vodFilterLanguages
	vodFilterLoad sync.Once
)

func vodFilterPath() string {
	dir := filepath.Dir(profilesFile)
	if dir == "" {
		dir = "."
	}
	return filepath.Join(dir, "vodfilter.json")
}

func getVODFilterMode() string {
	vodFilterLoad.Do(func() {
		b, err := os.ReadFile(vodFilterPath())
		if err != nil {
			return
		}
		var v struct {
			Mode string `json:"mode"`
		}
		if json.Unmarshal(b, &v) == nil && (v.Mode == vodFilterAll || v.Mode == vodFilterLanguages) {
			vodFilterMu.Lock()
			vodFilterMode = v.Mode
			vodFilterMu.Unlock()
		}
	})
	vodFilterMu.RLock()
	defer vodFilterMu.RUnlock()
	return vodFilterMode
}

func setVODFilterMode(mode string) {
	getVODFilterMode() // make sure the saved value was loaded first
	vodFilterMu.Lock()
	changed := vodFilterMode != mode
	vodFilterMode = mode
	vodFilterMu.Unlock()
	if !changed {
		return
	}
	if b, err := json.Marshal(map[string]string{"mode": mode}); err == nil {
		_ = os.MkdirAll(filepath.Dir(vodFilterPath()), 0o755)
		_ = os.WriteFile(vodFilterPath(), b, 0o644)
	}
	xtreamAllMu.Lock()
	xtreamAllCache = map[string]xtreamAllEntry{}
	xtreamAllMu.Unlock()
	LogInfo("SETTINGS", "vod filter mode=%s", mode)
}

// filterVODCategories drops categories outside the wanted languages when the
// filter is on. The "*" pseudo-category is always kept.
func filterVODCategories(cats []stalker.VODCategory) []stalker.VODCategory {
	if getVODFilterMode() != vodFilterLanguages {
		return cats
	}
	out := make([]stalker.VODCategory, 0, len(cats))
	for _, c := range cats {
		t := strings.ToUpper(c.Title)
		keep := c.ID == "*"
		for _, k := range vodFilterKeywords {
			if strings.Contains(t, k) {
				keep = true
				break
			}
		}
		if keep {
			out = append(out, c)
		}
	}
	return out
}

func RegisterVODFilterHandlers(mux *http.ServeMux) {
	mux.HandleFunc("/api/vodfilter", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
		case http.MethodPost:
			_ = r.ParseForm()
			m := r.FormValue("mode")
			if m != vodFilterAll && m != vodFilterLanguages {
				http.Error(w, "mode must be 'all' or 'languages'", http.StatusBadRequest)
				return
			}
			setVODFilterMode(m)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "mode": getVODFilterMode(), "languages": vodFilterKeywords})
	})
}
