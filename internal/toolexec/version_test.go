// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2023-present Datadog, Inc.

package toolexec

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/DataDog/orchestrion/internal/goflags"
	"github.com/DataDog/orchestrion/internal/injector/config"
	"github.com/DataDog/orchestrion/internal/jobserver/client"
	"github.com/DataDog/orchestrion/internal/toolexec/proxy"
	"github.com/otiai10/copy"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

var rootDir string

func init() {
	_, file, _, _ := runtime.Caller(0)
	rootDir = filepath.Join(file, "..", "..", "..")
}

func Test(t *testing.T) {
	t.Setenv(client.EnvVarJobserverURL, "") // Make sure we don't accidentally connect to an external jobserver...

	tmp := t.TempDir()
	runGo(t, tmp, "mod", "init", "github.com/DataDog/phony/package")
	runGo(t, tmp, "mod", "edit",
		fmt.Sprintf("-replace=github.com/DataDog/orchestrion=%s", rootDir),
		fmt.Sprintf("-replace=github.com/DataDog/orchestrion/instrument=%s", filepath.Join(rootDir, "instrument")),
	)

	require.NoError(t, os.WriteFile(filepath.Join(tmp, config.FilenameOrchestrionToolGo), []byte(`
		//go:build tools
		package tools

		import (
			_ "github.com/DataDog/orchestrion"
			_ "github.com/DataDog/orchestrion/instrument"
		)
	`), 0o644))
	runGo(t, tmp, "mod", "tidy")

	// "Fake" proxy command.
	cmd, err := proxy.ParseCommand(context.Background(), "github.com/DataDog/phony/package", []string{"go", "tool", "compile", "-V=full"})
	require.NoError(t, err)

	ctx := zerolog.New(zerolog.MultiLevelWriter(zerolog.NewTestWriter(t))).
		Level(zerolog.InfoLevel).
		WithContext(context.Background())

	// Artificially set goflags as this is needed for the test to run....
	goflags.SetFlags(ctx, tmp, []string{"test", "./..."})

	// Compute the initial version string...
	initial := inDir(t, tmp, func() string {
		v, err := ComputeVersion(ctx, cmd)
		require.NoError(t, err)
		return v
	})
	require.NotEmpty(t, initial)

	copyDir := t.TempDir()
	require.NoError(t, copy.Copy(rootDir, copyDir, copy.Options{
		Skip: func(_ os.FileInfo, src string, _ string) (bool, error) {
			return filepath.Base(src) == ".git", nil
		},
	}))
	beaconFile := filepath.Join(copyDir, "instrument", "beacon___.go")
	require.NoError(t, os.WriteFile(beaconFile, []byte("package instrument\nconst BEACON = 42"), 0o644))
	// Add an aspect that looks like it may inject github.com/DataDog/orchestrion/instrument
	require.NoError(t, os.WriteFile(
		filepath.Join(copyDir, "instrument", config.FilenameOrchestrionYML),
		[]byte(`aspects: [{join-point: {test-main: true}, advice: [{inject-declarations: {imports: {i: 'github.com/DataDog/orchestrion/instrument'}}}]}]`),
		0o644,
	))

	// Replace the orchestrion package with the copy we just made...
	runGo(t, tmp, "mod", "edit",
		fmt.Sprintf("-replace=github.com/DataDog/orchestrion=%s", copyDir),
		fmt.Sprintf("-replace=github.com/DataDog/orchestrion/instrument=%s", filepath.Join(copyDir, "instrument")),
	)
	runGo(t, tmp, "mod", "tidy") // The hash of the dependency has changed... go list would complain...
	updated := inDir(t, tmp, func() string {
		v, err := ComputeVersion(ctx, cmd)
		require.NoError(t, err)
		return v
	})
	require.NotEmpty(t, updated)
	require.NotEqual(t, initial, updated)

	// Modify the beacon
	require.NoError(t, os.WriteFile(beaconFile, []byte("package instrument\nconst BEACON = 1337"), 0o644))
	final := inDir(t, tmp, func() string {
		v, err := ComputeVersion(ctx, cmd)
		require.NoError(t, err)
		return v
	})
	require.NotEmpty(t, final)
	require.NotEqual(t, initial, final)
	require.NotEqual(t, updated, final)
}

func inDir[T any](t *testing.T, wd string, cb func() T) T {
	orig, err := os.Getwd()
	require.NoError(t, err)
	defer func() { require.NoError(t, os.Chdir(orig)) }()

	require.NoError(t, os.Chdir(wd))
	return cb()
}

func runGo(t *testing.T, wd string, args ...string) {
	cmd := exec.Command("go", args...)
	cmd.Dir = wd
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	require.NoError(t, cmd.Run(), "failed to run 'go %s'", strings.Join(args, " "))
}
