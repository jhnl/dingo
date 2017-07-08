package main

import (
	"fmt"

	"os"

	"github.com/jhnl/interpreter/common"
	"github.com/jhnl/interpreter/module"
)

func showErrors(oldErrors common.ErrorList, newError error, onlyFatal bool) bool {
	if newError == nil {
		return false
	}
	if errList, ok := newError.(*common.ErrorList); ok {
		oldErrors.Merge(errList)
		if onlyFatal && !oldErrors.IsFatal() {
			return false
		}
		errList.Sort()
		errList.Filter()
		for _, e := range errList.Errors {
			fmt.Printf("%s\n", e)
		}
	} else {
		fmt.Printf("%T\n", newError)
		fmt.Printf("[error] %s\n", newError)
	}
	return true
}

func exec(path string) {
	var errors common.ErrorList
	prog, err := module.Load(path)

	if showErrors(errors, err, false) {
		return
	}

	for _, mod := range prog.Modules {
		fmt.Println("Module", mod.Name.Literal)
		for _, file := range mod.Files {
			fmt.Println("  File", file.Path)
			for _, imp := range file.Imports {
				fmt.Println("    Import", imp.Literal.Literal)
			}
		}
	}

	/*	if err == nil {
			err = semantics.Check(mod)
		}


		fmt.Println(semantics.Print(mod))
		ip, code, mem := codegen.Compile(mod)

		fmt.Printf("Constants (%d):\n", len(mem.Constants))
		vm.DumpMemory(mem.Constants, os.Stdout)
		fmt.Printf("Globals (%d):\n", len(mem.Globals))
		vm.DumpMemory(mem.Globals, os.Stdout)
		fmt.Printf("\nCode (%d):\n", len(code))
		vm.Disasm(code, os.Stdout)
		fmt.Println()

		machine := vm.NewMachine(os.Stdout)
		machine.Exec(ip, code, mem)
		if machine.RuntimeError() {
			fmt.Println("Runtime error:", machine.Err)
		}*/
}

func main() {
	dir, err := os.Getwd()
	if err != nil {
		fmt.Println("failed to get working directory:", err)
		return
	}
	common.Cwd = dir

	exec("examples/modules/a.lang")
	//testVM()
}
