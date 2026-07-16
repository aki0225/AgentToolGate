package static

import (
	"embed"
	"io/fs"
)

//go:embed site
var embedded embed.FS

func Frontend() (fs.FS, bool) {
	sub, err := fs.Sub(embedded, "site")
	if err != nil {
		return nil, false
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil, false
	}
	return sub, true
}
