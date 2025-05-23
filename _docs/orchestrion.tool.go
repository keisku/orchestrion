// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2023-present Datadog, Inc.

//go:build tools

package tools

import (
	// Allows documentation generation for the V2 tracer integrations.
	_ "github.com/DataDog/dd-trace-go/orchestrion/all/v2"
)
