package frontend

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// DefaultLogoDir holds the custom logo: logo.<ext> with one of logoTypes.
const DefaultLogoDir = "branding"

const maxLogoBytes = 2 << 20 // 2 MiB

// logoTypes are the accepted image formats, in the order they are looked for.
var logoTypes = []struct{ ext, ctype string }{
	{".svg", "image/svg+xml"},
	{".png", "image/png"},
	{".webp", "image/webp"},
	{".jpg", "image/jpeg"},
	{".jpeg", "image/jpeg"},
	{".gif", "image/gif"},
	{".ico", "image/x-icon"},
}

type logo struct {
	path  string // file it was read from
	ext   string
	ctype string
	data  []byte
}

// loadLogo reads branding/logo.<ext>. Finding none is not an error: the
// built-in logo is used.
func loadLogo() (*logo, error) {
	for _, t := range logoTypes {
		path := filepath.Join(DefaultLogoDir, "logo"+t.ext)
		fi, err := os.Stat(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("frontend logo: %w", err)
		}
		if fi.Size() > maxLogoBytes {
			return nil, fmt.Errorf("frontend logo %s is %d bytes; the limit is %d", path, fi.Size(), maxLogoBytes)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("frontend logo: %w", err)
		}
		ext := t.ext
		if ext == ".jpeg" {
			ext = ".jpg"
		}
		return &logo{path: path, ext: ext, ctype: t.ctype, data: data}, nil
	}
	return nil, nil
}
