package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/patrickspencer/algo-lab-public/internal/ai"
	"github.com/patrickspencer/algo-lab-public/internal/dataset"
	"github.com/patrickspencer/algo-lab-public/internal/catalog"
	"github.com/patrickspencer/algo-lab-public/internal/run"
	"github.com/patrickspencer/algo-lab-public/internal/store"
)

// JSON shapes.

type problemJSON struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Slug       string   `json:"slug"`
	Difficulty string   `json:"difficulty"`
	AcRate     float64  `json:"acRate"`
	PaidOnly   bool     `json:"paidOnly"`
	Tags       []string `json:"tags"`
	Status     string   `json:"status"` // "ac", "notac" or ""
}

// progressToStatus maps stored progress to the list vocabulary the UI uses.
func progressToStatus(p string) string {
	switch p {
	case "solved":
		return "ac"
	case "attempted":
		return "notac"
	}
	return ""
}

func toProblemJSON(p catalog.Problem) problemJSON {
	tags := make([]string, 0, len(p.TopicTags))
	for _, t := range p.TopicTags {
		tags = append(tags, t.Name)
	}
	return problemJSON{ID: p.FrontendID, Title: p.Title, Slug: p.TitleSlug, Difficulty: p.Difficulty,
		AcRate: p.AcRate, PaidOnly: p.PaidOnly, Tags: tags, Status: p.Status}
}

type attemptJSON struct {
	ID        int64     `json:"id"`
	Lang      string    `json:"lang"`
	Code      string    `json:"code"`
	CreatedAt time.Time `json:"createdAt"`
}

func toAttemptsJSON(as []store.Attempt) []attemptJSON {
	out := make([]attemptJSON, 0, len(as))
	for _, a := range as {
		out = append(out, attemptJSON{ID: a.ID, Lang: a.Lang, Code: a.Code, CreatedAt: a.CreatedAt})
	}
	return out
}

type hintJSON struct {
	Level     int       `json:"level"`
	Text      string    `json:"text"`
	Provider  string    `json:"provider"`
	CreatedAt time.Time `json:"createdAt"`
	Cached    bool      `json:"cached"`
}

type reviewJSON struct {
	ID        int64      `json:"id"`
	Lang      string     `json:"lang"`
	Code      string     `json:"code"`
	Provider  string     `json:"provider"`
	Review    *ai.Review `json:"review"`
	CreatedAt time.Time  `json:"createdAt"`
	Cached    bool       `json:"cached"`
}

func toReviewJSON(rec store.ReviewRecord, cached bool) (reviewJSON, error) {
	var r ai.Review
	if err := json.Unmarshal([]byte(rec.Review), &r); err != nil {
		return reviewJSON{}, err
	}
	return reviewJSON{ID: rec.ID, Lang: rec.Lang, Code: rec.Code, Provider: rec.Provider, Review: &r,
		CreatedAt: rec.CreatedAt, Cached: cached}, nil
}

// ---------------------------------------------------------------------------
// Handlers

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	problems, err := s.problemList(r.Context())
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	aiName, aiErr := "", ""
	if s.ai != nil {
		aiName = s.ai.Name()
	} else if s.aiErr != nil {
		aiErr = s.aiErr.Error()
	}
	var me any
	if u, ok := s.currentUser(r); ok {
		me = map[string]any{"id": u.ID, "name": u.Name}
	}
	writeJSON(w, 200, map[string]any{
		"me":       me,
		"dbPath":   s.store.Path,
		"problems": len(problems),
		"ai":       aiName,
		"aiError":  aiErr,
	})
}

func (s *Server) handleProblems(w http.ResponseWriter, r *http.Request) {
	problems, err := s.problemList(r.Context())
	if err != nil {
		writeErr(w, 502, err)
		return
	}
	uid := userOf(r).ID
	counts, err := s.store.AttemptCounts(r.Context(), uid)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	progress, err := s.store.Progress(r.Context(), uid)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	out := make([]problemJSON, 0, len(problems))
	for _, p := range problems {
		pj := toProblemJSON(p)
		pj.Status = progressToStatus(progress[p.TitleSlug]) // per-user, overrides the shared field
		out = append(out, pj)
	}
	writeJSON(w, 200, map[string]any{"problems": out, "attemptCounts": counts})
}

func (s *Server) handleProblem(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	slug := r.PathValue("slug")
	if op, ok := dataset.BySlug(slug); ok {
		s.serveOriginal(w, r, op)
		return
	}
	_ = ctx
	writeErr(w, 404, errors.New("unknown problem"))
}

func (s *Server) handleDraft(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Lang string `json:"lang"`
		Code string `json:"code"`
	}
	if err := readJSON(w, r, &body); err != nil || body.Lang == "" {
		writeErr(w, 400, errors.New("lang and code required"))
		return
	}
	if err := s.store.SaveDraft(r.Context(), userOf(r).ID, r.PathValue("slug"), body.Lang, body.Code); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"savedAt": time.Now()})
}

func (s *Server) handleSaveAttempt(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Lang string `json:"lang"`
		Code string `json:"code"`
	}
	if err := readJSON(w, r, &body); err != nil || body.Lang == "" || strings.TrimSpace(body.Code) == "" {
		writeErr(w, 400, errors.New("lang and non-empty code required"))
		return
	}
	slug := r.PathValue("slug")
	uid := userOf(r).ID
	id, err := s.store.SaveAttempt(r.Context(), uid, slug, body.Lang, body.Code)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	_ = s.store.SaveDraft(r.Context(), uid, slug, body.Lang, body.Code)
	_ = s.store.MarkAttempted(r.Context(), uid, slug)
	attempts, err := s.store.Attempts(r.Context(), uid, slug)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"id": id, "attempts": toAttemptsJSON(attempts)})
}

func (s *Server) handleDeleteAttempt(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r.PathValue("id"))
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	if err := s.store.DeleteAttempt(r.Context(), userOf(r).ID, id); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": id})
}

func (s *Server) requireAI(w http.ResponseWriter) bool {
	if s.ai != nil {
		return true
	}
	err := s.aiErr
	if err == nil {
		err = ai.ErrUnavailable
	}
	writeErr(w, 503, err)
	return false
}

// promptContext returns the problem statement to feed the AI and a starter-code
// lookup, from the embedded original problem content.
func (s *Server) promptContext(ctx context.Context, slug string) (statement string, starter func(lang string) string, ok bool) {
	if op, exists := dataset.BySlug(slug); exists {
		var b strings.Builder
		b.WriteString("# " + op.Title + " (" + op.Difficulty + ")\n\n" + op.Statement + "\n")
		if len(op.Constraints) > 0 {
			b.WriteString("\nConstraints:\n")
			for _, c := range op.Constraints {
				b.WriteString("- " + c + "\n")
			}
		}
		return b.String(), op.StarterCode, true
	}
	_ = ctx
	return "", nil, false
}

func (s *Server) handleHint(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Level int    `json:"level"`
		Lang  string `json:"lang"`
		Code  string `json:"code"`
		Force bool   `json:"force"`
	}
	if err := readJSON(w, r, &body); err != nil || body.Level < 1 || body.Level > 3 {
		writeErr(w, 400, errors.New("level must be 1, 2 or 3"))
		return
	}
	ctx := r.Context()
	slug := r.PathValue("slug")
	if !body.Force {
		hints, err := s.store.Hints(ctx, slug)
		if err != nil {
			writeErr(w, 500, err)
			return
		}
		if h, ok := hints[body.Level]; ok {
			writeJSON(w, 200, hintJSON{Level: h.Level, Text: h.Text, Provider: h.Provider, CreatedAt: h.CreatedAt, Cached: true})
			return
		}
	}
	if !s.requireAI(w) {
		return
	}
	statement, starter, ok := s.promptContext(ctx, slug)
	if !ok {
		writeErr(w, 502, errors.New("could not load the problem"))
		return
	}
	code := body.Code
	if strings.TrimSpace(starter(body.Lang)) == strings.TrimSpace(code) {
		code = ""
	}
	system, user := ai.HintPrompt(body.Level, statement, body.Lang, code)
	text, err := s.ai.Complete(ctx, system, user)
	if err != nil {
		writeErr(w, 502, err)
		return
	}
	if err := s.store.SaveHint(ctx, slug, body.Level, s.ai.Name(), text); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, hintJSON{Level: body.Level, Text: text, Provider: s.ai.Name(), CreatedAt: time.Now()})
}

func (s *Server) handleReview(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Lang  string `json:"lang"`
		Code  string `json:"code"`
		Force bool   `json:"force"`
	}
	if err := readJSON(w, r, &body); err != nil || body.Lang == "" || strings.TrimSpace(body.Code) == "" {
		writeErr(w, 400, errors.New("lang and non-empty code required"))
		return
	}
	ctx := r.Context()
	slug := r.PathValue("slug")
	if !body.Force {
		if rec, ok, err := s.store.LatestReview(ctx, slug); err != nil {
			writeErr(w, 500, err)
			return
		} else if ok && rec.Code == body.Code {
			if rj, err := toReviewJSON(rec, true); err == nil {
				writeJSON(w, 200, rj)
				return
			}
		}
	}
	if !s.requireAI(w) {
		return
	}
	statement, _, ok := s.promptContext(ctx, slug)
	if !ok {
		writeErr(w, 502, errors.New("could not load the problem"))
		return
	}
	_ = s.store.SaveDraft(ctx, userOf(r).ID, slug, body.Lang, body.Code)
	system, user := ai.ReviewPrompt(statement, body.Lang, body.Code)
	text, err := s.ai.Complete(ctx, system, user)
	if err != nil {
		writeErr(w, 502, err)
		return
	}
	review, err := ai.ParseReview(text, strings.Count(body.Code, "\n")+1)
	if err != nil {
		writeErr(w, 502, fmt.Errorf("%w: %s", err, text))
		return
	}
	data, _ := json.Marshal(review)
	id, err := s.store.SaveReview(ctx, slug, body.Lang, body.Code, s.ai.Name(), string(data))
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, reviewJSON{ID: id, Lang: body.Lang, Code: body.Code, Provider: s.ai.Name(), Review: review, CreatedAt: time.Now()})
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	lang, err := s.store.Meta(r.Context(), "last_lang")
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	if lang == "" {
		lang = os.Getenv("ALGOLAB_LANG")
	}
	if lang == "" {
		lang = "python3"
	}
	writeJSON(w, 200, map[string]any{"lang": lang})
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Lang string `json:"lang"`
	}
	if err := readJSON(w, r, &body); err != nil || body.Lang == "" {
		writeErr(w, 400, errors.New("lang required"))
		return
	}
	if err := s.store.SetMeta(r.Context(), "last_lang", body.Lang); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"lang": body.Lang})
}

// ---------------------------------------------------------------------------
// Running code and scratchpads

func (s *Server) handleLanguages(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"languages": run.Languages(), "timeoutSeconds": int(run.Timeout().Seconds())})
}

func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Lang  string `json:"lang"`
		Code  string `json:"code"`
		Stdin string `json:"stdin"`
	}
	if err := readJSON(w, r, &body); err != nil || body.Lang == "" {
		writeErr(w, 400, errors.New("lang and code required"))
		return
	}
	res, err := run.Run(r.Context(), body.Lang, body.Code, body.Stdin)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, res)
}

type scratchJSON struct {
	ID        int64     `json:"id"`
	Lang      string    `json:"lang"`
	Code      string    `json:"code"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func (s *Server) scratchList(w http.ResponseWriter, r *http.Request) ([]scratchJSON, bool) {
	list, err := s.store.Scratches(r.Context(), userOf(r).ID)
	if err != nil {
		writeErr(w, 500, err)
		return nil, false
	}
	out := make([]scratchJSON, 0, len(list))
	for _, sc := range list {
		out = append(out, scratchJSON{ID: sc.ID, Lang: sc.Lang, Code: sc.Code, CreatedAt: sc.CreatedAt, UpdatedAt: sc.UpdatedAt})
	}
	return out, true
}

func (s *Server) handleScratches(w http.ResponseWriter, r *http.Request) {
	if list, ok := s.scratchList(w, r); ok {
		writeJSON(w, 200, map[string]any{"scratches": list})
	}
}

// handleSaveScratch creates (POST) or updates (PUT /{id}) a scratch.
func (s *Server) handleSaveScratch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Lang string `json:"lang"`
		Code string `json:"code"`
	}
	if err := readJSON(w, r, &body); err != nil || body.Lang == "" {
		writeErr(w, 400, errors.New("lang and code required"))
		return
	}
	var id int64
	if raw := r.PathValue("id"); raw != "" {
		v, err := parseID(raw)
		if err != nil {
			writeErr(w, 400, err)
			return
		}
		id = v
	}
	id, err := s.store.SaveScratch(r.Context(), userOf(r).ID, id, body.Lang, body.Code)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"id": id, "savedAt": time.Now()})
}

func (s *Server) handleDeleteScratch(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r.PathValue("id"))
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	if err := s.store.DeleteScratch(r.Context(), userOf(r).ID, id); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": id})
}

// ---------------------------------------------------------------------------
// Signed-in account and background caching of locked problems

func (s *Server) handleAccount(w http.ResponseWriter, r *http.Request) {
	// This build has no external account integration; report a stable shape
	// so the frontend can render its account panel.
	writeJSON(w, 200, map[string]any{"signedIn": false})
}

// handleSetProgress sets or clears the logged-in user's status for a problem.
func (s *Server) handleSetProgress(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Status string `json:"status"` // "solved" | "attempted" | ""
	}
	if err := readJSON(w, r, &body); err != nil {
		writeErr(w, 400, err)
		return
	}
	switch body.Status {
	case "", "solved", "attempted":
	default:
		writeErr(w, 400, errors.New("status must be solved, attempted or empty"))
		return
	}
	if err := s.store.SetProgress(r.Context(), userOf(r).ID, r.PathValue("slug"), body.Status); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"status": body.Status})
}

// starterLangs are the languages we synthesise a starter stub for.
var starterLangs = []struct{ Slug, Lang string }{
	{"python3", "Python3"}, {"javascript", "JavaScript"}, {"typescript", "TypeScript"},
	{"golang", "Go"}, {"java", "Java"}, {"cpp", "C++"},
}

// serveOriginal returns an original problem in the shape the solve screen
// expects. Per-user attempts, drafts, hints, reviews and progress still apply.
func (s *Server) serveOriginal(w http.ResponseWriter, r *http.Request, op dataset.Problem) {
	ctx := r.Context()
	uid := userOf(r).ID
	slug := op.Slug

	snippets := make([]map[string]string, 0, len(starterLangs))
	for _, l := range starterLangs {
		snippets = append(snippets, map[string]string{"lang": l.Lang, "langSlug": l.Slug, "code": op.StarterCode(l.Slug)})
	}
	examples := make([]map[string]any, 0, len(op.Examples))
	for _, e := range op.Examples {
		examples = append(examples, map[string]any{"args": []map[string]string{{"name": "input", "value": e.Input}, {"name": "output", "value": e.Output}}})
	}

	progress, _ := s.store.Progress(ctx, uid)
	attempts, _ := s.store.Attempts(ctx, uid, slug)
	hints, _ := s.store.Hints(ctx, slug)
	hintList := make([]hintJSON, 0, len(hints))
	for _, h := range hints {
		hintList = append(hintList, hintJSON{Level: h.Level, Text: h.Text, Provider: h.Provider, CreatedAt: h.CreatedAt, Cached: true})
	}
	var review *reviewJSON
	if rec, ok, err := s.store.LatestReview(ctx, slug); err == nil && ok {
		if rj, err := toReviewJSON(rec, true); err == nil {
			review = &rj
		}
	}
	drafts := map[string]string{}
	for _, l := range starterLangs {
		if code, ok, err := s.store.Draft(ctx, uid, slug, l.Slug); err == nil && ok {
			drafts[l.Slug] = code
		}
	}

	sig := ""
	if op.FunctionName != "" {
		parts := make([]string, 0, len(op.Params))
		for _, pa := range op.Params {
			parts = append(parts, pa.Name+": "+pa.Type)
		}
		sig = op.FunctionName + "(" + strings.Join(parts, ", ") + ")"
		if op.Returns != "" {
			sig += " -> " + op.Returns
		}
	}

	writeJSON(w, 200, map[string]any{
		"id":          op.ID,
		"title":       op.Title,
		"slug":        op.Slug,
		"difficulty":  op.Difficulty,
		"tags":        op.Tags,
		"original":    true,
		"statementMd": op.Statement,
		"constraints": op.Constraints,
		"signature":   sig,
		"examples":    examples,
		"snippets":    snippets,
		"hints":       []string{},
		"fromCache":   true,
		"progress":    progress[slug],
		"attempts":    toAttemptsJSON(attempts),
		"aiHints":     hintList,
		"review":      review,
		"drafts":      drafts,
	})
}
