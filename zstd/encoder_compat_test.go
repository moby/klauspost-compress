// This file is specific to github.com/moby/klauspost-compress. See README.md.
// It uses the standard testing package like the rest of this package, so the root
// module keeps no test dependencies. The golden tests are in compattest.

package zstd

import (
	"bytes"
	"io"
	"testing"
)

// TestEncoderCompatPropagation checks that the compat level reaches every encoder
// and every block that encodes: pooled EncodeAll encoders, the stream encoder and
// the second stream block.
func TestEncoderCompatPropagation(t *testing.T) {
	const c = EncoderCompatV1_20_2
	src := bytes.Repeat([]byte("propagation test input "), 20000)
	for _, l := range []EncoderLevel{SpeedFastest, SpeedDefault, SpeedBetterCompression, SpeedBestCompression} {
		for _, withDict := range []bool{false, true} {
			// Concurrency 2 so the stream uses its second block (state.writing).
			opts := []EOption{WithEncoderLevel(l), WithEncoderCompat(c), WithEncoderConcurrency(2)}
			if withDict {
				opts = append(opts, WithEncoderDictRaw(1234, src[:4096]))
			}
			var o encoderOptions
			o.setDefault()
			for _, opt := range opts {
				if err := opt(&o); err != nil {
					t.Fatal(err)
				}
			}
			enc := o.encoder()
			enc.Reset(o.dict, false)
			if got := enc.Block().compat; got != c {
				t.Errorf("level %v dict %v: block compat %d, want %d", l, withDict, got, c)
			}
			if !withDict { // ResetPrefix (concurrent-block jobs) has no dictionary variant.
				enc = o.encoder()
				enc.ResetPrefix(src[:100])
				if got := enc.Block().compat; got != c {
					t.Errorf("level %v: block compat after ResetPrefix %d, want %d", l, got, c)
				}
			}

			e, err := NewWriter(io.Discard, opts...)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := e.Write(src); err != nil {
				t.Fatal(err)
			}
			if err := e.Flush(); err != nil {
				t.Fatal(err)
			}
			if got := e.state.encoder.Block().compat; got != c {
				t.Errorf("level %v dict %v: stream block compat %d, want %d", l, withDict, got, c)
			}
			if e.state.writing == nil {
				t.Fatalf("level %v dict %v: stream has no second block", l, withDict)
			}
			if got := e.state.writing.compat; got != c {
				t.Errorf("level %v dict %v: second stream block compat %d, want %d", l, withDict, got, c)
			}
			_ = e.EncodeAll(src, nil)
			// Pooled encoders get their block on first use; check every used one.
			used := 0
			pool := make([]encoder, 0, cap(e.encoders))
			for range cap(e.encoders) {
				pool = append(pool, <-e.encoders)
			}
			for _, pooled := range pool {
				if blk := pooled.Block(); blk != nil {
					used++
					if blk.compat != c {
						t.Errorf("level %v dict %v: pooled encoder block compat %d, want %d", l, withDict, blk.compat, c)
					}
				}
				e.encoders <- pooled
			}
			if used == 0 {
				t.Errorf("level %v dict %v: no pooled encoder was used", l, withDict)
			}
			if err := e.Close(); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestWithEncoderCompatOptions(t *testing.T) {
	if _, err := NewWriter(nil, WithEncoderCompat(EncoderCompatCurrent+1)); err == nil {
		t.Error("compat above EncoderCompatCurrent accepted")
	}
	for _, c := range []EncoderCompat{0, EncoderCompatV1_20_2, EncoderCompatCurrent} {
		e, err := NewWriter(nil, WithEncoderCompat(c))
		if err != nil {
			t.Fatalf("compat %d: %v", c, err)
		}
		// 0 and EncoderCompatCurrent are the same level.
		if err := e.ResetWithOptions(nil, WithEncoderCompat(c.resolve())); err != nil {
			t.Errorf("compat %d: reset with same level: %v", c, err)
		}
		if c.resolve() != EncoderCompatV1_20_2 {
			if err := e.ResetWithOptions(nil, WithEncoderCompat(EncoderCompatV1_20_2)); err == nil {
				t.Errorf("compat %d: changing level on reset accepted", c)
			}
		}
		if err := e.Close(); err != nil {
			t.Errorf("compat %d: close: %v", c, err)
		}
	}
}
