package assets

import (
	"embed"
	"fmt"
	"io/fs"
)

//go:embed all:runtime
var runtimeAssets embed.FS

func RuntimeFile(name string) ([]byte, error) {
	data, err := runtimeAssets.ReadFile("runtime/" + name)
	if err != nil {
		return nil, fmt.Errorf("read embedded runtime asset %q: %w", name, err)
	}
	return data, nil
}

func RuntimeFS() fs.FS {
	return runtimeAssets
}
