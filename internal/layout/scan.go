package layout

import (
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
)

// Asset is one publishable unit found in a publish directory.
type Asset struct {
	Name          string
	Arches        []string // subset of amd64, arm64, arm, in that order
	HasUnit       bool
	HasEnvExample bool
}

var allArches = []string{"amd64", "arm64", "arm"}

const sumsFile = "SHA256SUMS"

// ScanDir validates a publish directory for project and returns its assets
// (sorted by name) and the sorted list of files to upload. A pre-existing
// SHA256SUMS is ignored.
func ScanDir(project, dir string) (assets []Asset, files []string, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}
	byName := map[string]*Asset{}
	get := func(name string) *Asset {
		a := byName[name]
		if a == nil {
			a = &Asset{Name: name}
			byName[name] = a
		}
		return a
	}
	for _, e := range entries {
		fn := e.Name()
		if fn == sumsFile {
			continue
		}
		if e.IsDir() {
			return nil, nil, fmt.Errorf("%s: unexpected directory", fn)
		}
		asset, kind, arch := classify(fn)
		if kind == "" {
			return nil, nil, fmt.Errorf("%s: unrecognised file (want <asset>-linux-<arch>, <asset>.service or <asset>.env.example)", fn)
		}
		if asset != project && !strings.HasPrefix(asset, project+"-") {
			return nil, nil, fmt.Errorf("%s: asset %q does not belong to project %q", fn, asset, project)
		}
		if !ValidName(asset) {
			return nil, nil, fmt.Errorf("%s: invalid asset name %q", fn, asset)
		}
		a := get(asset)
		switch kind {
		case "bin":
			a.Arches = append(a.Arches, arch)
		case "unit":
			a.HasUnit = true
			b, err := os.ReadFile(dir + string(os.PathSeparator) + fn)
			if err != nil {
				return nil, nil, err
			}
			if err := CheckUnitTemplate(string(b)); err != nil {
				return nil, nil, fmt.Errorf("%s: %w", fn, err)
			}
		case "env":
			a.HasEnvExample = true
		}
		files = append(files, fn)
	}
	if len(byName) == 0 {
		return nil, nil, fmt.Errorf("%s: no files to publish", dir)
	}
	for _, a := range byName {
		for _, need := range []string{"amd64", "arm64"} {
			if !slices.Contains(a.Arches, need) {
				return nil, nil, fmt.Errorf("asset %q is missing %s", a.Name, BinaryFile(a.Name, need))
			}
		}
		a.Arches = orderArches(a.Arches)
		assets = append(assets, *a)
	}
	sort.Slice(assets, func(i, j int) bool { return assets[i].Name < assets[j].Name })
	sort.Strings(files)
	return assets, files, nil
}

func orderArches(have []string) []string {
	var out []string
	for _, a := range allArches {
		if slices.Contains(have, a) {
			out = append(out, a)
		}
	}
	return out
}

// classify splits a file name into asset and kind ("bin", "unit", "env").
func classify(fn string) (asset, kind, arch string) {
	if a, ok := strings.CutSuffix(fn, ".env.example"); ok {
		return a, "env", ""
	}
	if a, ok := strings.CutSuffix(fn, ".service"); ok {
		return a, "unit", ""
	}
	for _, ar := range allArches {
		if a, ok := strings.CutSuffix(fn, "-linux-"+ar); ok {
			return a, "bin", ar
		}
	}
	return "", "", ""
}
