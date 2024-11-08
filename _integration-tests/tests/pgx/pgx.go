// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2023-present Datadog, Inc.

//go:build integration

package pgx

import (
	"context"
	"testing"
	"time"

	"datadoghq.dev/orchestrion/_integration-tests/utils"
	"datadoghq.dev/orchestrion/_integration-tests/validator/trace"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	testpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"gopkg.in/DataDog/dd-trace-go.v1/ddtrace/tracer"
)

type TestCase struct {
	container *testpostgres.PostgresContainer
	conn      *pgx.Conn
}

func (tc *TestCase) Setup(t *testing.T) {
	utils.SkipIfProviderIsNotHealthy(t)

	ctx := context.Background()

	var err error
	tc.container, err = testpostgres.Run(ctx,
		"docker.io/postgres:16-alpine",
		testcontainers.WithLogger(testcontainers.TestLogger(t)),
		utils.WithTestLogConsumer(t),
		// https://golang.testcontainers.org/modules/postgres/#wait-strategies_1
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").WithOccurrence(2),
			wait.ForListeningPort("5432/tcp"),
		),
	)
	utils.AssertTestContainersError(t, err)
	utils.RegisterContainerCleanup(t, tc.container)

	dbURL, err := tc.container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	tc.conn, err = pgx.Connect(ctx, dbURL)
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		assert.NoError(t, tc.conn.Close(ctx))
	})
}

func (tc *TestCase) Run(t *testing.T) {
	ctx := context.Background()
	span, ctx := tracer.StartSpanFromContext(ctx, "test.root")
	defer span.Finish()

	var x int
	err := tc.conn.QueryRow(ctx, "SELECT 1").Scan(&x)
	require.NoError(t, err)
	require.Equal(t, 1, x)
}

func (*TestCase) ExpectedTraces() trace.Traces {
	return trace.Traces{
		{
			Tags: map[string]any{
				"name": "test.root",
			},
			Children: trace.Traces{
				{
					Tags: map[string]any{
						"name":     "pgx.query",
						"service":  "postgres.db",
						"resource": "SELECT 1",
						"type":     "sql",
					},
					Meta: map[string]string{
						"component":    "jackc/pgx.v5",
						"span.kind":    "client",
						"db.system":    "postgresql",
						"db.operation": "Query",
					},
				},
			},
		},
	}
}