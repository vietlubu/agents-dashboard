// Package version carries the build version, injected at build time with
// -ldflags "-X github.com/vietlubu/agents-dashboard/internal/version.Version=vYY.MM.DD.NNN".
package version

import (
	"fmt"
	"strings"
	"time"
)

// Version is the application version. The default marks an unstamped development build.
var Version = "dev"

// Compare validates release versions before comparing their fixed-width fields.
// A single leading v is optional; development builds are not releases.
func Compare(a, b string) (int, error) {
	validate := func(v string) (string, error) {
		s := strings.TrimPrefix(v, "v")
		if len(s) != 12 || s[2] != '.' || s[5] != '.' || s[8] != '.' {
			return "", fmt.Errorf("invalid release version %q", v)
		}
		for i := range len(s) {
			if i != 2 && i != 5 && i != 8 && (s[i] < '0' || s[i] > '9') {
				return "", fmt.Errorf("invalid release version %q", v)
			}
		}
		if _, err := time.Parse("2006.01.02", "20"+s[:8]); err != nil || s[9:] == "000" {
			return "", fmt.Errorf("invalid release version %q", v)
		}
		return s, nil
	}
	a, err := validate(a)
	if err != nil {
		return 0, err
	}
	b, err = validate(b)
	if err != nil {
		return 0, err
	}
	return strings.Compare(a, b), nil
}
