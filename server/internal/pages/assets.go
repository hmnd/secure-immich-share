package pages

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
)

// Entry is the islands' entry in the web build.
const Entry = "src/islands.ts"

const manifestPath = ".vite/manifest.json"

// Assets are the stylesheets and module scripts a page links.
type Assets struct {
	Styles  []string
	Scripts []string
	// dev: the Vite dev server serves the styles through the scripts.
	dev bool
}

// DevAssets loads the islands from the Vite dev server, which proxies
// everything else to this server.
func DevAssets() Assets {
	return Assets{Scripts: []string{"/@vite/client", "/" + Entry}, dev: true}
}

// Static is what a page without islands links: only the stylesheets, except
// in dev, where they come with the scripts.
func (a Assets) Static() Assets {
	if a.dev {
		return a
	}
	return Assets{Styles: a.Styles}
}

// LoadAssets reads the entry's files from the web build's Vite manifest.
// Without a build (fs.ErrNotExist) pages render unstyled and without islands.
func LoadAssets(build fs.FS) (Assets, error) {
	if build == nil {
		return Assets{}, nil
	}
	data, err := fs.ReadFile(build, manifestPath)
	if errors.Is(err, fs.ErrNotExist) {
		return Assets{}, err
	}
	if err != nil {
		return Assets{}, fmt.Errorf("read %s: %w", manifestPath, err)
	}
	var manifest map[string]struct {
		File string   `json:"file"`
		CSS  []string `json:"css"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return Assets{}, fmt.Errorf("parse %s: %w", manifestPath, err)
	}
	entry, ok := manifest[Entry]
	if !ok || entry.File == "" {
		return Assets{}, fmt.Errorf("%s has no %s entry", manifestPath, Entry)
	}
	a := Assets{Scripts: []string{"/" + entry.File}}
	for _, css := range entry.CSS {
		a.Styles = append(a.Styles, "/"+css)
	}
	return a, nil
}
