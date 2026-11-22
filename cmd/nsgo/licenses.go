package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"

	"github.com/google/licenseclassifier/v2/assets"
)

const noticesName = "THIRD_PARTY_NOTICES.txt"

type licensePolicy struct {
	Unknown  string   `yaml:"unknown"`
	Copyleft string   `yaml:"copyleft"`
	Allow    []string `yaml:"allow"`
}

func (policy licensePolicy) validate() error {
	for _, rule := range []struct{ name, value string }{
		{"unknown", policy.Unknown}, {"copyleft", policy.Copyleft},
	} {
		switch rule.value {
		case "", "error", "warn":
		default:
			return fmt.Errorf("licenses.%s is %q; use error or warn", rule.name, rule.value)
		}
	}
	for _, module := range policy.Allow {
		if module == "" || len(strings.Fields(module)) != 1 || strings.TrimSpace(module) != module {
			return fmt.Errorf("licenses.allow needs exact module paths, got %q", module)
		}
	}
	return nil
}

type licenseModule struct {
	Path, Version, Dir string
	Replace            *licenseModule
}

type licensePackage struct {
	ImportPath, Dir, Root string
	DepOnly, Standard     bool
	Module                *licenseModule
	EmbedFiles            []string
}

type licenseSource struct {
	name, label, root string
	firstParty        bool
	dirs              map[string]bool
	extraFiles        map[string]bool
}

var licenseClassifier = sync.OnceValues(assets.DefaultClassifier)

func (p *project) licenseNoticesFile(ctx context.Context, env, tags, flags []string, overlay string) (string, error) {
	sources := map[string]*licenseSource{}
	var hasCore bool
	architectures := p.noticeArchitectures
	if len(architectures) == 0 {
		architectures = []string{""}
	}
	for _, arch := range architectures {
		targetEnv := slices.Clone(env)
		if arch != "" {
			targetEnv = append(targetEnv, "GOARCH="+arch)
		}
		core, err := p.listLicenseSources(ctx, targetEnv, tags, flags, overlay, sources)
		if err != nil {
			return "", err
		}
		hasCore = hasCore || core
	}
	notices, err := p.renderLicenseNotices(ctx, sources, env)
	if err != nil {
		return "", err
	}
	p.thirdPartyNotices = notices
	if !hasCore {
		return "", nil
	}
	source := fmt.Sprintf("// Written by nsgo: the app's third-party notices.\n\npackage main\n\nimport %q\n\nfunc init() { core.SetBundledThirdPartyNotices(%q) }\n",
		nsgoModule+"/core", notices)
	return p.cacheFile("notices", ".go", source)
}

func (p *project) listLicenseSources(ctx context.Context, env, tags, flags []string, overlay string, sources map[string]*licenseSource) (bool, error) {
	args := append([]string{"list", "-deps", "-json=ImportPath,Dir,Root,DepOnly,Standard,Module,EmbedFiles"}, flags...)
	args = append(args, tagsArgs(tags)...)
	if overlay != "" {
		args = append(args, "-overlay", overlay)
	}
	cmd := exec.CommandContext(ctx, "go", append(args, ".")...)
	cmd.Dir = p.Dir
	cmd.Env = append(os.Environ(), env...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("list linked packages for license notices: %w\n%s", err, strings.TrimSpace(stderr.String()))
	}

	target := licenseTargetForEnv(env)
	var hasCore bool
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var pkg licensePackage
		if err := dec.Decode(&pkg); err != nil {
			if errors.Is(err, io.EOF) {
				return hasCore, nil
			}
			return false, fmt.Errorf("decode linked packages for license notices: %w", err)
		}
		if pkg.Standard && pkg.Root != "" {
			if sources["Go"] == nil {
				label := "Go runtime and standard library"
				if version, err := os.ReadFile(filepath.Join(pkg.Root, "VERSION")); err == nil {
					line, _, _ := strings.Cut(string(version), "\n")
					label += " (" + line + ")"
				}
				root := pkg.Root
				// Homebrew installs Go's LICENSE beside libexec, its GOROOT.
				if filepath.Base(root) == "libexec" {
					parent := filepath.Dir(root)
					if data, err := os.ReadFile(filepath.Join(parent, "LICENSE")); err == nil && bytes.Contains(data, []byte("The Go Authors")) {
						root = parent
					}
				}
				sources["Go"] = &licenseSource{name: "Go", label: label, root: root, dirs: map[string]bool{root: true, pkg.Root: true}}
			}
			if strings.HasPrefix(pkg.ImportPath, "vendor/") {
				sources["Go"].addDirs(pkg.Dir)
			}
			continue
		}
		if pkg.Module == nil {
			continue
		}
		if pkg.ImportPath == nsgoModule+"/core" {
			hasCore = true
		}
		addLicensePackage(sources, pkg)
		if target != "" && pkg.Dir != "" {
			if err := addNativeLicenseFiles(sources[pkg.Module.Path], pkg, target); err != nil {
				return false, err
			}
		}
	}
}

func licenseTargetForEnv(env []string) string {
	goos, goarch := runtime.GOOS, runtime.GOARCH
	var cflags string
	for _, setting := range env {
		switch {
		case strings.HasPrefix(setting, "GOOS="):
			goos = strings.TrimPrefix(setting, "GOOS=")
		case strings.HasPrefix(setting, "GOARCH="):
			goarch = strings.TrimPrefix(setting, "GOARCH=")
		case strings.HasPrefix(setting, "CGO_CFLAGS="):
			cflags = strings.TrimPrefix(setting, "CGO_CFLAGS=")
		case strings.HasPrefix(setting, "CC=") && cflags == "":
			cflags = strings.TrimPrefix(setting, "CC=")
		}
	}
	switch goos {
	case "darwin":
		return "macos_" + goArchName(goarch)
	case "windows", "linux", "android":
		return goos + "_" + goArchName(goarch)
	case "ios":
		target := "ios"
		if strings.Contains(cflags, "tvos") {
			target = "tvos"
		}
		if strings.Contains(cflags, "-simulator") {
			target += "_simulator"
		}
		return target
	}
	return ""
}

func addNativeLicenseFiles(source *licenseSource, pkg licensePackage, target string) error {
	m, ok, err := loadNativeManifest(pkg.Dir)
	if err != nil {
		return fmt.Errorf("%s: %w", pkg.ImportPath, err)
	}
	if !ok {
		return nil
	}
	specs := m.Targets[target]
	if len(specs) == 0 {
		return nil
	}
	lists := make([][]string, 0, 1+len(specs))
	lists = append(lists, m.Licenses)
	for _, spec := range specs {
		lists = append(lists, spec.Licenses)
	}
	files, err := collectManifestLicenses(pkg.Dir, lists...)
	if err != nil {
		return fmt.Errorf("%s: %w", pkg.ImportPath, err)
	}
	for _, file := range files {
		source.addExtraFile(file)
	}
	return nil
}

func (source *licenseSource) addExtraFile(path string) {
	if source.extraFiles == nil {
		source.extraFiles = map[string]bool{}
	}
	source.extraFiles[filepath.Clean(path)] = true
}

func addLicensePackage(sources map[string]*licenseSource, pkg licensePackage) {
	module := pkg.Module
	source := sources[module.Path]
	if source == nil {
		label, root := moduleLabel(module), module.Dir
		if module.Replace != nil {
			root = module.Replace.Dir
			if module.Replace.Version == "" {
				label += " (local replacement)"
			} else {
				label += " => " + moduleLabel(module.Replace)
			}
		}
		if root == "" {
			// Vendored packages report their directory but omit Module.Dir.
			root = pkg.Dir
			if rel, ok := strings.CutPrefix(pkg.ImportPath, module.Path+"/"); ok {
				for range strings.SplitSeq(rel, "/") {
					root = filepath.Dir(root)
				}
			}
		}
		source = &licenseSource{name: module.Path, label: label, root: root, dirs: map[string]bool{root: true}}
		sources[module.Path] = source
	}
	source.firstParty = source.firstParty || !pkg.DepOnly
	source.addDirs(pkg.Dir)
	for _, file := range pkg.EmbedFiles {
		source.addDirs(filepath.Join(pkg.Dir, filepath.Dir(file)))
	}
}

func moduleLabel(module *licenseModule) string {
	if module.Version == "" {
		return module.Path
	}
	return module.Path + " " + module.Version
}

func (source *licenseSource) addDirs(dir string) {
	rel, err := filepath.Rel(source.root, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return
	}
	for dir != source.root && !source.dirs[dir] {
		source.dirs[dir] = true
		dir = filepath.Dir(dir)
	}
}

func (p *project) renderLicenseNotices(ctx context.Context, sources map[string]*licenseSource, env []string) (string, error) {
	classifier, err := licenseClassifier()
	if err != nil {
		return "", err
	}
	var out strings.Builder
	out.WriteString("Third-party license notices\n")
	for _, setting := range env {
		switch setting {
		case "GOOS=android":
			if sources[nsgoModule] != nil {
				out.WriteString("NSGo's license below covers its Android Java runtime (classes.dex).\n")
			}
		case "GOOS=js":
			out.WriteString("The Go license below covers wasm_exec.js.\n")
			if sources[nsgoModule] != nil {
				out.WriteString("NSGo's license covers its bundled JavaScript runtime.\n")
			}
		}
	}
	var violations []error
	for _, name := range slices.Sorted(maps.Keys(sources)) {
		source := sources[name]
		var section strings.Builder
		section.WriteString("\n" + source.label + "\n" + strings.Repeat("-", len(source.label)) + "\n")
		types := map[string]bool{}
		var unknown []string
		var documents int
		seenFiles := map[string]bool{}
		markSeen := func(path string) bool {
			clean := filepath.Clean(path)
			realPath, err := filepath.EvalSymlinks(clean)
			if err != nil {
				realPath = clean
			}
			if seenFiles[clean] || seenFiles[realPath] {
				return false
			}
			seenFiles[clean] = true
			seenFiles[realPath] = true
			return true
		}
		displayName := func(path string) string {
			if rel, err := filepath.Rel(source.root, path); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
				return filepath.ToSlash(rel)
			}
			if realRoot, err := filepath.EvalSymlinks(source.root); err == nil {
				if realPath, err := filepath.EvalSymlinks(path); err == nil {
					if rel, err := filepath.Rel(realRoot, realPath); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
						return filepath.ToSlash(rel)
					}
				}
			}
			return filepath.Base(path)
		}
		appendDoc := func(path string, primary bool) error {
			if !markSeen(path) {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("read license for %s: %w", source.name, err)
			}
			filename := displayName(path)
			known := false
			for _, match := range classifier.Match(data).Matches {
				if match.MatchType == "License" {
					types[match.Name] = true
					known = true
				}
			}
			if primary && !known {
				unknown = append(unknown, filename)
			}
			fmt.Fprintf(&section, "\n%s\n\n", filename)
			section.Write(data)
			documents++
			if !bytes.HasSuffix(data, []byte("\n")) {
				section.WriteByte('\n')
			}
			return nil
		}
		for _, dir := range slices.Sorted(maps.Keys(source.dirs)) {
			files, err := os.ReadDir(dir)
			if err != nil {
				return "", fmt.Errorf("read licenses for %s: %w", source.name, err)
			}
			for _, f := range files {
				primary, include := isLicenseDocument(f.Name())
				if f.IsDir() || !include {
					continue
				}
				if err := appendDoc(filepath.Join(dir, f.Name()), primary); err != nil {
					return "", err
				}
			}
		}
		for _, path := range slices.Sorted(maps.Keys(source.extraFiles)) {
			if err := appendDoc(path, !isNoticeOnlyDocument(filepath.Base(path))); err != nil {
				return "", err
			}
		}
		if source.firstParty && documents == 0 {
			continue
		}
		if !source.firstParty && (len(types) == 0 || len(unknown) > 0) {
			reason := "has no recognized license text"
			if len(unknown) > 0 {
				reason = "has unrecognized license text in " + strings.Join(unknown, ", ")
			}
			if err := p.Licenses.report(ctx, name, "unknown", reason); err != nil {
				violations = append(violations, err)
			}
			section.WriteString("License identification incomplete.\n")
		}
		var copyleft []string
		for _, id := range slices.Sorted(maps.Keys(types)) {
			if copyleftLicense(id) {
				copyleft = append(copyleft, id)
			}
		}
		if len(copyleft) > 0 && !source.firstParty {
			if err := p.Licenses.report(ctx, name, "copyleft", "has copyleft license text ("+strings.Join(copyleft, ", ")+")"); err != nil {
				violations = append(violations, err)
			}
		}
		out.WriteString(section.String())
	}
	return out.String(), errors.Join(violations...)
}

func isLicenseDocument(name string) (primary, include bool) {
	name = strings.ToUpper(name)
	prefix, _, _ := strings.Cut(name, ".")
	prefix, _, _ = strings.Cut(prefix, "-")
	switch prefix {
	case "LICENSE", "LICENCE", "COPYING", "UNLICENSE", "OFL":
		return true, true
	case "NOTICE", "NOTICES", "COPYRIGHT", "PATENTS":
		return false, true
	}
	return false, false
}

func isNoticeOnlyDocument(name string) bool {
	primary, include := isLicenseDocument(name)
	if include {
		return !primary
	}
	upper := strings.ToUpper(strings.TrimSuffix(name, filepath.Ext(name)))
	for _, token := range strings.FieldsFunc(upper, func(r rune) bool {
		return r == '-' || r == '_' || r == '.' || r == ' '
	}) {
		switch token {
		case "LICENSE", "LICENCE", "COPYING", "UNLICENSE", "OFL":
			return false
		case "NOTICE", "NOTICES", "COPYRIGHT", "PATENTS":
			return true
		}
	}
	return false
}

func copyleftLicense(id string) bool {
	family, _, _ := strings.Cut(id, "-")
	switch family {
	case "AGPL", "APSL", "CDDL", "CPL", "EPL", "EUPL", "GPL", "LGPL", "MPL", "OSL", "SSPL":
		return true
	}
	return false
}

func (policy licensePolicy) report(ctx context.Context, module, kind, reason string) error {
	if slices.Contains(policy.Allow, module) {
		return nil
	}
	if kind == "unknown" && policy.Unknown != "warn" || kind == "copyleft" && policy.Copyleft == "error" {
		return fmt.Errorf("%s %s; set licenses.%s to warn or add the module to licenses.allow", module, reason, kind)
	}
	warnIn(ctx, "%s %s", module, reason)
	return nil
}
