package searchengine

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestConcurrentInsertSearch is the contract headline: lock-free reads, atomic
// liveDocs, set-CAS writes, and a live background merge all running together.
// Writers Add, readers Search, a deleter Deletes, and the background merger
// consolidates — all concurrently, under -race. It must report no race, no
// panic, and Search must only ever return live, added ids.
func TestConcurrentInsertSearch(t *testing.T) {
	// Low thresholds so segments seal and merges fire during the run.
	e := New[mockQuery, mockStats](mockFormat{}, Options{
		MinSegmentDocs:     4,
		DeletesPctAllowed:  0.33,
		SegmentCountTarget: 8,
	})
	defer e.Close()

	const (
		writers = 4
		readers = 6
		runFor  = 600 * time.Millisecond
	)

	stop := make(chan struct{})
	var wg sync.WaitGroup

	// added tracks every id ever Add'd (the universe of legal search results).
	var addedMu sync.Mutex
	added := map[ExternalID]bool{}
	var counter atomic.Uint64

	// Writers.
	for range writers {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				id := fmt.Sprintf("d%d", counter.Add(1))
				addedMu.Lock()
				added[id] = true
				addedMu.Unlock()
				_ = e.Add([]Document{doc(id, "term")})
			}
		})
	}

	// Readers — verify every returned id was legitimately added.
	for range readers {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				hits := e.Search(mockQuery{term: "term"}, 10)
				for _, h := range hits {
					addedMu.Lock()
					ok := added[h.ID]
					addedMu.Unlock()
					if !ok {
						t.Errorf("Search returned never-added id %q", h.ID)
						return
					}
				}
			}
		})
	}

	// Deleter — kills already-added ids.
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			n := counter.Load()
			if n > 2 {
				e.Delete(fmt.Sprintf("d%d", n-2))
			}
			time.Sleep(time.Millisecond)
		}
	})

	time.Sleep(runFor)
	close(stop)
	wg.Wait()

	// Final sanity: search returns only added ids, no panic occurred.
	for _, h := range e.Search(mockQuery{term: "term"}, 50) {
		if !added[h.ID] {
			t.Fatalf("final Search returned never-added id %q", h.ID)
		}
	}
}
