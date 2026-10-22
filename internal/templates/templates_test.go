package templates_test

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zarigata/double-agent/internal/runtime"
	"github.com/zarigata/double-agent/internal/templates"
)

// testSpec returns a valid template specification.
func testSpec() templates.Spec {
	return templates.Spec{
		ID:         "test-template",
		Version:    "1.0.0",
		Title:      "Test Template",
		Runtime:    runtime.KindContainer,
		Image:      "docker.io/library/alpine:3.20",
		Resources:  runtime.Resources{CPU: 1, MemoryMB: 256},
		Network:    runtime.NetworkPolicy{Egress: "none"},
		TTLSeconds: 900,
	}
}

// TestTheBuiltinTemplatesAreValid checks that every template that ships with
// the control plane can be published.
func TestTheBuiltinTemplatesAreValid(t *testing.T) {
	builtins := templates.Builtin()
	if len(builtins) == 0 {
		t.Fatal("there are no builtin templates")
	}
	for _, tmpl := range builtins {
		t.Run(tmpl.Spec.ID, func(t *testing.T) {
			if err := tmpl.Spec.Validate(); err != nil {
				t.Errorf("the builtin template is invalid: %v", err)
			}
			if tmpl.Spec.ID == "" || tmpl.Spec.Version == "" {
				t.Error("the builtin template has no identifier or version")
			}
			if tmpl.Spec.Image == "" {
				t.Error("the builtin template has no image")
			}
			if tmpl.Digest == "" {
				t.Fatal("the builtin template has no digest")
			}
			if len(tmpl.Digest) != 64 {
				t.Errorf("the digest %q holds %d characters, want 64", tmpl.Digest, len(tmpl.Digest))
			}
			if _, err := hex.DecodeString(tmpl.Digest); err != nil {
				t.Errorf("the digest %q is not hexadecimal: %v", tmpl.Digest, err)
			}
		})
	}
}

// TestTheDigestIsStableAcrossRuns checks that the builtin templates carry the
// same digest every time they are built.
func TestTheDigestIsStableAcrossRuns(t *testing.T) {
	first := templates.Builtin()
	second := templates.Builtin()
	if len(first) != len(second) {
		t.Fatalf("the two runs returned %d and %d templates", len(first), len(second))
	}
	for i := range first {
		if first[i].Spec.ID != second[i].Spec.ID {
			t.Fatalf("the template %d changed identity: %q and %q", i, first[i].Spec.ID, second[i].Spec.ID)
		}
		if first[i].Digest != second[i].Digest {
			t.Errorf("the digest of %q changed: %q and %q",
				first[i].Spec.ID, first[i].Digest, second[i].Digest)
		}
	}
}

// TestTheDigestFollowsTheSpecification checks that a change of the
// specification changes the digest.
func TestTheDigestFollowsTheSpecification(t *testing.T) {
	spec := testSpec()
	first, err := templates.New(spec)
	if err != nil {
		t.Fatalf("build the template: %v", err)
	}
	second, err := templates.New(spec)
	if err != nil {
		t.Fatalf("build the template: %v", err)
	}
	if first.Digest != second.Digest {
		t.Errorf("the digest of the same specification changed: %q and %q", first.Digest, second.Digest)
	}
	if first.Digest != spec.Digest() {
		t.Errorf("the digest %q does not match the one of the specification %q", first.Digest, spec.Digest())
	}

	spec.Title = "Another Title"
	other, err := templates.New(spec)
	if err != nil {
		t.Fatalf("build the template: %v", err)
	}
	if other.Digest == first.Digest {
		t.Error("a changed specification kept its digest")
	}
}

// TestTheCatalogueRefusesADuplicateVersion checks that a version cannot be
// published twice with a different content.
func TestTheCatalogueRefusesADuplicateVersion(t *testing.T) {
	catalogue := templates.NewCatalogue()
	first, err := templates.New(testSpec())
	if err != nil {
		t.Fatalf("build the template: %v", err)
	}
	if err := catalogue.Publish(first); err != nil {
		t.Fatalf("publish the template: %v", err)
	}
	// Publishing the very same version again is accepted: the catalogue
	// stays idempotent.
	if err := catalogue.Publish(first); err != nil {
		t.Errorf("the same version was refused: %v", err)
	}

	spec := testSpec()
	spec.Title = "A Different Title"
	other, err := templates.New(spec)
	if err != nil {
		t.Fatalf("build the template: %v", err)
	}
	if err := catalogue.Publish(other); err == nil {
		t.Error("a second version with a different digest was published")
	}
	// The refused version did not replace the published one.
	published, ok := catalogue.Get("test-template", "1.0.0")
	if !ok {
		t.Fatal("the published version disappeared")
	}
	if published.Digest != first.Digest {
		t.Error("the published version was replaced")
	}
}

// TestGetReturnsTheRequestedVersion checks the lookup of a published
// version.
func TestGetReturnsTheRequestedVersion(t *testing.T) {
	catalogue := templates.NewCatalogue()
	for _, version := range []string{"1.0.0", "1.2.0", "2.0.0"} {
		spec := testSpec()
		spec.Version = version
		tmpl, err := templates.New(spec)
		if err != nil {
			t.Fatalf("build the template: %v", err)
		}
		if err := catalogue.Publish(tmpl); err != nil {
			t.Fatalf("publish the template: %v", err)
		}
	}

	for _, version := range []string{"1.0.0", "1.2.0", "2.0.0"} {
		tmpl, ok := catalogue.Get("test-template", version)
		if !ok {
			t.Fatalf("the version %q was not found", version)
		}
		if tmpl.Spec.Version != version {
			t.Errorf("the catalogue returned the version %q, want %q", tmpl.Spec.Version, version)
		}
	}

	// The empty version selects the highest published one.
	latest, ok := catalogue.Get("test-template", "")
	if !ok {
		t.Fatal("the latest version was not found")
	}
	if latest.Spec.Version != "2.0.0" {
		t.Errorf("the latest version is %q, want 2.0.0", latest.Spec.Version)
	}
}

// TestGetRefusesAnUnknownTemplate checks the lookup of a template that was
// never published.
func TestGetRefusesAnUnknownTemplate(t *testing.T) {
	catalogue := templates.NewCatalogue()
	if _, ok := catalogue.Get("missing", "1.0.0"); ok {
		t.Error("an unknown template was found")
	}
	if _, ok := catalogue.Get("missing", ""); ok {
		t.Error("an unknown template was found")
	}

	spec := testSpec()
	tmpl, err := templates.New(spec)
	if err != nil {
		t.Fatalf("build the template: %v", err)
	}
	if err := catalogue.Publish(tmpl); err != nil {
		t.Fatalf("publish the template: %v", err)
	}
	if _, ok := catalogue.Get("test-template", "9.9.9"); ok {
		t.Error("an unknown version was found")
	}
}

// TestTheCatalogueListsThePublishedTemplates checks the listing of a
// catalogue.
func TestTheCatalogueListsThePublishedTemplates(t *testing.T) {
	catalogue := templates.NewCatalogue()
	for _, id := range []string{"beta", "alpha"} {
		spec := testSpec()
		spec.ID = id
		tmpl, err := templates.New(spec)
		if err != nil {
			t.Fatalf("build the template: %v", err)
		}
		if err := catalogue.Publish(tmpl); err != nil {
			t.Fatalf("publish the template: %v", err)
		}
	}
	ids := catalogue.IDs()
	if len(ids) != 2 || ids[0] != "alpha" || ids[1] != "beta" {
		t.Errorf("the identifiers are %v, want [alpha beta]", ids)
	}
	list := catalogue.List()
	if len(list) != 2 {
		t.Fatalf("the listing holds %d templates, want 2", len(list))
	}
	if list[0].Spec.ID != "alpha" || list[1].Spec.ID != "beta" {
		t.Errorf("the listing is not sorted: %q and %q", list[0].Spec.ID, list[1].Spec.ID)
	}
}

// TestParseReadsAManifest checks the reading of a template manifest.
func TestParseReadsAManifest(t *testing.T) {
	tmpl, err := templates.Parse([]byte(`
id: python-repl
version: 2.1.0
title: Python REPL
runtime: container
image: docker.io/library/python:3.12-slim
resources:
  cpu: 2
  memory_mb: 2048
network:
  egress: none
ttl_seconds: 600
`))
	if err != nil {
		t.Fatalf("parse the manifest: %v", err)
	}
	if tmpl.Spec.ID != "python-repl" || tmpl.Spec.Version != "2.1.0" {
		t.Errorf("the manifest was read as %+v", tmpl.Spec)
	}
	if tmpl.Spec.Resources.CPU != 2 || tmpl.Spec.Resources.MemoryMB != 2048 {
		t.Errorf("the resources were read as %+v", tmpl.Spec.Resources)
	}
	if tmpl.Spec.TTLSeconds != 600 {
		t.Errorf("the lifetime was read as %d, want 600", tmpl.Spec.TTLSeconds)
	}
	if tmpl.Digest == "" {
		t.Error("the parsed template has no digest")
	}
}

// TestParseRefusesAnInvalidManifest checks that a manifest that cannot work
// is refused.
func TestParseRefusesAnInvalidManifest(t *testing.T) {
	if _, err := templates.Parse([]byte("id: broken\n")); err == nil {
		t.Error("a manifest without a version and an image was accepted")
	}
	if _, err := templates.Parse([]byte(":: not yaml ::\n")); err == nil {
		t.Error("a manifest that is not YAML was accepted")
	}
}

// TestLoadDirReadsTheManifests checks the reading of a directory of
// manifests.
func TestLoadDirReadsTheManifests(t *testing.T) {
	dir := t.TempDir()
	manifest := "id: %s\nversion: 1.0.0\nimage: alpine:3.20\nresources:\n  cpu: 1\n  memory_mb: 128\n"
	for _, name := range []string{"beta", "alpha"} {
		path := filepath.Join(dir, name+".yaml")
		if err := os.WriteFile(path, []byte(fmt.Sprintf(manifest, name)), 0o600); err != nil {
			t.Fatalf("write the manifest: %v", err)
		}
	}
	// A file that is not a manifest is ignored.
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatalf("write the note: %v", err)
	}

	loaded, err := templates.LoadDir(dir)
	if err != nil {
		t.Fatalf("load the directory: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("the directory holds %d templates, want 2", len(loaded))
	}
	if loaded[0].Spec.ID != "alpha" || loaded[1].Spec.ID != "beta" {
		t.Errorf("the templates are not sorted: %q and %q", loaded[0].Spec.ID, loaded[1].Spec.ID)
	}
}

// TestLoadDirOfAMissingDirectoryIsEmpty checks that a directory that does
// not exist is not an error.
func TestLoadDirOfAMissingDirectoryIsEmpty(t *testing.T) {
	loaded, err := templates.LoadDir(filepath.Join(t.TempDir(), "missing"))
	if err != nil {
		t.Fatalf("load the directory: %v", err)
	}
	if len(loaded) != 0 {
		t.Errorf("the directory holds %d templates, want 0", len(loaded))
	}
}

// TestTheDefaultLifetimeFallsBackToAnHour checks the lifetime of a template
// that does not declare one.
func TestTheDefaultLifetimeFallsBackToAnHour(t *testing.T) {
	spec := testSpec()
	spec.TTLSeconds = 0
	if got := spec.DefaultTTL(); got != time.Hour {
		t.Errorf("the default lifetime is %s, want 1h", got)
	}
	spec.TTLSeconds = 60
	if got := spec.DefaultTTL(); got != time.Minute {
		t.Errorf("the lifetime is %s, want 1m", got)
	}
}
