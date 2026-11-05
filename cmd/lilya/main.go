// Command lilya is the practice-library server and admin CLI.
//
//	lilya serve                       run migrations, the HTTP API and the background worker
//	lilya migrate                     apply database migrations and exit
//	lilya create-user -email E [-admin] [-name N]   create a user (password from LILYA_PASSWORD or prompt)
//	lilya set-password -email E       reset a user's password and sign them out everywhere
//	lilya set-role -email E -role admin|member
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/lilya/practice/internal/analysis"
	"github.com/lilya/practice/internal/auth"
	"github.com/lilya/practice/internal/config"
	"github.com/lilya/practice/internal/db"
	"github.com/lilya/practice/internal/httpapi"
	"github.com/lilya/practice/internal/media"
	"github.com/lilya/practice/internal/pipeline"
	"github.com/lilya/practice/internal/storage"
	"github.com/lilya/practice/internal/store"
	"github.com/lilya/practice/internal/worker"
	"github.com/lilya/practice/web"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch os.Args[1] {
	case "serve":
		err = serve(ctx, log)
	case "migrate":
		err = withStore(ctx, log, func(*store.Store) error { return nil })
	case "create-user":
		err = createUser(ctx, log, os.Args[2:])
	case "set-password":
		err = setPassword(ctx, log, os.Args[2:])
	case "set-role":
		err = setRole(ctx, log, os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: lilya <command>
  serve                                         run the server
  migrate                                       apply database migrations
  create-user -email E [-name N] [-admin]       create a user (password from $LILYA_PASSWORD or prompt)
  set-password -email E                         reset a password (signs the user out everywhere)
  set-role -email E -role admin|member          change a user's role`)
}

func withStore(ctx context.Context, log *slog.Logger, fn func(*store.Store) error) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool, log); err != nil {
		return err
	}
	return fn(store.New(pool))
}

func serve(ctx context.Context, log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool, log); err != nil {
		return err
	}
	st := store.New(pool)

	local, err := storage.NewLocal(cfg.DataDir)
	if err != nil {
		return err
	}

	runner, err := buildAnalysis(cfg, log)
	if err != nil {
		return err
	}
	w := worker.New(st, log.With("component", "worker"), cfg.WorkerConcurrency)
	pl := &pipeline.Pipeline{
		Store:    st,
		Storage:  local,
		FFmpeg:   media.FFmpeg{FFmpegPath: cfg.FFmpegPath, FFprobePath: cfg.FFprobePath},
		Analysis: runner,
		Worker:   w,
		Log:      log.With("component", "pipeline"),
		TempDir:  local.TempDir(),
		Lang:     cfg.AnalysisLanguage,
	}
	pl.Register()

	api := &httpapi.Server{Cfg: cfg, Store: st, Storage: local, Pipeline: pl, Log: log, WebFS: web.FS()}
	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
		// No Read/WriteTimeout: large uploads and long video streams are legitimate.
		MaxHeaderBytes: 64 << 10,
	}

	workerCtx, stopWorker := context.WithCancel(context.Background())
	workerDone := make(chan struct{})
	go func() {
		w.Run(workerCtx)
		close(workerDone)
	}()

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.ListenAddr, "public_url", cfg.PublicURL)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case <-ctx.Done():
	case err := <-errCh:
		if err != nil {
			stopWorker()
			return err
		}
	}
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	stopWorker()
	<-workerDone
	return nil
}

// buildAnalysis registers the available analysis steps, in execution order.
// To plug in a new tool or model, implement analysis.Step and add it here.
func buildAnalysis(cfg config.Config, log *slog.Logger) (*analysis.Runner, error) {
	var steps []analysis.Step
	if cfg.TranscribeEnabled {
		prompt := cfg.TranscribePrompt
		if prompt == "" {
			prompt = analysis.WhisperPrompt(cfg.AnalysisLanguage)
		}
		steps = append(steps, &analysis.TranscribeStep{
			T: &analysis.OpenAICompatTranscriber{
				ProviderName:   cfg.TranscribeProvider,
				BaseURL:        cfg.TranscribeBaseURL,
				APIKey:         cfg.TranscribeAPIKey,
				ModelName:      cfg.TranscribeModel,
				Prompt:         prompt,
				ResponseFormat: cfg.TranscribeResponseFormat,
			},
			Language:     cfg.TranscribeLanguage,
			ChunkSeconds: cfg.TranscribeChunkSeconds,
		})
	}
	if cfg.KeywordsEnabled {
		steps = append(steps, analysis.KeywordStep{})
	}
	if cfg.ClaudeEnabled {
		steps = append(steps, analysis.NewClaudeStep(analysis.ClaudeStep{
			ModelName:     cfg.ClaudeModel,
			Effort:        cfg.ClaudeEffort,
			Lang:          cfg.AnalysisLanguage,
			UseFrames:     config.Has(cfg.ClaudeInputs, "frames"),
			UseTranscript: config.Has(cfg.ClaudeInputs, "transcript"),
			MaxFrames:     cfg.ClaudeMaxFrames,
			FrameWidth:    cfg.ClaudeFrameWidth,
		}))
	}
	runner, err := analysis.NewRunner(steps, cfg.AnalysisSteps)
	if err != nil {
		return nil, err
	}
	for _, s := range runner.Steps() {
		log.Info("analysis step available", "step", s.Name, "provider", s.Provider, "model", s.Model,
			"default", config.Has(runner.Defaults(), s.Name))
	}
	if !cfg.TranscribeEnabled {
		log.Warn("speech transcription disabled (set TRANSCRIBE_BASE_URL and/or TRANSCRIBE_API_KEY)")
	}
	if !cfg.ClaudeEnabled {
		log.Warn("Claude analysis disabled (set ANTHROPIC_API_KEY)")
	}
	return runner, nil
}

func readPassword() (string, error) {
	if pw := os.Getenv("LILYA_PASSWORD"); pw != "" {
		return pw, nil
	}
	if term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprint(os.Stderr, "Password: ")
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}
		fmt.Fprint(os.Stderr, "Repeat password: ")
		b2, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}
		if string(b) != string(b2) {
			return "", errors.New("passwords do not match")
		}
		return string(b), nil
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func createUser(ctx context.Context, log *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("create-user", flag.ExitOnError)
	email := fs.String("email", "", "email")
	name := fs.String("name", "", "display name")
	admin := fs.Bool("admin", false, "grant admin role")
	_ = fs.Parse(args)
	if *email == "" {
		return errors.New("-email is required")
	}
	pw, err := readPassword()
	if err != nil {
		return err
	}
	if err := auth.ValidatePassword(pw); err != nil {
		return err
	}
	hash, err := auth.HashPassword(pw)
	if err != nil {
		return err
	}
	role := store.RoleMember
	if *admin {
		role = store.RoleAdmin
	}
	return withStore(ctx, log, func(st *store.Store) error {
		u, err := st.CreateUser(ctx, strings.ToLower(strings.TrimSpace(*email)), *name, role, hash)
		if errors.Is(err, store.ErrConflict) {
			return errors.New("a user with this email already exists")
		}
		if err != nil {
			return err
		}
		fmt.Printf("created %s user %s (id %d)\n", u.Role, u.Email, u.ID)
		return nil
	})
}

func setPassword(ctx context.Context, log *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("set-password", flag.ExitOnError)
	email := fs.String("email", "", "email")
	_ = fs.Parse(args)
	pw, err := readPassword()
	if err != nil {
		return err
	}
	if err := auth.ValidatePassword(pw); err != nil {
		return err
	}
	hash, err := auth.HashPassword(pw)
	if err != nil {
		return err
	}
	return withStore(ctx, log, func(st *store.Store) error {
		u, err := st.UserByEmail(ctx, strings.ToLower(strings.TrimSpace(*email)))
		if err != nil {
			return fmt.Errorf("find user: %w", err)
		}
		if err := st.SetPassword(ctx, u.ID, hash); err != nil {
			return err
		}
		if err := st.DeleteUserSessions(ctx, u.ID, 0); err != nil {
			return err
		}
		fmt.Println("password updated; all sessions revoked")
		return nil
	})
}

func setRole(ctx context.Context, log *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("set-role", flag.ExitOnError)
	email := fs.String("email", "", "email")
	role := fs.String("role", "", "admin or member")
	_ = fs.Parse(args)
	if *role != store.RoleAdmin && *role != store.RoleMember {
		return errors.New("-role must be admin or member")
	}
	return withStore(ctx, log, func(st *store.Store) error {
		u, err := st.UserByEmail(ctx, strings.ToLower(strings.TrimSpace(*email)))
		if err != nil {
			return fmt.Errorf("find user: %w", err)
		}
		if err := st.SetRole(ctx, u.ID, *role); err != nil {
			return err
		}
		fmt.Printf("%s is now %s\n", u.Email, *role)
		return nil
	})
}
