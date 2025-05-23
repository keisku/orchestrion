// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2023-present Datadog, Inc.

package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"github.com/DataDog/orchestrion/internal/filelock"
	"github.com/DataDog/orchestrion/internal/goflags"
	"github.com/DataDog/orchestrion/internal/jobserver"
	"github.com/DataDog/orchestrion/internal/jobserver/client"
	"github.com/fsnotify/fsnotify"
	"github.com/goccy/go-yaml"
	"github.com/rs/zerolog"

	"github.com/urfave/cli/v2"
)

var Server = &cli.Command{
	Name:        "server",
	Usage:       "Start an Objectsrion job server.",
	Description: "The job server is used to remove duplicated processing that can occur when instrumenting large applications, due to how Orchestrion injects new dependencies that the go toolchain was initially not aware of.\n\nUsers do not normally need to use this command directly, as Orchestrion automatically manages servers during runtime.",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:  "url-file",
			Usage: "Write a file containing the ClientURL for this server once it is ready to accept connections. The server automatically shuts down when the URL file is deleted.",
		},
		&cli.IntFlag{
			Name:        "port",
			Usage:       "Choose a port to listen on.",
			Value:       -1,
			DefaultText: "random",
		},
		&cli.DurationFlag{
			Name:  "inactivity-timeout",
			Usage: "Automatically shut down after a period without any connected client.",
			Value: time.Minute,
		},
		&cli.BoolFlag{
			Name:  "nats-logging",
			Usage: "Enable NATS server logging.",
		},
		&cli.StringFlag{
			Name:        "build-flags",
			Usage:       "Specify the 'go build' flags to use when resolving packages. This is specified as a YAML array and must start with a valid go subcommand (e.g, 'build').",
			DefaultText: "Looked up the process hierarchy",
			Action: func(ctx *cli.Context, val string) error {
				var args []string
				if err := yaml.Unmarshal([]byte(val), &args); err != nil {
					return cli.Exit(fmt.Errorf("invalid -build-flags value: %w", err), 2)
				}
				goflags.SetFlags(ctx.Context, ".", args)
				return nil
			},
		},
		&cli.IntFlag{
			Name:        "parent-pid",
			Usage:       "Specify which process created this server. This is useful when the server is started as a daemon, as it needs to be able to resolve the top-level go command line.",
			DefaultText: "This process' parent (may be inaccurate if the process is daemonized)",
			Action: func(ctx *cli.Context, val int) error {
				return goflags.SetFlagsFromPid(ctx.Context, val)
			},
		},
	},
	Hidden: true,
	Action: func(ctx *cli.Context) error {
		log := zerolog.Ctx(ctx.Context)

		opts := jobserver.Options{
			Port:              ctx.Int("port"),
			InactivityTimeout: ctx.Duration("inactivity-timeout"),
			EnableLogging:     ctx.Bool("nats-logging"),
		}

		if urlFile := ctx.String("url-file"); urlFile != "" {
			if err := startWithURLFile(ctx.Context, &opts, urlFile); err != nil {
				log.Error().Err(err).Str("url-file", urlFile).Msg("Failed to start job server")
			}
			return nil
		}
		_, err := start(ctx.Context, &opts, true)
		if err != nil {
			log.Error().Err(err).Msg("Failed to start job server")
		}
		return err
	},
}

// start starts a new job server, and waits for it to have completely shut down if `wait` is true.
// When `wait` is true, the server is always returned as `nil`.
func start(ctx context.Context, opts *jobserver.Options, wait bool) (*jobserver.Server, cli.ExitCoder) {
	server, err := jobserver.New(ctx, opts)
	if err != nil {
		return nil, cli.Exit(fmt.Errorf("failed to start job server: %w", err), 1)
	}

	if wait {
		server.WaitForShutdown()
		return nil, nil
	}

	return server, nil
}

// startWithURLFile starts a new job server using the provided URL file (unless the file contains the URL to a still
// running server), and waits for it to have completely shut down.
func startWithURLFile(ctx context.Context, opts *jobserver.Options, urlFile string) cli.ExitCoder {
	log := zerolog.Ctx(ctx)

	file := filelock.MutexAt(urlFile)
	if err := file.RLock(ctx); err != nil {
		return cli.Exit(fmt.Errorf("failed to acquire read lock on %q: %w", urlFile, err), 1)
	}
	log.Trace().
		Str("url-file", urlFile).
		Msg("Acquired read lock on URL file")

	// Check if there is already a server running...
	if url, err := hasURLToRunningServer(file); err != nil {
		return cli.Exit(err, 1)
	} else if url != "" {
		return cli.Exit(fmt.Sprintf("A server is already listening on %q", url), 2)
	}

	// No existing server, so now we're actually going to try starting our own
	if err := file.Lock(ctx); err != nil {
		return cli.Exit(fmt.Errorf("failed to upgrade to write lock on %q: %w", urlFile, err), 1)
	}
	log.Trace().
		Str("url-file", urlFile).
		Msg("Upgraded lock on URL file to write lock")

	// Check again whether there is a running server; as a concurrent process might have acquired the write lock first.
	if url, err := hasURLToRunningServer(file); err != nil {
		return cli.Exit(err, 1)
	} else if url != "" {
		return cli.Exit(fmt.Sprintf("A server is already listening on %q", url), 2)
	}

	// This process "owns" the URL file, so it'll try had to remove it when it terminates...
	cancelDeleteOnInterrupt := deleteOnInterrupt(ctx, urlFile)
	defer cancelDeleteOnInterrupt()
	defer os.Remove(urlFile)

	// Start the server normally...
	server, err := start(ctx, opts, false)
	if err != nil {
		return err
	}
	clientURL := server.ClientURL()
	log.Trace().
		Str("url-file", urlFile).
		Str("url", clientURL).
		Msg("Server component successfully started")

	// Write the ClientURL into the urlFile
	if _, err := file.Write([]byte(clientURL)); err != nil {
		return cli.Exit(fmt.Errorf("failed to write URL file at %q: %w", urlFile, err), 1)
	}
	log.Trace().
		Str("url-file", urlFile).
		Str("url", clientURL).
		Msg("Populated URL file with server URL")

	// Release the URL File lock
	if err := file.Unlock(ctx); err != nil {
		// Shut the server down, as we won't actually be returning it...
		server.Shutdown()
		return cli.Exit(fmt.Errorf("failed to release lock on %q: %w", urlFile, err), 1)
	}
	log.Trace().
		Str("url-file", urlFile).
		Str("url", clientURL).
		Msg("Released lock on URL file")

	// Try to watch for removal of the URL file, so we can shut down the server eagerly when that happens.
	cancelShutdownOnRemove := shutdownOnRemove(ctx, server, urlFile)
	defer cancelShutdownOnRemove()

	server.WaitForShutdown()
	return nil
}

// deleteOnInterrupt attempts to deletes the provided file when an interrupt signal is received. It returns a
// cancellation function that can be used to uninstall the signal handler.
func deleteOnInterrupt(ctx context.Context, path string) func() {
	sigChan := make(chan os.Signal, 1)
	cancel := func() {
		signal.Stop(sigChan)
		close(sigChan)
	}

	go func() {
		_, closed := <-sigChan
		if closed {
			return
		}
		defer cancel()
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			log := zerolog.Ctx(ctx)
			log.Warn().Str("path", path).Err(err).Msg("os.Remove failed")
		}
	}()

	signal.Notify(sigChan, os.Interrupt)

	return cancel
}

// hasURLToRunningServer checks whether the provided URL file contains the URL to a running server,
// by trying to connect to it. If that is the case, it returns the URL to the running server.
func hasURLToRunningServer(file io.ReadSeeker) (string, error) {
	urlData, err := io.ReadAll(file)
	if err != nil {
		return "", fmt.Errorf("failed to read URL file: %w", err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("failed to seek back to start of file: %w", err)
	}
	if len(urlData) == 0 {
		return "", nil
	}

	url := string(urlData)
	conn, err := client.Connect(url)
	if err != nil {
		return "", nil
	}
	conn.Close()
	return url, nil
}

// shutdownOnRemove shuts the server down when the designated file is removed. It returns a cancellation function that
// can be used to cancel the file watcher. Since fsnotify support is highly dependent on platform/kernel support, this
// function ignores any error and emits WARN log entries describing the problem.
func shutdownOnRemove(ctx context.Context, server *jobserver.Server, urlFile string) func() error {
	log := zerolog.Ctx(ctx)

	// noCancel is returned when there is nothing to cancel...
	noCancel := func() error { return nil }

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Warn().Err(err).Msg("Failed to create fsnotify watcher")
		log.Warn().Str("url-file", urlFile).Msg("The server will not automatically shut down when the URL file is removed; only when it reaches the configured inactivity timeout.")
		return noCancel
	}
	cancel := watcher.Close

	if err := watcher.Add(urlFile); err != nil {
		defer cancel()
		log.Warn().Str("url-file", urlFile).Err(err).Msg("Failed to watch URL file")
		log.Warn().Str("url-file", urlFile).Msg("The server will not automatically shut down when the URL file is removed; only when it reaches the configured inactivity timeout.")
		return noCancel
	}

	go func(events <-chan fsnotify.Event, errors <-chan error) {
		for {
			select {
			case event, ok := <-events:
				if !ok {
					return
				}
				if event.Has(fsnotify.Remove) {
					log.Trace().Str("url-file", event.Name).Msg("URL file was removed; shutting down...")
					server.Shutdown()
				}
			case err, ok := <-errors:
				if !ok {
					return
				}
				log.Warn().Err(err).Msg("File watcher produced an error")
			}
		}
	}(watcher.Events, watcher.Errors)

	return cancel
}
