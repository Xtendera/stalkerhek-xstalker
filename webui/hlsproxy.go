package webui

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
)

// Live TV is served by a separate HLS listener (default :4600). Behind a
// single-port tunnel (ngrok, Cloudflare) clients can't reach it, so the main
// port also forwards /iptv/ and /logo/ to the HLS listener of whichever
// profile owns the channel.

// viaProxy reports whether the request came through a reverse proxy/tunnel.
func viaProxy(r *http.Request) bool {
	for _, h := range []string{"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "Forwarded", "X-Real-Ip"} {
		if r.Header.Get(h) != "" {
			return true
		}
	}
	return false
}

func hlsPortForTitle(title string) (int, bool) {
	for _, pr := range ListProfiles() {
		if chs, _, ok := GetProfileChannels(pr.ID); ok {
			if _, found := chs[title]; found && pr.HlsPort > 0 {
				return pr.HlsPort, true
			}
		}
	}
	return 0, false
}

func handleHLSForward(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.EscapedPath(), "/"), "/")
	if len(parts) < 2 {
		http.NotFound(w, r)
		return
	}
	title, err := url.PathUnescape(parts[1])
	if err != nil {
		http.NotFound(w, r)
		return
	}
	port, ok := hlsPortForTitle(title)
	if !ok {
		http.NotFound(w, r)
		return
	}
	target, _ := url.Parse("http://127.0.0.1:" + strconv.Itoa(port))
	rp := httputil.NewSingleHostReverseProxy(target)
	rp.FlushInterval = -1
	rp.ServeHTTP(w, r)
}
