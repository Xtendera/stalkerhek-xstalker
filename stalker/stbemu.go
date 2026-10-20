package stalker

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// StbEmu replay mode: when STALKERHEK_STBEMU_REPLAY=1, requests mimic a
// captured StbEmu (MAG250) session byte-for-byte where the portal checks it.
// Device-specific values come from env vars so no secrets live in the source.
//   STALKERHEK_STBEMU_HW2      hw_version_2 value
//   STALKERHEK_STBEMU_PREHASH  prehash value
//   STALKERHEK_STBEMU_ADID     adid cookie value
//   STALKERHEK_STBEMU_HWVER    hw_version (default 1.7-BD-00)

const stbEmuUserAgent = "Mozilla/5.0 (QtEmbedded; U; Linux; C) AppleWebKit/533.3 (KHTML, like Gecko) MAG200 stbapp ver: 2 rev: 250 Safari/533.3"

func replayEnabled() bool { return os.Getenv("STALKERHEK_STBEMU_REPLAY") == "1" }

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func replayToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return strings.ToUpper(hex.EncodeToString(b))
}

// replayHeaders overwrites identity headers on req to match StbEmu.
func (p *Portal) replayHeaders(req *http.Request, withAuth bool) {
	req.Header.Set("User-Agent", stbEmuUserAgent)
	req.Header.Set("X-User-Agent", "Model: "+p.Model+"; Link: Ethernet")
	req.Header.Set("Cookie", "mac="+url.QueryEscape(p.MAC)+"; stb_lang=en; timezone="+url.QueryEscape(p.TimeZone)+"; adid="+envOr("STALKERHEK_STBEMU_ADID", ""))
	req.Header.Del("Origin")
	req.Header.Del("Accept-Language")
	req.Header.Del("Pragma")
	if u, err := url.Parse(p.Location); err == nil {
		req.Header.Set("Referer", u.Scheme+"://"+u.Host+"/stalker_portal/c/")
	}
	if withAuth {
		req.Header.Set("Authorization", "Bearer "+p.Token)
	}
}

func (p *Portal) replayHandshakeURL() string {
	if p.Token == "" {
		p.Token = replayToken()
	}
	return p.Location + "?type=stb&action=handshake&token=" + p.Token +
		"&prehash=" + os.Getenv("STALKERHEK_STBEMU_PREHASH") + "&JsHttpRequest=1-xml"
}

// replayProfileURL is the exact get_profile query StbEmu sends.
func (p *Portal) replayProfileURL(random string) string {
	metrics := `{"mac":"` + p.MAC + `","sn":"` + p.SerialNumber + `","model":"` + p.Model +
		`","type":"STB","uid":"","random":"` + random + `"}`
	q := "type=stb&action=get_profile&hd=1" +
		"&ver=" + strings.ReplaceAll(url.QueryEscape("ImageDescription: 0.2.18-r19-pub-250; ImageDate: Mon Jun 12 11:04:49 EEST 2017; PORTAL version: 5.6.1; API Version: JS API version: 343; STB API version: 146; Player Engine version: 0x588"), "+", "%20") +
		"&num_banks=2&sn=" + url.QueryEscape(p.SerialNumber) + "&stb_type=" + p.Model +
		"&client_type=STB&image_version=218&video_out=hdmi&device_id=&device_id2=&signature=" +
		"&auth_second_step=0&hw_version=" + url.QueryEscape(envOr("STALKERHEK_STBEMU_HWVER", "1.7-BD-00")) +
		"&not_valid_token=0&metrics=" + url.QueryEscape(metrics) +
		"&hw_version_2=" + os.Getenv("STALKERHEK_STBEMU_HW2") +
		"&timestamp=" + strconv.FormatInt(time.Now().Unix(), 10) + "&api_signature=262&prehash=" + os.Getenv("STALKERHEK_STBEMU_PREHASH") +
		"&JsHttpRequest=1-xml"
	return p.Location + "?" + q
}
