package cli

import (
	"errors"
	"path/filepath"
	"runtime/debug"
	"testing"
)

func TestResolvedVersionPrecedenceAndDevelopmentFallback(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		release string
		main    string
		ok      bool
		want    string
	}{
		{name: "release metadata wins", release: "v0.1.0", main: "v9.9.9", ok: true, want: "0.1.0"},
		{name: "go install metadata", main: "v0.1.0", ok: true, want: "0.1.0"},
		{name: "local build", main: "(devel)", ok: true, want: "devel"},
		{name: "missing build info", want: "devel"},
		{name: "dirty build", main: "v0.1.0+dirty", ok: true, want: "devel"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := resolvedVersion(test.release, func() (*debug.BuildInfo, bool) {
				return &debug.BuildInfo{Main: debug.Module{Version: test.main}}, test.ok
			})
			if got != test.want {
				t.Fatalf("resolvedVersion() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestVersionOutputIsScriptFriendly(t *testing.T) {
	t.Parallel()

	if got := versionOutput("0.1.0"); got != "dataporch v0.1.0\n" {
		t.Fatalf("versionOutput() = %q", got)
	}
	if got := versionOutput("devel"); got != "dataporch devel\n" {
		t.Fatalf("versionOutput() = %q", got)
	}
}

func TestInvocationPathPreservesSelectedSymlink(t *testing.T) {
	t.Parallel()

	lookedUp := false
	got, err := invocationPath("dataporch", func(value string) (string, error) {
		lookedUp = true
		if value != "dataporch" {
			t.Fatalf("LookPath argument = %q, want dataporch", value)
		}
		return "/opt/homebrew/bin/dataporch", nil
	}, func(value string) (string, error) { return value, nil })
	if err != nil {
		t.Fatalf("invocationPath() error = %v", err)
	}
	if !lookedUp || got != "/opt/homebrew/bin/dataporch" {
		t.Fatalf("invocationPath() = %q, lookedUp=%t", got, lookedUp)
	}
}

func TestInvocationPathMakesRelativePathAbsoluteWithoutCanonicalizing(t *testing.T) {
	t.Parallel()

	want := filepath.Join("/work", "bin", "dataporch")
	got, err := invocationPath("./bin/dataporch", func(string) (string, error) {
		return "", errors.New("lookpath must not be called")
	}, func(value string) (string, error) {
		if value != "./bin/dataporch" {
			t.Fatalf("Abs argument = %q", value)
		}
		return want, nil
	})
	if err != nil {
		t.Fatalf("invocationPath() error = %v", err)
	}
	if got != want {
		t.Fatalf("invocationPath() = %q, want %q", got, want)
	}
}
