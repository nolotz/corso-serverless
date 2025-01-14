package metadata

import "strings"

const (
	MetaFileSuffix    = ".meta"
	DirMetaFileSuffix = ".dirmeta"
	DataFileSuffix    = ".data"
)

func HasMetaSuffix(name string) bool {
	return strings.HasSuffix(name, MetaFileSuffix) || strings.HasSuffix(name, DirMetaFileSuffix)
}
