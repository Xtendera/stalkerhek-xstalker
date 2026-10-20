package stalker

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Start connects to stalker portal, authenticates, starts watchdog etc.
func (p *Portal) Start() error {
	// Reserve token in Stalker portal
	if err := p.handshake(); err != nil {
		return err
	}

	// Authorize token if credentials or deviceids are given
	if p.Username != "" && p.Password != "" {
		if err := p.authenticate(); err != nil {
			return err
		}
	} else if p.DeviceIdAuth == true || replayEnabled() {
		if err := p.authenticateWithDeviceIDs(); err != nil {
			return err
		}
	}

	// Run watchdog function once to check for errors:
	if err := p.watchdogUpdate(); err != nil {
		return err
	}

	// Run watchdog function every x minutes:
	if p.WatchDogTime > 0 {
		log.Println("Enabling Watchdog Updates ... ")
		go func() {
			for {
				time.Sleep(time.Duration(p.WatchDogTime) * time.Minute)
				if err := p.watchdogUpdate(); err != nil {
					log.Printf("Watchdog update failed: %v", err)
				}
			}
		}()
	} else {
		log.Println("Proceeding without Watchdog Updates")
	}
	return nil
}

var reauthMu sync.Mutex

// httpRequest performs a portal request and transparently re-authenticates
// (handshake + profile) once if the portal reports the session has expired.
func (p *Portal) httpRequest(link string) ([]byte, error) {
	b, err := p.httpRequestOnce(link)
	if err != nil || !bytes.HasPrefix(bytes.TrimSpace(b), []byte("Authorization failed")) {
		return b, err
	}
	reauthMu.Lock()
	if time.Since(p.lastReauth) > 10*time.Second {
		log.Println("Portal session expired, re-authenticating ...")
		if err := p.handshake(); err != nil {
			reauthMu.Unlock()
			return nil, err
		}
		if p.Username != "" && p.Password != "" {
			err = p.authenticate()
		} else {
			err = p.authenticateWithDeviceIDs()
		}
		p.lastReauth = time.Now()
		if err != nil {
			reauthMu.Unlock()
			return nil, err
		}
	}
	reauthMu.Unlock()
	return p.httpRequestOnce(link)
}

func (p *Portal) httpRequestOnce(link string) ([]byte, error) {
	req, err := http.NewRequest("GET", link, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (QtEmbedded; U; Linux; C) AppleWebKit/533.3 (KHTML, like Gecko) MAG200 stbapp ver: 4 rev: 2116 Mobile Safari/533.3")
	req.Header.Set("X-User-Agent", "Model: "+p.Model+"; Link: Ethernet")
	req.Header.Set("Authorization", "Bearer "+p.Token)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Pragma", "no-cache")
	if u, err := url.Parse(link); err == nil {
		req.Header.Set("Referer", u.Scheme+"://"+u.Host+"/")
		req.Header.Set("Origin", u.Scheme+"://"+u.Host)
	}

	cookieText := "PHPSESSID=null; sn=" + url.QueryEscape(p.SerialNumber) + "; mac=" + url.QueryEscape(p.MAC) + "; stb_lang=en; timezone=" + url.QueryEscape(p.TimeZone) + ";"

	req.Header.Set("Cookie", cookieText)
	if replayEnabled() {
		p.replayHeaders(req, true)
	}

	resp, err := HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		safeLink := link
		if u, err := url.Parse(link); err == nil {
			safeLink = u.Scheme + "://" + u.Host + u.Path
		}
		const maxDiag = 4096
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxDiag))
		msg := strings.TrimSpace(string(snippet))
		if len(msg) > 300 {
			msg = msg[:300]
		}
		if msg != "" {
			return nil, errors.New("Site '" + safeLink + "' returned " + resp.Status + ": " + msg)
		}
		return nil, errors.New("Site '" + safeLink + "' returned " + resp.Status)
	}

	contents, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	return contents, nil
}

// WatchdogUpdate performs watchdog update request.
func (p *Portal) watchdogUpdate() error {
	type wdStruct struct {
		Js   json.RawMessage `json:"js"`
		Text string          `json:"text"`
	}
	var wd wdStruct
	content, err := p.httpRequest(p.Location + "?action=get_events&event_active_id=0&init=0&type=watchdog&cur_play_type=1&JsHttpRequest=1-xml")
	if err != nil {
		return err
	}

	if err := json.Unmarshal(content, &wd); err != nil {
		return fmt.Errorf("watchdog update: invalid response: %w", err)
	}

	js := bytes.TrimSpace(wd.Js)
	if len(js) == 0 || js[0] == '[' {
		return nil
	}
	if js[0] == '{' {
		var payload struct {
			Data struct {
				Msgs                   int `json:"msgs"`
				Additional_services_on int `json:"additional_services_on"`
			} `json:"data"`
		}
		if err := json.Unmarshal(js, &payload); err != nil {
			return nil
		}
		return nil
	}

	return nil
}
