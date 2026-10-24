package webui

import (
	"testing"

	"github.com/kidpoleon/stalkerhek/stalker"
)

func TestFilterVODCategoriesFirstWord(t *testing.T) {
	vodFilterLoad.Do(func() {})
	vodFilterMode = vodFilterLanguages
	in := map[string]bool{
		"ENGLISH MOVIES | 4K":         true,
		"Hindi | Kids":                true,
		"GUJARATI | NATAK":            true,
		"HINDI DUB | ENG MOVIES":      true,
		"TAMIL DUB | ENGLISH MOVIES":  false,
		"TELUGU DUB | ENGLISH MOVIES": false,
		"SPORTING EVENTSENGLISH | TV": false,
		"PUNJABI | MOVIES":            false,
	}
	var cats []stalker.VODCategory
	for title := range in {
		cats = append(cats, stalker.VODCategory{ID: title, Title: title})
	}
	got := map[string]bool{}
	for _, c := range filterVODCategories(cats) {
		got[c.Title] = true
	}
	for title, want := range in {
		if got[title] != want {
			t.Errorf("%q kept=%v want %v", title, got[title], want)
		}
	}
}
