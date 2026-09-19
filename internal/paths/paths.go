// Package paths resolves where cs-npmrevs keeps files between runs: the data
// directory tarballs are fetched into and served from, and the cache that holds
// tarballs read out of images.
//
// Both follow the XDG base directory convention, and each has an environment
// variable that overrides it outright.
package paths

import (
	"errors"
	"os"
	"path/filepath"
)

// Getenv reads the environment. Tests replace it.
type Getenv func(string) string

// Data is the default data directory: $CS_NPMREVS_DATA, else
// $XDG_DATA_HOME/cs-npmrevs/data, else ~/.local/share/cs-npmrevs/data.
func Data(getenv Getenv) (string, error) {
	if d := getenv("CS_NPMREVS_DATA"); d != "" {
		return d, nil
	}
	return xdg(getenv, "XDG_DATA_HOME", filepath.Join(".local", "share"), "data")
}

// Cache is the cache directory: $CS_NPMREVS_CACHE, else $XDG_CACHE_HOME/cs-npmrevs,
// else ~/.cache/cs-npmrevs.
func Cache(getenv Getenv) (string, error) {
	if d := getenv("CS_NPMREVS_CACHE"); d != "" {
		return d, nil
	}
	return xdg(getenv, "XDG_CACHE_HOME", ".cache", "")
}

func xdg(getenv Getenv, variable, fallback, leaf string) (string, error) {
	base := getenv(variable)
	if base == "" {
		home := getenv("HOME")
		if home == "" {
			var err error
			if home, err = os.UserHomeDir(); err != nil || home == "" {
				return "", errors.New("no home directory to keep cs-npmrevs's files in; set " + variable)
			}
		}
		base = filepath.Join(home, fallback)
	}
	return filepath.Join(base, "cs-npmrevs", leaf), nil
}
