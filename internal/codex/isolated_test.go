package codex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Riki1312/agent-teleport/internal/archive"
	"github.com/Riki1312/agent-teleport/internal/provider"
)

func isolatedFixture(t *testing.T) isolatedImport {
	t.Helper()
	return isolatedImport{Version: 1, BaseHome: t.TempDir(), SessionID: testSessionID, Workspace: t.TempDir()}
}

func TestIsolationSharesRuntimeWithoutSharingHistory(t *testing.T) {
	i := isolatedFixture(t)
	for _, name := range []string{"config.toml", "auth.json", "work.config.toml"} {
		if err := os.WriteFile(filepath.Join(i.BaseHome, name), []byte("runtime fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"skills", "plugins", "sessions", "archived_sessions"} {
		mkdirTest(t, filepath.Join(i.BaseHome, name))
	}
	if err := prepareIsolation(i); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"config.toml", "auth.json", "work.config.toml", "skills", "plugins"} {
		target, err := os.Readlink(filepath.Join(i.home(), name))
		if err != nil || target != filepath.Join(i.BaseHome, name) {
			t.Fatalf("resource %s: %s, %v", name, target, err)
		}
	}
	for _, name := range []string{"sessions", "archived_sessions", "state_5.sqlite"} {
		assertMissing(t, filepath.Join(i.home(), name))
	}
	info, err := os.Stat(i.home())
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("private store permissions: %v, %v", info, err)
	}
	i.Ready = true
	if err := writeIsolation(i); err != nil {
		t.Fatal(err)
	}
	// Reusing or refreshing runtime links must not reset a committed receipt.
	stale := i
	stale.Ready = false
	if err := prepareIsolation(stale); err != nil {
		t.Fatal(err)
	}
	loaded, err := readIsolation(i.home())
	if err != nil || !loaded.Ready {
		t.Fatalf("committed receipt was lost: %+v, %v", loaded, err)
	}
	if loaded.reference() != stale.reference() {
		t.Fatal("reference changed without changing identity")
	}
	changed := i
	changed.Workspace += "-other"
	if changed.reference() == i.reference() {
		t.Fatal("different workspaces share a reference")
	}
}

func TestIsolationRejectsForeignReceiptsAndRuntimeLinks(t *testing.T) {
	for _, change := range []string{"version", "workspace", "unknown field", "runtime link", "storage link"} {
		t.Run(change, func(t *testing.T) {
			i := isolatedFixture(t)
			if change == "storage link" {
				mkdirTest(t, filepath.Dir(i.home()))
				if err := os.Symlink(i.BaseHome, i.home()); err != nil {
					t.Fatal(err)
				}
				if err := prepareIsolation(i); err == nil {
					t.Fatal("accepted symlinked history storage")
				}
				return
			}
			if err := prepareIsolation(i); err != nil {
				t.Fatal(err)
			}
			if change == "runtime link" {
				if err := os.WriteFile(filepath.Join(i.BaseHome, "auth.json"), []byte("placeholder"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), filepath.Join(i.home(), "auth.json")); err != nil {
					t.Fatal(err)
				}
				if err := prepareIsolation(i); err == nil {
					t.Fatal("accepted a foreign auth link")
				}
				return
			}
			data, err := os.ReadFile(filepath.Join(i.home(), isolationMarker))
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]any
			if err := json.Unmarshal(data, &fields); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "version":
				fields["version"] = 2
			case "workspace":
				fields["workspace"] = "/another"
			case "unknown field":
				fields["extra"] = true
			}
			data, _ = json.Marshal(fields)
			if err := os.WriteFile(filepath.Join(i.home(), isolationMarker), data, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := readIsolation(i.home()); err == nil {
				t.Fatal("accepted invalid receipt")
			}
		})
	}
}

func TestIsolatedDryRunCreatesNothing(t *testing.T) {
	i := isolatedFixture(t)
	file := fixtureFiles(t, parentFixture)[0]
	source, err := archive.New(archive.Header{Provider: Provider, ProviderVersion: testLayout.newestTested, SessionID: file.meta.ID, Workspace: file.meta.CWD, Home: "/home/source/.codex", CreatedAt: time.Now()}, []archive.Payload{{Path: file.name, Data: file.data}})
	if err != nil {
		t.Fatal(err)
	}
	m := &managedStore{primary: &Store{ctx: context.Background(), home: i.BaseHome, layout: testLayout, version: testLayout.newestTested}}
	result, err := m.Import(provider.ImportRequest{Archive: source, Workspace: i.Workspace, Isolated: true, DryRun: true})
	if err != nil || result.Outcome != provider.Imported || !strings.HasPrefix(result.Reference, "iso-") {
		t.Fatalf("dry run: %+v, %v", result, err)
	}
	assertMissing(t, filepath.Join(i.BaseHome, "agent-teleport"))
}

func TestIsolatedResumeBindsItsStoreAndOverridesSharedDatabaseConfig(t *testing.T) {
	i := isolatedFixture(t)
	store := (&Store{ctx: context.Background(), binary: "codex"}).isolatedStore(i)
	t.Setenv("CODEX_HOME", i.BaseHome)
	t.Setenv("CODEX_SQLITE_HOME", i.BaseHome)
	// A synthetic native preflight reports no managed requirements.
	probe := filepath.Join(t.TempDir(), "codex-probe")
	if err := os.WriteFile(probe, []byte("#!/bin/sh\nprintf '%s\\n' '{\"configRequirements\":{\"requirements\":null}}'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	store.binary = probe
	command, err := store.PrepareResume(i.SessionID, i.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	if command.Dir != i.Workspace || !strings.Contains(strings.Join(command.Args, " "), "--no-daemon") {
		t.Fatalf("resume command: %+v", command)
	}
	joined := strings.Join(command.Env, "\n")
	if !strings.Contains(joined, "CODEX_HOME="+i.home()+"\n") && !strings.HasSuffix(joined, "CODEX_HOME="+i.home()) {
		t.Fatal("resume did not select its own history home")
	}
	if !strings.Contains(strings.Join(command.Args, " "), "sqlite_home=") {
		t.Fatal("shared database config was not overridden")
	}
}

func mkdirTest(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestManagedReferencesSelectNativeForksWithoutChangingIdentity(t *testing.T) {
	parent, fork := fixtureFiles(t, parentFixture, forkFixture)[0], fixtureFiles(t, parentFixture, forkFixture)[1]
	i := isolatedFixture(t)
	i.SessionID, i.Workspace = parent.meta.ID, parent.meta.CWD
	if err := prepareIsolation(i); err != nil {
		t.Fatal(err)
	}
	m := &managedStore{primary: &Store{ctx: context.Background(), home: i.BaseHome, layout: testLayout, version: testLayout.newestTested}}
	if _, err := m.Session(i.reference()); err == nil {
		t.Fatal("unfinished import was exposed")
	}
	writeRollout(t, i.home(), parent.name, parent.data)
	writeRollout(t, i.home(), fork.name, fork.data)
	i.Ready = true
	if err := writeIsolation(i); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{parent.meta.ID, fork.meta.ID} {
		reference := i.referenceFor(id)
		session, err := m.Session(reference)
		if err != nil || session.ID != id || session.Selector() != reference {
			t.Fatalf("reference lookup: %+v, %v", session, err)
		}
		exported, err := m.Export(reference, time.Now())
		if err != nil || exported.Manifest.SessionID != id {
			t.Fatalf("reference export changed native identity: %v", err)
		}
	}
	if _, err := m.Session(i.reference() + ":../escape"); err == nil {
		t.Fatal("accepted invalid nested reference")
	}
	if _, err := m.Session(parent.meta.ID); err == nil {
		t.Fatal("native ID silently selected another history store")
	}
}

func TestManagedImportsSerializeBeforeStartingCodex(t *testing.T) {
	file := fixtureFiles(t, parentFixture)[0]
	i := isolatedFixture(t)
	i.SessionID = file.meta.ID
	if err := prepareIsolation(i); err != nil {
		t.Fatal(err)
	}
	unlock, err := lockThread(i.home(), "agent-teleport-import")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	source, err := archive.New(archive.Header{Provider: Provider, ProviderVersion: testLayout.newestTested, SessionID: file.meta.ID, Workspace: file.meta.CWD, Home: "/home/source/.codex", CreatedAt: time.Now()}, []archive.Payload{{Path: file.name, Data: file.data}})
	if err != nil {
		t.Fatal(err)
	}
	m := &managedStore{primary: &Store{ctx: context.Background(), home: i.BaseHome, layout: testLayout, version: testLayout.newestTested, binary: "must-not-start-codex"}}
	if _, err := m.Import(provider.ImportRequest{Archive: source, Workspace: i.Workspace, Isolated: true}); err == nil || !strings.Contains(err.Error(), "updating this isolated copy") {
		t.Fatalf("concurrent import: %v", err)
	}
	assertMissing(t, filepath.Join(i.home(), "sessions"))
}

func TestIsolationRejectsManagedSharedStorage(t *testing.T) {
	for _, test := range []struct {
		name, report string
		valid        bool
	}{
		{"no requirements", `{"configRequirements":{"requirements":null}}`, true},
		{"other requirements", `{"configRequirements":{"requirements":{"allowedApprovalPolicies":["never"]}}}`, true},
		{"isolated requirements", `{"configRequirements":{"requirements":{"sqliteHome":"/private/isolated","logDir":"/private/isolated/log"}}}`, true},
		{"shared database", `{"configRequirements":{"requirements":{"sqliteHome":"/private/normal"}}}`, false},
		{"shared log database", `{"configRequirements":{"requirements":{"logDir":"/private/normal/log"}}}`, false},
		{"missing report", `{}`, false},
		{"invalid report", `not JSON`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := validateStorageRequirements([]byte(test.report), "/private/isolated"); (err == nil) != test.valid {
				t.Fatalf("storage preflight: %v", err)
			}
		})
	}
}

func TestManagedStorageIsCheckedBeforeAppServerInitialization(t *testing.T) {
	i := isolatedFixture(t)
	if err := prepareIsolation(i); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "unsafe-start")
	probe := filepath.Join(t.TempDir(), "codex-probe")
	quotedMarker := "'" + strings.ReplaceAll(marker, "'", "'\\''") + "'"
	script := "#!/bin/sh\nfor argument do\n if [ \"$argument\" = --version ]; then printf 'codex-cli 0.169.0\\n'; exit 0; fi\n if [ \"$argument\" = --print-startup-config ]; then\n  printf '%s\\n' '{\"configRequirements\":{\"requirements\":{\"sqliteHome\":\"/shared/database\"}}}'\n  exit 0\n fi\ndone\n: > " + quotedMarker + "\nexit 1\n"
	if err := os.WriteFile(probe, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	store := (&Store{ctx: context.Background(), binary: probe}).isolatedStore(i)
	if _, err := store.startAppServer(); err == nil || !strings.Contains(err.Error(), "managed Codex sqlite_home") {
		t.Fatalf("expected managed storage refusal: %v", err)
	}
	t.Setenv(BinaryEnvironment, probe)
	t.Setenv("CODEX_HOME", i.home())
	diagnosis := Diagnose(context.Background())
	if diagnosis.Err == nil || !strings.Contains(diagnosis.Err.Error(), "managed Codex sqlite_home") {
		t.Fatalf("doctor bypassed managed storage preflight: %v", diagnosis.Err)
	}
	assertMissing(t, marker)
}
