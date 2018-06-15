package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/jhnl/dingo/internal/backend"

	"github.com/jhnl/dingo/internal/semantics"

	"github.com/jhnl/dingo/internal/common"

	"github.com/jhnl/dingo/internal/ir"
	"github.com/jhnl/dingo/internal/token"

	"github.com/jhnl/dingo/internal/module"
)

func main() {
	var manifest string

	flag.StringVar(&manifest, "manifest", "", "Test manifest")
	flag.Parse()

	var groups []*testGroup
	tester := &testRunner{}

	if len(manifest) > 0 {
		groups = readTestManifest(manifest)
		tester.baseDir = filepath.Dir(manifest)
	} else {
		groups = createTestGroups(flag.Args())
	}

	tester.runTestGroups("", groups)
	fmt.Printf("\nFinished %d test(s)\n%s: %d %s: %d %s: %d %s: %d\n\n",
		tester.total, common.BoldGreen(statusSuccess.String()), tester.success,
		common.BoldYellow(statusSkip.String()), tester.skip, common.BoldRed(statusFail.String()), tester.fail,
		common.BoldRed(statusBad.String()), tester.bad)
}

type testRunner struct {
	baseDir string

	// stats
	total   int
	success int
	skip    int
	fail    int
	bad     int
}

type testGroup struct {
	Disable bool
	Dir     string
	Modules []string
	Tests   []string
}

type testOutput struct {
	pos  token.Position
	text string
}

type outputKind int

const (
	unknownOutput outputKind = iota
	exeOutput
	compilerOutput
)

type testResult struct {
	status status
	reason []string
}

func (r *testResult) addReason(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	r.reason = append(r.reason, msg)
}

type status int

const (
	statusSuccess status = iota
	statusFail
	statusSkip
	statusBad
)

func (t status) String() string {
	switch t {
	case statusSuccess:
		return common.BoldGreen("OK")
	case statusFail:
		return common.BoldRed("FAIL")
	case statusSkip:
		return common.BoldYellow("SKIP")
	case statusBad:
		return common.BoldRed("BAD")
	default:
		return "-"
	}
}

func abort(err error) {
	fmt.Println("error:", err)
	os.Exit(1)
}

func readTestManifest(manifest string) []*testGroup {
	bytes, err := ioutil.ReadFile(manifest)
	if err != nil {
		abort(err)
	}
	var groups []*testGroup
	err = json.Unmarshal(bytes, &groups)
	if err != nil {
		abort(err)
	}
	return groups
}

func createTestGroups(testFiles []string) []*testGroup {
	var groups []*testGroup
	for _, testFile := range testFiles {
		test := &testGroup{}
		test.Tests = append(test.Tests, testFile)
		test.Dir = ""
		groups = append(groups, test)
	}
	return groups
}

func toTestName(testDir string, testFile string) string {
	ext := filepath.Ext(testFile)
	baseName := filepath.Base(testFile)
	baseName = baseName[:len(baseName)-len(ext)]
	testName := filepath.Join(testDir, baseName)
	return testName
}

func (t *testRunner) runTestGroups(baseDir string, groups []*testGroup) {
	dots := 50
	for _, group := range groups {
		testDir := filepath.Join(baseDir, group.Dir)
		status := statusSuccess

		if group.Disable {
			status = statusSkip
		} else if len(group.Tests) == 0 {
			status = statusBad
		}

		if status != statusSuccess {
			if len(group.Tests) > 0 {
				for _, testFile := range group.Tests {
					testName := toTestName(testDir, testFile)
					fmt.Printf("TEST %s%s[%s]\n", testName, strings.Repeat(".", dots-len(testName)), status)
					t.updateStats(status)
				}
			} else {
				fmt.Printf("GROUP %s%s[%s]\n", testDir, strings.Repeat(".", dots-1-len(testDir)), status)
				t.updateStats(status)
			}
			continue
		}

		for _, testFile := range group.Tests {
			testName := toTestName(testDir, testFile)
			fmt.Printf("TEST %s%s", testName, strings.Repeat(".", dots-len(testName)))

			result := t.runTest(testName, testDir, testFile, group.Modules)
			t.updateStats(result.status)

			fmt.Printf("[%s]\n", result.status)
			for _, txt := range result.reason {
				fmt.Printf("  >> %s\n", txt)
			}
		}
	}
}

func (t *testRunner) updateStats(res status) {
	t.total++
	switch res {
	case statusSuccess:
		t.success++
	case statusFail:
		t.fail++
	case statusSkip:
		t.skip++
	case statusBad:
		t.bad++
	}
}

func (t *testRunner) runTest(testName string, testDir string, testFile string, testModules []string) *testResult {
	var filenames []string
	filenames = append(filenames, filepath.Join(t.baseDir, testDir, testFile))

	for _, mod := range testModules {
		filename := filepath.Join(t.baseDir, testDir, mod)
		filenames = append(filenames, filename)
	}

	var expectedCompilerOutput []*testOutput
	var expectedExeOutput []*testOutput
	errors := &common.ErrorList{}

	result := &testResult{status: statusSuccess}
	set, err := module.Load(filenames)

	if set != nil {
		if mod := set.FindModule("main"); mod != nil {
			file := mod.FindFileWithFQN("main")
			expectedCompilerOutput, expectedExeOutput = parseTestDescription(file.Comments, result)
		} else {
			result.status = statusFail
			result.addReason("no main module")
		}

		if result.status != statusSuccess {
			return result
		}
	}

	config := common.NewBuildConfig()
	config.Exe = filepath.Join(os.TempDir(), strings.Replace(testName, "/", "_", -1))

	if !addError(err, errors) {
		target := backend.NewLLVMTarget()
		err = semantics.Check(set, target)
		if !addError(err, errors) {
			err = backend.BuildLLVM(set, target, config)
			addError(err, errors)
		}
	}

	var compilerOutput []*testOutput

	errors.Sort()
	addCompilerOutput(errors.Warnings, &compilerOutput)
	addCompilerOutput(errors.Errors, &compilerOutput)
	compareOutput(expectedCompilerOutput, compilerOutput, result)

	var exeOutput []*testOutput

	if !errors.IsError() {
		cmd := exec.Command(config.Exe)
		bytes, err := cmd.CombinedOutput()
		if err != nil {
			result.addReason("internal error: %s", err)
		} else {
			addExeOutput(bytes, &exeOutput)
		}
	}

	compareOutput(expectedExeOutput, exeOutput, result)

	if len(result.reason) > 0 {
		result.status = statusFail
	}

	os.Remove(config.Exe)

	return result
}

func addError(newError error, errors *common.ErrorList) bool {
	if newError == nil {
		return false
	}
	errors.AddGeneric1(newError)
	return errors.IsError()
}

func addCompilerOutput(errors []*common.Error, output *[]*testOutput) {
	for _, err := range errors {
		pos := err.Pos

		msg := fmt.Sprintf("%s(%d): %s", err.ID, pos.Line, err.Msg)
		*output = append(*output, &testOutput{pos: pos, text: msg})

		for _, line := range err.Context {
			*output = append(*output, &testOutput{pos: pos, text: line})
		}
	}
}

func addExeOutput(bytes []byte, output *[]*testOutput) {
	tmp := strings.Split(string(bytes), "\n")
	for i, line := range tmp {
		line = strings.TrimSpace(line)
		if len(line) > 0 {
			pos := token.Position{Line: i + 1, Column: 1}
			*output = append(*output, &testOutput{pos: pos, text: line})
		}
	}
}

func compareOutput(expectedOutput []*testOutput, actualOutput []*testOutput, result *testResult) {
	expectedIdx := 0
	actualIdx := 0
	regexPrefix := "re:"

	for ; expectedIdx < len(expectedOutput); expectedIdx++ {
		expected := expectedOutput[expectedIdx]

		if actualIdx >= len(actualOutput) {
			break
		}

		actual := actualOutput[actualIdx]
		actualIdx++
		match := true

		if strings.HasPrefix(expected.text, regexPrefix) {
			pattern := strings.TrimSpace(expected.text[len(regexPrefix):])
			regex, err := regexp.Compile(pattern)
			if err != nil {
				result.addReason("bad regex: %s: %s", expected.pos, err)
			} else {
				found := regex.FindString(actual.text)
				match = found == actual.text
			}
		} else {
			match = expected.text == actual.text
		}

		if !match {
			result.addReason("%s(%s): %s", common.BoldGreen("expected"), expected.pos, expected.text)
			result.addReason("     %s(%s): %s", common.BoldRed("got"), actual.pos, actual.text)
		}
	}

	if actualIdx < len(actualOutput) {
		result.addReason("%s:", common.BoldGray("surplus"))
		for i := actualIdx; i < len(actualOutput); i++ {
			result.addReason("%s(%s): %s", common.BoldRed("got"), actualOutput[i].pos, actualOutput[i].text)
		}
	}

	if expectedIdx < len(expectedOutput) {
		result.addReason("%s:", common.BoldGray("deficit"))
		for i := expectedIdx; i < len(expectedOutput); i++ {
			result.addReason("%s(%s): %s", common.BoldGreen("expected"), expectedOutput[i].pos, expectedOutput[i].text)
		}
	}
}

var lineNumRegex *regexp.Regexp

func init() {
	var err error
	lineNumRegex, err = regexp.Compile("\\((?:\\+|-)?\\d+\\)")
	if err != nil {
		panic(err)
	}
}

func match(lit *string, prefix string) bool {
	if strings.HasPrefix(*lit, prefix) {
		(*lit) = (*lit)[len(prefix):]
		return true
	}
	return false
}

func parseTestDescription(comments []*ir.Comment, result *testResult) (compiler []*testOutput, exe []*testOutput) {
	for _, comment := range comments {
		// Only check single-line comments
		if comment.Tok.Is(token.Comment) {
			lit := comment.Literal[2:]
			lit = strings.TrimSpace(lit)

			if match(&lit, "expect-") {
				ok := false

				if match(&lit, "output:") {
					lit = strings.TrimSpace(lit)
					exe = append(exe, &testOutput{pos: comment.Pos, text: lit})
					ok = true
				} else if match(&lit, "dgc:") {
					lit = strings.TrimSpace(lit)
					compiler = append(compiler, &testOutput{pos: comment.Pos, text: lit})
					ok = true
				} else if match(&lit, "error") {
					lineNum := comment.Pos.Line

					rematch := lineNumRegex.FindString(lit)
					if len(rematch) > 0 {
						lit = lit[len(rematch):]
						rematch = rematch[1 : len(rematch)-1]

						res, _ := strconv.ParseInt(rematch, 10, 32)
						if strings.HasPrefix(rematch, "+") || strings.HasPrefix(rematch, "-") {
							lineNum += int(res)
						} else {
							lineNum = int(res)
						}
					}

					if match(&lit, ":") {
						lit = strings.TrimSpace(lit)
						lit = fmt.Sprintf("%s(%d): %s", common.ErrorMsg, lineNum, lit)
						compiler = append(compiler, &testOutput{pos: comment.Pos, text: lit})
						ok = true
					}
				}

				if !ok {
					result.status = statusBad
					result.addReason("bad test description at '%s'", comment.Pos)
				}
			}
		}
	}

	return compiler, exe
}