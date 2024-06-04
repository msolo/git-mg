// A Go version of the sample Perl script to trap modifications from
// watching file system notificatios.
//
// https://github.com/git/git/blob/master/templates/hooks--fsmonitor-watchman.sample
//
// Enable it via:
//
//	git config core.fsmonitor git-fsmonitor
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

type watchmanReply interface {
	Error() string
}

// watchman encodes all errors in JSON.
type wReply struct {
	Err string `json:"error"`
}

func (rep *wReply) Error() string {
	return rep.Err
}

func watchmanCmd(req interface{}, reply watchmanReply) error {
	reqData, err := json.Marshal(req)
	if err != nil {
		return err
	}

	cmd := exec.Command("watchman", "-j")
	cmd.Stdin = bytes.NewReader(reqData)

	replyData, err := cmd.Output()
	if err != nil {
		return err
	}

	if err := json.Unmarshal(replyData, reply); err != nil {
		return err
	}

	// all watchman error replies are also errors.
	if reply.Error() != "" {
		return reply
	}

	return nil
}

// git-fsmonitor <protocol> <timestamp_nanoseconds>
func main() {
	log.SetFlags(0)
	log.SetPrefix("git-fsmonitor: ")

	if len(os.Args) < 3 {
		log.Fatal("Not enough arguments: git-fsmonitor <protocol> <timestamp_nanoseconds>")
	}

	version := os.Args[1]
	if version != "1" {
		log.Fatalf("Unsupported fsmonitor hook version %s", version)
	}

	tsNs, err := strconv.ParseInt(os.Args[2], 0, 64)
	if err != nil {
		log.Fatalf("Timestamp cannot be parsed: %s", err)
	}
	// Watchman only has 1 second accuracy.
	// FIXME(msolo) Should we rewind one full second to catch edit races?
	ts := tsNs / 1e9

	// git changes the working dir before executing the hook.
	gitWorkdir, err := os.Getwd()
	if err != nil {
		log.Fatalf("Cannot get working directory: %s", err)
	}

	query := []interface{}{
		"query",
		gitWorkdir,
		map[string]interface{}{
			"fields": []interface{}{"name"},
			// Query only files and symlinks since git doesn't track directories.
			// Ignore transient files since the last timestamp.
			"expression": []interface{}{"allof",
				[]interface{}{"anyof", []interface{}{"type", "f"}, []interface{}{"type", "l"}},
				[]interface{}{"not", []interface{}{"allof", []interface{}{"since", ts, "cclock"}, []interface{}{"not", "exists"}}},
			},
			"since": ts,
		},
	}

	type queryReply struct {
		wReply          // handle error capture.
		Files  []string `json:"files"`
	}
	qReply := &queryReply{}
	err = watchmanCmd(query, qReply)

	// The first call to watchman always returns all files; emulate that by
	// telling git that everything is dirty in any error case.
	files := []string{"/"}
	if err != nil {
		if strings.Contains(err.Error(), "unable to resolve root") &&
			strings.HasSuffix(err.Error(), "is not watched") {
			watchProject := []interface{}{
				"watch-project",
				gitWorkdir,
			}
			reply := &wReply{}
			err = watchmanCmd(watchProject, reply)
			if err != nil {
				log.Fatalf("Failed to add project to watchman: %s", err)
			}
		} else {
			log.Fatalf("Unknown watchman error: %s", err)
		}
	} else {
		files = make([]string, 0, len(qReply.Files))
		for _, fname := range qReply.Files {
			// Only send information about the working directory, not git internals.
			if fname == ".git" || strings.HasPrefix(fname, ".git/") {
				continue
			}
			files = append(files, fname)
		}
	}

	fmt.Print(joinNullTerminated(files))
}

func joinNullTerminated(ss []string) string {
	if len(ss) == 0 {
		return ""
	}
	return strings.Join(ss, "\000") + "\000"
}
