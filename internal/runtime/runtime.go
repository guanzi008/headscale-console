package runtimeenv

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/guanzi008/headscale-console/internal/assets"
)

const (
	nodeAsset      = "node.gz"
	headplaneAsset = "headplane.tar.gz"
	headscaleAsset = "headscale.gz"
	manifestAsset  = "manifest.json"
)

type Bundle struct {
	NodeBinary      string
	HeadplaneDir    string
	HeadscaleBinary string
	Root            string
}

type manifest struct {
	ID string `json:"id"`
}

var unsafeIDChars = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

func Resolve(dataDir string) (Bundle, error) {
	node := os.Getenv("HEADSCALE_CONSOLE_NODE")
	headplane := os.Getenv("HEADSCALE_CONSOLE_HEADPLANE_DIR")
	headscale := os.Getenv("HEADSCALE_CONSOLE_HEADSCALE")

	if node != "" || headplane != "" || headscale != "" {
		if node == "" || headplane == "" || headscale == "" {
			return Bundle{}, fmt.Errorf("set all of HEADSCALE_CONSOLE_NODE, HEADSCALE_CONSOLE_HEADPLANE_DIR, and HEADSCALE_CONSOLE_HEADSCALE, or none of them")
		}
		if err := validateOverrides(node, headplane, headscale); err != nil {
			return Bundle{}, err
		}
		return Bundle{NodeBinary: node, HeadplaneDir: headplane, HeadscaleBinary: headscale}, nil
	}

	id, err := runtimeID()
	if err != nil {
		return Bundle{}, err
	}
	root := filepath.Join(dataDir, "runtime", id)
	marker := filepath.Join(root, ".ready")
	if _, err := os.Stat(marker); errors.Is(err, os.ErrNotExist) {
		if err := extract(root); err != nil {
			return Bundle{}, err
		}
	} else if err != nil {
		return Bundle{}, fmt.Errorf("check runtime marker: %w", err)
	}
	return Bundle{
		NodeBinary:      filepath.Join(root, "node"),
		HeadplaneDir:    filepath.Join(root, "headplane"),
		HeadscaleBinary: filepath.Join(root, "headscale"),
		Root:            root,
	}, nil
}

func validateOverrides(node, headplane, headscale string) error {
	if info, err := os.Stat(node); err != nil || info.IsDir() {
		return fmt.Errorf("HEADSCALE_CONSOLE_NODE is not an executable file: %s", node)
	}
	if _, err := os.Stat(filepath.Join(headplane, "build", "server", "index.js")); err != nil {
		return fmt.Errorf("HEADSCALE_CONSOLE_HEADPLANE_DIR must contain build/server/index.js: %w", err)
	}
	if info, err := os.Stat(headscale); err != nil || info.IsDir() {
		return fmt.Errorf("HEADSCALE_CONSOLE_HEADSCALE is not an executable file: %s", headscale)
	}
	return nil
}

func runtimeID() (string, error) {
	data, err := assets.RuntimeFile(manifestAsset)
	if err != nil {
		return "dev", nil
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return "", fmt.Errorf("parse runtime manifest: %w", err)
	}
	if m.ID == "" {
		return "dev", nil
	}
	id := unsafeIDChars.ReplaceAllString(strings.TrimSpace(m.ID), "-")
	id = strings.Trim(id, ".-_")
	if id == "" {
		return "dev", nil
	}
	return id, nil
}

func extract(root string) error {
	nodeData, err := assets.RuntimeFile(nodeAsset)
	if err != nil {
		return err
	}
	headplaneData, err := assets.RuntimeFile(headplaneAsset)
	if err != nil {
		return err
	}
	headscaleData, err := assets.RuntimeFile(headscaleAsset)
	if err != nil {
		return err
	}

	parent := filepath.Dir(root)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("create runtime parent: %w", err)
	}
	tmp, err := os.MkdirTemp(parent, "."+filepath.Base(root)+".tmp-")
	if err != nil {
		return fmt.Errorf("create runtime staging directory: %w", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(tmp)
		}
	}()

	if err := writeGzipFile(tmp, "node", nodeData, 0o755); err != nil {
		return err
	}
	if err := writeGzipFile(tmp, "headscale", headscaleData, 0o755); err != nil {
		return err
	}
	if err := extractTarGz(headplaneData, filepath.Join(tmp, "headplane")); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(tmp, ".ready"), []byte("ok\n"), 0o600); err != nil {
		return fmt.Errorf("write runtime marker: %w", err)
	}

	if err := os.RemoveAll(root); err != nil {
		return fmt.Errorf("remove stale runtime: %w", err)
	}
	if err := os.Rename(tmp, root); err != nil {
		return fmt.Errorf("publish runtime: %w", err)
	}
	cleanup = false
	return nil
}

func writeGzipFile(dir, name string, compressed []byte, mode os.FileMode) error {
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return fmt.Errorf("open embedded %s: %w", name, err)
	}
	defer reader.Close()
	file, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("create extracted %s: %w", name, err)
	}
	_, copyErr := io.Copy(file, reader)
	closeErr := file.Close()
	if copyErr != nil {
		return fmt.Errorf("extract %s: %w", name, copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close extracted %s: %w", name, closeErr)
	}
	return nil
}

func extractTarGz(compressed []byte, destination string) error {
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return fmt.Errorf("open embedded Headplane archive: %w", err)
	}
	defer reader.Close()
	archive := tar.NewReader(reader)

	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read Headplane archive: %w", err)
		}

		clean := path.Clean(header.Name)
		if clean == "." || clean == "" {
			continue
		}
		if path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
			return fmt.Errorf("unsafe path in Headplane archive: %q", header.Name)
		}
		target := filepath.Join(destination, filepath.FromSlash(clean))
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("create Headplane directory: %w", err)
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("create Headplane parent directory: %w", err)
			}
			mode := os.FileMode(header.Mode) & 0o755
			if mode == 0 {
				mode = 0o644
			}
			file, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				return fmt.Errorf("create Headplane file: %w", err)
			}
			_, copyErr := io.Copy(file, archive)
			closeErr := file.Close()
			if copyErr != nil {
				return fmt.Errorf("extract Headplane file: %w", copyErr)
			}
			if closeErr != nil {
				return fmt.Errorf("close Headplane file: %w", closeErr)
			}
		default:
			return fmt.Errorf("unsupported entry in Headplane archive: %s (%d)", header.Name, header.Typeflag)
		}
	}
	return nil
}
