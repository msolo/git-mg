/*

{
  // Comments are allowed, this is a JSONC file. See github.com/msolo/jsonc for more details.
  "triggers": [
    {
      "name": "gofmt-or-go-home", // A short name to disambiguate.
      "input_type": "args", // Specify that files are appended as arguments to the command.
      "cmd": ["gofmt", "-w"] // Run this command when files are matched.
      // TODO(msolo) Implement json, null-terminated and line-terminated options on stdin.
      "includes": ["*.go"], // Run on modified files that match the given glob. See fnmatch for more details.
      "excludes": ["vendor/*"] // Skip included files that match any of these globs. ** is not supported.
    }
  ]
}

*/
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path"
	"sort"
	"strings"

	"github.com/msolo/git-mg/gitapi"
	log "github.com/msolo/go-bis/glug"
	"github.com/msolo/jsonc"

	"github.com/posener/complete/v2"
	"github.com/posener/complete/v2/predict"
)

const (
	InputTypeArgs = "args"
)

// Define a command that will be executed when a relevant file changed.
type TriggerConfig struct {
	Name string
	Cmd  []string
	// Define how the changed files are passed to the command.
	InputType string
	Includes  []string
	Excludes  []string
}

// Config global include/exclude rules
type PreflightConfig struct {
	// Triggers are executed in order.
	// FIXME(msolo) specify how to run them in parallel? Or just rely on shell scripts underneath?
	Triggers []TriggerConfig
}

func readConfig(fname string) (*PreflightConfig, error) {
	f, err := os.Open(fname)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	cfg := &PreflightConfig{}
	dec := jsonc.NewDecoder(f)
	dec.DisallowUnknownFields()
	if err := dec.Decode(cfg); err != nil {
		return nil, err
	}
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func validateConfig(cfg *PreflightConfig) error {
	nameMap := make(map[string]bool)
	for _, t := range cfg.Triggers {
		if exists := nameMap[t.Name]; exists {
			return fmt.Errorf("duplicate trigger name: %s", t.Name)
		} else {
			nameMap[t.Name] = true
		}
		if err := validateTrigger(&t); err != nil {
			return err
		}
	}
	return nil
}

func validateTrigger(tr *TriggerConfig) error {
	// NOTE: Multiple keys with the same name is not an error in JSON, last value wins.
	if tr.Name == "" {
		return fmt.Errorf("empty trigger name")
	} else if strings.ContainsAny(tr.Name, " \t\r\n") {
		return fmt.Errorf("invalid trigger name containing whitespace: %q", tr.Name)
	}

	switch tr.InputType {
	case "args":
	default:
		return fmt.Errorf("invalid trigger input type %q for trigger %s", tr.InputType, tr.Name)
	}
	for _, pat := range tr.Includes {
		if _, err := path.Match(pat, ""); err != nil {
			return fmt.Errorf("invalid include pattern %q for trigger %s: %v", pat, tr.Name, err)
		}
	}

	for _, pat := range tr.Excludes {
		if _, err := path.Match(pat, ""); err != nil {
			return fmt.Errorf("invalid exclude pattern %q for trigger %s: %v", pat, tr.Name, err)
		}
	}
	return nil
}

// Match is similar to fnmatch.
// Patterns containing no / are only matched against the basename, unlike path.Match.
// Includes are applied first and then filtered by excludes.
// FIXME(msolo) Incorporate ideas from gitignore style matching like ** and ! ?
func match(tr *TriggerConfig, fname string) (bool, error) {
	for _, pat := range tr.Includes {
		matchName := fname
		if !strings.Contains(pat, "/") {
			matchName = path.Base(fname)
		}
		include, err := path.Match(pat, matchName)
		//fmt.Println("check fname", fname, "matchName", matchName, "pattern", pat, include)
		if err != nil {
			return false, err
		}
		if !include {
			continue
		}
		exclude := false
		for _, pat := range tr.Excludes {
			exclude, err = path.Match(pat, matchName)
			if err != nil {
				return false, err
			}
			if exclude {
				return false, nil
			}
		}
		return true, nil
	}
	return false, nil
}

func exitOnError(err error) {
	if err != nil {
		// log.Fatal and glug.Exit are about the same. glug.Fatal has a lot of stack litter.
		log.Exit(err)
	}
}

func isDir(fname string) bool {
	fi, err := os.Stat(fname)
	if err != nil {
		return false
	}
	return fi.IsDir()
}

func runPreflight() {
	triggerNames := flag.Args()

	gitWorkdir := gitapi.GitWorkdir()
	if err := os.Chdir(gitWorkdir); err != nil{
		log.Fatal(err)
	}

	if *verbose {
		os.Setenv("GIT_PREFLIGHT_VERBOSE", "1")
	}

	if *configFile == "" {
		*configFile = path.Join(gitWorkdir, ".git-preflight")
	}
	cfg, err := readConfig(*configFile)
	exitOnError(err)
	if *validate {
		return
	}

	var changedFiles []string
	if *commitHash != "" {
		changedFiles, err = gitapi.GetGitCommitChanges(gitWorkdir, *commitHash)
		exitOnError(err)
	} else {
		mergeBaseHash, err := gitapi.GetMergeBaseCommitHash(gitWorkdir)
		exitOnError(err)
		committedFiles, err := gitapi.GetGitDiffChanges(gitWorkdir, mergeBaseHash)
		exitOnError(err)
		unstagedFiles, err := gitapi.GetGitUnstagedChanges(gitWorkdir)
		exitOnError(err)
		stagedFiles, err := gitapi.GetGitStagedChanges(gitWorkdir)
		exitOnError(err)

		changedFileSet := make(map[string]bool, 64)
		for _, fnames := range [][]string{committedFiles, unstagedFiles, stagedFiles} {
			for _, fname := range fnames {
				changedFileSet[fname] = true
			}
		}
		changedFiles = stringSet2Slice(changedFileSet)
	}

	sort.Strings(changedFiles)

	changedDirSet := make(map[string]bool)
	for _, f := range changedFiles {
		dirName := path.Dir(f)
		if isDir(dirName) {
			changedDirSet[dirName] = true
		}
	}

	changedDirs := stringSet2Slice(changedDirSet)
	sort.Strings(changedDirs)

	log.Infof("changedFiles: %s\n", strings.Join(changedFiles, ", "))
	log.Infof("changedDirs: %s\n", strings.Join(changedDirs, ", "))

	cfgTriggerMap := make(map[string]*TriggerConfig)

	allTriggerNames := make([]string, 0, len(cfg.Triggers))
	for _, tr := range cfg.Triggers {
		cfgTriggerMap[tr.Name] = &tr
		allTriggerNames = append(allTriggerNames, tr.Name)
	}

	// If there are no explicit triggers, run them all.
	if len(triggerNames) == 0 {
		triggerNames = allTriggerNames
	}

	enabledTriggers := make(map[string]bool)
	for _, name := range triggerNames {
		if _, ok := cfgTriggerMap[name]; !ok {
			exitOnError(fmt.Errorf("no such trigger: %q", name))
		}
		enabledTriggers[name] = true
	}

	hasError := false
	// Iterate over triggers as configured to preserve execution order.
	for _, tr := range cfg.Triggers {
		if !enabledTriggers[tr.Name] {
			continue
		}

		fnames := make([]string, 0, len(changedFiles))
		for _, fname := range changedFiles {
			matched, err := match(&tr, fname)
			if err != nil {
				exitOnError(err)
			}
			if matched {
				fnames = append(fnames, fname)
			}
		}
		if len(fnames) == 0 {
			continue
		}

		if *verbose {
			fmt.Fprintf(os.Stderr, "run trigger %s: %s\n", tr.Name, strings.Join(fnames, ", "))
		}

		cmdArgs := make([]string, 0, len(tr.Cmd))
		cmdArgs = append(cmdArgs, tr.Cmd...)
		if tr.InputType == "args" {
			cmdArgs = append(cmdArgs, fnames...)
		} else {
			exitOnError(fmt.Errorf("invalid input type %q for trigger %q", tr.InputType, tr.Name))
		}

		if *dryRun {
			fmt.Fprintf(os.Stderr, "skipping %s: %s\n", tr.Name, strings.Join(gitapi.BashQuote(cmdArgs...), " "))
			continue
		}

		cmd := exec.Command(cmdArgs[0], cmdArgs[1:]...)
		cmd.Dir = gitWorkdir
		if err := cmd.Run(); err != nil {
			hasError = true
		}
	}

	if hasError {
		os.Exit(1)
	}
}

func stringSet2Slice(ss map[string]bool) []string {
	if len(ss) == 0 {
		return nil
	}
	sl := make([]string, 0, len(ss))
	for x := range ss {
		sl = append(sl, x)
	}
	return sl
}

type predictTrigger struct{}

// Predict a single valid name for a trigger.
func (*predictTrigger) Predict(prefix string) []string {
	gitWorkdir := gitapi.GitWorkdir()
	cfg, err := readConfig(path.Join(gitWorkdir, ".git-preflight"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "unable to complete: %s", err)
		return nil
	}

	names := make([]string, 0, len(cfg.Triggers))
	for _, t := range cfg.Triggers {
		if strings.HasPrefix(t.Name, prefix) {
			names = append(names, t.Name)
		}
	}
	sort.Strings(names)
	return names
}

var (
	// Add variables to the program. Since we are using the compflag library, we can pass options to
	// enable bash completion to the flag values.
	commitHash = flag.String("commit-hash", "", "Use a specific commit to generate a list of changed files.")
	configFile = flag.String("config-file", "", "Use the specified config file.")
	validate   = flag.Bool("validate", false, "Exit after validating the config.")
	verbose    = flag.Bool("v", false, "Print more debug data.")
	dryRun     = flag.Bool("dry-run", false, "Log the triggers and commands that would have been executed.")
)

var docPreamble = `git-preflight [-validate] [-config-file] [-v] [-dry-run] [-commit-hash] [<trigger name>, ...]

Run all triggers for all files changed with respect to the merge base:
  git-preflight

Run a specific trigger for all files changed with respect to the merge base:
	git-preflight <trigger name>

Setting GIT_TRACE_PERFORMANCE=1 or setting -log.level=INFO shows detailed performance logging.

The config file .git-preflight should be place in the root directory of the repository.

This is an annotated sample config that runs gofmt on all changed *.go files that aren't vendored.

{
  // Comments are allowed, this is a JSONC file. See github.com/msolo/jsonc for more details.
  "triggers": [
    {
      "name": "gofmt-or-go-home", // A short name to disambiguate.
      "input_type": "args", // Specify that files are appended as arguments to the command.
      "cmd": ["gofmt", "-w"] // Run this command when files are matched.
      // TODO(msolo) Implement json, null-terminated and line-terminated options on stdin.
      "includes": ["*.go"], // Run on modified files that match the given glob. See fnmatch for more details.
      "excludes": ["vendor/*"] // Skip included files that match any of these globs. ** is not supported.
    }
  ]
}
`

var docTrailer = `
Install bash completions by running:
  complete -C git-prefight git-preflight
`

func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage of git-preflight:\n\n%s\n", docPreamble)
		flag.PrintDefaults()
		fmt.Fprint(os.Stderr, docTrailer)
	}
	if val := os.Getenv("GIT_TRACE_PERFORMANCE"); val != "" && val != "0" {
		log.SetLevel("INFO")
	} else {
		log.SetLevel("WARNING")
	}

	log.RegisterFlags(flag.CommandLine)

	cmd := &complete.Command{
		Args: &predictTrigger{},
		Flags: map[string]complete.Predictor{
			"commit-hash": predict.Something,
			"config-file": predict.Files("*"),
			"validate":    predict.Nothing,
			"v":           predict.Nothing,
			"dry-run":     predict.Nothing,
			"log.level":   predict.Set([]string{"INFO", "WARNING", "ERROR"}),
		},
	}

	// Run the completion.
	cmd.Complete(os.Args[0])

	flag.Parse()

	runPreflight()
}
