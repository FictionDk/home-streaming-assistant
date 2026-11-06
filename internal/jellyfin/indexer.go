package jellyfin

import (
	"context"
	"log"
	"path/filepath"
	"sync"
	"time"

	"home-streaming-assistant/internal/model"
	"home-streaming-assistant/internal/storage"
)

// Indexer maps published files to Jellyfin items by exact path.
type Indexer struct {
	store  *storage.Store
	client *Client
	// FilesDir is the app's /data/files, used to build Jellyfin's
	// container-visible /media/<relative_path> mapping.
	FilesDir string

	wake     chan struct{}
	stop     chan struct{}
	wg       sync.WaitGroup
	mu       sync.Mutex
	updating bool
}

// NewIndexer builds the indexer.
func NewIndexer(store *storage.Store, client *Client, filesDir string) *Indexer {
	return &Indexer{store: store, client: client, FilesDir: filesDir,
		wake: make(chan struct{}, 1), stop: make(chan struct{})}
}

// Wake nudges the indexer (never blocks the caller).
func (ix *Indexer) Wake() {
	select {
	case ix.wake <- struct{}{}:
	default:
	}
}

// Start launches the 10s index loop.
func (ix *Indexer) Start() {
	ix.wg.Add(1)
	go ix.loop()
}

// Stop terminates the loop.
func (ix *Indexer) Stop() {
	close(ix.stop)
	ix.wg.Wait()
}

func (ix *Indexer) loop() {
	defer ix.wg.Done()
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			ix.run()
		case <-ix.wake:
			ix.run()
		case <-ix.stop:
			return
		}
	}
}

func (ix *Indexer) run() {
	ix.mu.Lock()
	if ix.updating {
		ix.mu.Unlock()
		return
	}
	ix.updating = true
	ix.mu.Unlock()
	defer func() {
		ix.mu.Lock()
		ix.updating = false
		ix.mu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Ask Jellyfin to scan; a 204 only means the scan began.
	if err := ix.client.StartRefresh(ctx); err != nil {
		log.Printf("indexer: refresh: %v", err)
	}

	pending, err := ix.store.LibraryInStates(ctx, model.LibPendingIndex)
	if err != nil {
		log.Printf("indexer: list pending: %v", err)
		return
	}
	if len(pending) == 0 {
		return
	}
	items, err := ix.client.AllItems(ctx)
	if err != nil {
		log.Printf("indexer: query items: %v", err)
		return
	}
	for _, p := range pending {
		abs := "/media/" + filepath.ToSlash(p.RelativePath)
		var matches []MediaItem
		for _, it := range items {
			if it.Path == abs {
				matches = append(matches, it)
				continue
			}
			for _, ms := range it.MediaSources {
				if ms.Path == abs {
					matches = append(matches, it)
					break
				}
			}
		}
		switch {
		case len(matches) == 0:
			// Not indexed yet; wait for a later cycle.
		case len(matches) == 1:
			// The saved ID must still reference the same file.
			fields := map[string]any{
				"state":              model.LibPlayable,
				"error_code":         nil,
				"jellyfin_item_id":   matches[0].ID,
				"jellyfin_server_id": matches[0].ServerID,
			}
			if err := ix.store.UpdateLibraryFields(ctx, p.ID, fields); err != nil {
				log.Printf("indexer: update %s: %v", p.ID, err)
			}
		default:
			// Index conflict: report it, never guess an ID.
			_ = ix.store.UpdateLibraryFields(ctx, p.ID, map[string]any{
				"error_code": model.CodeIndexConflict,
			})
		}
	}
}

// UpdateSavedStillValid re-verifies a saved item still maps to the file.
func (ix *Indexer) UpdateSavedStillValid(ctx context.Context, lib *storage.LibraryRow) bool {
	if lib.JellyfinItemID.Valid {
		abs := "/media/" + filepath.ToSlash(lib.RelativePath)
		items, err := ix.client.FindByPath(ctx, abs)
		if err != nil {
			return false
		}
		for _, it := range items {
			if it.ID == lib.JellyfinItemID.String {
				return true
			}
		}
		return false
	}
	return false
}
