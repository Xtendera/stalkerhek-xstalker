package stalker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// VODCategory is a movies/VOD grouping from the portal.
type VODCategory struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// VODItem is a movie, series, season, or episode from get_ordered_list.
type VODItem struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	CMD           string `json:"cmd,omitempty"`
	Logo          string `json:"logo,omitempty"`
	IsSeries      string `json:"is_series,omitempty"`
	Category      string `json:"category_id,omitempty"`
	MovieID       string `json:"movie_id,omitempty"`
	SeasonID      string `json:"season_id,omitempty"`
	EpisodeNumber string `json:"episode_number,omitempty"`
	ContainerExt  string `json:"container_extension,omitempty"`
	Added         string `json:"added,omitempty"`       // unix seconds
	Rating        string `json:"rating,omitempty"`      // 0-10
	Description   string `json:"description,omitempty"`
	Director      string `json:"director,omitempty"`
	Actors        string `json:"actors,omitempty"`
	Year          string `json:"year,omitempty"`
	Genres        string `json:"genres,omitempty"`
}

// VODPlayLink is a seekable on-demand URL (movie.php or direct stream).
type VODPlayLink struct {
	URL       string `json:"url"`
	Stream    string `json:"stream,omitempty"`
	PlayToken string `json:"play_token,omitempty"`
	Type      string `json:"type"`
}

var reFFmpegPrefix = regexp.MustCompile(`(?i)^ffmpeg\s*`)

// GetVODCategories returns movie categories (excludes TV/series-style names), matching reference player.
func (p *Portal) GetVODCategories() ([]VODCategory, error) {
	all, err := p.getCategories("vod")
	if err != nil {
		return nil, err
	}
	out := make([]VODCategory, 0, len(all))
	for _, c := range all {
		if isSeriesStyleCategory(c.Title) {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

// GetSeriesCategories returns series categories (type=series, else vod cats that look like TV/series).
func (p *Portal) GetSeriesCategories() ([]VODCategory, error) {
	cats, err := p.getCategories("series")
	if err == nil && len(cats) > 0 {
		return cats, nil
	}
	all, err := p.getCategories("vod")
	if err != nil {
		return nil, err
	}
	out := make([]VODCategory, 0)
	for _, c := range all {
		if isSeriesStyleCategory(c.Title) {
			out = append(out, c)
		}
	}
	return out, nil
}

func isSeriesStyleCategory(name string) bool {
	low := strings.ToLower(strings.TrimSpace(name))
	if low == "" {
		return false
	}
	for _, kw := range []string{"series", "tv show", "tv shows", "tv series", "season"} {
		if strings.Contains(low, kw) {
			return true
		}
	}
	return strings.HasPrefix(low, "tv ") || strings.HasSuffix(low, " tv")
}

func (p *Portal) getCategories(typ string) ([]VODCategory, error) {
	link := p.Location + "?type=" + url.QueryEscape(typ) + "&action=get_categories&JsHttpRequest=1-xml"
	content, err := p.httpRequest(link)
	if err != nil {
		return nil, err
	}
	rows, err := parseCategoryList(content)
	if err != nil {
		return nil, fmt.Errorf("get_categories(%s): %w", typ, err)
	}
	out := make([]VODCategory, 0, len(rows))
	for _, c := range rows {
		title := strings.TrimSpace(c.Title)
		if title == "" {
			title = strings.TrimSpace(c.Alias)
		}
		id := strings.TrimSpace(fmt.Sprint(c.ID))
		if id == "" || id == "<nil>" {
			continue
		}
		out = append(out, VODCategory{ID: id, Title: title})
	}
	return out, nil
}

type categoryRow struct {
	ID    interface{} `json:"id"`
	Title string      `json:"title"`
	Alias string      `json:"alias"`
}

func parseCategoryList(content []byte) ([]categoryRow, error) {
	var wrap struct {
		Js json.RawMessage `json:"js"`
	}
	if err := json.Unmarshal(content, &wrap); err != nil {
		return nil, err
	}
	js := bytes.TrimSpace(wrap.Js)
	if len(js) == 0 {
		return nil, nil
	}
	var list []categoryRow
	if err := json.Unmarshal(js, &list); err == nil {
		return list, nil
	}
	var obj struct {
		Data []categoryRow `json:"data"`
	}
	if err := json.Unmarshal(js, &obj); err == nil {
		return obj.Data, nil
	}
	return nil, fmt.Errorf("unexpected categories payload")
}

// GetVODList fetches one page of movies (0-based page index, per Stalker portal API).
func (p *Portal) GetVODList(categoryID string, page int) ([]VODItem, int, error) {
	return p.getOrderedList(vodListParams{
		typeParam:  "vod",
		categoryID: categoryID,
		page:       page,
		moviesOnly: true,
	})
}

// FetchAllVODMovies loads all pages for a movie category.
func (p *Portal) FetchAllVODMovies(categoryID string, maxPages int) ([]VODItem, error) {
	return p.fetchAllOrderedPages(vodListParams{
		typeParam:  "vod",
		categoryID: categoryID,
		moviesOnly: true,
	}, maxPages)
}

// GetSeriesList fetches one page of series in a category.
func (p *Portal) GetSeriesList(categoryID string, page int) ([]VODItem, int, error) {
	items, total, err := p.getOrderedList(vodListParams{
		typeParam:  "series",
		categoryID: categoryID,
		page:       page,
	})
	if err != nil || len(items) > 0 {
		return items, total, err
	}
	return p.getOrderedList(vodListParams{
		typeParam:  "vod",
		categoryID: categoryID,
		page:       page,
		seriesOnly: true,
	})
}

// FetchAllSeries loads all pages for a series category.
func (p *Portal) FetchAllSeries(categoryID string, maxPages int) ([]VODItem, error) {
	items, err := p.fetchAllOrderedPages(vodListParams{
		typeParam:  "series",
		categoryID: categoryID,
	}, maxPages)
	if err == nil && len(items) > 0 {
		return items, nil
	}
	return p.fetchAllOrderedPages(vodListParams{
		typeParam:  "vod",
		categoryID: categoryID,
		seriesOnly: true,
	}, maxPages)
}

// GetSeasons lists seasons for a series movie_id.
func (p *Portal) GetSeasons(movieID string, page int) ([]VODItem, int, error) {
	return p.getOrderedList(vodListParams{
		typeParam: "vod",
		movieID:   movieID,
		seasonID:  "0",
		episodeID: "0",
		page:      page,
		seasons:   true,
	})
}

// GetEpisodes lists episodes for a season.
func (p *Portal) GetEpisodes(movieID, seasonID string, page int) ([]VODItem, int, error) {
	return p.getOrderedList(vodListParams{
		typeParam: "vod",
		movieID:   movieID,
		seasonID:  seasonID,
		episodeID: "0",
		page:      page,
	})
}

// FetchAllEpisodes loads every episode in a season.
func (p *Portal) FetchAllEpisodes(movieID, seasonID string, maxPages int) ([]VODItem, error) {
	return p.fetchAllOrderedPages(vodListParams{
		typeParam: "vod",
		movieID:   movieID,
		seasonID:  seasonID,
		episodeID: "0",
	}, maxPages)
}

type vodListParams struct {
	typeParam  string
	categoryID string
	movieID    string
	seasonID   string
	episodeID  string
	page       int
	moviesOnly bool
	seriesOnly bool
	seasons    bool
}

// vodPageSem bounds concurrent portal list requests across all categories.
var vodPageSem = make(chan struct{}, 8)

func (p *Portal) getPageRetry(prm vodListParams) (items []VODItem, total, perPage int, err error) {
	for try := 0; try < 3; try++ {
		vodPageSem <- struct{}{}
		items, total, perPage, err = p.getOrderedListPP(prm)
		<-vodPageSem
		if err == nil {
			return
		}
		time.Sleep(time.Duration(try+1) * 300 * time.Millisecond)
	}
	return
}

// fetchAllOrderedPages loads every page (portal pages are 1-based; p=0 repeats page 1).
// maxPages <= 0 means no limit.
func (p *Portal) fetchAllOrderedPages(prm vodListParams, maxPages int) ([]VODItem, error) {
	prm.page = 1
	first, total, perPage, err := p.getPageRetry(prm)
	if err != nil {
		return nil, err
	}
	if len(first) == 0 && total == 0 {
		return first, nil
	}
	if perPage <= 0 {
		perPage = 14
	}
	totalPages := (total + perPage - 1) / perPage
	if totalPages < 1 {
		totalPages = 1
	}
	if maxPages > 0 && totalPages > maxPages {
		totalPages = maxPages
	}
	pages := make([][]VODItem, totalPages+1)
	pages[1] = first
	var wg sync.WaitGroup
	var errCount int32
	for page := 2; page <= totalPages; page++ {
		wg.Add(1)
		go func(page int) {
			defer wg.Done()
			q := prm
			q.page = page
			items, _, _, err := p.getPageRetry(q)
			if err != nil {
				atomic.AddInt32(&errCount, 1)
				return
			}
			pages[page] = items
		}(page)
	}
	wg.Wait()
	if errCount > 0 {
		log.Printf("[VOD] category %q: %d of %d pages failed after retries", prm.categoryID, errCount, totalPages)
	}
	seen := make(map[string]struct{}, total)
	out := make([]VODItem, 0, total)
	for _, list := range pages {
		for _, it := range list {
			k := it.ID + "|" + it.CMD
			if _, ok := seen[k]; ok {
				continue
			}
			seen[k] = struct{}{}
			out = append(out, it)
		}
	}
	return out, nil
}

func (p *Portal) getOrderedList(prm vodListParams) ([]VODItem, int, error) {
	items, total, _, err := p.getOrderedListPP(prm)
	return items, total, err
}

func (p *Portal) getOrderedListPP(prm vodListParams) ([]VODItem, int, int, error) {
	if prm.page < 0 {
		prm.page = 0
	}
	q := url.Values{}
	q.Set("type", prm.typeParam)
	q.Set("action", "get_ordered_list")
	q.Set("JsHttpRequest", "1-xml")
	q.Set("p", strconv.Itoa(prm.page))
	if prm.categoryID != "" {
		q.Set("category", prm.categoryID)
	}
	if prm.movieID != "" {
		q.Set("movie_id", prm.movieID)
	}
	if prm.seasonID != "" {
		q.Set("season_id", prm.seasonID)
	}
	if prm.episodeID != "" {
		q.Set("episode_id", prm.episodeID)
	}
	link := p.Location + "?" + q.Encode()
	content, err := p.httpRequest(link)
	if err != nil {
		return nil, 0, 0, err
	}
	items, total, perPage, err := parseVODListPP(content, prm)
	for i := range items {
		items[i].Logo = p.absoluteImageURL(items[i].Logo)
	}
	return items, total, perPage, err
}

// absoluteImageURL turns the portal's relative poster paths
// ("/stalker_portal/screenshots/...") into URLs clients can fetch; the portal
// serves them publicly. "false"/empty means no poster.
func (p *Portal) absoluteImageURL(img string) string {
	img = strings.TrimSpace(img)
	if img == "" || img == "false" || img == "null" || strings.HasPrefix(img, "http") {
		if img == "false" || img == "null" {
			return ""
		}
		return img
	}
	u, err := url.Parse(p.Location)
	if err != nil || u.Host == "" {
		return img
	}
	if !strings.HasPrefix(img, "/") {
		img = "/" + img
	}
	return u.Scheme + "://" + u.Host + img
}

func parseVODList(content []byte, prm vodListParams) ([]VODItem, int, error) {
	items, total, _, err := parseVODListPP(content, prm)
	return items, total, err
}

// parseVODListPP also returns the portal's page size (max_page_items), 0 if unknown.
func parseVODListPP(content []byte, prm vodListParams) ([]VODItem, int, int, error) {
	var lastPerPage int
	var wrap struct {
		Js json.RawMessage `json:"js"`
	}
	if err := json.Unmarshal(content, &wrap); err != nil {
		return nil, 0, 0, fmt.Errorf("vod list: invalid response: %w", err)
	}
	js := bytes.TrimSpace(wrap.Js)
	if len(js) == 0 {
		return nil, 0, 0, nil
	}

	type row struct {
		ID           interface{} `json:"id"`
		Name         flexString  `json:"name"`
		CMD          flexString  `json:"cmd"`
		Logo         flexString  `json:"screenshot_uri"`
		IsSeries     interface{} `json:"is_series"`
		IsSeason     interface{} `json:"is_season"`
		VideoID      interface{} `json:"video_id"`
		SeriesNumber interface{} `json:"series_number"`
		ContainerExt flexString  `json:"container_extension"`
		Added        flexString  `json:"added"`
		RatingIMDB   flexString  `json:"rating_imdb"`
		RatingKP     flexString  `json:"rating_kinopoisk"`
		Description  flexString  `json:"description"`
		Director     flexString  `json:"director"`
		Actors       flexString  `json:"actors"`
		Year         flexString  `json:"year"`
		GenresStr    flexString  `json:"genres_str"`
	}

	var rows []row
	var total int

	// Payload shape: { "data": [...], "total_items": N }
	var payload struct {
		TotalItems   interface{} `json:"total_items"`
		MaxPageItems interface{} `json:"max_page_items"`
		Data         []row       `json:"data"`
	}
	if err := json.Unmarshal(js, &payload); err == nil && (len(payload.Data) > 0 || payload.TotalItems != nil) {
		rows = payload.Data
		total = atoiFlexible(payload.TotalItems)
		lastPerPage = atoiFlexible(payload.MaxPageItems)
	} else {
		// Some portals return a bare array in js
		if err := json.Unmarshal(js, &rows); err != nil {
			return nil, 0, 0, fmt.Errorf("vod list: invalid js payload: %w", err)
		}
		total = len(rows)
	}

	out := make([]VODItem, 0, len(rows))
	for _, row := range rows {
		id := strings.TrimSpace(fmt.Sprint(row.ID))
		if id == "" || id == "<nil>" {
			id = strings.TrimSpace(string(row.CMD))
		}
		name := strings.TrimSpace(string(row.Name))
		if name == "" {
			continue
		}
		isSeries := isTruthy(row.IsSeries)
		isSeason := isTruthy(row.IsSeason)

		if prm.moviesOnly && isSeries {
			continue
		}
		if prm.seriesOnly && !isSeries {
			continue
		}
		if prm.seasons && !isSeason {
			continue
		}

		item := VODItem{
			ID:           id,
			Name:         name,
			CMD:          strings.TrimSpace(string(row.CMD)),
			Logo:         strings.TrimSpace(string(row.Logo)),
			IsSeries:     fmt.Sprint(row.IsSeries),
			ContainerExt: strings.TrimSpace(string(row.ContainerExt)),
		}
		item.Description = strings.TrimSpace(string(row.Description))
		item.Director = strings.TrimSpace(string(row.Director))
		item.Actors = strings.TrimSpace(string(row.Actors))
		item.Genres = strings.TrimSpace(string(row.GenresStr))
		if y := strings.TrimSpace(string(row.Year)); y != "0" {
			item.Year = y
		}
		if t, err := time.ParseInLocation("2006-01-02 15:04:05", strings.TrimSpace(string(row.Added)), time.UTC); err == nil {
			item.Added = strconv.FormatInt(t.Unix(), 10)
		}
		for _, rs := range []string{string(row.RatingIMDB), string(row.RatingKP)} {
			if f, err := strconv.ParseFloat(strings.TrimSpace(rs), 64); err == nil && f > 0 {
				item.Rating = strconv.FormatFloat(f, 'f', -1, 64)
				break
			}
		}
		if vid := strings.TrimSpace(fmt.Sprint(row.VideoID)); vid != "" && vid != "<nil>" {
			item.MovieID = vid
		}
		if prm.movieID != "" {
			item.MovieID = prm.movieID
		}
		if isSeason {
			item.SeasonID = id
		}
		if prm.seasonID != "" && prm.seasonID != "0" {
			item.SeasonID = prm.seasonID
		}
		if sn := strings.TrimSpace(fmt.Sprint(row.SeriesNumber)); sn != "" && sn != "<nil>" {
			item.EpisodeNumber = sn
		}
		out = append(out, item)
	}
	if total == 0 {
		total = len(out)
	}
	return out, total, lastPerPage, nil
}

func atoiFlexible(v interface{}) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(t))
		return n
	case json.Number:
		n, _ := t.Int64()
		return int(n)
	default:
		return 0
	}
}

func isTruthy(v interface{}) bool {
	switch t := v.(type) {
	case bool:
		return t
	case float64:
		return t != 0
	case int:
		return t != 0
	case string:
		s := strings.ToLower(strings.TrimSpace(t))
		return s == "1" || s == "true" || s == "yes"
	}
	return false
}

// NewVODLink creates a playable URL for a VOD item (movie).
func (p *Portal) NewVODLink(cmd string) (string, error) {
	pl, err := p.CreateVODPlayLink(cmd, "movie", "")
	if err != nil {
		return "", err
	}
	return pl.URL, nil
}

// CreateVODPlayLink resolves create_link for movies or series episodes.
func (p *Portal) CreateVODPlayLink(cmd, linkType, seriesNum string) (*VODPlayLink, error) {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return nil, fmt.Errorf("empty vod cmd")
	}
	if !strings.HasPrefix(cmd, "/") && !strings.HasPrefix(strings.ToLower(cmd), "ffmpeg") {
		cmd = "/media/file_" + cmd + ".mpg"
	}
	if linkType == "" {
		linkType = "movie"
	}
	q := url.Values{}
	q.Set("action", "create_link")
	q.Set("type", "vod")
	q.Set("cmd", cmd)
	q.Set("JsHttpRequest", "1-xml")
	if seriesNum != "" {
		q.Set("series", seriesNum)
	}
	link := p.Location + "?" + q.Encode()
	content, err := p.httpRequest(link)
	if err != nil {
		return nil, err
	}
	return p.parseCreateLinkVOD(content, linkType)
}

// CreateVODPlayLinkByID uses stream/file id when cmd is unknown.
func (p *Portal) CreateVODPlayLinkByID(streamID, linkType, seriesNum string) (*VODPlayLink, error) {
	streamID = strings.TrimSpace(streamID)
	if streamID == "" {
		return nil, fmt.Errorf("empty stream id")
	}
	if seriesNum == "" {
		if fid := p.lookupMovieFileID(streamID); fid != "" {
			return p.CreateVODPlayLink("/media/file_"+fid+".mpg", linkType, seriesNum)
		}
	}
	cmd := "/media/file_" + streamID + ".mpg"
	return p.CreateVODPlayLink(cmd, linkType, seriesNum)
}

// lookupMovieFileID resolves a movie id to its playable file id (the portal's
// create_link wants /media/file_<fileid>.mpg, not the movie id).
func (p *Portal) lookupMovieFileID(movieID string) string {
	return p.lookupFileID(movieID, "", "")
}

// LookupEpisodeFileID resolves an episode to its playable file id.
func (p *Portal) LookupEpisodeFileID(seriesID, seasonID, episodeID string) string {
	return p.lookupFileID(seriesID, seasonID, episodeID)
}

func (p *Portal) lookupFileID(movieID, seasonID, episodeID string) string {
	q := url.Values{}
	q.Set("type", "vod")
	q.Set("action", "get_ordered_list")
	q.Set("movie_id", movieID)
	if episodeID != "" {
		q.Set("season_id", seasonID)
		q.Set("episode_id", episodeID)
	}
	q.Set("JsHttpRequest", "1-xml")
	content, err := p.httpRequest(p.Location + "?" + q.Encode())
	if err != nil {
		return ""
	}
	var raw struct {
		Js struct {
			Data []struct {
				ID      flexString `json:"id"`
				Status  flexString `json:"status"`
				Quality flexString `json:"quality"`
			} `json:"data"`
		} `json:"js"`
	}
	if json.Unmarshal(content, &raw) != nil {
		return ""
	}
	best, bestQ := "", -1
	for _, f := range raw.Js.Data {
		if st := strings.TrimSpace(string(f.Status)); st != "" && st != "1" {
			continue
		}
		qv, _ := strconv.Atoi(strings.TrimSpace(string(f.Quality)))
		if id := strings.TrimSpace(string(f.ID)); id != "" && qv > bestQ {
			best, bestQ = id, qv
		}
	}
	return best
}

func (p *Portal) parseCreateLinkVOD(content []byte, linkType string) (*VODPlayLink, error) {
	var raw struct {
		Js struct {
			Cmd       string     `json:"cmd"`
			URL       string     `json:"url"`
			ID        flexString `json:"id"`
			PlayToken flexString `json:"play_token"`
		} `json:"js"`
	}
	if err := json.Unmarshal(content, &raw); err != nil {
		return nil, fmt.Errorf("create_link(vod): invalid response: %w", err)
	}
	if u := strings.TrimSpace(raw.Js.URL); u != "" {
		return &VODPlayLink{URL: normalizeVODStreamURL(u), Type: linkType}, nil
	}
	streamFile := strings.TrimSpace(string(raw.Js.ID))
	if streamFile != "" && !strings.Contains(streamFile, ".") {
		streamFile = streamFile + ".mp4"
	}
	token := strings.TrimSpace(string(raw.Js.PlayToken))
	rawCmd := strings.TrimSpace(raw.Js.Cmd)
	if rawCmd != "" {
		if u := extractStreamURL(rawCmd); strings.HasPrefix(strings.ToLower(u), "http") {
			if pl := parseMoviePHPPlayURL(u); pl != nil {
				pl.Type = linkType
				return pl, nil
			}
			return &VODPlayLink{URL: normalizeVODStreamURL(u), Type: linkType}, nil
		}
	}
	if streamFile != "" && token != "" {
		if u := p.buildMoviePlayURL(streamFile, token, linkType); u != "" {
			return &VODPlayLink{URL: u, Stream: streamFile, PlayToken: token, Type: linkType}, nil
		}
	}
	return nil, fmt.Errorf("empty create_link(vod) response")
}

func (p *Portal) buildMoviePlayURL(streamFile, playToken, linkType string) string {
	base := p.StreamServerBase()
	if base == "" {
		return ""
	}
	q := url.Values{}
	q.Set("mac", p.MAC)
	q.Set("stream", streamFile)
	q.Set("play_token", playToken)
	q.Set("type", linkType)
	return base + "/play/movie.php?" + q.Encode()
}

// StreamServerBase returns origin used for /play/movie.php (scheme://host[:port]).
func (p *Portal) StreamServerBase() string {
	u, err := url.Parse(strings.TrimSpace(p.Location))
	if err != nil {
		return ""
	}
	pth := u.Path
	if i := strings.Index(pth, "/stalker_portal"); i >= 0 {
		pth = pth[:i]
	} else if i := strings.Index(pth, "/c/"); i >= 0 {
		pth = pth[:i]
	} else {
		pth = path.Dir(pth)
	}
	if pth == "." || pth == "/" {
		pth = ""
	}
	u.Path = pth
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimRight(u.String(), "/")
}

func parseMoviePHPPlayURL(u string) *VODPlayLink {
	parsed, err := url.Parse(u)
	if err != nil || !strings.Contains(parsed.Path, "movie.php") {
		return nil
	}
	q := parsed.Query()
	return &VODPlayLink{
		URL:       u,
		Stream:    q.Get("stream"),
		PlayToken: q.Get("play_token"),
		Type:      q.Get("type"),
	}
}

func normalizeVODStreamURL(u string) string {
	u = strings.TrimSpace(u)
	u = reFFmpegPrefix.ReplaceAllString(u, "")
	return strings.TrimSpace(u)
}

func extractStreamURL(cmd string) string {
	cmd = normalizeVODStreamURL(cmd)
	parts := strings.Fields(cmd)
	if len(parts) == 0 {
		return ""
	}
	last := parts[len(parts)-1]
	if strings.HasPrefix(strings.ToLower(last), "http") {
		return last
	}
	return cmd
}

// flexString decodes a JSON string or number (portals are inconsistent) into a string.
type flexString string

func (f *flexString) UnmarshalJSON(b []byte) error {
	var v interface{}
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	switch t := v.(type) {
	case string:
		*f = flexString(t)
	case float64:
		*f = flexString(strconv.FormatFloat(t, 'f', -1, 64))
	default:
		*f = ""
	}
	return nil
}
