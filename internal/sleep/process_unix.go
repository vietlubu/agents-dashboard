//go:build !windows

package sleep

import (
	"os/exec"
	"strings"
)

// DefaultProcessLister lists running process command lines using ps(1), which is present
// on both macOS and Linux. Command lines rather than bare executable names, because an
// agent installed as a Node CLI runs as `node /path/to/claude/cli.js`; the token match in
// the monitor finds it there. Only the command line is read, never environment or files.
func DefaultProcessLister() ProcessLister { return unixProcessLister{} }

type unixProcessLister struct{}

func (unixProcessLister) Running() ([]string, error) {
	out, err := exec.Command("ps", "-axo", "command=").Output()
	if err != nil {
		return nil, err
	}
	var lines []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines, nil
}
