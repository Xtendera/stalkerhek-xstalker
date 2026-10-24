package webui

import (
	"log"
	"os"
	"strconv"
)

// LoadProfilesFromEnv merges profiles defined through environment variables
// into the in-memory list, so a container can be configured without any file.
//
// Profile N (1, 2, 3, ...) is read from STALKERHEK_PROFILE_<N>_<FIELD>:
//
//	PORTAL_URL (required), MAC (required), NAME, HLS_PORT, PROXY_PORT, MODEL,
//	SERIAL_NUMBER, DEVICE_ID, DEVICE_ID2, SIGNATURE, TIME_ZONE, USERNAME,
//	PASSWORD, WATCHDOG_TIME, EPG_URL
//
// Numbering stops at the first N without a PORTAL_URL. An env profile replaces
// a persisted profile with the same ID; other persisted profiles are kept.
func LoadProfilesFromEnv() int {
	n := 0
	for i := 1; ; i++ {
		get := func(field string) string {
			return os.Getenv("STALKERHEK_PROFILE_" + strconv.Itoa(i) + "_" + field)
		}
		portal := get("PORTAL_URL")
		if portal == "" {
			break
		}
		mac := get("MAC")
		if mac == "" {
			log.Printf("env profile %d: MAC is required, skipping", i)
			continue
		}
		p := Profile{
			ID:           i,
			Name:         get("NAME"),
			PortalURL:    portal,
			MAC:          mac,
			Model:        get("MODEL"),
			SerialNumber: get("SERIAL_NUMBER"),
			DeviceID:     get("DEVICE_ID"),
			DeviceID2:    get("DEVICE_ID2"),
			Signature:    get("SIGNATURE"),
			TimeZone:     get("TIME_ZONE"),
			Username:     get("USERNAME"),
			Password:     get("PASSWORD"),
			EPGURL:       get("EPG_URL"),
		}
		if p.Name == "" {
			p.Name = "profile-" + strconv.Itoa(i)
		}
		p.HlsPort, _ = strconv.Atoi(get("HLS_PORT"))
		p.ProxyPort, _ = strconv.Atoi(get("PROXY_PORT"))
		p.WatchDogTime, _ = strconv.Atoi(get("WATCHDOG_TIME"))

		profMu.Lock()
		replaced := false
		for j := range profiles {
			if profiles[j].ID == p.ID {
				profiles[j] = p
				replaced = true
				break
			}
		}
		if !replaced {
			profiles = append(profiles, p)
		}
		if p.ID >= nextProfile {
			nextProfile = p.ID + 1
		}
		profMu.Unlock()
		n++
	}
	return n
}
