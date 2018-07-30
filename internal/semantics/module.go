package semantics

import (
	"bytes"
	"fmt"

	"github.com/jhnl/dingo/internal/ir"
	"github.com/jhnl/dingo/internal/token"
)

type module struct {
	name             *ir.Ident
	builtinScope     *ir.Scope
	scope            *ir.Scope
	sym              *ir.Symbol
	decls            []*ir.TopDecl
	fqn              string
	public           bool
	modParentIndex2  int
	fileParentIndex1 int
	fileParentIndex2 int
}

type moduleList struct {
	filename string
	mods     []*module
}

type moduleMatrix []moduleList

func (c *checker) createModuleMatrix(fileMatrix ir.FileMatrix) moduleMatrix {
	var modMatrix moduleMatrix
	for _, fileList := range fileMatrix {
		modList := c.createModuleList(fileList, len(modMatrix))
		modMatrix = append(modMatrix, modList)
	}
	return modMatrix
}

func (c *checker) createModuleList(fileList ir.FileList, CUID int) moduleList {
	mods := make([][]*module, len(fileList))

	for fileID, file := range fileList {
		mods2 := make([]*module, len(file.Modules))
		fileParts := fileFQNParts(fileList, fileID)
		for modID, incompleteMod := range file.Modules {
			var parts []string
			parts = append(parts, fileParts...)
			parts = append(parts, moduleFQNParts(file, modID)...)
			fqn := toFQN(parts)
			mod := &module{
				name:             incompleteMod.Name,
				fqn:              fqn,
				public:           incompleteMod.Visibility.Is(token.Public),
				modParentIndex2:  incompleteMod.ParentIndex,
				fileParentIndex1: file.ParentIndex1,
				fileParentIndex2: file.ParentIndex2,
			}
			mod.builtinScope = ir.NewScope(ir.BuiltinScope, builtinScope, CUID)
			mod.scope = ir.NewScope(ir.ModuleScope, mod.builtinScope, CUID)
			mod.decls = incompleteMod.Decls
			mods2[modID] = mod
		}
		mods[fileID] = mods2
	}

	modList := moduleList{filename: fileList[0].Filename}
	localMap := make(map[string]token.Position)

	for index1, fileMods := range mods {
		var fileModParents []*module
		root := mods[0][0]

		if index1 == 0 {
			// Parent of root module in root file is itself
			c.insertBuiltinModuleScopeSymbol(root, root, CUID, ir.SelfModuleName, token.NoPosition)
			c.insertBuiltinModuleScopeSymbol(root, root, CUID, ir.ParentModuleName, token.NoPosition)
			c.insertBuiltinModuleScopeSymbol(root, root, CUID, ir.RootModuleName, token.NoPosition)
			modList.mods = append(modList.mods, root)
			localMap[""] = token.NoPosition
		} else {
			// Move root module decls to module where the file was included
			parentIndex1 := fileMods[0].fileParentIndex1
			parentIndex2 := fileMods[0].fileParentIndex2
			for parentIndex1 != 0 && parentIndex2 == 0 {
				parent := mods[parentIndex1][0]
				parentIndex1 = parent.fileParentIndex1
				parentIndex2 = parent.fileParentIndex2
			}
			mod := mods[parentIndex1][parentIndex2]
			mod.decls = append(mod.decls, fileMods[0].decls...)
			// Create a list of file parent modules
			parentIndex1 = fileMods[0].fileParentIndex1
			parentIndex2 = fileMods[0].fileParentIndex2
			for parentIndex1 != 0 || parentIndex2 != 0 {
				if parentIndex2 != 0 {
					mod := mods[parentIndex1][parentIndex2]
					fileModParents = append(fileModParents, mod)
				}
				parent := mods[parentIndex1][parentIndex2]
				parentIndex1 = parent.fileParentIndex1
				parentIndex2 = parent.fileParentIndex2
			}
		}

		for index2 := 1; index2 < len(fileMods); index2++ {
			var modPath []*module
			mod := fileMods[index2]
			parentIndex2 := mod.modParentIndex2
			modPath = append(modPath, mod)
			for parentIndex2 != 0 {
				parent := fileMods[parentIndex2]
				parentIndex2 = parent.modParentIndex2
				modPath = append(modPath, parent)
			}
			modPath = append(modPath, fileModParents...)
			modPath = append(modPath, root)
			// Reverse order so top-most module is first
			for i, j := 0, len(modPath)-1; i < j; i, j = i+1, j-1 {
				tmp := modPath[i]
				modPath[i] = modPath[j]
				modPath[j] = tmp
			}
			if existing, ok := localMap[mod.fqn]; ok {
				c.errors.Add(mod.name.Pos(), "redefinition of private module '%s' (different definition is at '%s')", mod.fqn, existing)
			} else {
				// Ensure modpath has all entries.
				// If fqn of current module is foo.bar.baz, then bar is created in foo and baz is created in bar.
				for i := 0; i < len(modPath)-1; i++ {
					parent := modPath[i]
					child := modPath[i+1]
					if child.sym != nil {
						continue
					}
					sym := ir.NewSymbol(ir.ModuleSymbol, parent.scope, CUID, child.fqn, child.name.Literal, child.name.Pos())
					sym.Key = c.nextSymKey()
					sym.Public = child.public
					sym.Flags = ir.SymFlagDefined | ir.SymFlagReadOnly
					sym.T = ir.NewModuleType(sym, child.scope)
					child.sym = sym
					if existing := parent.scope.Insert(sym.Name, sym); existing != nil {
						panic(fmt.Sprintf("duplicate fqn %s", mod.fqn))
					}
				}

				mod = modPath[len(modPath)-1]
				parent := modPath[len(modPath)-2]

				c.insertBuiltinModuleScopeSymbol(mod, mod, CUID, ir.SelfModuleName, mod.name.Pos())
				c.insertBuiltinModuleScopeSymbol(mod, parent, CUID, ir.ParentModuleName, parent.name.Pos())
				c.insertBuiltinModuleScopeSymbol(mod, root, CUID, ir.RootModuleName, token.NoPosition)

				localMap[mod.fqn] = mod.name.Pos()
				modList.mods = append(modList.mods, mod)
				if mod.sym.Public {
					if existing, ok := c.importMap[mod.fqn]; ok {
						c.errors.Add(mod.sym.Pos, "redefinition of public module '%s' (different definition is at '%s')", mod.fqn, existing.Pos)
					} else {
						c.importMap[mod.fqn] = mod.sym
					}
				}
			}
		}
	}

	return modList
}

func (c *checker) insertBuiltinModuleScopeSymbol(mod *module, scopeMod *module, CUID int, name string, pos token.Position) *ir.Symbol {
	sym := ir.NewSymbol(ir.ModuleSymbol, mod.scope, CUID, scopeMod.fqn, name, pos)
	sym.Key = c.nextSymKey()
	sym.Flags = builtinSymFlags | ir.SymFlagReadOnly
	sym.T = ir.NewModuleType(sym, scopeMod.scope)
	if existing := mod.scope.Insert(name, sym); existing != nil {
		panic(fmt.Sprintf("fqn '%s' has duplicate symbols for builtin module '%s'", scopeMod.fqn, name))
	}
	return sym
}

func toFQN(parts []string) string {
	var buf bytes.Buffer
	for i, part := range parts {
		buf.WriteString(part)
		if (i + 1) < len(parts) {
			buf.WriteString(".")
		}
	}
	return buf.String()
}

func reverseFQNParts(parts []string) []string {
	var reversed []string
	for i := len(parts) - 1; i >= 0; i-- {
		reversed = append(reversed, parts[i])
	}
	return reversed
}

func fileFQNParts(fileList ir.FileList, index1 int) []string {
	var parts []string
	for index1 != 0 {
		file := fileList[index1]
		index2 := file.ParentIndex2
		index1 = file.ParentIndex1
		file = fileList[index1]
		for index2 != 0 {
			mod := file.Modules[index2]
			parts = append(parts, mod.Name.Literal)
			index2 = mod.ParentIndex
		}
	}
	return reverseFQNParts(parts)
}

func moduleFQNParts(file *ir.File, index2 int) []string {
	var parts []string
	for index2 != 0 {
		mod := file.Modules[index2]
		parts = append(parts, mod.Name.Literal)
		index2 = mod.ParentIndex
	}
	return reverseFQNParts(parts)
}
