package webui

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kidpoleon/stalkerhek/filterstore"
	"github.com/kidpoleon/stalkerhek/stalker"
)

const maxVODPagesFetch = 0 // 0 = no limit

// xtreamPassword is the password Xtream clients must send. Override with
// STALKERHEK_XTREAM_PASSWORD (recommended before exposing the port to the internet).
var xtreamPassword = func() string {
	if v := strings.TrimSpace(os.Getenv("STALKERHEK_XTREAM_PASSWORD")); v != "" {
		return v
	}
	return "stalkerhek"
}()

// RegisterXtreamHandlers exposes Xtream Codes API and playback paths for IPTV apps.
func RegisterXtreamHandlers(mux *http.ServeMux) {
	mux.HandleFunc("/player_api.php", handleXtreamPlayerAPI)
	mux.HandleFunc("/panel_api.php", handleXtreamPlayerAPI)
	mux.HandleFunc("/xtream/", handleXtreamRoute)
	mux.HandleFunc("/movie/", logPlay(handleXtreamMovie))
	mux.HandleFunc("/series/", logPlay(handleXtreamSeries))
	mux.HandleFunc("/live/", handleXtreamLive)
	mux.HandleFunc("/iptv/", handleHLSForward)
	mux.HandleFunc("/logo/", handleHLSForward)
}

func handleXtreamPlayerAPI(w http.ResponseWriter, r *http.Request) {
	pid, ok := profileIDFromXtreamUser(r.URL.Query().Get("username"))
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{"user_info": map[string]any{"auth": 0}})
		return
	}
	if !validateXtreamPassword(r.URL.Query().Get("password")) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{"user_info": map[string]any{"auth": 0}})
		return
	}
	dispatchXtreamAction(w, r, pid)
}

func handleXtreamRoute(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 3 || parts[0] != "xtream" {
		http.NotFound(w, r)
		return
	}
	pid := atoiSafe(parts[1])
	if parts[2] != "player_api.php" {
		http.NotFound(w, r)
		return
	}
	if !validateXtreamAuth(pid, r) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{"user_info": map[string]any{"auth": 0}})
		return
	}
	dispatchXtreamAction(w, r, pid)
}

func dispatchXtreamAction(w http.ResponseWriter, r *http.Request, profileID int) {
	action := strings.TrimSpace(r.URL.Query().Get("action"))
	if action == "" {
		serveXtreamUserInfo(w, r, profileID)
		return
	}
	switch action {
	case "get_live_categories":
		serveXtreamLiveCategories(w, profileID)
	case "get_live_streams":
		serveXtreamLiveStreams(w, r, profileID)
	case "get_vod_categories":
		serveXtreamVODCategories(w, profileID, false)
	case "get_vod_streams":
		serveXtreamVODStreams(w, r, profileID)
	case "get_series_categories":
		serveXtreamVODCategories(w, profileID, true)
	case "get_series":
		serveXtreamSeries(w, r, profileID)
	case "get_vod_info":
		serveXtreamVODInfo(w, r)
	case "get_series_info":
		serveXtreamSeriesInfo(w, r, profileID)
	case "get_short_epg", "get_simple_data_table":
		serveXtreamShortEPG(w, r, profileID)
	default:
		writeJSON(w, []any{})
	}
}

func profileIDFromXtreamUser(username string) (int, bool) {
	mac := normalizeMAC(username)
	if mac == "" {
		return 0, false
	}
	for _, p := range ListProfiles() {
		if normalizeMAC(p.MAC) == mac {
			return p.ID, true
		}
	}
	return 0, false
}

func validateXtreamAuth(profileID int, r *http.Request) bool {
	p, ok := GetProfile(profileID)
	if !ok {
		return false
	}
	user := strings.TrimSpace(r.URL.Query().Get("username"))
	if user == "" {
		return true
	}
	if normalizeMAC(user) != normalizeMAC(p.MAC) {
		return false
	}
	return validateXtreamPassword(r.URL.Query().Get("password"))
}

func validateXtreamPassword(pass string) bool {
	pass = strings.TrimSpace(pass)
	return pass == xtreamPassword
}

func normalizeMAC(mac string) string {
	mac = strings.ToUpper(strings.TrimSpace(mac))
	mac = strings.ReplaceAll(mac, "-", ":")
	return mac
}

func serveXtreamUserInfo(w http.ResponseWriter, r *http.Request, profileID int) {
	p, ok := GetProfile(profileID)
	if !ok {
		http.NotFound(w, r)
		return
	}
	scheme, host := requestSchemeHost(r)
	epgURL := scheme + "://" + host + "/epg/" + strconv.Itoa(profileID) + "/xmltv.xml?programs=1&limit=500"
	if u := EffectiveEPGURL(p); u != "" {
		epgURL = u
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"user_info": map[string]any{
			"username":               p.MAC,
			"password":               xtreamPassword,
			"message":                "stalkerhek",
			"auth":                   1,
			"status":                 "Active",
			"exp_date":               "1999999999",
			"is_trial":               "0",
			"active_cons":            "0",
			"created_at":             "1600000000",
			"max_connections":        "1",
			"allowed_output_formats": []string{"m3u8", "ts", "mp4"},
		},
		"server_info": map[string]any{
			"url":             scheme + "://" + host,
			"port":            xtreamPort(host),
			"https_port":      "443",
			"server_protocol": scheme,
			"rtmp_port":       "",
			"timezone":        p.TimeZone,
			"timestamp_now":   true,
			"time_now":        "",
			"epg_url":         epgURL,
		},
	})
}

func xtreamPort(host string) string {
	if i := strings.LastIndex(host, ":"); i >= 0 {
		return host[i+1:]
	}
	return "4400"
}

func serveXtreamLiveCategories(w http.ResponseWriter, profileID int) {
	chs, _, ok := GetProfileChannels(profileID)
	if !ok {
		writeJSON(w, []any{})
		return
	}
	seen := map[string]struct{}{}
	var cats []map[string]any
	idx := 1
	for _, ch := range chs {
		if ch == nil || !filterstore.IsAllowed(profileID, ch) {
			continue
		}
		g := stalker.CleanGenreForM3U8(ch.RawGenre())
		if g == "" {
			g = "Other"
		}
		if _, dup := seen[g]; dup {
			continue
		}
		seen[g] = struct{}{}
		cats = append(cats, map[string]any{
			"category_id":   strconv.Itoa(idx),
			"category_name": g,
			"parent_id":     0,
		})
		idx++
	}
	writeJSON(w, cats)
}

func serveXtreamLiveStreams(w http.ResponseWriter, r *http.Request, profileID int) {
	_, ok := GetProfile(profileID)
	if !ok {
		writeJSON(w, []any{})
		return
	}
	chs, keys, ok := GetProfileChannels(profileID)
	if !ok {
		writeJSON(w, []any{})
		return
	}
	catFilter := strings.TrimSpace(r.URL.Query().Get("category_id"))
	catMap := buildCategoryIDMap(profileID, chs)
	var out []map[string]any
	num := 1
	for _, title := range keys {
		ch := chs[title]
		if ch == nil || !filterstore.IsAllowed(profileID, ch) {
			continue
		}
		g := stalker.CleanGenreForM3U8(ch.RawGenre())
		if g == "" {
			g = "Other"
		}
		cid := catMap[g]
		if catFilter != "" && catFilter != cid {
			continue
		}
		out = append(out, map[string]any{
			"num":                 num,
			"name":                stalker.CleanTitleForM3U8(title),
			"stream_type":         "live",
			"stream_id":           num,
			"stream_icon":         ch.Logo(),
			"epg_channel_id":      ch.EPGChannelID(),
			"added":               "0",
			"category_id":         cid,
			"custom_sid":          "",
			"tv_archive":          0,
			"direct_source":       "",
			"tv_archive_duration": 0,
		})
		num++
	}
	writeJSON(w, out)
}

func buildCategoryIDMap(profileID int, chs map[string]*stalker.Channel) map[string]string {
	seen := map[string]struct{}{}
	out := map[string]string{}
	idx := 1
	for _, ch := range chs {
		if ch == nil || !filterstore.IsAllowed(profileID, ch) {
			continue
		}
		g := stalker.CleanGenreForM3U8(ch.RawGenre())
		if g == "" {
			g = "Other"
		}
		if _, ok := seen[g]; ok {
			continue
		}
		seen[g] = struct{}{}
		out[g] = strconv.Itoa(idx)
		idx++
	}
	return out
}

func serveXtreamVODCategories(w http.ResponseWriter, profileID int, series bool) {
	portal, ok := GetProfilePortal(profileID)
	if !ok {
		writeJSON(w, []any{})
		return
	}
	var cats []stalker.VODCategory
	var err error
	if series {
		cats, err = portal.GetSeriesCategories()
	} else {
		cats, err = portal.GetVODCategories()
	}
	if err != nil {
		log.Printf("[XTREAM] profile=%d vod categories err=%v", profileID, err)
		writeJSON(w, []any{})
		return
	}
	var out []map[string]any
	for _, c := range filterVODCategories(cats) {
		out = append(out, map[string]any{
			"category_id":   c.ID,
			"category_name": c.Title,
			"parent_id":     0,
		})
	}
	writeJSON(w, out)
}

// xtreamAllCache holds the merged "every category" listing that clients such as
// TiviMate request in one call (the portal itself has no "all" category).
var (
	xtreamAllMu    sync.Mutex
	xtreamAllCache = map[string]xtreamAllEntry{}
)

type xtreamAllEntry struct {
	data    []map[string]any
	updated time.Time
}

// xtreamAll returns the cached aggregate listing for key, building it on first
// use. The cache never expires on its own: it is rebuilt by the scheduled sync
// (see sync.go) so a refresh can't interrupt viewing at a random time.
func xtreamAll(key string, cats []stalker.VODCategory, fetch func(cat string) []map[string]any) []map[string]any {
	xtreamAllMu.Lock()
	defer xtreamAllMu.Unlock()
	if e, ok := xtreamAllCache[key]; ok {
		return e.data
	}
	out := xtreamBuild(key, cats, fetch)
	if len(out) > 0 {
		xtreamAllCache[key] = xtreamAllEntry{data: out, updated: time.Now()}
	}
	return out
}

// xtreamRefresh rebuilds a listing without blocking readers of the old one and
// swaps it in only when the rebuild produced data.
func xtreamRefresh(key string, cats []stalker.VODCategory, fetch func(cat string) []map[string]any) int {
	out := xtreamBuild(key, cats, fetch)
	if len(out) == 0 {
		log.Printf("[SYNC] %s: rebuild returned nothing, keeping previous data", strings.TrimRightFunc(key, func(r rune) bool { return r >= '0' && r <= '9' }))
		return 0
	}
	xtreamAllMu.Lock()
	xtreamAllCache[key] = xtreamAllEntry{data: out, updated: time.Now()}
	xtreamAllMu.Unlock()
	return len(out)
}

func xtreamBuild(key string, cats []stalker.VODCategory, fetch func(cat string) []map[string]any) []map[string]any {
	results := make([][]map[string]any, len(cats))
	label := strings.TrimRightFunc(key, func(r rune) bool { return r >= '0' && r <= '9' })
	started := time.Now()
	totalCats := 0
	for _, c := range cats {
		if c.ID != "*" {
			totalCats++
		}
	}
	var doneCats, doneItems int64
	log.Printf("[SYNC] %s: started, %d categories", label, totalCats)
	sem := make(chan struct{}, 6)
	var wg sync.WaitGroup
	for i, c := range cats {
		if c.ID == "*" {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, id string) {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = fetch(id)
			n := atomic.AddInt64(&doneCats, 1)
			items := atomic.AddInt64(&doneItems, int64(len(results[i])))
			if n%5 == 0 || int(n) == totalCats {
				log.Printf("[SYNC] %s: %d/%d categories (%d%%), %d items, %s elapsed", label, n, totalCats, int(n)*100/totalCats, items, time.Since(started).Round(time.Second))
			}
		}(i, c.ID)
	}
	wg.Wait()
	out := []map[string]any{}
	for _, r := range results {
		out = append(out, r...)
	}
	for i, m := range out {
		m["num"] = i + 1
	}
	log.Printf("[SYNC] %s: FINISHED, %d items in %s", label, len(out), time.Since(started).Round(time.Second))
	return out
}

func isAllCategory(c string) bool { return c == "" || c == "*" }

func serveXtreamVODStreams(w http.ResponseWriter, r *http.Request, profileID int) {
	portal, ok := GetProfilePortal(profileID)
	if !ok {
		writeJSON(w, []any{})
		return
	}
	cat := strings.TrimSpace(r.URL.Query().Get("category_id"))
	if isAllCategory(cat) {
		cats, err := portal.GetVODCategories()
		if err != nil {
			log.Printf("[XTREAM] profile=%d vod categories err=%v", profileID, err)
			writeJSON(w, []any{})
			return
		}
		writeJSON(w, xtreamAll(fmt.Sprintf("vod%d", profileID), filterVODCategories(cats), vodFetcher(profileID, portal)))
		return
	}
	items, err := portal.FetchAllVODMovies(cat, maxVODPagesFetch)
	if err != nil {
		log.Printf("[XTREAM] profile=%d vod streams cat=%s err=%v", profileID, cat, err)
		writeJSON(w, []any{})
		return
	}
	p, _ := GetProfile(profileID)
	user := ""
	if p.MAC != "" {
		user = p.MAC
	}
	writeJSON(w, xtreamMovieItems(user, items, cat))
}

func xtreamSeriesItems(items []stalker.VODItem, cat string) []map[string]any {
	var out []map[string]any
	for i, it := range items {
		seriesMeta.Store(it.ID, it)
		out = append(out, map[string]any{
			"num":              i + 1,
			"name":             it.Name,
			"series_id":        it.ID,
			"cover":            it.Logo,
			"plot":             it.Description,
			"cast":             it.Actors,
			"director":         it.Director,
			"genre":            it.Genres,
			"releaseDate":      it.Year,
			"last_modified":    addedOrZero(it.Added),
			"rating":           it.Rating,
			"rating_5based":    rating5(it.Rating),
			"backdrop_path":    []string{},
			"youtube_trailer":  "",
			"episode_run_time": "",
			"category_id":      cat,
			"stream_type":      "series",
		})
	}
	return out
}

func serveXtreamSeries(w http.ResponseWriter, r *http.Request, profileID int) {
	portal, ok := GetProfilePortal(profileID)
	if !ok {
		writeJSON(w, []any{})
		return
	}
	cat := strings.TrimSpace(r.URL.Query().Get("category_id"))
	if isAllCategory(cat) {
		cats, err := portal.GetSeriesCategories()
		if err != nil {
			log.Printf("[XTREAM] profile=%d series categories err=%v", profileID, err)
			writeJSON(w, []any{})
			return
		}
		writeJSON(w, xtreamAll(fmt.Sprintf("series%d", profileID), filterVODCategories(cats), seriesFetcher(portal)))
		return
	}
	items, err := portal.FetchAllSeries(cat, maxVODPagesFetch)
	if err != nil {
		log.Printf("[XTREAM] profile=%d series cat=%s err=%v", profileID, cat, err)
		writeJSON(w, []any{})
		return
	}
	out := xtreamSeriesItems(items, cat)
	writeJSON(w, out)
}

func serveXtreamSeriesInfo(w http.ResponseWriter, r *http.Request, profileID int) {
	portal, ok := GetProfilePortal(profileID)
	if !ok {
		writeJSON(w, map[string]any{})
		return
	}
	seriesID := strings.TrimSpace(r.URL.Query().Get("series_id"))
	if seriesID == "" {
		seriesID = strings.TrimSpace(r.URL.Query().Get("series"))
	}
	if seriesID == "" {
		writeJSON(w, map[string]any{})
		return
	}
	seasons, _, err := portal.GetSeasons(seriesID, 0)
	if err != nil || len(seasons) == 0 {
		log.Printf("[XTREAM] profile=%d series_info id=%s seasons err=%v", profileID, seriesID, err)
		writeJSON(w, map[string]any{"seasons": []any{}, "info": map[string]any{}, "episodes": map[string]any{}})
		return
	}
	var meta stalker.VODItem
	if v, ok := seriesMeta.Load(seriesID); ok {
		meta = v.(stalker.VODItem)
	}
	p, _ := GetProfile(profileID)
	user := p.MAC
	seasonList := make([]map[string]any, 0, len(seasons))
	episodes := map[string][]map[string]any{}
	// Portal season ids are opaque (e.g. 209540); clients want 1,2,3... in order.
	sort.SliceStable(seasons, func(a, b int) bool {
		na, nb := leadingNumber(seasons[a].Name), leadingNumber(seasons[b].Name)
		if na != nb {
			return na < nb
		}
		return false
	})
	for si, sn := range seasons {
		sid := sn.SeasonID
		if sid == "" {
			sid = sn.ID
		}
		snum := leadingNumber(sn.Name)
		if snum <= 0 {
			snum = si + 1
		}
		skey := strconv.Itoa(snum)
		seasonList = append(seasonList, map[string]any{
			"air_date":      "",
			"episode_count": 0,
			"id":            sid,
			"name":          sn.Name,
			"overview":      "",
			"season_number": snum,
			"cover":         sn.Logo,
			"cover_big":     sn.Logo,
		})
		eps, err := portal.FetchAllEpisodes(seriesID, sid, maxVODPagesFetch)
		if err != nil {
			continue
		}
		var epOut []map[string]any
		for _, ep := range eps {
			epNum := ep.EpisodeNumber
			if epNum == "" {
				epNum = ep.ID
			}
			ext := extOrDefault(ep.ContainerExt, "m3u8")
			rememberEpisode(profileID, ep.ID, seriesID, sid, epNum)
			epOut = append(epOut, map[string]any{
				"id":                  ep.ID,
				"episode_num":         epNum,
				"title":               ep.Name,
				"container_extension": ext,
				"info":                map[string]any{},
				"custom_sid":          "",
				"added":               addedOrZero(meta.Added),
				"season":              snum,
				"direct_source":       "",
			})
			_ = user
		}
		sort.SliceStable(epOut, func(a, b int) bool {
			return atoiSafe(fmt.Sprint(epOut[a]["episode_num"])) < atoiSafe(fmt.Sprint(epOut[b]["episode_num"]))
		})
		episodes[skey] = epOut
	}
	flushEpisodes()
	writeJSON(w, map[string]any{
		"seasons":  seasonList,
		"info": map[string]any{
			"name": meta.Name, "cover": meta.Logo, "plot": meta.Description, "cast": meta.Actors,
			"director": meta.Director, "genre": meta.Genres, "releaseDate": meta.Year,
			"last_modified": addedOrZero(meta.Added), "rating": meta.Rating,
			"rating_5based": rating5(meta.Rating), "backdrop_path": []string{}, "youtube_trailer": "",
		},
		"episodes": episodes,
	})
}

func serveXtreamShortEPG(w http.ResponseWriter, r *http.Request, profileID int) {
	streamID := strings.TrimSpace(r.URL.Query().Get("stream_id"))
	chs, keys, ok := GetProfileChannels(profileID)
	if !ok {
		writeJSON(w, map[string]any{})
		return
	}
	idx, _ := strconv.Atoi(streamID)
	if idx < 1 || idx > len(keys) {
		writeJSON(w, map[string]any{})
		return
	}
	ch := chs[keys[idx-1]]
	if ch == nil {
		writeJSON(w, map[string]any{})
		return
	}
	programs, err := ch.GetShortEPG(8)
	if err != nil || len(programs) == 0 {
		programs, _ = ch.GetEPGInfo()
	}
	loc := ch.Portal.TimeLocation()
	var listings []map[string]any
	for _, pr := range programs {
		listings = append(listings, map[string]any{
			"id":              pr.Title,
			"epg_id":          ch.EPGChannelID(),
			"title":           pr.Title,
			"lang":            "en",
			"start":           pr.Start.In(loc).Format("2006-01-02 15:04:05"),
			"end":             pr.Stop.In(loc).Format("2006-01-02 15:04:05"),
			"description":     pr.Description,
			"channel_id":      ch.EPGChannelID(),
			"start_timestamp": pr.Start.Unix(),
			"stop_timestamp":  pr.Stop.Unix(),
		})
	}
	writeJSON(w, map[string]any{"epg_listings": listings})
}

var seriesMeta sync.Map // series id / "m"+movie id -> stalker.VODItem

func addedOrZero(a string) string {
	if a == "" {
		return "0"
	}
	return a
}

func rating5(r string) float64 {
	f, err := strconv.ParseFloat(r, 64)
	if err != nil {
		return 0
	}
	return math.Round(f/2*10) / 10
}

func xtreamMovieItems(user string, items []stalker.VODItem, cat string) []map[string]any {
	var out []map[string]any
	for i, it := range items {
		ext := extOrDefault(it.ContainerExt, "m3u8")
		seriesMeta.Store("m"+it.ID, it)
		out = append(out, map[string]any{
			"num":                 i + 1,
			"name":                it.Name,
			"stream_type":         "movie",
			"stream_id":           it.ID,
			"stream_icon":         it.Logo,
			"rating":              it.Rating,
			"rating_5based":       rating5(it.Rating),
			"added":               addedOrZero(it.Added),
			"category_id":         cat,
			"container_extension": ext,
			"custom_sid":          "",
			"direct_source":       "",
		})
		_ = user
	}
	return out
}

// --- Xtream playback URLs: /movie/{user}/{pass}/{id}.ext ---

func handleXtreamMovie(w http.ResponseWriter, r *http.Request) {
	playXtreamVOD(w, r, "movie")
}

func handleXtreamSeries(w http.ResponseWriter, r *http.Request) {
	playXtreamVOD(w, r, "series")
}

func playXtreamVOD(w http.ResponseWriter, r *http.Request, linkType string) {
	if w == nil || r == nil {
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 4 {
		http.NotFound(w, r)
		return
	}
	pid, ok := profileIDFromXtreamUser(parts[1])
	if !ok || !validateXtreamPassword(parts[2]) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	streamID := strings.TrimSpace(strings.TrimSuffix(parts[3], path.Ext(parts[3])))
	if streamID == "" {
		http.Error(w, "missing stream id", http.StatusBadRequest)
		return
	}
	seriesNum := strings.TrimSpace(r.URL.Query().Get("series"))
	portal, ok := GetProfilePortal(pid)
	if !ok || portal == nil {
		http.Error(w, "profile not running", http.StatusServiceUnavailable)
		return
	}
	var pl *stalker.VODPlayLink
	var err error
	if cmd := strings.TrimSpace(r.URL.Query().Get("cmd")); cmd != "" {
		pl, err = portal.CreateVODPlayLink(cmd, linkType, seriesNum)
	} else if ei, epID, ok := resolveEpisode(pid, streamID); ok && linkType == "series" {
		if ei.Num == "" { // entry saved before episode numbers were tracked
			if eps, e := portal.FetchAllEpisodes(ei.Series, ei.Season, 0); e == nil {
				for _, x := range eps {
					if x.ID == epID {
						ei.Num = x.EpisodeNumber
						rememberEpisode(pid, epID, ei.Series, ei.Season, ei.Num)
						flushEpisodes()
					}
				}
			}
		}
		fid := portal.LookupEpisodeFileID(ei.Series, ei.Season, epID)
		if fid == "" {
			err = fmt.Errorf("no file for episode (series=%s season=%s)", ei.Series, ei.Season)
		} else {
			// The portal serves episode 1 for any file id unless told which
			// episode of the season file is wanted via "series".
			pl, err = portal.CreateVODPlayLink("/media/file_"+fid+".mpg", linkType, ei.Num)
		}
	} else {
		pl, err = portal.CreateVODPlayLinkByID(streamID, linkType, seriesNum)
	}
	if err != nil {
		log.Printf("[XTREAM] play profile=%d type=%s id=%s err=%v", pid, linkType, streamID, err)
		http.Error(w, "playback failed", http.StatusBadGateway)
		return
	}
	if pl == nil || strings.TrimSpace(pl.URL) == "" {
		log.Printf("[XTREAM] play profile=%d type=%s id=%s empty url", pid, linkType, streamID)
		http.Error(w, "empty playback url", http.StatusBadGateway)
		return
	}
	http.Redirect(w, r, pl.URL, http.StatusFound)
}

func handleXtreamLive(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 4 {
		http.NotFound(w, r)
		return
	}
	pid, ok := profileIDFromXtreamUser(parts[1])
	if !ok || !validateXtreamPassword(parts[2]) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	streamID, _ := strconv.Atoi(strings.TrimSuffix(parts[3], path.Ext(parts[3])))
	p, ok := GetProfile(pid)
	if !ok {
		http.NotFound(w, r)
		return
	}
	chs, keys, ok := GetProfileChannels(pid)
	if !ok || streamID < 1 || streamID > len(keys) {
		http.Error(w, "channel not found", http.StatusNotFound)
		return
	}
	title := keys[streamID-1]
	ch := chs[title]
	if ch == nil || !filterstore.IsAllowed(pid, ch) {
		http.Error(w, "channel blocked", http.StatusForbidden)
		return
	}
	scheme, host := requestSchemeHost(r)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	hostPort := host + ":" + strconv.Itoa(p.HlsPort)
	if viaProxy(r) {
		// Tunnel (ngrok etc.): only the main port is reachable, which forwards /iptv/.
		hostPort = r.Header.Get("X-Forwarded-Host")
		if hostPort == "" {
			hostPort = r.Host
		}
	}
	redirect := scheme + "://" + hostPort + "/iptv/" + url.PathEscape(title)
	http.Redirect(w, r, redirect, http.StatusFound)
}

// Episode ids alone don't identify a file; the portal needs series+season too.
// Xtream clients (TiviMate) want plain numeric episode ids, so the mapping is
// remembered when get_series_info lists episodes and persisted across restarts.
type episodeInfo struct {
	Series string `json:"s"`
	Season string `json:"n"`
	Num    string `json:"e,omitempty"` // episode number within the season
}

var (
	episodeMu     sync.RWMutex
	episodeMap    = map[string]episodeInfo{}
	episodeLoaded bool
	episodeDirty  bool
)

func episodeFile() string { return filepath.Join(filepath.Dir(profilesFile), "episodes.json") }

func loadEpisodesLocked() {
	if episodeLoaded {
		return
	}
	episodeLoaded = true
	if b, err := os.ReadFile(episodeFile()); err == nil {
		_ = json.Unmarshal(b, &episodeMap)
	}
}

func rememberEpisode(profileID int, epID, series, season, num string) {
	episodeMu.Lock()
	loadEpisodesLocked()
	k := fmt.Sprintf("%d/%s", profileID, epID)
	if e, ok := episodeMap[k]; !ok || e.Series != series || e.Season != season || e.Num != num {
		episodeMap[k] = episodeInfo{series, season, num}
		episodeDirty = true
	}
	episodeMu.Unlock()
}

// flushEpisodes writes the mapping to disk if it changed.
func flushEpisodes() {
	episodeMu.Lock()
	defer episodeMu.Unlock()
	if !episodeDirty {
		return
	}
	episodeDirty = false
	if b, err := json.Marshal(episodeMap); err == nil {
		_ = os.MkdirAll(filepath.Dir(episodeFile()), 0o755)
		_ = os.WriteFile(episodeFile(), b, 0o644)
	}
}

func resolveEpisode(profileID int, id string) (episodeInfo, string, bool) {
	episodeMu.Lock()
	loadEpisodesLocked()
	e, ok := episodeMap[fmt.Sprintf("%d/%s", profileID, id)]
	episodeMu.Unlock()
	return e, id, ok
}

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (s *statusRecorder) WriteHeader(c int) { s.code = c; s.ResponseWriter.WriteHeader(c) }

// logPlay logs every VOD play request with its result so client-side errors
// (e.g. a 404 in the player) can be matched to what the proxy answered.
func logPlay(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, code: 200}
		h(rec, r)
		loc := rec.Header().Get("Location")
		if len(loc) > 70 {
			loc = loc[:70]
		}
		log.Printf("[XTREAM] %s %s?%s -> %d %s", r.Method, r.URL.Path, r.URL.RawQuery, rec.code, loc)
	}
}

// leadingNumber returns the first integer in names like "Season 4. SHOW: Season 04"
// or "Episode 13. ...", or 0 when there is none.
func leadingNumber(name string) int {
	n, started := 0, false
	for _, c := range name {
		if c >= '0' && c <= '9' {
			n = n*10 + int(c-'0')
			started = true
		} else if started {
			break
		}
	}
	return n
}

func serveXtreamVODInfo(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("vod_id"))
	v, ok := seriesMeta.Load("m" + id)
	if !ok {
		writeJSON(w, map[string]any{"info": map[string]any{}, "movie_data": map[string]any{}})
		return
	}
	it := v.(stalker.VODItem)
	writeJSON(w, map[string]any{
		"info": map[string]any{
			"name": it.Name, "o_name": it.Name, "movie_image": it.Logo, "cover_big": it.Logo,
			"plot": it.Description, "description": it.Description, "cast": it.Actors, "actors": it.Actors,
			"director": it.Director, "genre": it.Genres, "releasedate": it.Year,
			"rating": it.Rating, "rating_5based": rating5(it.Rating), "backdrop_path": []string{},
			"youtube_trailer": "", "duration": "", "duration_secs": 0,
		},
		"movie_data": map[string]any{
			"stream_id": it.ID, "name": it.Name, "added": addedOrZero(it.Added),
			"category_id": it.Category, "container_extension": extOrDefault(it.ContainerExt, "m3u8"),
			"custom_sid": "", "direct_source": "",
		},
	})
}

func vodFetcher(profileID int, portal *stalker.Portal) func(string) []map[string]any {
	p, _ := GetProfile(profileID)
	return func(id string) []map[string]any {
		items, err := portal.FetchAllVODMovies(id, maxVODPagesFetch)
		if err != nil {
			return nil
		}
		return xtreamMovieItems(p.MAC, items, id)
	}
}

func seriesFetcher(portal *stalker.Portal) func(string) []map[string]any {
	return func(id string) []map[string]any {
		items, err := portal.FetchAllSeries(id, maxVODPagesFetch)
		if err != nil {
			return nil
		}
		return xtreamSeriesItems(items, id)
	}
}
