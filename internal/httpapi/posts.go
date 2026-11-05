package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/lilya/practice/internal/analysis"
	"github.com/lilya/practice/internal/store"
)

type listResponse struct {
	Items []store.PostCard `json:"items"`
	Total int              `json:"total"`
}

// parseListFilter reads the shared query parameters:
//
//	q=text  min_duration=sec  max_duration=sec  tag=kind:slug (repeatable, AND)
//	done=true|false  limit  offset
func parseListFilter(r *http.Request) (store.ListFilter, error) {
	q := r.URL.Query()
	f := store.ListFilter{Query: strings.TrimSpace(q.Get("q"))}
	if len(f.Query) > 200 {
		f.Query = f.Query[:200]
	}
	intParam := func(name string) (*int, error) {
		v := q.Get(name)
		if v == "" {
			return nil, nil
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return nil, errors.New("invalid " + name)
		}
		return &n, nil
	}
	var err error
	if f.MinDuration, err = intParam("min_duration"); err != nil {
		return f, err
	}
	if f.MaxDuration, err = intParam("max_duration"); err != nil {
		return f, err
	}
	for _, t := range q["tag"] {
		kind, slug, ok := strings.Cut(t, ":")
		if !ok || !store.ValidTagKind(kind) || slug == "" {
			return f, errors.New("invalid tag filter " + strconv.Quote(t))
		}
		f.Tags = append(f.Tags, store.TagRef{Kind: kind, Slug: slug})
	}
	if len(f.Tags) > 20 {
		return f, errors.New("too many tag filters")
	}
	switch q.Get("done") {
	case "true":
		b := true
		f.Done = &b
	case "false":
		b := false
		f.Done = &b
	}
	if l, err := intParam("limit"); err != nil {
		return f, err
	} else if l != nil {
		f.Limit = *l
	}
	if o, err := intParam("offset"); err != nil {
		return f, err
	} else if o != nil {
		f.Offset = *o
	}
	return f, nil
}

func (s *Server) listPosts(w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(r.Context())
	f, err := parseListFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_filter", err.Error())
		return
	}
	f.UserID = u.ID
	f.Statuses = []string{store.PostPublished}
	items, total, err := s.Store.ListPosts(r.Context(), f)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, listResponse{Items: items, Total: total})
}

func (s *Server) getPost(w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(r.Context())
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	p, err := s.Store.GetPost(r.Context(), id, u.ID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && p.Status != store.PostPublished && !u.IsAdmin()) {
		writeError(w, http.StatusNotFound, "not_found", "practice not found")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if !u.IsAdmin() {
		redactForMember(&p)
	}
	writeJSON(w, http.StatusOK, p)
}

// redactForMember hides operational details (errors, token usage, in-flight
// analyses) from regular members.
func redactForMember(p *store.PostDetail) {
	if p.Analysis != nil {
		if p.Analysis.Status != store.AnalysisDone {
			p.Analysis = nil
		} else {
			p.Analysis.Error = nil
			p.Analysis.Steps = nil
			p.Analysis.RequestedSteps = nil
			p.Analysis.Provider, p.Analysis.Model = "", ""
			p.Analysis.InputTokens = nil
			p.Analysis.OutputTokens = nil
		}
	}
	for i := range p.Tags {
		p.Tags[i].Origin = ""
	}
	if p.Video != nil {
		p.Video.Error = nil
		p.Video.OriginalFilename = ""
	}
}

func (s *Server) visiblePost(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, ok := idParam(w, r, "id")
	if !ok {
		return 0, false
	}
	p, err := s.Store.GetPost(r.Context(), id, 0)
	if errors.Is(err, store.ErrNotFound) || (err == nil && p.Status != store.PostPublished) {
		writeError(w, http.StatusNotFound, "not_found", "practice not found")
		return 0, false
	}
	if err != nil {
		s.internalError(w, r, err)
		return 0, false
	}
	return id, true
}

func (s *Server) addCompletion(w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(r.Context())
	id, ok := s.visiblePost(w, r)
	if !ok {
		return
	}
	if _, err := s.Store.AddCompletion(r.Context(), u.ID, id); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.completionState(w, r, u.ID, id, http.StatusCreated)
}

func (s *Server) removeCompletion(w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(r.Context())
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	if err := s.Store.RemoveLatestCompletion(r.Context(), u.ID, id); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.internalError(w, r, err)
		return
	}
	s.completionState(w, r, u.ID, id, http.StatusOK)
}

func (s *Server) completionState(w http.ResponseWriter, r *http.Request, userID, postID int64, status int) {
	p, err := s.Store.GetPost(r.Context(), postID, userID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, status, map[string]any{"completion": p.Completion})
}

func (s *Server) tagFacets(w http.ResponseWriter, r *http.Request) {
	facets, err := s.Store.TagFacets(r.Context(), true)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": facets})
}

func (s *Server) vocabulary(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, max-age=3600")
	writeJSON(w, http.StatusOK, analysis.GetVocabulary())
}
