// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2023-present Datadog, Inc.

package advice

import (
	gocontext "context"
	"fmt"

	"github.com/DataDog/orchestrion/internal/fingerprint"
	"github.com/DataDog/orchestrion/internal/injector/aspect/advice/code"
	"github.com/DataDog/orchestrion/internal/injector/aspect/context"
	"github.com/DataDog/orchestrion/internal/yaml"
	"github.com/dave/dst"
	"github.com/goccy/go-yaml/ast"
)

type wrapExpression struct {
	Template *code.Template
}

func WrapExpression(template *code.Template) *wrapExpression {
	return &wrapExpression{Template: template}
}

func (a *wrapExpression) Apply(ctx context.AdviceContext) (bool, error) {
	var (
		kve *dst.KeyValueExpr
		ok  bool
	)

	if kve, ok = ctx.Node().(*dst.KeyValueExpr); ok {
		ctx = ctx.Child(kve.Value, "Value", -1)
		defer ctx.Release()
	} else if _, ok = ctx.Node().(dst.Expr); !ok {
		return false, fmt.Errorf("wrap-expression: expected dst.Expr or *dst.KeyValueExpr, got %T", ctx.Node())
	}

	repl, err := a.Template.CompileExpression(ctx)
	if err != nil {
		return false, fmt.Errorf("wrap-expression: %w", err)
	}

	if kve == nil {
		ctx.ReplaceNode(repl)
	} else {
		kve.Value = repl
	}

	ctx.EnsureMinGoLang(a.Template.Lang)

	return true, nil
}

func (a *wrapExpression) Hash(h *fingerprint.Hasher) error {
	return h.Named("wrap-expression", a.Template)
}

func (a *wrapExpression) AddedImports() []string {
	return a.Template.AddedImports()
}

func init() {
	unmarshalers["wrap-expression"] = func(ctx gocontext.Context, node ast.Node) (Advice, error) {
		var template code.Template
		if err := yaml.NodeToValueContext(ctx, node, &template); err != nil {
			return nil, err
		}
		return WrapExpression(&template), nil
	}
}
