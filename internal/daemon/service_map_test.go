package daemon

import (
	"io"
	"log"
	"os"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/config"
)

func TestRefreshServiceMapsThrottleAndNoRigs(t *testing.T) {
	town := t.TempDir()
	if err := config.SaveRigsConfig(town+"/mayor/rigs.json", &config.RigsConfig{Version: config.CurrentRigsVersion}); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveProjectConfig(town, &config.ProjectConfig{Name: "shop", Prefix: "sh"}); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{config: &Config{TownRoot: town}, logger: log.New(io.Discard, "", 0)}

	d.refreshServiceMaps()
	first, ok := d.serviceMapLastRun["shop"]
	if !ok {
		t.Fatal("project not refreshed")
	}
	if _, err := os.Stat(town + "/projects/shop/service-map.json"); err != nil {
		t.Errorf("map not written: %v", err)
	}

	d.refreshServiceMaps()
	if !d.serviceMapLastRun["shop"].Equal(first) {
		t.Error("second call within the interval was not throttled")
	}

	d.serviceMapLastRun["shop"] = time.Now().Add(-2 * serviceMapInterval)
	d.refreshServiceMaps()
	if !d.serviceMapLastRun["shop"].After(first) {
		t.Error("refresh after the interval did not run")
	}
}
