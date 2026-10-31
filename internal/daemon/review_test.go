package daemon

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/review"
)

func TestWatchReviewsIsNoopWithoutOpenReviews(t *testing.T) {
	town := t.TempDir()
	d := &Daemon{config: &Config{TownRoot: town}, logger: log.New(io.Discard, "", 0)}
	d.watchReviews()
	if d.reviewEngine != nil {
		t.Fatal("engine created with no review store")
	}
	rec := &review.Record{ID: "rv-00000001", Bead: "gt-1", Kind: review.KindPlan, State: review.StateApproved}
	if err := review.Create(town, rec); err != nil {
		t.Fatal(err)
	}
	d.watchReviews()
	if d.reviewEngine != nil {
		t.Fatal("engine created with only decided reviews")
	}
}

func TestWatchReviewsStartsAndStopsPolls(t *testing.T) {
	// An empty PATH has no lavish-axi: the poll fails fast, which is enough to
	// see that the tick started it and that stopping waits for it.
	t.Setenv("PATH", t.TempDir())
	town := t.TempDir()
	page := filepath.Join(review.AssetDir(town, "rv-00000002"), "p.html")
	if err := os.MkdirAll(filepath.Dir(page), 0o755); err != nil {
		t.Fatal(err)
	}
	rec := &review.Record{ID: "rv-00000002", Bead: "gt-1", Kind: review.KindPlan, State: review.StateOpen, HTML: page}
	if err := review.Create(town, rec); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{config: &Config{TownRoot: town}, logger: log.New(io.Discard, "", 0)}
	d.watchReviews()
	if d.reviewEngine == nil {
		t.Fatal("engine not created for an open review")
	}
	var got *review.Record
	for i := 0; i < 200; i++ {
		got, _ = review.Load(town, rec.ID)
		if got != nil && got.Error != "" {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	d.stopReviews()
	if got == nil || got.State != review.StateOpen || got.Error == "" {
		t.Fatalf("record = %+v (want open with the poll error noted)", got)
	}
}
