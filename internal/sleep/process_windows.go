//go:build windows

package sleep

import (
	"os/exec"
	"strings"
)

// DefaultProcessLister lists running image names using tasklist. Only the image name is
// read, never a command line.
func DefaultProcessLister() ProcessLister { return windowsProcessLister{} }

type windowsProcessLister struct{}

func (windowsProcessLister) Running() ([]string, error) {
	out, err := exec.Command("tasklist", "/FO", "CSV", "/NH").Output()
	if err != nil {
		return nil, err
	}
	var names []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if len(line) < 2 || line[0] != '"' {
			continue
		}
		end := strings.IndexByte(line[1:], '"')
		if end < 0 {
			continue
		}
		names = append(names, line[1:1+end])
	}
	return names, nil
}
