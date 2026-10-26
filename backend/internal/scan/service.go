package scan

import (
	"context"
	"log/slog"
	"time"
)

const (
	// rescanAge: the CVE database changes even if the image does not; once a
	// week what was already scanned is looked at again.
	rescanAge = 7 * 24 * time.Hour
	// minAge avoids manually re-scanning what was scanned a moment ago (except
	// with force).
	minAge = time.Hour
)

// CredentialLookup returns the credentials of an image's registry, if the
// panel has them (internal/registries).
type CredentialLookup func(ctx context.Context, host string) (*Credentials, bool)

// Service is the scan queue: one worker (trivy serializes over its cache)
// that takes pending images from the DB and scans them.
type Service struct {
	Store  *Store
	Runner *Runner
	creds  CredentialLookup
	logger *slog.Logger
	wake   chan struct{}
}

func NewService(store *Store, runner *Runner, creds CredentialLookup, logger *slog.Logger) *Service {
	return &Service{Store: store, Runner: runner, creds: creds, logger: logger, wake: make(chan struct{}, 1)}
}

// Start launches the worker and the weekly rescan.
func (s *Service) Start(ctx context.Context) {
	if !s.Runner.Available() {
		return
	}
	if err := s.Store.ResetStale(ctx); err != nil {
		s.logger.Warn("scan: failed to re-queue stuck scans", "error", err)
	}
	go s.worker(ctx)
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				stale, err := s.Store.StaleImages(ctx, rescanAge, 50)
				if err != nil {
					continue
				}
				for _, img := range stale {
					_, _ = s.Store.Enqueue(ctx, img, "rescan", 0, true)
				}
				if len(stale) > 0 {
					s.Wake()
				}
			}
		}
	}()
}

// Wake tells the worker there is work (otherwise it checks every 30 s).
func (s *Service) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Service) worker(ctx context.Context) {
	for {
		image, ok, err := s.Store.NextQueued(ctx)
		if err != nil {
			s.logger.Error("scan: failed to read the queue", "error", err)
		}
		if !ok {
			select {
			case <-ctx.Done():
				return
			case <-s.wake:
			case <-time.After(30 * time.Second):
			}
			continue
		}
		var creds *Credentials
		if s.creds != nil {
			creds, _ = s.creds(ctx, RegistryHost(image))
		}
		rep, scanErr := s.Runner.Scan(ctx, image, creds)
		// The server's ctx may be cancelled by now (shutdown mid-scan): the
		// row is updated with its own short context, or it would stay
		// "running" and the error would be the shutdown, not the image's.
		dbCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if ctx.Err() != nil {
			if err := s.Store.Requeue(dbCtx, image); err != nil {
				s.logger.Warn("scan: failed to re-queue interrupted scan", "image", image, "error", err)
			}
			cancel()
			return
		}
		err = s.Store.Finish(dbCtx, image, s.Runner.Version(), rep, scanErr)
		cancel()
		if err != nil {
			s.logger.Error("scan: failed to store result", "image", image, "error", err)
		}
		if scanErr != nil {
			s.logger.Warn("scan: scan failed", "image", image, "error", scanErr)
		}
	}
}

// Enqueue queues an image and wakes the worker.
func (s *Service) Enqueue(ctx context.Context, image, requestedBy string, force bool) (bool, error) {
	if err := validateImageRef(image); err != nil {
		return false, err
	}
	queued, err := s.Store.Enqueue(ctx, image, requestedBy, minAge, force)
	if queued {
		s.Wake()
	}
	return queued, err
}
