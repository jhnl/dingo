package semantics

import (
	"fmt"

	"github.com/jhnl/dingo/internal/ir"
	"github.com/jhnl/dingo/internal/token"
)

func (v *typeChecker) visitModuleSet(set *ir.ModuleSet, signature bool) {
	set.ResetDeclColors()
	v.signature = signature
	for _, mod := range set.Modules {
		v.c.scope = mod.Scope
		for _, decl := range mod.Decls {
			// Nil symbol means a redeclaration
			if decl.Symbol() == nil {
				continue
			}
			v.visitTopDecl(decl)
		}
	}
	if v.signature {
		for _, mod := range set.Modules {
			for _, file := range mod.Files {
				for _, dep := range file.ModDeps {
					v.warnUnusedDirectives(dep.Directives)
				}
			}
		}
	}
}

func (v *typeChecker) visitDependencies(decl ir.TopDecl) {
	graph := *decl.DependencyGraph()
	for dep, node := range graph {
		if !node.Weak {
			v.visitTopDecl(dep)
		}
	}
}

func (v *typeChecker) setWeakDependencies(decl ir.TopDecl) {
	graph := *decl.DependencyGraph()

	switch decl.(type) {
	case *ir.FuncDecl:
		for k, v := range graph {
			sym := k.Symbol()
			if sym.ID == ir.FuncSymbol {
				v.Weak = true
			}
		}
	case *ir.ValTopDecl:
		for k, v := range graph {
			sym := k.Symbol()
			if sym.ID == ir.ValSymbol || sym.ID == ir.FuncSymbol {
				v.Weak = true
			}
		}
	case *ir.StructDecl:
		for _, v := range graph {
			weakCount := 0
			for i := 0; i < len(v.Links); i++ {
				link := v.Links[i]
				if link.Sym == nil || link.Sym.T == nil {
					weakCount++
				} else if link.IsType {
					t := link.Sym.T
					if t.ID() == ir.TPointer || t.ID() == ir.TSlice || t.ID() == ir.TFunc {
						weakCount++
					}
				}
			}
			if weakCount == len(v.Links) {
				v.Weak = true
			}
		}
	default:
		panic(fmt.Sprintf("Unhandled top decl %T", decl))
	}
}

func (v *typeChecker) visitTopDecl(decl ir.TopDecl) {
	if decl.Color() != ir.WhiteColor {
		return
	}

	v.c.pushTopDecl(decl)
	decl.SetColor(ir.GrayColor)
	ir.VisitDecl(v, decl)
	decl.SetColor(ir.BlackColor)
	v.c.popTopDecl()
}

func (v *typeChecker) VisitValTopDecl(decl *ir.ValTopDecl) {
	if v.signature {
		return
	}

	v.setWeakDependencies(decl)
	v.visitDependencies(decl)

	v.warnUnusedDirectives(decl.Directives)
	v.visitValDeclSpec(decl.Sym, &decl.ValDeclSpec, true)

	if !ir.IsUntyped(decl.Sym.T) && decl.Sym.ID != ir.ConstSymbol {
		init := decl.Initializer
		if !v.checkCompileTimeConstant(init) {
			v.c.error(init.Pos(), "top-level initializer must be a compile-time constant")
		}
	}
}

func (v *typeChecker) VisitValDecl(decl *ir.ValDecl) {
	if decl.Sym != nil {
		v.visitValDeclSpec(decl.Sym, &decl.ValDeclSpec, decl.Init())
	}
}

func (v *typeChecker) warnUnusedDirectives(directives []ir.Directive) {
	for _, dir := range directives {
		v.c.warning(dir.Name.Pos(), "unused directive '%s'", dir.Name.Literal)
	}
}

func (v *typeChecker) checkCABI(abi *ir.Ident) bool {
	if abi == nil {
		return false
	}
	if abi.Literal != ir.CABI {
		v.c.error(abi.Pos(), "unknown abi '%s'", abi.Literal)
		return false
	}
	return true
}

func (v *typeChecker) visitValDeclSpec(sym *ir.Symbol, decl *ir.ValDeclSpec, defaultInit bool) {
	if decl.Decl.OneOf(token.Const, token.Val) {
		sym.Flags |= ir.SymFlagReadOnly
	}

	var tdecl ir.Type

	if decl.Type != nil {
		decl.Type = v.visitType(decl.Type)
		tdecl = decl.Type.Type()

		if ir.IsIncompleteType(tdecl, nil) {
			v.c.error(decl.Type.Pos(), "incomplete type %s", tdecl)
			tdecl = ir.TBuiltinUntyped
		}

		if ir.IsUntyped(tdecl) {
			sym.T = tdecl
			return
		}
	}

	if decl.Initializer != nil {
		decl.Initializer = v.makeTypedExpr(decl.Initializer, tdecl)
		tinit := decl.Initializer.Type()

		if decl.Type == nil {
			if tdecl == nil {
				tdecl = tinit
			}
		} else {
			if ir.IsUntyped(tdecl) || ir.IsUntyped(tinit) {
				tdecl = ir.TBuiltinUntyped
			} else if !tdecl.Equals(tinit) {
				v.c.error(decl.Initializer.Pos(), "type mismatch %s and %s", tdecl, tinit)
				tdecl = ir.TBuiltinUntyped
			}
		}

		if decl.Decl.Is(token.Const) && !ir.IsUntyped(tdecl) {
			if !v.checkCompileTimeConstant(decl.Initializer) {
				v.c.error(decl.Initializer.Pos(), "const initializer must be a compile-time constant")
				tdecl = ir.TBuiltinUntyped
			}
		}
	} else if decl.Type == nil {
		v.c.error(decl.Name.Pos(), "missing type or initializer")
		tdecl = ir.TBuiltinUntyped
	} else if defaultInit {
		decl.Initializer = createDefaultLit(tdecl)
	}

	// Wait to set type until the final step in order to be able to detect cycles
	sym.T = tdecl

	if decl.Decl.Is(token.Const) && !ir.IsUntyped(tdecl) {
		v.c.constExprs[sym] = decl.Initializer
	}
}

func (v *typeChecker) VisitFuncDecl(decl *ir.FuncDecl) {
	if v.signature {
		c := v.checkCABI(decl.ABI)
		v.VisitIdent(decl.Name)

		defer setScope(setScope(v.c, decl.Scope))

		var params []ir.Field
		untyped := false

		for _, param := range decl.Params {
			v.VisitValDecl(param)
			tparam := ir.TBuiltinUntyped
			if param.Sym == nil || ir.IsUntyped(param.Sym.T) {
				untyped = true
			} else {
				tparam = param.Sym.T
			}
			params = append(params, ir.Field{Sym: param.Sym, T: tparam})
		}

		decl.Return.Type = v.visitType(decl.Return.Type)

		v.warnUnusedDirectives(decl.Directives)

		tret := decl.Return.Type.Type()
		if ir.IsUntyped(tret) {
			untyped = true
		}

		tfun := ir.TBuiltinUntyped

		if !untyped {
			tfun = ir.NewFuncType(params, tret, c)
			if decl.Sym.T != nil && !checkTypes(v.c, decl.Sym.T, tfun) {
				v.c.error(decl.Name.Pos(), "redeclaration of '%s' (previously declared with a different signature at %s)",
					decl.Name.Literal, decl.Sym.DeclPos)
			}
		}

		if decl.Sym.T == nil {
			decl.Sym.T = tfun
		}

		return
	} else if decl.SignatureOnly() {
		return
	}

	v.visitDependencies(decl)
	v.setWeakDependencies(decl)

	defer setScope(setScope(v.c, decl.Scope))
	v.VisitBlockStmt(decl.Body)

	endsWithReturn := false
	for i, stmt := range decl.Body.Stmts {
		if _, ok := stmt.(*ir.ReturnStmt); ok {
			if (i + 1) == len(decl.Body.Stmts) {
				endsWithReturn = true
			}
		}
	}

	if !endsWithReturn {
		if decl.Return.Type.Type().ID() != ir.TVoid {
			v.c.error(decl.Body.EndPos(), "missing return")
		} else {
			returnStmt := &ir.ReturnStmt{}
			returnStmt.SetRange(token.NoPosition, token.NoPosition)
			decl.Body.Stmts = append(decl.Body.Stmts, returnStmt)
		}
	}
}

func (v *typeChecker) VisitStructDecl(decl *ir.StructDecl) {
	if !v.signature {
		v.warnUnusedDirectives(decl.Directives)
		//decl.Sym.T = ir.NewIncompleteStructType(decl)
		return
	}

	v.visitDependencies(decl)

	untyped := false

	for _, field := range decl.Fields {
		v.VisitValDecl(field)
		if ir.IsUntyped(field.Sym.T) {
			untyped = true
		}
	}

	v.setWeakDependencies(decl)

	if untyped {
		decl.Sym.T = ir.TBuiltinUntyped
	} else {
		structt := decl.Sym.T.(*ir.StructType)
		structt.SetBody(decl)
	}
}