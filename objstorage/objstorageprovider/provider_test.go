// Copyright 2023 The LevelDB-Go and Pebble Authors. All rights reserved. Use
// of this source code is governed by a BSD-style license that can be found in
// the LICENSE file.

package objstorageprovider

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/cockroachdb/datadriven"
	"github.com/cockroachdb/pebble/internal/base"
	"github.com/cockroachdb/pebble/objstorage"
	"github.com/cockroachdb/pebble/objstorage/shared"
	"github.com/cockroachdb/pebble/vfs"
	"github.com/stretchr/testify/require"
)

func TestProvider(t *testing.T) {
	datadriven.Walk(t, "testdata/provider", func(t *testing.T, path string) {
		var log base.InMemLogger
		fs := vfs.WithLogging(vfs.NewMem(), func(fmt string, args ...interface{}) {
			log.Infof("<local fs> "+fmt, args...)
		})
		sharedStore := shared.WithLogging(shared.NewInMem(), func(fmt string, args ...interface{}) {
			log.Infof("<shared> "+fmt, args...)
		})
		tmpFileCounter := 0

		providers := make(map[string]objstorage.Provider)
		// We maintain both backings and backing handles to allow tests to use the
		// backings after the handles have been closed.
		backings := make(map[string]objstorage.SharedObjectBacking)
		backingHandles := make(map[string]objstorage.SharedObjectBackingHandle)
		var curProvider objstorage.Provider
		datadriven.RunTest(t, path, func(t *testing.T, d *datadriven.TestData) string {
			scanArgs := func(desc string, args ...interface{}) {
				t.Helper()
				if len(d.CmdArgs) != len(args) {
					d.Fatalf(t, "usage: %s %s", d.Cmd, desc)
				}
				for i := range args {
					_, err := fmt.Sscan(d.CmdArgs[i].String(), args[i])
					if err != nil {
						d.Fatalf(t, "%s: error parsing argument '%s'", d.Cmd, d.CmdArgs[i])
					}
				}
			}
			ctx := context.Background()

			log.Reset()
			switch d.Cmd {
			case "open":
				var fsDir string
				var creatorID objstorage.CreatorID
				scanArgs("<fs-dir> <shared-creator-id>", &fsDir, &creatorID)

				st := DefaultSettings(fs, fsDir)
				if creatorID != 0 {
					st.Shared.Storage = sharedStore
				}
				require.NoError(t, fs.MkdirAll(fsDir, 0755))
				p, err := Open(st)
				require.NoError(t, err)
				if creatorID != 0 {
					require.NoError(t, p.SetCreatorID(creatorID))
				}
				// Checking refs on open affects the test output. We don't want tests to
				// only pass when the `invariants` tag is used, so unconditionally
				// enable ref checking on open.
				p.(*provider).shared.checkRefsOnOpen = true
				providers[fsDir] = p
				curProvider = p

				return log.String()

			case "switch":
				var fsDir string
				scanArgs("<fs-dir>", &fsDir)
				curProvider = providers[fsDir]
				if curProvider == nil {
					t.Fatalf("unknown provider %s", fsDir)
				}

				return ""

			case "close":
				require.NoError(t, curProvider.Sync())
				require.NoError(t, curProvider.Close())
				delete(providers, curProvider.(*provider).st.FSDirName)
				curProvider = nil

				return log.String()

			case "create":
				opts := objstorage.CreateOptions{
					SharedCleanupMethod: objstorage.SharedRefTracking,
				}
				if len(d.CmdArgs) == 5 && d.CmdArgs[4].Key == "no-ref-tracking" {
					d.CmdArgs = d.CmdArgs[:4]
					opts.SharedCleanupMethod = objstorage.SharedNoCleanup
				}
				var fileNum base.FileNum
				var typ string
				var salt, size int
				scanArgs("<file-num> <local|shared> <salt> <size> [no-ref-tracking]", &fileNum, &typ, &salt, &size)
				switch typ {
				case "local":
				case "shared":
					opts.PreferSharedStorage = true
				default:
					d.Fatalf(t, "'%s' should be 'local' or 'shared'", typ)
				}
				w, _, err := curProvider.Create(ctx, base.FileTypeTable, fileNum.DiskFileNum(), opts)
				if err != nil {
					return err.Error()
				}
				data := make([]byte, size)
				// TODO(radu): write in chunks?
				genData(byte(salt), 0, data)
				require.NoError(t, w.Write(data))
				require.NoError(t, w.Finish())

				return log.String()

			case "link-or-copy":
				opts := objstorage.CreateOptions{
					SharedCleanupMethod: objstorage.SharedRefTracking,
				}
				if len(d.CmdArgs) == 5 && d.CmdArgs[4].Key == "no-ref-tracking" {
					d.CmdArgs = d.CmdArgs[:4]
					opts.SharedCleanupMethod = objstorage.SharedNoCleanup
				}
				var fileNum base.FileNum
				var typ string
				var salt, size int
				scanArgs("<file-num> <local|shared> <salt> <size> [no-ref-tracking]", &fileNum, &typ, &salt, &size)
				switch typ {
				case "local":
				case "shared":
					opts.PreferSharedStorage = true
				default:
					d.Fatalf(t, "'%s' should be 'local' or 'shared'", typ)
				}

				tmpFileCounter++
				tmpFilename := fmt.Sprintf("temp-file-%d", tmpFileCounter)
				f, err := fs.Create(tmpFilename)
				require.NoError(t, err)
				data := make([]byte, size)
				genData(byte(salt), 0, data)
				n, err := f.Write(data)
				require.Equal(t, len(data), n)
				require.NoError(t, err)
				require.NoError(t, f.Close())

				_, err = curProvider.LinkOrCopyFromLocal(
					ctx, fs, tmpFilename, base.FileTypeTable, fileNum.DiskFileNum(), opts,
				)
				require.NoError(t, err)
				return log.String()

			case "read":
				var fileNum base.FileNum
				scanArgs("<file-num>", &fileNum)
				r, err := curProvider.OpenForReading(ctx, base.FileTypeTable, fileNum.DiskFileNum(), objstorage.OpenOptions{})
				if err != nil {
					return err.Error()
				}
				rh := r.NewReadHandle(ctx)
				log.Infof("size: %d", r.Size())
				for _, l := range strings.Split(d.Input, "\n") {
					var offset, size int
					fmt.Sscanf(l, "%d %d", &offset, &size)
					data := make([]byte, size)
					err := rh.ReadAt(ctx, data, int64(offset))
					if err != nil {
						log.Infof("%d %d: %v", offset, size, err)
					} else {
						salt := checkData(d, t, offset, data)
						log.Infof("%d %d: ok (salt %d)", offset, size, salt)
					}
				}
				require.NoError(t, rh.Close())
				require.NoError(t, r.Close())
				return log.String()

			case "remove":
				var fileNum base.FileNum
				scanArgs("<file-num>", &fileNum)
				if err := curProvider.Remove(base.FileTypeTable, fileNum.DiskFileNum()); err != nil {
					return err.Error()
				}
				return log.String()

			case "list":
				for _, meta := range curProvider.List() {
					log.Infof("%s -> %s", meta.DiskFileNum, curProvider.Path(meta))
				}
				return log.String()

			case "save-backing":
				var key string
				var fileNum base.FileNum
				scanArgs("<key> <file-num>", &key, &fileNum)
				meta, err := curProvider.Lookup(base.FileTypeTable, fileNum.DiskFileNum())
				require.NoError(t, err)
				handle, err := curProvider.SharedObjectBacking(&meta)
				if err != nil {
					return err.Error()
				}
				backing, err := handle.Get()
				require.NoError(t, err)
				backings[key] = backing
				backingHandles[key] = handle
				return log.String()

			case "close-backing":
				var key string
				scanArgs("<key>", &key)
				backingHandles[key].Close()
				return ""

			case "attach":
				lines := strings.Split(d.Input, "\n")
				if len(lines) == 0 {
					d.Fatalf(t, "at least one row expected; format: <key> <file-num>")
				}
				var objs []objstorage.SharedObjectToAttach
				for _, l := range lines {
					var key string
					var fileNum base.FileNum
					_, err := fmt.Sscan(l, &key, &fileNum)
					require.NoError(t, err)
					b, ok := backings[key]
					if !ok {
						d.Fatalf(t, "unknown backing key %q", key)
					}
					objs = append(objs, objstorage.SharedObjectToAttach{
						FileType: base.FileTypeTable,
						FileNum:  fileNum.DiskFileNum(),
						Backing:  b,
					})
				}
				metas, err := curProvider.AttachSharedObjects(objs)
				if err != nil {
					return log.String() + "error: " + err.Error()
				}
				for _, meta := range metas {
					log.Infof("%s -> %s", meta.DiskFileNum, curProvider.Path(meta))
				}
				return log.String()

			default:
				d.Fatalf(t, "unknown command %s", d.Cmd)
				return ""
			}
		})
	})
}

func TestNotExistError(t *testing.T) {
	fs := vfs.NewMem()
	st := DefaultSettings(fs, "")
	st.Shared.Storage = shared.NewInMem()
	provider, err := Open(st)
	require.NoError(t, err)
	require.NoError(t, provider.SetCreatorID(1))

	for i, shared := range []bool{false, true} {
		fileNum := base.FileNum(1 + i).DiskFileNum()
		name := "local"
		if shared {
			name = "shared"
		}
		t.Run(name, func(t *testing.T) {
			// Removing or opening an object that the provider doesn't know anything
			// about should return a not-exist error.
			err := provider.Remove(base.FileTypeTable, fileNum)
			require.True(t, provider.IsNotExistError(err))
			_, err = provider.OpenForReading(context.Background(), base.FileTypeTable, fileNum, objstorage.OpenOptions{})
			require.True(t, provider.IsNotExistError(err))

			w, _, err := provider.Create(context.Background(), base.FileTypeTable, fileNum, objstorage.CreateOptions{
				PreferSharedStorage: shared,
			})
			require.NoError(t, err)
			require.NoError(t, w.Write([]byte("foo")))
			require.NoError(t, w.Finish())

			// Remove the underlying file or object.
			if !shared {
				require.NoError(t, fs.Remove(base.MakeFilename(base.FileTypeTable, fileNum)))
			} else {
				meta, err := provider.Lookup(base.FileTypeTable, fileNum)
				require.NoError(t, err)
				require.NoError(t, st.Shared.Storage.Delete(sharedObjectName(meta)))
			}

			_, err = provider.OpenForReading(context.Background(), base.FileTypeTable, fileNum, objstorage.OpenOptions{})
			require.True(t, provider.IsNotExistError(err))

			// It's acceptable for Remove to return a not-exist error, or no error at all.
			if err := provider.Remove(base.FileTypeTable, fileNum); err != nil {
				require.True(t, provider.IsNotExistError(err))
			}
		})
	}
}

// genData generates object data that can be checked later with checkData.
func genData(salt byte, offset int, p []byte) {
	for i := range p {
		p[i] = salt ^ xor(offset+i)
	}
}

func checkData(d *datadriven.TestData, t *testing.T, offset int, p []byte) (salt byte) {
	t.Helper()
	salt = p[0] ^ xor(offset)
	for i := range p {
		if p[i]^xor(offset+i) != salt {
			d.Fatalf(t, "invalid data")
		}
	}
	return salt
}

// xor returns the XOR of all bytes representing the integer.
func xor(n int) byte {
	v := uint64(n)
	v ^= v >> 32
	v ^= v >> 16
	v ^= v >> 8
	return byte(v)
}
