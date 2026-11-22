// The relay sits between the site's repair-request form and benchhud-intake.
// It takes the form POST, checks it against intake's contract, forwards it
// with a write-only token and answers with a redirect. It keeps nothing.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

type config struct {
	listenAddr string
	intakeURL  string
	token      string
}

// loadConfig reads the settings from the environment. The relay refuses to
// start without an intake to send to and a token to send with: a relay that
// starts and loses every request is worse than one that doesn't start.
func loadConfig(getenv func(string) string) (config, error) {
	cfg := config{
		listenAddr: getenv("LISTEN_ADDR"),
		intakeURL:  strings.TrimRight(strings.TrimSpace(getenv("INTAKE_URL")), "/"),
		token:      strings.TrimSpace(getenv("INTAKE_SUBMIT_TOKEN")),
	}
	if cfg.listenAddr == "" {
		cfg.listenAddr = ":8080"
	}
	u, err := url.Parse(cfg.intakeURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return config{}, errors.New("INTAKE_URL must be an http or https URL, like http://benchhud-intake:8000")
	}
	if cfg.token == "" {
		return config{}, errors.New("INTAKE_SUBMIT_TOKEN is not set")
	}
	return cfg, nil
}

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("relay stopped", "error", err.Error())
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	s := &server{intake: newIntakeClient(cfg.intakeURL, cfg.token), log: log}
	srv := &http.Server{
		Addr:              cfg.listenAddr,
		Handler:           s.routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second, // covers two tries at intake
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	failed := make(chan error, 1)
	go func() { failed <- srv.ListenAndServe() }()
	log.Info("relay listening", "addr", cfg.listenAddr)

	select {
	case err := <-failed:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
