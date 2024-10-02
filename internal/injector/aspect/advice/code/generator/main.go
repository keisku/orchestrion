// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2023-present Datadog, Inc.

package main

import (
	"flag"
	"fmt"
	"go/types"
	"log"
	"os"
	"strings"

	"github.com/dave/jennifer/jen"
	"golang.org/x/tools/go/packages"
)

func main() {
	var filename string
	flag.StringVar(&filename, "o", "/dev/null", "output file to produce")
	flag.Parse()

	pkgs, err := packages.Load(&packages.Config{Mode: packages.LoadTypes}, "github.com/dave/dst")
	if err != nil {
		log.Fatalf("Failed to load type information for github.com/dave/dst: %v\n", err)
	}

	scope := pkgs[0].Types.Scope()
	dstNode, _ := scope.Lookup("Node").Type().(*types.Named).Underlying().(*types.Interface)

	file := jen.NewFile("code")
	file.HeaderComment("Unless explicitly stated otherwise all files in this repository are licensed")
	file.HeaderComment("under the Apache License Version 2.0.")
	file.HeaderComment("This product includes software developed at Datadog (https://www.datadoghq.com/).")
	file.HeaderComment("Copyright 2023-present Datadog, Inc.")
	file.HeaderComment("")
	file.HeaderComment(fmt.Sprintf("Code generated by %q; DO NOT EDIT.", "github.com/DataDog/orchestrion/internal/advice/code/generator "+strings.Join(os.Args[1:], " ")))

	proxyCases := make([]jen.Code, 0, scope.Len())
	for _, name := range scope.Names() {
		obj := scope.Lookup(name)
		if !obj.Exported() {
			continue
		}

		typ, ok := obj.Type().(*types.Named)
		if !ok || typ.Obj().IsAlias() {
			continue
		}
		def, ok := typ.Underlying().(*types.Struct)
		if !ok {
			continue
		}
		if !types.Implements(types.NewPointer(typ), dstNode) {
			continue
		}

		proxyName := "proxy" + name
		file.Type().Id(proxyName).Struct(
			jen.Op("*").Qual("github.com/dave/dst", name),
			jen.Id("placeholders").Op("*").Id("placeholders"),
		)

		file.Func().Params(
			jen.Id("p").Op("*").Id(proxyName),
		).Id("Copy").Params().String().Block(
			jen.Return().Id("p").Dot("placeholders").Dot("forNode").Call(
				jen.Id("p").Dot(name),
				jen.False(),
			),
		)

		file.Line().Func().Params(
			jen.Id("p").Op("*").Id(proxyName),
		).Id("String").Params().String().Block(
			jen.Return().Id("p").Dot("placeholders").Dot("forNode").Call(
				jen.Id("p").Dot(name),
				jen.True(),
			),
		)

		proxyCases = append(proxyCases, jen.Case(jen.Op("*").Qual("github.com/dave/dst", name)).Add(
			jen.Id("rv").Op("=").Op("&").Id(proxyName).Values(
				jen.Id("node"),
				jen.Id("placeholders"),
			),
		))

		// TODO: If the type is a [fmt.Stringer], we need to directly implement `String() string`, otherwise the one from
		// the embedded proxy will not be found (there would be a conflict).

		for i := 0; i < def.NumFields(); i++ {
			field := def.Field(i)
			if !field.Exported() {
				continue
			}

			proxyable := field.Type()

			var (
				arr, ptr   bool
				path, name string
			)
			switch ft := proxyable.(type) {
			case *types.Named:
				path = ft.Obj().Pkg().Path()
				name = ft.Obj().Name()
			case *types.Pointer:
				named, ok := ft.Elem().(*types.Named)
				if !ok {
					continue
				}
				ptr = true
				path = named.Obj().Pkg().Path()
				name = named.Obj().Name()
			case *types.Slice:
				named, ok := ft.Elem().(*types.Named)
				if !ok {
					continue
				}
				arr = true
				proxyable = named
				path = named.Obj().Pkg().Path()
				name = named.Obj().Name()
			default:
				continue
			}

			if !types.Implements(proxyable, dstNode) {
				continue
			}

			if !types.IsInterface(proxyable) {
				path = ""
				name = "proxy" + name
			}

			fnDef := file.Line().Func()
			fnDef = fnDef.Params(jen.Id("p").Op("*").Id(proxyName))
			fnDef = fnDef.Id(field.Name())
			fnDef = fnDef.Params()
			if ptr {
				fnDef = fnDef.Op("*")
			} else if arr {
				fnDef = fnDef.Index()
			}
			fnDef = fnDef.Qual(path, name)

			rt := jen.Qual(path, name)
			if ptr {
				rt = jen.Op("*").Add(rt)
			}

			if arr {
				fieldval := jen.Id("p").Dot(typ.Obj().Name()).Dot(field.Name())
				fnDef.Block(
					jen.If(fieldval.Clone().Op("==").Nil()).Block(jen.Return(jen.Nil())),
					jen.Id("res").Op(":=").Make(jen.Index().Add(rt), jen.Len(fieldval)),
					jen.For(jen.List(jen.Id("i"), jen.Id("node")).Op(":=").Range().Add(fieldval)).Block(
						jen.Id("res").Index(jen.Id("i")).Op("=").Id("newProxy").Types(rt).Call(
							jen.Id("node"),
							jen.Id("p").Dot("placeholders"),
						),
					),
					jen.Return(jen.Id("res")),
				)
			} else {
				fnDef.Block(
					jen.Return(jen.Id("newProxy").Types(rt).Call(
						jen.Id("p").Dot(typ.Obj().Name()).Dot(field.Name()),
						jen.Id("p").Dot("placeholders"),
					)),
				)
			}
		}
	}

	newProxy := file.Line().Func().Id("newProxy")
	newProxy = newProxy.Types(jen.Id("T").Id("any"))
	newProxy = newProxy.Params(
		jen.Id("node").Id("any"),
		jen.Id("placeholders").Op("*").Id("placeholders"),
	)
	newProxy = newProxy.Id("T")
	newProxy.BlockFunc(func(g *jen.Group) {
		cond := jen.Id("node").Op(":=").Id("node").Op(".").Call(jen.Type())
		g.Id("rv").Op(":=").Id("node")
		g.Switch(cond).Block(proxyCases...)
		g.Return(jen.Id("rv").Op(".").Call(jen.Id("T")))
	})

	if err := file.Save(filename); err != nil {
		log.Fatalf("Failed to write output file %q: %v\n", filename, err)
	}
}
