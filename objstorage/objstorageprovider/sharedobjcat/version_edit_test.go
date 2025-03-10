// Copyright 2023 The LevelDB-Go and Pebble Authors. All rights reserved. Use
// of this source code is governed by a BSD-style license that can be found in
// the LICENSE file.

package sharedobjcat

import (
	"bytes"
	"strings"
	"testing"

	"github.com/cockroachdb/errors"
	"github.com/cockroachdb/pebble/internal/base"
	"github.com/cockroachdb/pebble/objstorage"
	"github.com/kr/pretty"
)

func TestVersionEditRoundTrip(t *testing.T) {
	for _, ve := range []versionEdit{
		{},
		{
			CreatorID: 12345,
		},
		{
			NewObjects: []SharedObjectMetadata{
				{
					FileNum:        base.FileNum(1).DiskFileNum(),
					FileType:       base.FileTypeTable,
					CreatorID:      12,
					CreatorFileNum: base.FileNum(123).DiskFileNum(),
					CleanupMethod:  objstorage.SharedNoCleanup,
				},
			},
		},
		{
			DeletedObjects: []base.DiskFileNum{base.FileNum(1).DiskFileNum()},
		},
		{
			CreatorID: 12345,
			NewObjects: []SharedObjectMetadata{
				{
					FileNum:        base.FileNum(1).DiskFileNum(),
					FileType:       base.FileTypeTable,
					CreatorID:      12,
					CreatorFileNum: base.FileNum(123).DiskFileNum(),
					CleanupMethod:  objstorage.SharedRefTracking,
				},
				{
					FileNum:        base.FileNum(2).DiskFileNum(),
					FileType:       base.FileTypeTable,
					CreatorID:      22,
					CreatorFileNum: base.FileNum(223).DiskFileNum(),
				},
				{
					FileNum:        base.FileNum(3).DiskFileNum(),
					FileType:       base.FileTypeTable,
					CreatorID:      32,
					CreatorFileNum: base.FileNum(323).DiskFileNum(),
					CleanupMethod:  objstorage.SharedRefTracking,
				},
			},
			DeletedObjects: []base.DiskFileNum{base.FileNum(4).DiskFileNum(), base.FileNum(5).DiskFileNum()},
		},
	} {
		if err := checkRoundTrip(ve); err != nil {
			t.Fatalf("%+v did not roundtrip: %v", ve, err)
		}
	}
}

func checkRoundTrip(e0 versionEdit) error {
	var e1 versionEdit
	buf := new(bytes.Buffer)
	if err := e0.Encode(buf); err != nil {
		return errors.Wrap(err, "encode")
	}
	if err := e1.Decode(buf); err != nil {
		return errors.Wrap(err, "decode")
	}
	if diff := pretty.Diff(e0, e1); diff != nil {
		return errors.Errorf("%s", strings.Join(diff, "\n"))
	}
	return nil
}
