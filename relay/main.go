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
	"strconv"
	"strings"
	"syscall"
	"time"
)

type config struct {
	listenAddr string
	intakeURL  string
	token      string
	perIP      int // forwarded requests per client per hour
	overall    int // forwarded requests per hour, everyone together
	challenge  challenge
}

const (
	defaultPerIP   = 5
	defaultOverall = 30
	rateWindow     = time.Hour
)

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
	if cfg.perIP, err = limitFrom(getenv, "RATE_LIMIT_PER_IP", defaultPerIP); err != nil {
		return config{}, err
	}
	if cfg.overall, err = limitFrom(getenv, "RATE_LIMIT_TOTAL", defaultOverall); err != nil {
		return config{}, err
	}
	if cfg.challenge, err = newChallenge(strings.TrimSpace(getenv("CHALLENGE"))); err != nil {
		return config{}, err
	}
	return cfg, nil
}

// limitFrom reads an hourly limit. Unset means the default; anything that
// isn't a whole number of at least 1 is an error, so a typo can't switch a
// limit off.
func limitFrom(getenv func(string) string, name string, fallback int) (int, error) {
	raw := strings.TrimSpace(getenv(name))
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("%s must be a whole number, 1 or more", name)
	}
	return n, nil
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

	s := &server{
		intake:    newIntakeClient(cfg.intakeURL, cfg.token),
		limiter:   newRateLimiter(cfg.perIP, cfg.overall, rateWindow),
		challenge: cfg.challenge,
		log:       log,
	}
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
	log.Info("relay listening", "addr", cfg.listenAddr,
		"per_ip_per_hour", cfg.perIP, "total_per_hour", cfg.overall)

	select {
	case err := <-failed:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
