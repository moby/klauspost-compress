// Package compattest records zstd encoder output for a fixed corpus and case matrix,
// so the output of github.com/moby/klauspost-compress can be compared with the output
// of upstream github.com/klauspost/compress releases.
//
// cases.go and record.go are also compiled against upstream releases by _gen/gen.sh,
// which rewrites the import path. They must only import the standard library and
// the zstd package, and only use zstd API that exists in every compared release.
package compattest

import (
	"archive/tar"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/moby/klauspost-compress/zstd"
)

// Files in the corpus directory. They are copies frozen for these tests.
var corpusFiles = []string{
	"Mark.Twain-Tom.Sawyer.txt",
	"dict-sample.bin",
	"e.txt",
	"gettysburg.txt",
	"html.txt",
	"pi.txt",
	"pngdata.bin",
	"sharnd.out",
}

const (
	dictFile  = "dict-d0.dict" // trained dictionary, compresses dict-sample.bin
	rawDictID = 0x6d6f6279
	padSize   = 4096
)

// flushRawLitPieces is written with a flush after each piece. With
// SpeedBetterCompression, releases before v1.20.4 write a stream they cannot
// decode (upstream #1299).
var flushRawLitPieces = []string{
	"ahbkkdaflabkbbecakeiaaddaaaaacjaajadbbfalbkmfaamalfkja",
	"mmiijagdaafcbaajcmjdbalcmgbcagafbijfakbabgcfdkcaeldgfik",
	"akecbajaaaaahfabgahc",
	"jkcbaemekbmibgaffdegkdlifhaabiaaca",
	"bcjihjimfaemceeamhkabgebaaacaaaafaaback",
}

// Corpus holds the inputs of all cases.
type Corpus struct {
	Inputs  map[string][]byte
	Dict    []byte // trained dictionary
	RawDict []byte // raw content dictionary
}

// LoadCorpus reads the corpus directory and generates the synthetic inputs.
func LoadCorpus(dir string) (*Corpus, error) {
	c := &Corpus{Inputs: map[string][]byte{}}
	for _, f := range corpusFiles {
		b, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			return nil, err
		}
		c.Inputs[f] = b
	}
	d, err := os.ReadFile(filepath.Join(dir, dictFile))
	if err != nil {
		return nil, err
	}
	c.Dict = d
	html := c.Inputs["html.txt"]
	c.RawDict = html

	c.Inputs["empty"] = []byte{}
	c.Inputs["one"] = []byte("x")
	c.Inputs["small100"] = html[:100]
	c.Inputs["zeros4m"] = make([]byte, 4<<20)
	c.Inputs["mixed12m"] = mixed(html, 12<<20)
	layer, err := layerTar(c.Inputs)
	if err != nil {
		return nil, err
	}
	c.Inputs["layer.tar"] = layer
	var pieces []byte
	for _, p := range flushRawLitPieces {
		pieces = append(pieces, p...)
	}
	c.Inputs["flushrawlit"] = pieces
	return c, nil
}

// rng is xorshift64*, fixed here so generated inputs never change.
type rng struct{ s uint64 }

func (r *rng) next() uint64 {
	r.s ^= r.s >> 12
	r.s ^= r.s << 25
	r.s ^= r.s >> 27
	return r.s * 2685821657736338717
}

func (r *rng) intn(n int) int { return int(r.next() % uint64(n)) }

func (r *rng) bytes(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(r.next() >> 56)
	}
	return b
}

// mixed interleaves prefixes of text with random runs.
func mixed(text []byte, size int) []byte {
	r := &rng{s: 1}
	out := make([]byte, 0, size+len(text))
	for len(out) < size {
		if r.intn(10) < 3 {
			out = append(out, r.bytes(1+r.intn(70000))...)
		} else {
			out = append(out, text[:1+r.intn(len(text))]...)
		}
	}
	return out[:size]
}

// code generates compressible binary data, like executable code: tokens from
// a small vocabulary with random immediates.
func code(r *rng, size int) []byte {
	vocab := make([][]byte, 256)
	for i := range vocab {
		vocab[i] = r.bytes(2 + r.intn(14))
	}
	out := make([]byte, 0, size+16)
	for len(out) < size {
		out = append(out, vocab[r.intn(len(vocab))]...)
		if r.intn(4) == 0 {
			out = append(out, r.bytes(4)...)
		}
	}
	return out[:size]
}

// layerTar builds a container-layer-like tar of about 24 MiB.
func layerTar(in map[string][]byte) ([]byte, error) {
	r := &rng{s: 2}
	text := in["Mark.Twain-Tom.Sawyer.txt"]
	files := []string{"e.txt", "html.txt", "pi.txt", "pngdata.bin", "sharnd.out", "gettysburg.txt"}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for i := range 200 {
		var data []byte
		switch i % 4 {
		case 0:
			start := r.intn(len(text))
			data = text[start:min(len(text), start+1+r.intn(200<<10))]
		case 1:
			data = code(r, 50<<10+r.intn(300<<10))
		case 2:
			data = in[files[(i/4)%len(files)]]
		case 3:
			data = r.bytes(1 + r.intn(20<<10))
		}
		hdr := &tar.Header{
			Typeflag: tar.TypeReg,
			Name:     fmt.Sprintf("layer/dir%02d/file%04d", i%10, i),
			Mode:     0o644,
			Size:     int64(len(data)),
			ModTime:  time.Unix(0, 0),
			Format:   tar.FormatUSTAR,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, err
		}
		if _, err := tw.Write(data); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Case is one encoding of one input.
type Case struct {
	Name  string
	Input string
	// Mode is how the input is written: "all" (EncodeAll), "write" (one Write),
	// "chunks" (64 KiB writes), "flush" (1000 byte writes, each flushed; Step changes the sizes),
	// "pieces" (flushRawLitPieces, each flushed), "readfrom" (ReadFrom),
	// "contentsize" (ResetContentSize, then one Write), "reuse" (one encoder:
	// a stream of html.txt, Reset, a stream of the input, then EncodeAll of it) or
	// "resetopts" (one encoder: a stream of html.txt, then streams of the input after
	// ResetWithOptions adding a raw dictionary, switching to the trained dictionary,
	// and deleting the dictionary while turning off the CRC).
	Mode string
	Opts []zstd.EOption
	// Pad adds WithEncoderPadding(padSize). The padding content is random, so the
	// result covers the output without it plus the padding length.
	Pad bool
	// Dict marks cases that need a dictionary decoder.
	Dict bool
	// Step overrides the write size of the "chunks" and "flush" modes.
	Step int
}

var levels = []zstd.EncoderLevel{zstd.SpeedFastest, zstd.SpeedDefault, zstd.SpeedBetterCompression, zstd.SpeedBestCompression}

// Cases returns the case matrix in a fixed order.
func Cases(c *Corpus) []Case {
	var cases []Case
	add := func(input, variant, level, mode string, opts ...zstd.EOption) *Case {
		cases = append(cases, Case{Name: input + "/" + variant + "/" + level, Input: input, Mode: mode, Opts: opts})
		return &cases[len(cases)-1]
	}
	c1 := zstd.WithEncoderConcurrency(1)
	c4 := zstd.WithEncoderConcurrency(4)
	base := []string{
		"Mark.Twain-Tom.Sawyer.txt", "e.txt", "empty", "gettysburg.txt", "html.txt", "layer.tar",
		"mixed12m", "one", "pi.txt", "pngdata.bin", "sharnd.out", "small100", "zeros4m",
	}
	optInputs := []string{"Mark.Twain-Tom.Sawyer.txt", "html.txt", "mixed12m", "small100"}
	for _, l := range levels {
		ln := l.String()
		lo := zstd.WithEncoderLevel(l)
		for _, in := range base {
			add(in, "all", ln, "all", lo)
			add(in, "write-c1", ln, "write", lo, c1)
			add(in, "write-c4", ln, "write", lo, c4)
			// Default concurrency, like most callers.
			add(in, "chunks", ln, "chunks", lo)
			if len(c.Inputs[in]) <= 1<<20 {
				add(in, "flush", ln, "flush", lo)
			}
		}
		for _, in := range optInputs {
			add(in, "crc-off", ln, "write", lo, c1, zstd.WithEncoderCRC(false))
			add(in, "lowmem", ln, "write", lo, c1, zstd.WithLowerEncoderMem(true))
			add(in, "window1m", ln, "write", lo, c1, zstd.WithWindowSize(1<<20))
			add(in, "all-lit", ln, "write", lo, c1, zstd.WithAllLitEntropyCompression(true))
			add(in, "no-entropy", ln, "write", lo, c1, zstd.WithNoEntropyCompression(true))
			add(in, "single-segment", ln, "all", lo, zstd.WithSingleSegment(true))
			add(in, "pad-all", ln, "all", lo).Pad = true
			add(in, "pad-write", ln, "write", lo, c1).Pad = true
			add(in, "rawdict-all", ln, "all", lo, zstd.WithEncoderDictRaw(rawDictID, c.RawDict)).Dict = true
			add(in, "rawdict-flush", ln, "flush", lo, c1, zstd.WithEncoderDictRaw(rawDictID, c.RawDict)).Dict = true
			add(in, "reuse", ln, "reuse", lo, c1)
			add(in, "readfrom-c1", ln, "readfrom", lo, c1)
			add(in, "readfrom", ln, "readfrom", lo)
			add(in, "contentsize", ln, "contentsize", lo, c1)
			add(in, "resetopts", ln, "resetopts", lo, c1).Dict = true
		}
		add("empty", "zero-frames", ln, "write", lo, c1, zstd.WithZeroFrames(true))
		// Dictionary output depends on the concurrency: always set it.
		for _, cc := range []struct {
			name string
			opt  zstd.EOption
		}{{"c1", c1}, {"c4", c4}} {
			d := zstd.WithEncoderDict(c.Dict)
			add("dict-sample.bin", "dict-all-"+cc.name, ln, "all", lo, cc.opt, d).Dict = true
			add("dict-sample.bin", "dict-write-"+cc.name, ln, "write", lo, cc.opt, d).Dict = true
			add("dict-sample.bin", "dict-flush-"+cc.name, ln, "flush", lo, cc.opt, d).Dict = true
			// Concurrent blocks depend on the concurrency too. A 1 MiB window gives
			// 4 MiB jobs, so these inputs span several jobs.
			cb := zstd.WithConcurrentBlocks(true)
			add("mixed12m", "concurrent-blocks-window1m-"+cc.name, ln, "write", lo, cc.opt, cb, zstd.WithWindowSize(1<<20))
			add("layer.tar", "concurrent-blocks-window1m-"+cc.name, ln, "write", lo, cc.opt, cb, zstd.WithWindowSize(1<<20))
			add("Mark.Twain-Tom.Sawyer.txt", "concurrent-blocks-"+cc.name, ln, "write", lo, cc.opt, cb)
		}
		add("flushrawlit", "pieces", ln, "pieces", lo, c1)
	}
	// Numeric zstd levels, as callers such as BuildKit pass them.
	for zl := 1; zl <= 22; zl++ {
		add("html.txt", "all", fmt.Sprintf("zstd%02d", zl), "all", zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(zl)))
	}
	// BuildKit: zstd.NewWriter(dest) with an optional numeric level.
	add("layer.tar", "buildkit", "default", "write")
	for _, zl := range []int{1, 3, 9, 19} {
		add("layer.tar", "buildkit", fmt.Sprintf("zstd%02d", zl), "write", zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(zl)))
	}
	// estargz zstd:chunked: numeric level with lower memory.
	add("layer.tar", "estargz", "zstd03", "write", zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(3)), zstd.WithLowerEncoderMem(true))
	return append(cases, randomCases(c, base)...)
}

// randomCases returns option combinations drawn from a fixed seed, for combinations the
// matrix above does not list. Never change the seed or the draws: that changes the cases.
// Concurrency is always set (dictionary and concurrent-blocks output depend on it), and
// windows stay at or below 16 MiB.
func randomCases(c *Corpus, inputs []string) []Case {
	r := &rng{s: 3}
	chance := func(pct int) bool { return r.intn(100) < pct }
	windows := []int{1 << 10, 1 << 12, 1 << 15, 1 << 16, 1 << 17, 1 << 20, 1 << 22, 1 << 23, 1 << 24}
	var cases []Case
	for i := range 300 {
		in := inputs[r.intn(len(inputs))]
		// Large inputs cost the most: keep a few.
		for (in == "mixed12m" || in == "layer.tar") && !chance(25) {
			in = inputs[r.intn(len(inputs))]
		}
		var opts []zstd.EOption
		var level string
		if chance(70) {
			l := levels[r.intn(len(levels))]
			level = l.String()
			opts = append(opts, zstd.WithEncoderLevel(l))
		} else {
			zl := 1 + r.intn(22)
			level = fmt.Sprintf("zstd%02d", zl)
			opts = append(opts, zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(zl)))
		}
		opts = append(opts, zstd.WithEncoderConcurrency(1+r.intn(4)))
		modes := []string{"all", "write", "chunks", "readfrom", "contentsize", "reuse", "resetopts"}
		if len(c.Inputs[in]) <= 1<<20 {
			modes = append(modes, "flush")
		}
		cs := Case{Input: in, Mode: modes[r.intn(len(modes))]}
		switch cs.Mode {
		case "chunks":
			cs.Step = 1 + r.intn(256<<10)
		case "flush":
			cs.Step = 256 + r.intn(64<<10)
		}
		if chance(20) {
			opts = append(opts, zstd.WithEncoderCRC(false))
		}
		if chance(20) {
			opts = append(opts, zstd.WithLowerEncoderMem(true))
		}
		if chance(40) {
			opts = append(opts, zstd.WithWindowSize(windows[r.intn(len(windows))]))
		}
		if chance(15) {
			opts = append(opts, zstd.WithAllLitEntropyCompression(true))
		}
		if chance(10) {
			opts = append(opts, zstd.WithNoEntropyCompression(true))
		}
		if chance(10) {
			opts = append(opts, zstd.WithSingleSegment(true))
		}
		if chance(10) {
			opts = append(opts, zstd.WithZeroFrames(true))
		}
		switch d := r.intn(8); {
		case d == 0:
			opts = append(opts, zstd.WithEncoderDictRaw(rawDictID, c.RawDict))
			cs.Dict = true
		case d == 1:
			opts = append(opts, zstd.WithEncoderDict(c.Dict))
			cs.Dict = true
		case d == 2 && (cs.Mode == "write" || cs.Mode == "chunks" || cs.Mode == "readfrom"):
			opts = append(opts, zstd.WithConcurrentBlocks(true))
		}
		cs.Dict = cs.Dict || cs.Mode == "resetopts"
		// Padding is checked on a single frame; an empty EncodeAll gets none.
		cs.Pad = chance(10) && cs.Mode != "reuse" && cs.Mode != "resetopts" && len(c.Inputs[in]) > 0
		cs.Opts = opts
		cs.Name = fmt.Sprintf("%s/random%03d/%s", in, i, level)
		cases = append(cases, cs)
	}
	return cases
}

// Encode runs the case and returns the output, the padding length and the
// expected decoded output.
func (cs Case) Encode(c *Corpus, extra ...zstd.EOption) (out []byte, padLen int, want []byte, err error) {
	out, want, err = cs.encode(c, cs.Opts, extra)
	if err != nil || !cs.Pad {
		return out, 0, want, err
	}
	padded, _, err := cs.encode(c, append(cs.Opts[:len(cs.Opts):len(cs.Opts)], zstd.WithEncoderPadding(padSize)), extra)
	if err != nil {
		return nil, 0, nil, err
	}
	if len(padded) < len(out) || !bytes.Equal(padded[:len(out)], out) {
		return nil, 0, nil, errors.New("padded output does not start with the unpadded output")
	}
	if len(padded)%padSize != 0 {
		return nil, 0, nil, fmt.Errorf("padded output size %d is not a multiple of %d", len(padded), padSize)
	}
	if err := checkSkippable(padded[len(out):]); err != nil {
		return nil, 0, nil, err
	}
	return out, len(padded) - len(out), want, nil
}

// checkSkippable checks that b is a sequence of skippable frames.
func checkSkippable(b []byte) error {
	for len(b) > 0 {
		if len(b) < 8 || b[0]&0xf0 != 0x50 || b[1] != 0x2a || b[2] != 0x4d || b[3] != 0x18 {
			return errors.New("padding is not a skippable frame")
		}
		n := int(b[4]) | int(b[5])<<8 | int(b[6])<<16 | int(b[7])<<24
		if len(b) < 8+n {
			return errors.New("truncated skippable frame")
		}
		b = b[8+n:]
	}
	return nil
}

func (cs Case) step(def int) int {
	if cs.Step > 0 {
		return cs.Step
	}
	return def
}

func (cs Case) encode(c *Corpus, opts, extra []zstd.EOption) (out, want []byte, err error) {
	src, ok := c.Inputs[cs.Input]
	if !ok {
		return nil, nil, fmt.Errorf("unknown input %q", cs.Input)
	}
	opts = append(opts[:len(opts):len(opts)], extra...)
	var buf bytes.Buffer
	enc, err := zstd.NewWriter(&buf, opts...)
	if err != nil {
		return nil, nil, err
	}
	// Releases the encoder on error paths; the stream modes check Close themselves.
	defer func() { _ = enc.Close() }()
	write := func(p []byte, step int, flush bool) error {
		// An empty input is still written once.
		for i := 0; i == 0 || i < len(p); i += step {
			if _, err := enc.Write(p[i:min(len(p), i+step)]); err != nil {
				return err
			}
			if flush {
				if err := enc.Flush(); err != nil {
					return err
				}
			}
		}
		return nil
	}
	switch cs.Mode {
	case "all":
		return enc.EncodeAll(src, nil), src, nil
	case "write":
		err = write(src, max(1, len(src)), false)
	case "chunks":
		err = write(src, cs.step(64<<10), false)
	case "flush":
		err = write(src, cs.step(1000), true)
	case "pieces":
		for _, p := range flushRawLitPieces {
			if err = write([]byte(p), len(p), true); err != nil {
				break
			}
		}
	case "readfrom":
		if _, err = enc.ReadFrom(bytes.NewReader(src)); err != nil {
			break
		}
	case "contentsize":
		enc.ResetContentSize(&buf, int64(len(src)))
		err = write(src, max(1, len(src)), false)
	case "resetopts":
		html := c.Inputs["html.txt"]
		if err = write(html, len(html), false); err != nil {
			break
		}
		if err = enc.Close(); err != nil {
			break
		}
		out = append([]byte{}, buf.Bytes()...)
		want = append([]byte{}, html...)
		for _, opts := range [][]zstd.EOption{
			{zstd.WithEncoderDictRaw(rawDictID, c.RawDict)},
			{zstd.WithEncoderDict(c.Dict)},
			{zstd.WithEncoderDictDelete(), zstd.WithEncoderCRC(false)},
		} {
			var next bytes.Buffer
			if err = enc.ResetWithOptions(&next, opts...); err != nil {
				break
			}
			if err = write(src, max(1, len(src)), false); err != nil {
				break
			}
			if err = enc.Close(); err != nil {
				break
			}
			out = append(out, next.Bytes()...)
			want = append(want, src...)
		}
		if err != nil {
			break
		}
		return out, want, nil
	case "reuse":
		html := c.Inputs["html.txt"]
		if err = write(html, len(html), false); err != nil {
			break
		}
		if err = enc.Close(); err != nil {
			break
		}
		var second bytes.Buffer
		enc.Reset(&second)
		if err = write(src, max(1, len(src)), false); err != nil {
			break
		}
		if err = enc.Close(); err != nil {
			break
		}
		out = append(append(buf.Bytes(), second.Bytes()...), enc.EncodeAll(src, nil)...)
		return out, append(append(append([]byte{}, html...), src...), src...), nil
	default:
		return nil, nil, fmt.Errorf("unknown mode %q", cs.Mode)
	}
	if err != nil {
		return nil, nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, nil, err
	}
	return buf.Bytes(), src, nil
}
