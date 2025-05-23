// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2023-present Datadog, Inc.

package main

import (
	"context"
	"embed" // For go:embed
	"fmt"
	"html/template"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/DataDog/orchestrion/internal/injector/config"
	"golang.org/x/tools/go/packages"
)

var (
	//go:embed "*.tmpl"
	templateFS embed.FS
	templates  *template.Template
)

type Generator struct {
	// Dir is the directory in which to write the generated files.
	Dir string
	// ConfigSource is the directory from which to load configuration.
	ConfigSource string
	// Validate determines whether the config parser runs in validation mode.
	Validate bool

	// CommonPrefix is used to trim common prefixes from package paths when
	// generating doc titles.
	CommonPrefix string
	// TrimPrefix is used to trim common prefixes from package paths when
	// generating doc titles. This is done after [Generator.CommonPrefix].
	TrimPrefix string
	// TrimSuffix is used to remove common suffixes from package paths when
	// generating doc titles.
	TrimSuffix string

	generatedFiles map[string]struct{}
}

func (g *Generator) Generate() (err error) {
	if err := os.MkdirAll(g.Dir, 0o755); err != nil {
		return fmt.Errorf("mkdir -p %s: %w", g.Dir, err)
	}

	cfg, err := config.NewLoader(nil, g.ConfigSource, g.Validate).Load(context.Background())
	if err != nil {
		return fmt.Errorf("config.Load(%s): %w", g.ConfigSource, err)
	}

	if err := g.updateToolsFile(cfg); err != nil {
		return err
	}

	tree := make(map[string][]config.File)
	if err := config.Visit(cfg, func(cfg config.File, pkgPath string) error {
		tree[pkgPath] = append(tree[pkgPath], cfg)
		return nil
	}); err != nil {
		return err
	}

	g.generatedFiles = make(map[string]struct{}, len(tree))
	for pkgPath, files := range tree {
		if err := g.renderPackage(pkgPath, files); err != nil {
			return err
		}
	}

	return g.cleanupDir()
}

func (g *Generator) updateToolsFile(cfg config.Config) error {
	implied := make(map[string]struct{})
	for _, aspect := range cfg.Aspects() {
		for _, path := range aspect.JoinPoint.ImpliesImported() {
			implied[path] = struct{}{}
		}
	}

	pkgs, err := packages.Load(
		&packages.Config{Mode: packages.NeedDeps | packages.NeedName},
		"github.com/DataDog/orchestrion",
		"github.com/DataDog/orchestrion/instrument",
		"std",
	)
	if err != nil {
		return err
	}

	packages.Visit(
		pkgs,
		func(pkg *packages.Package) bool {
			delete(implied, pkg.PkgPath)
			return true
		},
		nil,
	)

	toImport := make([]string, 0, len(implied))
	for path := range implied {
		toImport = append(toImport, path)
	}
	slices.Sort(toImport)

	file, err := os.Create(filepath.Join(thisFile, "..", "tools.go"))
	if err != nil {
		return err
	}
	defer file.Close()

	if err := template.Must(templates.Clone()).
		ExecuteTemplate(file, "tools.go.tmpl", toImport); err != nil {
		return err
	}

	cmd := exec.Command("go", "mod", "tidy")
	cmd.Dir = docsDir
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("go mod tidy: %w", err)
	}

	return nil
}

func (g *Generator) renderPackage(pkgPath string, files []config.File) error {
	if pkgPath == "github.com/DataDog/orchestrion" {
		return nil
	}

	shortName := strings.TrimPrefix(pkgPath, g.CommonPrefix)
	shortName = strings.TrimPrefix(shortName, g.TrimPrefix)
	shortName = strings.TrimSuffix(shortName, g.TrimSuffix)
	filename := strings.ReplaceAll(shortName, "/", "-") + ".md"
	g.generatedFiles[filename] = struct{}{}

	file, err := os.Create(filepath.Join(g.Dir, filename))
	if err != nil {
		return err
	}
	defer file.Close()

	type context struct {
		Title   string
		PkgPath string
		Files   []config.File
	}
	return template.Must(templates.Clone()).ExecuteTemplate(
		file,
		"doc.md.tmpl",
		context{
			shortName,
			pkgPath,
			files,
		},
	)
}

// cleanupDir removes files from [Generator.Dir] that are found to no longer be
// part of the generation set, so that only needed files are left.
func (g *Generator) cleanupDir() error {
	return filepath.WalkDir(g.Dir, func(path string, entry fs.DirEntry, entryErr error) error {
		if entryErr != nil {
			return entryErr
		}

		if entry.IsDir() {
			return nil
		}

		rel, err := filepath.Rel(g.Dir, path)
		if err != nil {
			return fmt.Errorf("rel %s %s: %w", g.Dir, path, err)
		}

		if rel == "_index.md" {
			// Always keep the root `_index.md` file.
			return nil
		}

		if _, found := g.generatedFiles[rel]; found {
			return nil
		}

		if err := os.Remove(path); err != nil {
			return fmt.Errorf("rm %s: %w", path, err)
		}

		return nil
	})
}

func init() {
	funcs := template.FuncMap{
		"packageName": packageName,
		"render":      render,
		"safe":        func(s string) template.HTML { return template.HTML(s) },
		"tabIndent":   tabIndent,
		"trim":        func(s template.HTML) template.HTML { return template.HTML(strings.TrimSpace(string(s))) },
	}

	templates = template.Must(template.New("").
		Funcs(funcs).
		ParseFS(templateFS, "*.tmpl"))
}
