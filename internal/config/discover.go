// -------------------------------------------------------------------------------
// Configuration Discovery
//
// Author: Alex Freidah
//
// Where configuration comes from when nobody said: a flag beats an environment
// variable beats a search path. A path named explicitly that does not exist is
// an error rather than a fallthrough, since someone who passed -config meant
// that file and not some other set of providers.
// -------------------------------------------------------------------------------

package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// -------------------------------------------------------------------------
// LOCATIONS
// -------------------------------------------------------------------------

// EnvConfig names the configuration file or directory.
const EnvConfig = "VAGABOND_CONFIG"

// Extension is what a configuration file is called inside a directory.
const Extension = ".hcl"

// The search path, tried in order when neither a flag nor the environment says
// otherwise. The working directory comes first so a repository can carry the
// providers its own jobs expect; the system directory comes last.
const (
	workingDirConfig = "vagabond.hcl"
	userConfig       = "vagabond/config.hcl"
	systemConfig     = "/etc/vagabond.d"
)

// ErrNoConfig reports that nothing was found anywhere on the search path.
var ErrNoConfig = errors.New("no configuration found")

// ErrNotFound reports that a path someone named does not exist.
var ErrNotFound = errors.New("configuration not found")

// -------------------------------------------------------------------------
// DISCOVERY
// -------------------------------------------------------------------------

// Discover returns the configuration path to load: the flag, then the
// environment, then the first search path entry that exists.
func Discover(flagPath string) (string, error) {
	if flagPath != "" {
		return stat(flagPath, "-config")
	}

	if envPath := os.Getenv(EnvConfig); envPath != "" {
		return stat(envPath, EnvConfig)
	}

	candidates := SearchPath()

	for _, path := range candidates {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}

	return "", fmt.Errorf("%w. Looked in %s. Pass -config or set %s",
		ErrNoConfig, strings.Join(candidates, ", "), EnvConfig)
}

// SearchPath returns the locations Discover tries, in order, so the paths an
// error prints cannot drift from the paths tried.
func SearchPath() []string {
	paths := []string{workingDirConfig}

	// No user config dir only means this location contributes nothing
	if dir, err := os.UserConfigDir(); err == nil {
		paths = append(paths, filepath.Join(dir, userConfig))
	}

	return append(paths, systemConfig)
}

// stat confirms a named path exists, naming who named it. The path is cleaned
// so the one reported is the one checked; this is not a sandbox boundary.
func stat(path, source string) (string, error) {
	clean := filepath.Clean(path)

	if _, err := os.Stat(clean); err != nil { //nolint:gosec // the operator named this path
		return "", fmt.Errorf("%w: %s names %s, which cannot be read: %w",
			ErrNotFound, source, path, err)
	}

	return clean, nil
}
