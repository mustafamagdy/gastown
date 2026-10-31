package daemon

import (
	"github.com/steveyegge/gastown/internal/mail"
	"github.com/steveyegge/gastown/internal/review"
)

// watchReviews makes sure every open visual review has its lavish poll
// running. It is a no-op when no review is open. The polls are children of
// the daemon; each one handles its own result (record, bead, requester) when
// the human answers, so the heartbeat only has to keep them alive.
func (d *Daemon) watchReviews() {
	if d.reviewEngine == nil {
		if !review.HasOpen(d.config.TownRoot) {
			return
		}
		d.reviewEngine = d.newReviewEngine()
	}
	d.reviewEngine.Tick()
}

func (d *Daemon) newReviewEngine() *review.Engine {
	town := d.config.TownRoot
	notifier := review.Notifier{
		TownRoot: town,
		GtBin:    d.gtPath,
		Alive: func(addr string) bool {
			for _, s := range mail.AddressToSessionIDs(addr) {
				if ok, err := d.tmux.HasSession(s); err == nil && ok {
					return true
				}
			}
			return false
		},
	}
	return review.NewEngine(town, &review.Client{TownRoot: town}, review.BeadMirror{TownRoot: town},
		notifier.Notify, func(format string, args ...any) { d.logger.Printf("review: "+format, args...) })
}

// stopReviews ends the review polls when the daemon stops. Feedback that was
// not delivered yet stays queued in lavish for the next poll.
func (d *Daemon) stopReviews() {
	if d.reviewEngine != nil {
		d.reviewEngine.Stop()
	}
}
