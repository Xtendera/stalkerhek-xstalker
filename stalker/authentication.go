package stalker

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Handshake reserves a offered token in Portal. If offered token is not available - new one will be issued by stalker portal, reservedMAG254 and Stalker's config will be updated.
func (p *Portal) handshake() error {
	// This HTTP request has different headers from the rest of HTTP requests, so perform it manually
	type tmpStruct struct {
		Js map[string]interface{} `json:"js"`
	}
	var tmp tmpStruct

	handshakeURL := p.Location + "?type=stb&action=handshake&token=" + p.Token + "&JsHttpRequest=1-xml"
	if replayEnabled() {
		handshakeURL = p.replayHandshakeURL()
	}
	req, err := http.NewRequest("GET", handshakeURL, nil)
	if err != nil {
		return err
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (QtEmbedded; U; Linux; C) AppleWebKit/533.3 (KHTML, like Gecko) MAG200 stbapp ver: 4 rev: 2116 Mobile Safari/533.3")
	req.Header.Set("X-User-Agent", "Model: "+p.Model+"; Link: Ethernet")
	req.Header.Set("Cookie", "sn="+p.SerialNumber+"; mac="+p.MAC+"; stb_lang=en; timezone="+p.TimeZone)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Pragma", "no-cache")
	if u, err := url.Parse(p.Location); err == nil {
		req.Header.Set("Referer", u.Scheme+"://"+u.Host+"/")
		req.Header.Set("Origin", u.Scheme+"://"+u.Host)
	}

	if replayEnabled() {
		p.replayHeaders(req, false)
	}

	resp, err := HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	contents, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if err = json.Unmarshal(contents, &tmp); err != nil {
		log.Println(string(contents))
		return err
	}

	if r, ok := tmp.Js["random"].(string); ok {
		p.random = r
	}

	token, ok := tmp.Js["token"]
	if !ok || token == "" {
		// Token accepted. Using accepted token
		return nil
	}
	// Server provided new token. Using new provided token
	p.Token = token.(string)
	return nil
}

// Authenticate associates credentials with token. In other words - logs you in
func (p *Portal) authenticate() (err error) {
	// This HTTP request has different headers from the rest of HTTP requests, so perform it manually
	type tmpStruct struct {
		Js   bool   `json:"js"`
		Text string `json:"text"`
	}
	var tmp tmpStruct

	// Build POST request to avoid logging password in URL
	formData := url.Values{}
	formData.Set("type", "stb")
	formData.Set("action", "do_auth")
	formData.Set("login", p.Username)
	formData.Set("password", p.Password)
	formData.Set("device_id", p.DeviceID)
	formData.Set("device_id2", p.DeviceID2)
	formData.Set("JsHttpRequest", "1-xml")

	req, err := http.NewRequest("POST", p.Location, strings.NewReader(formData.Encode()))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Mozilla/5.0 (QtEmbedded; U; Linux; C) AppleWebKit/533.3 (KHTML, like Gecko) MAG200 stbapp ver: 4 rev: 2116 Mobile Safari/533.3")
	req.Header.Set("X-User-Agent", "Model: "+p.Model+"; Link: Ethernet")
	req.Header.Set("Authorization", "Bearer "+p.Token)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Pragma", "no-cache")
	if u, err := url.Parse(p.Location); err == nil {
		req.Header.Set("Referer", u.Scheme+"://"+u.Host+"/")
		req.Header.Set("Origin", u.Scheme+"://"+u.Host)
	}
	cookieText := "PHPSESSID=null; sn=" + url.QueryEscape(p.SerialNumber) + "; mac=" + url.QueryEscape(p.MAC) + "; stb_lang=en; timezone=" + url.QueryEscape(p.TimeZone) + ";"
	req.Header.Set("Cookie", cookieText)

	resp, err := HTTPClient.Do(req)
	if err != nil {
		log.Println("HTTP authentication request failed")
		return err
	}
	defer resp.Body.Close()

	content, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Println("HTTP authentication request failed")
		return err
	}

	if err = json.Unmarshal(content, &tmp); err != nil {
		log.Println("parsing authentication response failed")
		return err
	}

	log.Println("Logging in to Stalker says:")
	log.Println(tmp.Text)

	if tmp.Js {
		// all good
		return nil
	}

	// questionable, but probably bad credentials
	return errors.New("invalid credentials")
}

// Authenticate with Device IDs
func (p *Portal) authenticateWithDeviceIDs() (err error) {
	// This HTTP request has different headers from the rest of HTTP requests, so perform it manually
	type tmpStruct struct {
		Js struct {
			Id    any    `json:"id"`
			Fname string `json:"fname"`
		} `json:"js"`
		Text string `json:"text"`
	}
	var tmp tmpStruct

	log.Println("Authenticating with DeviceId and DeviceId2")
	link := p.Location + "?type=stb&action=get_profile&JsHttpRequest=1-xml&hd=1&sn=" + p.SerialNumber + "&stb_type=" + p.Model + "&device_id=" + p.DeviceID + "&device_id2=" + p.DeviceID2 + "&auth_second_step=1"
	if replayEnabled() {
		link = p.replayProfileURL(p.random)
	}
	content, err := p.httpRequestOnce(link)

	if err != nil {
		log.Println("HTTP authentication request failed")
		return err
	}

	dec := json.NewDecoder(bytes.NewReader(content))
	dec.UseNumber()
	if err = dec.Decode(&tmp); err != nil {
		log.Println("Unexpected authentication response")
		return err
	}

	log.Println("Logging in to Stalker says:")
	log.Println(tmp.Text)

	id := ""
	switch v := tmp.Js.Id.(type) {
	case string:
		id = v
	case float64:
		id = strconv.FormatInt(int64(v), 10)
	case json.Number:
		id = v.String()
	}
	if id != "" {
		log.Println("Authenticated as " + tmp.Js.Fname)
		return nil
	}

	// questionable, but probably bad credentials
	return errors.New("invalid credentials")
}
