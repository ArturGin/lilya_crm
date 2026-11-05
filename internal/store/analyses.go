package store

import (
	"context"
	"encoding/json"
	"time"
)

const (
	AnalysisPending = "pending"
	AnalysisRunning = "running"
	AnalysisDone    = "done"
	AnalysisFailed  = "failed"
	AnalysisSkipped = "skipped"
)

type Analysis struct {
	ID       int64           `json:"id"`
	PostID   int64           `json:"-"`
	AssetID  int64           `json:"-"`
	Status   string          `json:"status"`
	Provider string          `json:"provider"`
	Model    string          `json:"model"`
	Result   json.RawMessage `json:"result"`
	// Steps is the per-step log ([]analysis.StepRecord).
	Steps          json.RawMessage `json:"steps,omitempty"`
	RequestedSteps []string        `json:"requested_steps,omitempty"`
	Error          *string         `json:"error,omitempty"`
	InputTokens    *int            `json:"input_tokens,omitempty"`
	OutputTokens   *int            `json:"output_tokens,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
	CompletedAt    *time.Time      `json:"completed_at"`
}

const analysisCols = `id, post_id, asset_id, status, provider, model, result, steps, requested_steps, error,
	input_tokens, output_tokens, created_at, completed_at`

func scanAnalysis(row interface{ Scan(...any) error }) (Analysis, error) {
	var a Analysis
	var result, steps []byte
	err := row.Scan(&a.ID, &a.PostID, &a.AssetID, &a.Status, &a.Provider, &a.Model, &result, &steps, &a.RequestedSteps,
		&a.Error, &a.InputTokens, &a.OutputTokens, &a.CreatedAt, &a.CompletedAt)
	if result != nil {
		a.Result = json.RawMessage(result)
	}
	if steps != nil {
		a.Steps = json.RawMessage(steps)
	}
	return a, mapErr(err)
}

func (s *Store) CreateAnalysis(ctx context.Context, postID, assetID int64, status, provider, model string, requested []string, errMsg *string) (Analysis, error) {
	return scanAnalysis(s.pool.QueryRow(ctx, `
		INSERT INTO video_analyses (post_id, asset_id, status, provider, model, requested_steps, error)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING `+analysisCols, postID, assetID, status, provider, model, requested, errMsg))
}

func (s *Store) AnalysisByID(ctx context.Context, id int64) (Analysis, error) {
	return scanAnalysis(s.pool.QueryRow(ctx, `SELECT `+analysisCols+` FROM video_analyses WHERE id = $1`, id))
}

func (s *Store) LatestAnalysis(ctx context.Context, postID int64) (Analysis, error) {
	return scanAnalysis(s.pool.QueryRow(ctx,
		`SELECT `+analysisCols+` FROM video_analyses WHERE post_id = $1 ORDER BY id DESC LIMIT 1`, postID))
}

func (s *Store) SetAnalysisRunning(ctx context.Context, id int64, model string) error {
	_, err := s.pool.Exec(ctx, `UPDATE video_analyses SET status = 'running', model = $2, error = NULL, updated_at = now() WHERE id = $1`, id, model)
	return err
}

// SetAnalysisSteps stores live per-step progress.
func (s *Store) SetAnalysisSteps(ctx context.Context, id int64, steps json.RawMessage) error {
	_, err := s.pool.Exec(ctx, `UPDATE video_analyses SET steps = $2, updated_at = now() WHERE id = $1`, id, []byte(steps))
	return err
}

func (s *Store) FailAnalysis(ctx context.Context, id int64, msg string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE video_analyses SET status = 'failed', error = $2, updated_at = now(), completed_at = now()
		WHERE id = $1`, id, msg)
	return err
}

func (s *Store) CompleteAnalysis(ctx context.Context, id int64, result, steps json.RawMessage, searchText string, inTok, outTok int) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE video_analyses
		SET status = 'done', result = $2, steps = $3, search_text = $4, input_tokens = $5, output_tokens = $6,
		    error = NULL, updated_at = now(), completed_at = now()
		WHERE id = $1`, id, []byte(result), []byte(steps), searchText, inTok, outTok)
	return err
}

// ---- transcripts ----

type Transcript struct {
	ID        int64           `json:"id"`
	AssetID   int64           `json:"asset_id"`
	Provider  string          `json:"provider"`
	Model     string          `json:"model"`
	Language  string          `json:"language"`
	Text      string          `json:"text"`
	Segments  json.RawMessage `json:"segments"`
	CreatedAt time.Time       `json:"created_at"`
}

const transcriptCols = `id, asset_id, provider, model, language, text, segments, created_at`

func scanTranscript(row interface{ Scan(...any) error }) (Transcript, error) {
	var t Transcript
	var seg []byte
	err := row.Scan(&t.ID, &t.AssetID, &t.Provider, &t.Model, &t.Language, &t.Text, &seg, &t.CreatedAt)
	t.Segments = json.RawMessage(seg)
	return t, mapErr(err)
}

func (s *Store) GetTranscript(ctx context.Context, assetID int64, provider, model string) (Transcript, error) {
	return scanTranscript(s.pool.QueryRow(ctx, `SELECT `+transcriptCols+` FROM media_transcripts
		WHERE asset_id = $1 AND provider = $2 AND model = $3`, assetID, provider, model))
}

func (s *Store) LatestTranscript(ctx context.Context, assetID int64) (Transcript, error) {
	return scanTranscript(s.pool.QueryRow(ctx, `SELECT `+transcriptCols+` FROM media_transcripts
		WHERE asset_id = $1 ORDER BY id DESC LIMIT 1`, assetID))
}

func (s *Store) PutTranscript(ctx context.Context, t Transcript) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO media_transcripts (asset_id, provider, model, language, text, segments)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (asset_id, provider, model) DO UPDATE
		SET language = EXCLUDED.language, text = EXCLUDED.text, segments = EXCLUDED.segments, created_at = now()`,
		t.AssetID, t.Provider, t.Model, t.Language, t.Text, []byte(t.Segments))
	return err
}

// DeleteTranscripts drops cached transcripts of an asset (forces re-transcription).
func (s *Store) DeleteTranscripts(ctx context.Context, assetID int64) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM media_transcripts WHERE asset_id = $1`, assetID)
	return err
}
