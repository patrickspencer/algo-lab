package server

import (
	"errors"
	"net/http"

	"github.com/patrickspencer/algo-lab-public/internal/dataset"
)

// Curated problem lists. This build bundles a single list: the original open
// problem set that ships with the app.

type curatedList struct {
	Slug   string   `json:"slug"`
	Name   string   `json:"name"`
	Source string   `json:"source"`
	Static []string `json:"-"` // bundled slugs
}

var curatedLists []curatedList

func init() {
	if dataset.Count() > 0 {
		curatedLists = append(curatedLists, curatedList{
			Slug: "original-100", Name: "Original 100 (open)", Source: "open", Static: dataset.Slugs(),
		})
	}
}

func listBySlug(slug string) (curatedList, bool) {
	for _, l := range curatedLists {
		if l.Slug == slug {
			return l, true
		}
	}
	return curatedList{}, false
}

func (s *Server) handleLists(w http.ResponseWriter, r *http.Request) {
	type listInfo struct {
		curatedList
		Count  int  `json:"count"`
		Cached bool `json:"cached"`
	}
	out := make([]listInfo, 0, len(curatedLists))
	for _, l := range curatedLists {
		out = append(out, listInfo{curatedList: l, Count: len(l.Static), Cached: true})
	}
	writeJSON(w, 200, map[string]any{"lists": out})
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	l, ok := listBySlug(r.PathValue("slug"))
	if !ok {
		writeErr(w, 404, errors.New("unknown list"))
		return
	}
	writeJSON(w, 200, map[string]any{"slug": l.Slug, "name": l.Name, "source": l.Source, "slugs": l.Static})
}
