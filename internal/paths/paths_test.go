package paths_test

import (
	"path/filepath"
	"testing"

	"github.com/codesweep-ai/npmrevs/internal/paths"
)

func env(m map[string]string) paths.Getenv { return func(k string) string { return m[k] } }

func TestDataPrecedence(t *testing.T) {
	cases := []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{"CS_NPMREVS_DATA": "/x", "XDG_DATA_HOME": "/y", "HOME": "/h"}, "/x"},
		{map[string]string{"XDG_DATA_HOME": "/y", "HOME": "/h"}, "/y/cs-npmrevs/data"},
		{map[string]string{"HOME": "/h"}, filepath.Join("/h", ".local", "share", "cs-npmrevs", "data")},
	}
	for _, c := range cases {
		got, err := paths.Data(env(c.env))
		if err != nil || got != c.want {
			t.Errorf("%v: %q %v, want %q", c.env, got, err, c.want)
		}
	}
}

func TestCachePrecedence(t *testing.T) {
	cases := []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{"CS_NPMREVS_CACHE": "/c", "XDG_CACHE_HOME": "/y"}, "/c"},
		{map[string]string{"XDG_CACHE_HOME": "/y"}, "/y/cs-npmrevs"},
		{map[string]string{"HOME": "/h"}, filepath.Join("/h", ".cache", "cs-npmrevs")},
	}
	for _, c := range cases {
		got, err := paths.Cache(env(c.env))
		if err != nil || got != c.want {
			t.Errorf("%v: %q %v, want %q", c.env, got, err, c.want)
		}
	}
}
