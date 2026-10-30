package projectmap

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/config"
)

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// addRig creates <town>/<rig>/.repo.git (bare, with an origin/main ref) holding
// the given files, and returns the work tree used to add further commits.
func addRig(t *testing.T, town, rig string, files map[string]string) string {
	t.Helper()
	work := filepath.Join(t.TempDir(), "work")
	if err := os.MkdirAll(work, 0755); err != nil {
		t.Fatal(err)
	}
	runGit(t, work, "init", "-q", "-b", "main")
	commitFiles(t, work, files)
	rigPath := filepath.Join(town, rig)
	if err := os.MkdirAll(rigPath, 0755); err != nil {
		t.Fatal(err)
	}
	bare := filepath.Join(rigPath, ".repo.git")
	runGit(t, work, "clone", "-q", "--bare", work, bare)
	syncBare(t, work, bare)
	return work
}

func commitFiles(t *testing.T, work string, files map[string]string) {
	t.Helper()
	for p, c := range files {
		full := filepath.Join(work, p)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0644); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, work, "add", "-A")
	runGit(t, work, "commit", "-q", "-m", "c")
}

// syncBare points origin/main of the bare repo at the work tree's HEAD while
// leaving refs/heads/main stale, as a rig's .repo.git is after a fetch.
func syncBare(t *testing.T, work, bare string) {
	t.Helper()
	runGit(t, work, "push", "-q", bare, "HEAD:refs/remotes/origin/main")
}

const ordersManifest = `name = "orders-svc"
owns = "orders"
calls = ["catalog-svc", "billing-svc"]
publishes = ["order.placed"]
database = "orders.db"
`

const ordersOpenAPI = `openapi: 3.0.3
paths:
  /health:
    get: {summary: x}
  /orders:
    get: {summary: x}
    post: {summary: x}
    parameters: []
  /orders/{id}:
    delete: {summary: x}
`

func writeRigs(t *testing.T, town string, rigs map[string]string) {
	t.Helper()
	rc := &config.RigsConfig{Version: config.CurrentRigsVersion, Rigs: map[string]config.RigEntry{}}
	for rig, url := range rigs {
		rc.Rigs[rig] = config.RigEntry{GitURL: url, Project: "shop"}
	}
	if err := config.SaveRigsConfig(filepath.Join(town, "mayor", "rigs.json"), rc); err != nil {
		t.Fatal(err)
	}
}

func TestScanRigManifestOpenAPIAndStaleLocalBranch(t *testing.T) {
	t.Parallel()
	town := t.TempDir()
	work := addRig(t, town, "orders_svc", map[string]string{"service.toml": "name = \"stale\"\n"})
	// Newer commit only on origin/main; refs/heads/main stays stale.
	commitFiles(t, work, map[string]string{"service.toml": ordersManifest, "openapi.yaml": ordersOpenAPI})
	syncBare(t, work, filepath.Join(town, "orders_svc", ".repo.git"))

	svc, err := scanRig("orders_svc", "orders-svc", filepath.Join(town, "orders_svc", ".repo.git"), "main", filepath.Join(town, "none.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if svc.Name != "orders-svc" || svc.Owns != "orders" || svc.Database != "orders.db" || svc.Source != "manifest,openapi" {
		t.Errorf("unexpected service: %+v", svc)
	}
	if !reflect.DeepEqual(svc.Calls, []string{"billing-svc", "catalog-svc"}) {
		t.Errorf("calls = %v", svc.Calls)
	}
	var got []string
	for _, e := range svc.Serves {
		got = append(got, e.String())
	}
	want := []string{"GET /health", "GET /orders", "POST /orders", "DELETE /orders/{id}"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("serves = %v, want %v", got, want)
	}
}

func TestScanRigFallbackNameAndOpenAPIJSON(t *testing.T) {
	t.Parallel()
	town := t.TempDir()
	addRig(t, town, "web", map[string]string{
		"package.json":     `{"name": "storefront"}`,
		"api/openapi.json": `{"openapi":"3.0.0","paths":{"/x":{"get":{}}}}`,
	})
	svc, err := scanRig("web", "web", filepath.Join(town, "web", ".repo.git"), "main", "")
	if err != nil {
		t.Fatal(err)
	}
	if svc.Name != "storefront" || len(svc.Serves) != 1 || svc.Serves[0].String() != "GET /x" {
		t.Errorf("unexpected: %+v", svc)
	}

	addRig(t, town, "gosvc", map[string]string{"go.mod": "module github.com/acme/ledger\n\ngo 1.22\n"})
	svc, _ = scanRig("gosvc", "gosvc", filepath.Join(town, "gosvc", ".repo.git"), "main", "")
	if svc.Name != "ledger" {
		t.Errorf("go.mod name = %q", svc.Name)
	}

	addRig(t, town, "bare", map[string]string{"README.md": "x"})
	svc, _ = scanRig("bare", "bare-repo", filepath.Join(town, "bare", ".repo.git"), "main", "")
	if svc.Name != "bare-repo" {
		t.Errorf("repo name fallback = %q", svc.Name)
	}
}

func TestOverridePrecedenceFieldByField(t *testing.T) {
	t.Parallel()
	town := t.TempDir()
	addRig(t, town, "orders_svc", map[string]string{"service.toml": ordersManifest, "openapi.yaml": ordersOpenAPI})
	ov := filepath.Join(town, "orders.toml")
	if err := os.WriteFile(ov, []byte("owns = \"order lifecycle\"\ncalls = [\"users-svc\"]\nserves = [\"get /v2/orders\"]\n"), 0644); err != nil {
		t.Fatal(err)
	}
	svc, err := scanRig("orders_svc", "orders-svc", filepath.Join(town, "orders_svc", ".repo.git"), "main", ov)
	if err != nil {
		t.Fatal(err)
	}
	if svc.Owns != "order lifecycle" || !reflect.DeepEqual(svc.Calls, []string{"users-svc"}) {
		t.Errorf("override not applied: %+v", svc)
	}
	// Fields the override leaves out come from the manifest.
	if svc.Name != "orders-svc" || svc.Database != "orders.db" || !reflect.DeepEqual(svc.Publishes, []string{"order.placed"}) {
		t.Errorf("manifest fields lost: %+v", svc)
	}
	if len(svc.Serves) != 1 || svc.Serves[0].String() != "GET /v2/orders" {
		t.Errorf("serves = %v", svc.Serves)
	}
	if svc.Source != "override,manifest,openapi" {
		t.Errorf("source = %q", svc.Source)
	}
}

func TestRepoName(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"file:///x/remotes/billing-svc.git":    "billing-svc",
		"https://github.com/acme/orders.git/":  "orders",
		"git@github.com:acme/api-gateway.git":  "api-gateway",
		"git@ssh.dev.azure.com:v3/o/p/web-app": "web-app",
	} {
		if got := repoName(in); got != want {
			t.Errorf("repoName(%q) = %q, want %q", in, got, want)
		}
	}
}

func shopMap() *Map {
	return &Map{Project: "shop", Services: []Service{
		{Rig: "api_gateway", Repo: "api-gateway", Name: "api-gateway", Calls: []string{"orders-svc", "catalog-svc"}},
		{Rig: "billing_svc", Repo: "billing-svc", Name: "billing-svc", Publishes: []string{"payment.captured"}, Consumes: []string{"order.placed"}},
		{Rig: "catalog_svc", Repo: "catalog-svc", Name: "catalog-svc"},
		{Rig: "notify_svc", Repo: "notify-svc", Name: "notify-svc", Consumes: []string{"order.placed", "payment.captured"}},
		{Rig: "orders_svc", Repo: "orders-svc", Name: "orders-svc", Calls: []string{"catalog-svc", "billing_svc", "orders-svc"}, Publishes: []string{"order.placed"}},
	}}
}

func TestNeighbours(t *testing.T) {
	t.Parallel()
	m := shopMap()
	cases := map[string][]string{
		// calls catalog + billing (hyphen/underscore), called by gateway, event consumers billing + notify; self call ignored
		"orders_svc":  {"api_gateway", "billing_svc", "catalog_svc", "notify_svc"},
		"billing_svc": {"notify_svc", "orders_svc"}, // consumes orders' event, publishes to notify, called by orders
		"catalog_svc": {"api_gateway", "orders_svc"},
		"notify_svc":  {"billing_svc", "orders_svc"}, // producers of what it consumes
		"unknown":     nil,
	}
	for rig, want := range cases {
		if got := Neighbours(m, rig); !reflect.DeepEqual(got, want) {
			t.Errorf("Neighbours(%s) = %v, want %v", rig, got, want)
		}
	}
	rels := Relations(m, "billing_svc")
	if len(rels) != 2 || !rels[1].CalledBy || rels[1].Rig != "orders_svc" || len(rels[1].Producers) != 1 || len(rels[0].Consumers) != 1 {
		t.Errorf("relations = %+v", rels)
	}
}

func TestBuildIncrementalAndForce(t *testing.T) {
	t.Parallel()
	town := t.TempDir()
	work := addRig(t, town, "orders_svc", map[string]string{"service.toml": ordersManifest, "openapi.yaml": ordersOpenAPI})
	addRig(t, town, "billing_svc", map[string]string{"service.toml": "name = \"billing-svc\"\nconsumes = [\"order.placed\"]\n"})
	writeRigs(t, town, map[string]string{
		"orders_svc":  "file:///r/orders-svc.git",
		"billing_svc": "file:///r/billing-svc.git",
	})
	if err := os.MkdirAll(filepath.Join(town, "projects", "shop"), 0755); err != nil {
		t.Fatal(err)
	}

	m, res, err := Build(town, "shop", BuildOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Services) != 2 || m.Services[0].Rig != "billing_svc" {
		t.Fatalf("services = %+v", m.Services)
	}
	// Calls are normalised to rig names.
	if o := m.Find("orders_svc"); !reflect.DeepEqual(o.Calls, []string{"billing_svc", "catalog-svc"}) {
		t.Errorf("calls = %v", o.Calls)
	}
	for _, r := range res {
		if r.Status != "scanned" {
			t.Errorf("first build: %+v", r)
		}
	}
	for _, p := range []string{Path(town, "shop"), MarkdownPath(town, "shop")} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("missing %s", p)
		}
	}
	first := m.GeneratedAt

	// Nothing changed: all skipped, map not rewritten.
	time.Sleep(10 * time.Millisecond)
	m, res, err = Build(town, "shop", BuildOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		if r.Status != "unchanged" {
			t.Errorf("second build: %+v", r)
		}
	}
	if !m.GeneratedAt.Equal(first) {
		t.Errorf("map rewritten although nothing changed")
	}

	// New commit in one rig: only that rig is rescanned.
	commitFiles(t, work, map[string]string{"service.toml": strings.Replace(ordersManifest, `owns = "orders"`, `owns = "order flow"`, 1)})
	syncBare(t, work, filepath.Join(town, "orders_svc", ".repo.git"))
	m, res, err = Build(town, "shop", BuildOptions{})
	if err != nil {
		t.Fatal(err)
	}
	status := map[string]string{}
	for _, r := range res {
		status[r.Rig] = r.Status
	}
	if status["orders_svc"] != "scanned" || status["billing_svc"] != "unchanged" || m.Find("orders_svc").Owns != "order flow" {
		t.Errorf("status = %v, owns = %q", status, m.Find("orders_svc").Owns)
	}

	// Force rescans everything.
	_, res, _ = Build(town, "shop", BuildOptions{Force: true})
	for _, r := range res {
		if r.Status != "scanned" {
			t.Errorf("forced build: %+v", r)
		}
	}

	// A new override file triggers a rescan of its rig only.
	ovDir := OverrideDir(town, "shop")
	if err := os.MkdirAll(ovDir, 0755); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(ovDir, "billing_svc.toml"), []byte("owns = \"payments\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	m, _, _ = Build(town, "shop", BuildOptions{})
	if m.Find("billing_svc").Owns != "payments" {
		t.Errorf("override not picked up: %+v", m.Find("billing_svc"))
	}

	// --rig limits the rescan and rejects unknown rigs.
	if _, _, err := Build(town, "shop", BuildOptions{Rig: "nope"}); err == nil {
		t.Error("expected error for rig outside project")
	}
	loaded, err := Load(town, "shop")
	if err != nil || len(loaded.Services) != 2 {
		t.Errorf("load: %v %+v", err, loaded)
	}
}

func TestBuildSkipsRigWithoutRepo(t *testing.T) {
	t.Parallel()
	town := t.TempDir()
	writeRigs(t, town, map[string]string{"ghost": "file:///r/ghost.git"})
	m, res, err := Build(town, "shop", BuildOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Status != "skipped" || res[0].Err == nil || len(m.Services) != 0 {
		t.Errorf("res = %+v services = %+v", res, m.Services)
	}
}

func TestRenderNeighbourhoodCap(t *testing.T) {
	t.Parallel()
	m := &Map{Project: "p", Services: []Service{{Rig: "hub", Name: "hub", Owns: "x"}}}
	for i := 0; i < 13; i++ {
		rig := "s" + string(rune('a'+i))
		m.Services = append(m.Services, Service{Rig: rig, Name: rig, Calls: []string{"hub"}})
	}
	out := RenderNeighbourhood(m, "hub", 10, "gt project map show p --rig hub")
	if !strings.Contains(out, "and 3 more (gt project map show p --rig hub)") {
		t.Errorf("cap line missing:\n%s", out)
	}
	if n := strings.Count(out, "it calls you"); n != 10 {
		t.Errorf("neighbours shown = %d", n)
	}
	if RenderNeighbourhood(m, "zzz", 10, "") != "" {
		t.Error("unknown rig should render nothing")
	}
}

func TestRenderMarkdownEdges(t *testing.T) {
	t.Parallel()
	md := RenderMarkdown(shopMap())
	for _, want := range []string{"| orders_svc |", "- api_gateway calls orders_svc", "- orders_svc publishes order.placed, consumed by billing_svc"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q:\n%s", want, md)
		}
	}
}
