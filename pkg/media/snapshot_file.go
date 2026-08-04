package media

import (
	"io/fs"
	"os"
	"reflect"
)

func sameSnapshotFile(before, after fs.FileInfo) bool {
	return os.SameFile(before, after)
}

func sameSnapshotChangeToken(before, after any) bool {
	return reflect.DeepEqual(before, after)
}
