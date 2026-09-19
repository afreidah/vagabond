// -------------------------------------------------------------------------------
// Configuration Discovery
//
// Author: Alex Freidah
//
// Where configuration comes from when nobody said. A flag beats an environment
// variable beats a search path, which is the order every HashiCorp tool uses
// and the one an operator already has in their fingers.
//
// A path that was named explicitly and does not exist is an error rather than a
// fallthrough. Someone who passed -config meant that file, and silently ranking
// against a different set of providers than they asked for is the worst thing
// this package could do.
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

// EnvConfig names the configuration file or directory, the way NOMAD_ADDR and
// VAULT_ADDR are named.
const EnvConfig = "VAGABOND_CONFIG"

// Extension is what a configuration file is called inside a directory.
const Extension = ".hcl"

// The search path, tried in order when neither a flag nor the environment says
// otherwise.
//
// The working directory comes first so that a repository can carry the
// providers its own jobs expect. The system directory comes last, and is where
// a daemon's configuration will live once there is a daemon.
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

// Discover returns the configuration path to load.
//
// The flag wins, then the environment, then the first entry on the search path
// that exists. A flag or environment variable naming something absent fails
// rather than falling through, because a caller who named a file meant that
// file.
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

// SearchPath returns the locations Discover tries, in order.
//
// Exported so that the error a command prints and the paths it actually tried
// cannot drift apart.
func SearchPath() []string {
	paths := []string{workingDirConfig}

	// Absent or unreadable is not worth reporting: it only means this location
	// contributes nothing, and the caller is about to be told every path that
	// was tried anyway.
	if dir, err := os.UserConfigDir(); err == nil {
		paths = append(paths, filepath.Join(dir, userConfig))
	}

	return append(paths, systemConfig)
}

// stat confirms a named path exists, naming who named it.
//
// Cleaned before use so that the path reported back is the one that was
// checked. Nothing here is a sandbox boundary: an operator who runs this
// command can already read their own files, and naming the configuration to
// load is the entire purpose of the flag.
func stat(path, source string) (string, error) {
	clean := filepath.Clean(path)

	if _, err := os.Stat(clean); err != nil {
		return "", fmt.Errorf("%w: %s names %s, which cannot be read: %w",
			ErrNotFound, source, path, err)
	}

	return clean, nil
}
