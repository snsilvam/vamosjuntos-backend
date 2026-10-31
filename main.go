// Command vamosjuntos-backend is the API of Vamos Juntos .club.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"cloud.google.com/go/firestore"
)

func main() {
	if err := run(); err != nil {
		log.Printf("vamosjuntos-backend: %v", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client, err := firestore.NewClientWithDatabase(ctx, cfg.ProjectID, cfg.DatabaseID)
	if err != nil {
		return fmt.Errorf("firestore client: %w", err)
	}
	defer func() {
		if err := client.Close(); err != nil {
			log.Printf("close firestore client: %v", err)
		}
	}()

	// The catalog is read again on every request; reading it here too makes a revision with an
	// invalid catalog fail to start, so Cloud Run keeps serving the previous one.
	guests := NewGuestStore(client, cfg.EventID)
	catalog, err := guests.Catalog(ctx)
	if err != nil {
		return err
	}

	events := NewEventStore(client, cfg.EventID)
	// The event may not exist yet (FR-004 answers that case per request), but if it exists the
	// catalog must fit in its capacity.
	switch event, err := events.Get(ctx); {
	case err == nil && catalog.Len() > event.Capacity:
		return fmt.Errorf("el catálogo tiene %d invitados y la capacidad del evento es %d", catalog.Len(), event.Capacity)
	case err != nil && !errors.Is(err, errEventNotFound):
		return err
	}

	verifier, err := NewFirebaseVerifier(ctx, cfg.ProjectID)
	if err != nil {
		return err
	}

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           NewServer(guests, events, NewAttendanceStore(client, cfg.EventID), NewAttemptStore(client), verifier, NewLinkStore(client, cfg.EventID), NewTransportStore(client, cfg.EventID)).routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	log.Printf("vamosjuntos-backend: %d invitados, escuchando en :%s", catalog.Len(), cfg.Port)

	select {
	case err := <-errCh:
		return fmt.Errorf("listen: %w", err)
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}
