package compattest_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/moby/klauspost-compress/compattest"
	"github.com/moby/klauspost-compress/zstd"
	"github.com/stretchr/testify/require"
)

const corpus = "testdata/corpus"

// levels lists every EncoderCompat with the upstream release whose output it reproduces.
// testdata/golden/<release>.json is generated from that release by _gen/gen.sh --upstream.
// While a release is being integrated, the golden of its new level is generated from the
// upstream commit integrated last (a pseudo-version); the sync replaces it at the release.
var levels = []struct {
	compat  zstd.EncoderCompat
	release string
}{
	{zstd.EncoderCompatV1_20_2, "v1.20.2"},
	{zstd.EncoderCompatV1_20_3, "v1.20.3"},
}

var pseudoSource = regexp.MustCompile(`^github\.com/klauspost/compress@v[0-9]+\.[0-9]+\.[0-9]+-0\.[0-9]{14}-[0-9a-f]{12} h1:`)

func golden(t *testing.T, i int) *compattest.Results {
	t.Helper()
	release := levels[i].release
	r, err := compattest.Load(filepath.Join("testdata", "golden", release+".json"))
	require.NoError(t, err)
	if strings.HasPrefix(r.Source, "github.com/klauspost/compress@"+release+" h1:") {
		return r
	}
	require.True(t, i == len(levels)-1 && pseudoSource.MatchString(r.Source),
		"golden %s was not generated from the upstream release: source %q", release, r.Source)
	return r
}

func requireSame(t *testing.T, want, got *compattest.Results) {
	t.Helper()
	d := compattest.Diff(want, got)
	if len(d) == 0 {
		return
	}
	if len(d) > 30 {
		d = append(d[:30], fmt.Sprintf("... %d more", len(d)-30))
	}
	require.FailNow(t, "output differs from "+want.Source, strings.Join(d, "\n"))
}

func TestLevels(t *testing.T) {
	for i, l := range levels {
		require.Equal(t, zstd.EncoderCompat(i+1), l.compat, "levels must be listed in order without gaps")
	}
	require.Equal(t, zstd.EncoderCompatCurrent, levels[len(levels)-1].compat, "the last level must be EncoderCompatCurrent")
	files, err := filepath.Glob(filepath.Join("testdata", "golden", "*.json"))
	require.NoError(t, err)
	var releases []string
	for _, l := range levels {
		releases = append(releases, filepath.Join("testdata", "golden", l.release+".json"))
	}
	require.ElementsMatch(t, releases, files, "every golden file needs a level and every level a golden file")

	// The level table in README.md lists exactly these levels.
	readme, err := os.ReadFile(filepath.Join("..", "README.md"))
	require.NoError(t, err)
	var want []string
	for _, l := range levels {
		want = append(want, fmt.Sprintf("zstd.EncoderCompatV%s` (%d)", strings.ReplaceAll(strings.TrimPrefix(l.release, "v"), ".", "_"), l.compat))
	}
	got := regexp.MustCompile("zstd\\.EncoderCompatV[0-9_]+` \\([0-9]+\\)").FindAllString(string(readme), -1)
	require.Equal(t, want, got, "README.md level table")
}

// TestGoldens checks that each compat level reproduces the output of its release.
func TestGoldens(t *testing.T) {
	if testing.Short() {
		t.Skip("slow")
	}
	for i, l := range levels {
		t.Run(l.release, func(t *testing.T) {
			want := golden(t, i)
			got, err := compattest.Record(corpus, zstd.WithEncoderCompat(l.compat))
			require.NoError(t, err)
			requireSame(t, want, got)
		})
	}
}

// TestDefault checks that the default output is the output of EncoderCompatCurrent.
func TestDefault(t *testing.T) {
	if testing.Short() {
		t.Skip("slow")
	}
	want := golden(t, len(levels)-1)
	got, err := compattest.Record(corpus)
	require.NoError(t, err)
	requireSame(t, want, got)
}
