package cliflow

import (
	"fmt"
	"io"

	"github.com/tdwgm/tuta-alias/internal/config"
	"github.com/tdwgm/tuta-alias/internal/consts"
	"github.com/tdwgm/tuta-alias/internal/logx"
	"github.com/tdwgm/tuta-alias/internal/update"
)

// Update checks for a newer build of the ref in UPDATE_REF and, unless
// checkOnly, builds it and replaces the installed binary.
//
// There is no release to download: the repository is private and has no
// release pipeline, so an update is a rebuild from the ref using the git
// credentials and the Go toolchain the machine already has. internal/update
// says so plainly when either is missing rather than failing halfway.
func Update(stdout, stderr io.Writer, checkOnly bool) int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	st, err := update.Check(cfg.UpdateRef)
	if err != nil {
		fmt.Fprintln(stderr, err)
		logx.Errorf("update check: %v", err)
		return 2
	}
	fmt.Fprintf(stdout, "Installed: %s\n", localDescription())
	fmt.Fprintf(stdout, "Ref:       %s (%s)\n", st.Ref, short(st.Remote))
	switch {
	case st.Err != "":
		// The check worked but cannot answer the question, which is not the
		// same as a failure: say why and let the user decide.
		fmt.Fprintf(stdout, "Status:    %s\n", st.Err)
		if checkOnly {
			return 1
		}
	case !st.Newer:
		fmt.Fprintln(stdout, "Status:    this is the build the ref points at")
		return 0
	default:
		// Two commits and no history to compare them with: ls-remote says
		// what the ref points at, not whether it is ahead. On a feature
		// branch the ref is usually BEHIND, and calling that "newer" would
		// talk people into downgrading.
		fmt.Fprintln(stdout, "Status:    the ref points at a different build (update switches to it)")
	}
	if checkOnly {
		return 1
	}

	if err := update.Available(); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	dst := cfg.BinaryPath()
	fmt.Fprintf(stdout, "\nBuilding %s@%s and replacing %s\n\n", consts.ModulePath, st.Ref, dst)
	if err := update.Install(st.Ref, dst, stdout); err != nil {
		fmt.Fprintln(stderr, err)
		logx.Errorf("update install: %v", err)
		return 2
	}
	logx.Infof("updated to %s@%s", st.Ref, short(st.Remote))
	fmt.Fprintf(stdout, "\nReplaced %s. Run `tuta-alias version` to confirm.\n", dst)
	return 0
}

// localDescription is the running version with its commit, or a plain version
// when the build did not stamp one.
func localDescription() string {
	if consts.CommitHash() == "" {
		return consts.AppVersion() + " (built without a commit stamp)"
	}
	return consts.AppVersion() + " (" + consts.CommitHash() + ")"
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	if sha == "" {
		return "unknown"
	}
	return sha
}
