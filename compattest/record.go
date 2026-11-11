package compattest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/bits"
	"os"
	"runtime"
	"sort"
	"sync"

	"github.com/moby/klauspost-compress/zstd"
)

// Results is the recorded output of all cases.
type Results struct {
	// Source identifies what produced the results, for example
	// "github.com/klauspost/compress@v1.20.2 h1:...".
	Source string `json:"source"`
	// Inputs maps each input name to the sha256 of its content. A change here
	// means the corpus changed, not the encoder.
	Inputs map[string]string `json:"inputs"`
	Cases  map[string]Result `json:"cases"`
}

// Result is the output of one case.
type Result struct {
	Len    int    `json:"len"`
	SHA256 string `json:"sha256,omitempty"`
	PadLen int    `json:"pad_len,omitempty"`
	// Invalid is set when the output did not decode to the input. Its bytes are not recorded.
	Invalid bool   `json:"invalid,omitempty"`
	Error   string `json:"error,omitempty"`
}

func hash(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Record encodes every case. extra options are added to every encoder.
func Record(corpusDir string, extra ...zstd.EOption) (*Results, error) {
	c, err := LoadCorpus(corpusDir)
	if err != nil {
		return nil, err
	}
	res := &Results{Inputs: map[string]string{}, Cases: map[string]Result{}}
	for name, b := range c.Inputs {
		res.Inputs[name] = hash(b)
	}
	res.Inputs[dictFile] = hash(c.Dict)

	dec, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(1<<31),
		zstd.WithDecoderDicts(c.Dict), zstd.WithDecoderDictRaw(rawDictID, c.RawDict))
	if err != nil {
		return nil, err
	}
	defer dec.Close()

	cases := Cases(c)
	results := make([]Result, len(cases))
	errs := make([]error, len(cases))
	var wg sync.WaitGroup
	work := make(chan int)
	var decMu sync.Mutex
	// Cases with default concurrency use GOMAXPROCS encoders each: on 32-bit, encode one
	// case at a time to stay within the address space.
	workers := max(1, runtime.GOMAXPROCS(0)/4)
	if bits.UintSize == 32 {
		workers = 1
	}
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				cs := cases[i]
				out, padLen, want, err := cs.Encode(c, extra...)
				if err != nil {
					errs[i] = fmt.Errorf("%s: %w", cs.Name, err)
					continue
				}
				decMu.Lock()
				got, derr := dec.DecodeAll(out, nil)
				decMu.Unlock()
				r := Result{Len: len(out), PadLen: padLen}
				if derr != nil || !bytes.Equal(got, want) {
					r.Invalid = true
					if derr != nil {
						r.Error = derr.Error()
					}
				} else {
					r.SHA256 = hash(out)
				}
				results[i] = r
			}
		}()
	}
	for i := range cases {
		work <- i
	}
	close(work)
	wg.Wait()
	for i, cs := range cases {
		if errs[i] != nil {
			return nil, errs[i]
		}
		if _, dup := res.Cases[cs.Name]; dup {
			return nil, fmt.Errorf("duplicate case %s", cs.Name)
		}
		res.Cases[cs.Name] = results[i]
	}
	return res, nil
}

// Load reads results written by Save.
func Load(path string) (*Results, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r Results
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &r, nil
}

// Save writes the results as JSON with sorted keys.
func (r *Results) Save(path string) error {
	b, err := json.MarshalIndent(r, "", "\t")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// Diff lists the differences of got from want. Cases that are invalid in want
// are only checked for presence: their output was not decodable, so it is not
// reproduced.
func Diff(want, got *Results) []string {
	var d []string
	for _, k := range keys(want.Inputs, got.Inputs) {
		if want.Inputs[k] != got.Inputs[k] {
			d = append(d, fmt.Sprintf("input %s: sha256 %q, want %q", k, got.Inputs[k], want.Inputs[k]))
		}
	}
	for _, k := range keys(want.Cases, got.Cases) {
		w, wok := want.Cases[k]
		g, gok := got.Cases[k]
		switch {
		case !wok:
			d = append(d, fmt.Sprintf("case %s: not in want", k))
		case !gok:
			d = append(d, fmt.Sprintf("case %s: missing", k))
		case w.Invalid:
		case g != w:
			d = append(d, fmt.Sprintf("case %s: got len %d sha256 %s pad %d invalid %v, want len %d sha256 %s pad %d",
				k, g.Len, g.SHA256, g.PadLen, g.Invalid, w.Len, w.SHA256, w.PadLen))
		}
	}
	return d
}

func keys[V any](a, b map[string]V) []string {
	m := map[string]bool{}
	for k := range a {
		m[k] = true
	}
	for k := range b {
		m[k] = true
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
