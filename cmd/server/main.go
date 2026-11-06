// Command server assembles config, storage, sources, the download
// worker, scheduler, indexer and HTTP surface, with strict startup
// ordering and graceful shutdown.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"home-streaming-assistant/internal/api"
	"home-streaming-assistant/internal/catalog"
	"home-streaming-assistant/internal/config"
	"home-streaming-assistant/internal/downloads"
	"home-streaming-assistant/internal/jellyfin"
	"home-streaming-assistant/internal/model"
	"home-streaming-assistant/internal/schedule"
	"home-streaming-assistant/internal/storage"
	webassets "home-streaming-assistant/web"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata"
)

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	// Admin password: startup-only read, kept as SHA-256.
	password, err := config.ReadSecretFile(cfg.AdminPasswordFile)
	if err != nil {
		log.Fatalf("admin password: %v", err)
	}
	if password == "" {
		log.Fatalf("admin password file is empty")
	}

	// Exclusive process lock before any migration or worker start.
	lock, err := storage.AcquireLock(cfg.StateDir)
	if err != nil {
		log.Fatalf("state lock: %v", err)
	}
	defer lock.Release()

	store, err := storage.Open(cfg.StateDir)
	if err != nil {
		log.Fatalf("storage: %v", err)
	}
	defer store.Close()

	// Directories.
	for _, d := range []string{cfg.FilesDir, filepath.Join(cfg.FilesDir, ".incoming"), filepath.Join(cfg.FilesDir, "media")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			log.Fatalf("create %s: %v", d, err)
		}
	}

	// Sources.
	var aliyun, gdrive catalog.Backend
	if cfg.SourceConfigured("aliyun") {
		pass, err := config.ReadSecretFile(cfg.Aliyun.PasswordFile)
		if err != nil {
			log.Fatalf("aliyun password: %v", err)
		}
		aliyun, err = catalog.NewAliyun(cfg.Aliyun.BaseURL, cfg.Aliyun.Username, pass, cfg.Aliyun.Root)
		if err != nil {
			log.Fatalf("aliyun source: %v", err)
		}
	}
	if cfg.SourceConfigured("gdrive") {
		gdrive, err = catalog.NewGDrive(cfg.RcloneBinary, cfg.RcloneConfig, cfg.GDrive.Root)
		if err != nil {
			// Scope/config mismatch disables the source instead of
			// aborting; requests fail with NOT_CONFIGURED.
			log.Printf("gdrive source disabled: %v", err)
			gdrive = nil
		}
	}
	cat := catalog.NewManager(aliyun, gdrive)

	// Jellyfin.
	var jelly *jellyfin.Client
	var indexer *jellyfin.Indexer
	if cfg.JellyfinConfigured() {
		key, err := config.ReadSecretFile(cfg.Jellyfin.APIKeyFile)
		if err != nil {
			log.Fatalf("jellyfin api key: %v", err)
		}
		jelly = jellyfin.New(cfg.Jellyfin.BaseURL, key, cfg.Jellyfin.LibraryID, cfg.Jellyfin.WebURLs)
		indexer = jellyfin.NewIndexer(store, jelly, cfg.FilesDir)
	}

	// Download manager + recovery.
	dlm := downloads.New(store, cfg, cat)

	ctx := context.Background()
	if err := recoverState(ctx, store, cfg, dlm); err != nil {
		log.Fatalf("recovery: %v", err)
	}
	if err := store.MarkInterruptedRuns(ctx); err != nil {
		log.Printf("mark interrupted schedule runs: %v", err)
	}
	if err := store.CleanupExpiredSessions(ctx); err != nil {
		log.Printf("cleanup sessions: %v", err)
	}

	sched := schedule.New(store, dlm)

	// Static assets.
	webFS, err := webassets.Dist()
	if err != nil {
		log.Fatalf("embedded assets: %v", err)
	}

	srv := api.New(cfg, store, cat, dlm, sched, jelly, indexer, password, webFS)

	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		log.Fatalf("listen %s: %v", cfg.Listen, err)
	}
	httpServer := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}

	// Start background components.
	dlm.IndexerWake = func() {
		if indexer != nil {
			indexer.Wake()
		}
	}
	dlm.Start()
	sched.Start()
	if indexer != nil {
		indexer.Start()
	}
	srv.SetReady(true)

	// Probe source roots in the background.
	go func() {
		for _, src := range model.Sources {
			cat.Probe(context.Background(), src)
		}
	}()

	httpErr := make(chan error, 1)
	go func() { httpErr <- httpServer.Serve(ln) }()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	log.Printf("home-streaming-assistant listening on %s", cfg.Listen)
	select {
	case s := <-sig:
		log.Printf("received %s, shutting down", s)
	case err := <-httpErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("http server error: %v", err)
		}
	}

	srv.SetReady(false)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutdownCtx)
	sched.Stop()
	if indexer != nil {
		indexer.Stop()
	}
	// Terminates and waits for rclone children.
	dlm.Stop()
	log.Printf("shutdown complete")
}

// recoverState restores interrupted jobs and library consistency after a
// crash, before any worker starts.
func recoverState(ctx context.Context, store *storage.Store, cfg *config.Config, dlm *downloads.Manager) error {
	root, err := os.OpenRoot(cfg.FilesDir)
	if err != nil {
		return fmt.Errorf("open files root: %w", err)
	}
	defer root.Close()

	// publishing: finish or quarantine.
	pub, err := store.JobsInStates(ctx, model.JobPublishing)
	if err != nil {
		return err
	}
	for _, job := range pub {
		if job.FinalRel.Valid && job.LocalSHA256.Valid {
			if fi, err := root.Stat(job.FinalRel.String); err == nil &&
				fi.Mode().IsRegular() && (job.BytesTotal <= 0 || fi.Size() == job.BytesTotal) {
				lib := &storage.LibraryRow{
					ID: storage.RandomID(16), JobID: job.ID, Attempt: job.Attempt,
					Source: job.Source, Path: job.Path, Version: job.Version,
					RelativePath: job.FinalRel.String, Size: fi.Size(),
				}
				if err := store.CompletePublish(ctx, job.ID, lib); err != nil {
					return fmt.Errorf("resume publish %s: %w", job.ID, err)
				}
				continue
			}
		}
		// Try finishing from the temp file.
		tempPath := fmt.Sprintf(".incoming/%s/%d/part.bin", job.ID, job.Attempt)
		if fi, err := root.Stat(tempPath); err == nil && fi.Mode().IsRegular() &&
			(job.BytesTotal <= 0 || fi.Size() == job.BytesTotal) {
			sum, err := hashForRecovery(root, tempPath)
			if err == nil {
				rel := finalRelForRecovery(job)
				if err := publishForRecovery(root, tempPath, rel); err == nil {
					lib := &storage.LibraryRow{
						ID: storage.RandomID(16), JobID: job.ID, Attempt: job.Attempt,
						Source: job.Source, Path: job.Path, Version: job.Version,
						RelativePath: rel, Size: fi.Size(), LocalSHA256: sum,
					}
					if err := store.CompletePublish(ctx, job.ID, lib); err != nil {
						return fmt.Errorf("resume publish %s: %w", job.ID, err)
					}
					continue
				}
			}
		}
		// Quarantine anomalies into .incoming, never mark playable.
		_ = quarantine(root, job)
		_ = store.UpdateJobFields(ctx, job.ID, map[string]any{
			"state": model.JobFailed, "error_code": model.InterruptedCode,
		})
	}

	// downloading/verifying -> failed INTERRUPTED, drop temp dirs.
	for _, st := range []string{model.JobDownloading, model.JobVerifying} {
		jobs, err := store.JobsInStates(ctx, st)
		if err != nil {
			return err
		}
		for _, job := range jobs {
			_ = root.RemoveAll(fmt.Sprintf(".incoming/%s/%d", job.ID, job.Attempt))
			_ = store.UpdateJobFields(ctx, job.ID, map[string]any{
				"state": model.JobFailed, "error_code": model.InterruptedCode,
			})
		}
	}

	// Clean .incoming dirs not owned by queued jobs.
	queued, err := store.JobsInStates(ctx, model.JobQueued)
	if err != nil {
		return err
	}
	keep := map[string]bool{}
	for _, j := range queued {
		keep[j.ID] = true
	}
	if ents, err := os.ReadDir(filepath.Join(cfg.FilesDir, ".incoming")); err == nil {
		for _, e := range ents {
			if !keep[e.Name()] {
				_ = root.RemoveAll(".incoming/" + e.Name())
			}
		}
	}

	// Library consistency: deleting -> retry removal; playable/pending
	// files that vanished become missing.
	libs, err := store.LibraryInStates(ctx, model.LibDeleting)
	if err != nil {
		return err
	}
	for _, lib := range libs {
		if err := root.Remove(lib.RelativePath); err != nil && !os.IsNotExist(err) {
			continue // keep deleting for a later retry
		}
		_ = store.UpdateLibraryFields(ctx, lib.ID, map[string]any{"state": model.LibDeleted})
	}
	libs, err = store.LibraryInStates(ctx, model.LibPlayable, model.LibPendingIndex)
	if err != nil {
		return err
	}
	for _, lib := range libs {
		if _, err := root.Stat(lib.RelativePath); err != nil {
			if os.IsNotExist(err) {
				_ = store.UpdateLibraryFields(ctx, lib.ID, map[string]any{
					"state": model.LibMissing,
				})
			}
		}
	}
	return nil
}

func hashForRecovery(root *os.Root, rel string) (string, error) {
	f, err := root.Open(rel)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func finalRelForRecovery(job *storage.JobRow) string {
	var entry model.Entry
	name := "part.bin"
	if err := json.Unmarshal([]byte(job.RemoteJSON), &entry); err == nil && entry.Name != "" {
		name = strings.ReplaceAll(entry.Name, "/", "_")
	}
	return downloads.FinalRel(job.Source, job.Origin, job.ID, job.Attempt, name)
}

func publishForRecovery(root *os.Root, tempPath, rel string) error {
	dir := path.Dir(rel)
	if err := root.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return root.Rename(tempPath, rel)
}

func quarantine(root *os.Root, job *storage.JobRow) error {
	if !job.FinalRel.Valid {
		return nil
	}
	dst := fmt.Sprintf(".incoming/orphan-%s-%d", job.ID, job.Attempt)
	return root.Rename(job.FinalRel.String, dst)
}
