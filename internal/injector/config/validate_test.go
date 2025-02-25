// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2023-present Datadog, Inc.

package config_test

import (
	"bytes"
	_ "embed"
	"fmt"
	"sync"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/require"
)

func TestSchemaValidity(t *testing.T) {
	count := validateExamples(t, getSchema(), "", nil)
	// Make sure we verified some examples...
	require.Greater(t, count, 30)
}

func validateExamples(t *testing.T, schema *jsonschema.Schema, path string, visited map[*jsonschema.Schema]struct{}) int {
	if schema == nil {
		return 0
	}

	if _, dup := visited[schema]; dup {
		return 0
	}
	if visited == nil {
		visited = make(map[*jsonschema.Schema]struct{})
	}
	visited[schema] = struct{}{}

	for idx, example := range schema.Examples {
		require.NoError(t, schema.Validate(example), "invalid example at %s.Examples[%d]", path, idx)
	}
	count := len(schema.Examples)

	count += validateExamples(t, schema.Ref, path+".Ref", visited)
	count += validateExamples(t, schema.RecursiveRef, path+".RecursiveRef", visited)
	if schema.DynamicRef != nil {
		count += validateExamples(t, schema.DynamicRef.Ref, path+".DynamicRef.Ref", visited)
	}
	count += validateExamples(t, schema.Not, path+".Not", visited)
	count += validateExamplesList(t, schema.AllOf, path+".AllOf", visited)
	count += validateExamplesList(t, schema.AnyOf, path+".AnyOf", visited)
	count += validateExamplesList(t, schema.OneOf, path+".OneOf", visited)
	count += validateExamples(t, schema.If, path+".If", visited)
	count += validateExamples(t, schema.Then, path+".Then", visited)
	count += validateExamples(t, schema.Else, path+".Else", visited)
	count += validateExamples(t, schema.PropertyNames, path+".PropertyNames", visited)
	count += validateExamplesMap(t, schema.Properties, path+".Properties", visited)
	count += validateExamplesMap(t, schema.PatternProperties, path+".PatternProperties", visited)
	count += validateExamplesMap(t, schema.DependentSchemas, path+".DependentSchemas", visited)
	count += validateExamples(t, schema.UnevaluatedProperties, path+".UnevaluatedProperties", visited)
	count += validateExamples(t, schema.Contains, path+".Contains", visited)
	count += validateExamplesList(t, schema.PrefixItems, path+".PrefixItems", visited)
	count += validateExamples(t, schema.Items2020, path+".Items2020", visited)
	count += validateExamples(t, schema.UnevaluatedItems, path+".UnevaluatedItems", visited)
	count += validateExamples(t, schema.ContentSchema, path+".ContentSchema", visited)

	return count
}

func validateExamplesList(t *testing.T, schemas []*jsonschema.Schema, path string, visited map[*jsonschema.Schema]struct{}) int {
	count := 0
	for idx, schema := range schemas {
		count += validateExamples(t, schema, fmt.Sprintf("%s[%d]", path, idx), visited)
	}
	return count
}

func validateExamplesMap[K comparable](t *testing.T, schemas map[K]*jsonschema.Schema, path string, visited map[*jsonschema.Schema]struct{}) int {
	count := 0
	for key, schema := range schemas {
		count += validateExamples(t, schema, fmt.Sprintf("%s[%v]", path, key), visited)
	}
	return count
}

var (
	//go:embed "schema.json"
	schemaBytes []byte
	schema      *jsonschema.Schema
	schemaOnce  sync.Once
)

func getSchema() *jsonschema.Schema {
	schemaOnce.Do(compileSchema)
	return schema
}

func compileSchema() {
	rawSchema, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaBytes))
	if err != nil {
		panic(fmt.Errorf("parsing JSON schema: %w", err))
	}
	mapSchema, _ := rawSchema.(map[string]any)
	schemaURL, _ := mapSchema["$id"].(string)

	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(schemaURL, rawSchema); err != nil {
		panic(fmt.Errorf("preparing JSON schema compiler: %w", err))
	}

	schema, err = compiler.Compile(schemaURL)
	if err != nil {
		panic(fmt.Errorf("compiling JSON schema: %w", err))
	}
}
