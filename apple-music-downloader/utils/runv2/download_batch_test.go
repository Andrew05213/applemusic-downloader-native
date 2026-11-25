package runv2

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Andrew05213/applemusic_downloader/internal/nativebatch"
	"github.com/Andrew05213/applemusic_downloader/utils/structs"
	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/grafov/m3u8"
)

func TestDownloadBatchProducesIdenticalFinalizedFragments(t *testing.T) {
	// Everything in the fixture, including the keys and the sample bytes,
	// is generated here. Neither route accesses media servers or accounts.
	key, iv := make([]byte, 16), bytes.Repeat([]byte{0x24}, 16)
	aesBlock, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	init := mp4.CreateEmptyInit()
	init.AddEmptyTrack(48000, "audio", "eng")
	if err := init.Moov.Trak.SetAACDescriptor(2, 48000); err != nil {
		t.Fatal(err)
	}
	var kid mp4.UUID = make([]byte, 16)
	protection, err := mp4.InitProtect(init, key, iv, "cbcs", kid, nil)
	if err != nil {
		t.Fatal(err)
	}
	var input, legacyResponses bytes.Buffer
	if err := init.Encode(&input); err != nil {
		t.Fatal(err)
	}
	var original [][]byte
	const fixtureURI = "skd://itunes.apple.com/synthetic"
	segments := []*m3u8.MediaSegment{{Key: &m3u8.Key{URI: prefetchKey}}, {Key: &m3u8.Key{URI: fixtureURI}}, {}}
	for i := range segments {
		frag, err := mp4.CreateFragment(uint32(i+1), 1)
		if err != nil {
			t.Fatal(err)
		}
		for j, n := range []int{35, 48, 96, 31, nativebatch.MaxBytes + 23, 40, 32} {
			sample := bytes.Repeat([]byte{byte(10*i + j + 1)}, n)
			original = append(original, bytes.Clone(sample))
			legacyResponses.Write(sample[:n&^15])
			frag.AddFullSample(mp4.FullSample{Sample: mp4.Sample{Dur: 1024, Size: uint32(n)}, Data: sample})
		}
		frag.Moof.Traf.Tfdt.SetBaseMediaDecodeTime(uint64(i * 7 * 1024))
		if err := mp4.EncryptFragment(frag, key, iv, protection); err != nil {
			t.Fatal(err)
		}
		if err := frag.Encode(&input); err != nil {
			t.Fatal(err)
		}
	}
	for _, maxMemory := range []int{0, 2} {
		ctx := context.Background()
		dir := t.TempDir()
		single, batch := filepath.Join(dir, "single.m4a"), filepath.Join(dir, "batch.m4a")
		legacy := struct {
			io.Reader
			io.Writer
		}{bytes.NewReader(legacyResponses.Bytes()), io.Discard}
		cfg := structs.ConfigSet{MaxMemoryLimit: maxMemory}
		if err := downloadAndDecryptFileContext(ctx, legacy, bytes.NewReader(input.Bytes()), single, "42", segments, int64(input.Len()), cfg); err != nil {
			t.Fatal(err)
		}
		var requests []int
		var ids []string
		ctx = WithDownloadBatch(ctx, func(_ context.Context, id, uri string, blocks [][]byte) error {
			if (id == "0" && uri != prefetchKey) || (id == "42" && uri != fixtureURI) || (id != "0" && id != "42") {
				t.Fatalf("wrong key context for sample batch")
			}
			ids = append(ids, id)
			requests = append(requests, len(blocks))
			for _, block := range blocks {
				cipher.NewCBCDecrypter(aesBlock, iv).CryptBlocks(block, block)
			}
			return nil
		})
		if err := downloadAndDecryptFileContext(ctx, nil, bytes.NewReader(input.Bytes()), batch, "42", segments, int64(input.Len()), cfg); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(requests, []int{4, 1, 2, 4, 1, 2, 4, 1, 2}) || !reflect.DeepEqual(ids, []string{"0", "0", "0", "42", "42", "42", "42", "42", "42"}) {
			t.Fatalf("wrong batching across fragments and contexts: %v %v", requests, ids)
		}
		read := func(file string) []byte {
			t.Helper()
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			return data
		}
		got := read(batch)
		if !bytes.Equal(got, read(single)) {
			t.Fatal("batch and legacy wrote different MP4 bytes")
		}
		parsed, err := mp4.DecodeFile(bytes.NewReader(got))
		if err != nil {
			t.Fatal(err)
		}
		if parsed.Moov.Trak.Mdia.Mdhd.Duration != 21*1024 {
			t.Fatal("finalized track duration changed")
		}
		idx := 0
		for _, seg := range parsed.Segments {
			for _, frag := range seg.Fragments {
				samples, err := frag.GetFullSamples(parsed.Moov.Mvex.Trex)
				if err != nil {
					t.Fatal(err)
				}
				for _, sample := range samples {
					if idx >= len(original) || !bytes.Equal(sample.Data, original[idx]) || sample.DecodeTime != uint64(idx*1024) {
						t.Fatalf("sample %d did not round trip", idx)
					}
					idx++
				}
			}
		}
		if idx != len(original) {
			t.Fatalf("decoded only %d samples", idx)
		}
	}
}

func TestSampleBatchBudgetAndStripeDoesNotSendClearBytes(t *testing.T) {
	var lengths [][]int
	b := sampleBatch{send: func(blocks [][]byte) error {
		var group []int
		for _, block := range blocks {
			group = append(group, len(block))
			for i := range block {
				block[i] ^= 0xff
			}
		}
		lengths = append(lengths, group)
		return nil
	}}
	striped := make([]byte, 57)
	for _, test := range []struct {
		data        []byte
		crypt, skip int
	}{
		{make([]byte, 3), 0, 0}, {make([]byte, nativebatch.MaxBytes-16), 0, 0}, {make([]byte, 32), 0, 0}, {striped, 16, 16},
	} {
		if err := b.add(test.data, test.crypt, test.skip); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.flush(); err != nil || !reflect.DeepEqual(lengths, [][]int{{nativebatch.MaxBytes - 16}, {32}, {32}}) {
		t.Fatalf("bad frame grouping: %v, %v", lengths, err)
	}
	for i, value := range striped {
		want := byte(0)
		if i < 16 || (i >= 32 && i < 48) {
			want = 255
		}
		if want != value {
			t.Fatal("stripe copy modified an unprotected byte")
		}
	}
	failure := errors.New("fixture failed")
	b.send = func([][]byte) error { return failure }
	_ = b.add(make([]byte, 16), 0, 0)
	if err := b.add(striped, 16, 16); !errors.Is(err, failure) {
		t.Fatal("failed prefix was not propagated")
	}
	if b.count != 0 || b.size != 0 {
		t.Fatal("failed request was retained for replay")
	}
}

func TestDownloadBatchFailureIsNotMaskedByTransferTimeout(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(errMediaReadIdle)
	unknown := errors.New("fixture batch outcome unknown")
	err := mediaTransferError(ctx, &batchRequestError{err: unknown})
	if !errors.Is(err, unknown) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("transfer watchdog changed the batch outcome: %v", err)
	}
}
