package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"unicode"

	"github.com/nsgo-dev/nsgo/cmd/nsgo/apk"
)

// Native libraries the app's packages ship: NSGo's own libghostty-vt for
// base/terminal, and third-party Go packages declaring nsgo.natives.yaml in
// their package directory. Each target build discovers linked packages via
// go list -deps, verifies local or cached HTTPS libraries against SHA-256,
// stages them into the target's package, and signs them before the app.

const (
	// ghosttyPackage is the package holding libghostty-vt's builds.
	ghosttyPackage = "github.com/nsgo-dev/nsgo/base/terminal/internal/library"

	// ghosttyFramework is libghostty-vt as an iOS app's framework, where the
	// terminal looks for it.
	ghosttyFramework = "GhosttyVT.framework"

	// nativeManifestName is the YAML manifest a Go package places in its
	// directory to ship native libraries with apps that link it.
	nativeManifestName = "nsgo.natives.yaml"

	// nativeManifestVersion is the only supported schema version for
	// nsgo.natives.yaml.
	nativeManifestVersion = 1
)

// nativeTargets are the target names accepted in nsgo.natives.yaml.
var nativeTargets = []string{
	"macos_arm64", "macos_x64",
	"windows_arm64", "windows_x64",
	"linux_arm64", "linux_x64",
	"ios", "ios_simulator",
	"tvos", "tvos_simulator",
	"android_arm64", "android_x64",
}

// nativeManifest is a package's nsgo.natives.yaml.
type nativeManifest struct {
	Version  int                            `yaml:"version"`
	Licenses []string                       `yaml:"licenses"`
	Targets  map[string][]nativeLibrarySpec `yaml:"targets"`
}

// nativeLibrarySpec declares one native library file for a target.
type nativeLibrarySpec struct {
	Name      string   `yaml:"name"`
	URL       string   `yaml:"url"`
	Path      string   `yaml:"path"`
	SHA256    string   `yaml:"sha256"`
	Framework string   `yaml:"framework"`
	Licenses  []string `yaml:"licenses"`
}

// linkedPackage is a non-standard Go package linked by a build.
type linkedPackage struct {
	importPath string
	dir        string
}

// resolvedNativeLibrary is one validated library entry from a linked package
// for a concrete build target.
type resolvedNativeLibrary struct {
	pkg      string
	dir      string
	target   string
	spec     nativeLibrarySpec
	licenses []string
}

// nativeHTTPClient overrides the HTTP client used to fetch remote native
// libraries in tests. When nil, defaultNativeHTTPClient is used.
var nativeHTTPClient *http.Client

var defaultNativeHTTPClient = &http.Client{
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		if req.URL.Scheme != "https" {
			return fmt.Errorf("refusing redirect to non-https URL %q", req.URL.String())
		}
		return nil
	},
}

// nativeFetching serializes cache lookups and downloads across concurrent
// target builds in buildPackages.
var nativeFetching sync.Mutex

// parseNativeManifest decodes and validates nsgo.natives.yaml. When pkgDir is
// non-empty, local library and license paths are also resolved and checked to
// stay inside pkgDir after resolving symlinks.
func parseNativeManifest(data []byte, pkgDir string) (*nativeManifest, error) {
	var m nativeManifest
	if err := decodeStrictYAML(data, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", nativeManifestName, err)
	}
	if m.Version != nativeManifestVersion {
		return nil, fmt.Errorf("%s: unsupported version %d (expected %d)", nativeManifestName, m.Version, nativeManifestVersion)
	}
	if len(m.Targets) == 0 {
		return nil, fmt.Errorf("%s: targets must declare at least one target", nativeManifestName)
	}
	for i, lic := range m.Licenses {
		if err := validateNativeRelPath(lic); err != nil {
			return nil, fmt.Errorf("%s: licenses[%d]: %w", nativeManifestName, i, err)
		}
		if pkgDir != "" {
			if _, err := resolvePackageFile(pkgDir, lic); err != nil {
				return nil, fmt.Errorf("%s: licenses[%d]: %w", nativeManifestName, i, err)
			}
		}
	}
	for _, target := range slices.Sorted(maps.Keys(m.Targets)) {
		if err := validateNativeTarget(target); err != nil {
			return nil, fmt.Errorf("%s: %w", nativeManifestName, err)
		}
		specs := m.Targets[target]
		if len(specs) == 0 {
			return nil, fmt.Errorf("%s: targets.%s must list at least one library", nativeManifestName, target)
		}
		seenNames := make(map[string]string, len(specs))
		seenFrameworks := make(map[string]string, len(specs))
		for i := range specs {
			spec := &specs[i]
			prefix := fmt.Sprintf("%s: targets.%s[%d]", nativeManifestName, target, i)
			if err := validateNativeLibraryName(spec.Name); err != nil {
				return nil, fmt.Errorf("%s: name: %w", prefix, err)
			}
			key := strings.ToLower(spec.Name)
			if prev, dup := seenNames[key]; dup {
				return nil, fmt.Errorf("%s: duplicate library name %q (conflicts with %q)", prefix, spec.Name, prev)
			}
			seenNames[key] = spec.Name

			hasURL := spec.URL != ""
			hasPath := spec.Path != ""
			switch {
			case !hasURL && !hasPath:
				return nil, fmt.Errorf("%s (%s): set either url or path", prefix, spec.Name)
			case hasURL && hasPath:
				return nil, fmt.Errorf("%s (%s): set either url or path, not both", prefix, spec.Name)
			case hasURL:
				if err := validateNativeURL(spec.URL); err != nil {
					return nil, fmt.Errorf("%s (%s): url: %w", prefix, spec.Name, err)
				}
				if spec.SHA256 == "" {
					return nil, fmt.Errorf("%s (%s): sha256 is required when url is set", prefix, spec.Name)
				}
				sum, err := normalizeSHA256(spec.SHA256)
				if err != nil {
					return nil, fmt.Errorf("%s (%s): sha256: %w", prefix, spec.Name, err)
				}
				spec.SHA256 = sum
			case hasPath:
				if err := validateNativeRelPath(spec.Path); err != nil {
					return nil, fmt.Errorf("%s (%s): path: %w", prefix, spec.Name, err)
				}
				if pkgDir != "" {
					if _, err := resolvePackageFile(pkgDir, spec.Path); err != nil {
						return nil, fmt.Errorf("%s (%s): path: %w", prefix, spec.Name, err)
					}
				}
				if spec.SHA256 != "" {
					sum, err := normalizeSHA256(spec.SHA256)
					if err != nil {
						return nil, fmt.Errorf("%s (%s): sha256: %w", prefix, spec.Name, err)
					}
					spec.SHA256 = sum
				}
			}

			if isAppleMobileNativeTarget(target) {
				fw, err := nativeFrameworkName(*spec)
				if err != nil {
					return nil, fmt.Errorf("%s (%s): framework: %w", prefix, spec.Name, err)
				}
				fwKey := strings.ToLower(fw)
				if prev, dup := seenFrameworks[fwKey]; dup {
					return nil, fmt.Errorf("%s (%s): duplicate framework name %q (conflicts with %q)", prefix, spec.Name, fw, prev)
				}
				seenFrameworks[fwKey] = fw
			} else if spec.Framework != "" {
				return nil, fmt.Errorf("%s (%s): framework is only valid on iOS and tvOS targets", prefix, spec.Name)
			}

			for j, lic := range spec.Licenses {
				if err := validateNativeRelPath(lic); err != nil {
					return nil, fmt.Errorf("%s (%s): licenses[%d]: %w", prefix, spec.Name, j, err)
				}
				if pkgDir != "" {
					if _, err := resolvePackageFile(pkgDir, lic); err != nil {
						return nil, fmt.Errorf("%s (%s): licenses[%d]: %w", prefix, spec.Name, j, err)
					}
				}
			}
		}
		m.Targets[target] = specs
	}
	return &m, nil
}

// loadNativeManifest reads and validates nsgo.natives.yaml in pkgDir.
// It returns (nil, false, nil) when the directory has no manifest.
func loadNativeManifest(pkgDir string) (*nativeManifest, bool, error) {
	data, err := os.ReadFile(filepath.Join(pkgDir, nativeManifestName))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	m, err := parseNativeManifest(data, pkgDir)
	if err != nil {
		return nil, false, err
	}
	return m, true, nil
}

func validateNativeTarget(target string) error {
	if slices.Contains(nativeTargets, target) {
		return nil
	}
	switch target {
	case "macos_universal":
		return fmt.Errorf("unknown target %q; declare macos_arm64 and macos_x64 instead (macos_universal joins them with lipo)", target)
	case "android_universal":
		return fmt.Errorf("unknown target %q; declare android_arm64 and android_x64 instead", target)
	default:
		return fmt.Errorf("unknown target %q; use one of %s", target, strings.Join(nativeTargets, ", "))
	}
}

func validateNativeLibraryName(name string) error {
	if name == "" {
		return errors.New("must not be empty")
	}
	if strings.TrimSpace(name) != name {
		return fmt.Errorf("%q must not have leading or trailing whitespace", name)
	}
	if name == "." || name == ".." {
		return fmt.Errorf("%q is not a valid library file name", name)
	}
	if strings.ContainsAny(name, `/\:`+"\x00") || filepath.Base(name) != name || filepath.IsAbs(name) {
		return fmt.Errorf("%q must be a single file name without path separators", name)
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return fmt.Errorf("%q contains a control character", name)
		}
	}
	return nil
}

func validateNativeURL(raw string) error {
	if strings.TrimSpace(raw) != raw {
		return fmt.Errorf("%q must not have leading or trailing whitespace", raw)
	}
	for _, r := range raw {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return fmt.Errorf("%q must not contain spaces or control characters", raw)
		}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("%q must be an https URL with a host", raw)
	}
	return nil
}

func validateNativeRelPath(p string) error {
	if p == "" {
		return errors.New("must not be empty")
	}
	if strings.TrimSpace(p) != p {
		return fmt.Errorf("%q must not have leading or trailing whitespace", p)
	}
	if strings.ContainsAny(p, `\:`+"\x00") {
		return fmt.Errorf("%q must be a relative slash-separated path", p)
	}
	for _, r := range p {
		if unicode.IsControl(r) {
			return fmt.Errorf("%q contains a control character", p)
		}
	}
	if strings.HasPrefix(p, "/") || filepath.IsAbs(p) {
		return fmt.Errorf("%q must be relative to the package directory", p)
	}
	for seg := range strings.SplitSeq(p, "/") {
		if seg == ".." {
			return fmt.Errorf("%q must stay inside the package directory", p)
		}
	}
	cleaned := path.Clean(p)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return fmt.Errorf("%q must name a file inside the package directory", p)
	}
	return nil
}

// resolvePackageFile resolves relPath inside pkgDir, rejecting path traversal,
// symlink escapes and directories.
func resolvePackageFile(pkgDir, relPath string) (string, error) {
	if err := validateNativeRelPath(relPath); err != nil {
		return "", err
	}
	realDir, err := filepath.EvalSymlinks(pkgDir)
	if err != nil {
		return "", err
	}
	joined := filepath.Join(pkgDir, filepath.FromSlash(path.Clean(relPath)))
	realPath, err := filepath.EvalSymlinks(joined)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(realDir, realPath)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("%q escapes the package directory", relPath)
	}
	info, err := os.Stat(realPath)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%q is not a regular file", relPath)
	}
	return realPath, nil
}

func normalizeSHA256(s string) (string, error) {
	if len(s) != sha256.Size*2 {
		return "", fmt.Errorf("expected 64 hex characters, got %d", len(s))
	}
	if _, err := hex.DecodeString(s); err != nil {
		return "", fmt.Errorf("must be 64 hex characters: %w", err)
	}
	return strings.ToLower(s), nil
}

func isAppleMobileNativeTarget(target string) bool {
	switch target {
	case "ios", "ios_simulator", "tvos", "tvos_simulator":
		return true
	}
	return false
}

func nativeFrameworkName(spec nativeLibrarySpec) (string, error) {
	name := spec.Framework
	if name == "" {
		base := spec.Name
		for _, ext := range []string{".dylib", ".so"} {
			if trimmed, ok := strings.CutSuffix(base, ext); ok {
				base = trimmed
				break
			}
		}
		base = strings.TrimPrefix(base, "lib")
		name = base
	}
	if name == "" {
		return "", errors.New("set framework to a valid framework name")
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return "", fmt.Errorf("%q must use ASCII letters, digits, underscores or hyphens", name)
		}
	}
	return name, nil
}

func sanitizeBundleIDComponent(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "native"
	}
	return strings.ToLower(out)
}

// linkedDir returns the directory of package pkg when the app's build for env, with the tool's tags extra, links it, else "".
func linkedDir(ctx context.Context, p *project, env []string, pkg string, extra ...string) (string, error) {
	args := []string{"list", "-deps", "-f", fmt.Sprintf(`{{if eq .ImportPath %q}}{{.Dir}}{{end}}`, pkg)}
	if p.testBinary {
		args = append(args, "-test")
		extra = append(extra[:len(extra):len(extra)], "nsgotest")
	}
	tags, err := goTags(ctx, p, env, extra...)
	if err != nil {
		return "", err
	}
	args = append(args, tagsArgs(tags)...)
	cmd := exec.CommandContext(ctx, "go", append(args, ".")...)
	cmd.Dir = p.Dir
	cmd.Env = append(os.Environ(), env...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("go list: %w\n%s", err, stderr.Bytes())
	}
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line, nil
		}
	}
	return "", nil
}

// linkedPackages returns the non-standard Go packages the app links for env
// with the tool's tags extra, deduplicated by directory and sorted by import
// path for deterministic builds.
func linkedPackages(ctx context.Context, p *project, env []string, extra ...string) ([]linkedPackage, error) {
	args := []string{"list", "-deps", "-f", `{{if not .Standard}}{{.ImportPath}}	{{.Dir}}{{end}}`}
	extraTags := append([]string(nil), extra...)
	if p.testBinary {
		args = append(args, "-test")
		extraTags = append(extraTags, "nsgotest")
	}
	if p.Dev.Client {
		extraTags = append(extraTags, "nsgodev")
		if p.Dev.Automation {
			extraTags = append(extraTags, "nsgoautomation")
		}
	}
	tags, err := goTags(ctx, p, env, extraTags...)
	if err != nil {
		return nil, err
	}
	args = append(args, tagsArgs(tags)...)
	cmd := exec.CommandContext(ctx, "go", append(args, ".")...)
	cmd.Dir = p.Dir
	cmd.Env = append(os.Environ(), env...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list: %w\n%s", err, stderr.Bytes())
	}
	byDir := map[string]linkedPackage{}
	for line := range strings.SplitSeq(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		imp, dir, ok := strings.Cut(line, "\t")
		if !ok || dir == "" {
			continue
		}
		cleanDir := filepath.Clean(dir)
		cur, exists := byDir[cleanDir]
		if !exists || preferImportPath(imp, cur.importPath) {
			byDir[cleanDir] = linkedPackage{importPath: imp, dir: cleanDir}
		}
	}
	pkgs := make([]linkedPackage, 0, len(byDir))
	for _, lp := range byDir {
		pkgs = append(pkgs, lp)
	}
	slices.SortFunc(pkgs, func(a, b linkedPackage) int {
		if c := strings.Compare(a.importPath, b.importPath); c != 0 {
			return c
		}
		return strings.Compare(a.dir, b.dir)
	})
	return pkgs, nil
}

func preferImportPath(cand, cur string) bool {
	candSynth := strings.Contains(cand, " [") || strings.HasSuffix(cand, ".test")
	curSynth := strings.Contains(cur, " [") || strings.HasSuffix(cur, ".test")
	if candSynth != curSynth {
		return !candSynth
	}
	return cand < cur
}

func findLinkedDir(pkgs []linkedPackage, importPath string) string {
	for _, lp := range pkgs {
		if lp.importPath == importPath {
			return lp.dir
		}
	}
	return ""
}

// discoverNativeLibraries reads nsgo.natives.yaml from each linked package in
// deterministic order, validates cross-package collisions, and returns the
// libraries declared for target.
func discoverNativeLibraries(pkgs []linkedPackage, target string) ([]resolvedNativeLibrary, error) {
	seenNames := map[string]string{}
	seenFrameworks := map[string]string{}
	var out []resolvedNativeLibrary
	for _, lp := range pkgs {
		m, ok, err := loadNativeManifest(lp.dir)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", lp.importPath, err)
		}
		if !ok {
			continue
		}
		specs := m.Targets[target]
		if len(specs) == 0 {
			continue
		}
		for _, spec := range specs {
			key := strings.ToLower(spec.Name)
			if prev, dup := seenNames[key]; dup {
				return nil, fmt.Errorf("native library %q for %s declared by both %s and %s", spec.Name, target, prev, lp.importPath)
			}
			seenNames[key] = lp.importPath
			if isAppleMobileNativeTarget(target) {
				fw, err := nativeFrameworkName(spec)
				if err != nil {
					return nil, fmt.Errorf("%s: %w", lp.importPath, err)
				}
				fwKey := strings.ToLower(fw)
				if prev, dup := seenFrameworks[fwKey]; dup {
					return nil, fmt.Errorf("native framework %q for %s declared by both %s and %s", fw, target, prev, lp.importPath)
				}
				seenFrameworks[fwKey] = lp.importPath
			}
			licenses, err := collectManifestLicenses(lp.dir, m.Licenses, spec.Licenses)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", lp.importPath, err)
			}
			out = append(out, resolvedNativeLibrary{
				pkg:      lp.importPath,
				dir:      lp.dir,
				target:   target,
				spec:     spec,
				licenses: licenses,
			})
		}
	}
	return out, nil
}

func collectManifestLicenses(pkgDir string, lists ...[]string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, list := range lists {
		for _, rel := range list {
			abs, err := resolvePackageFile(pkgDir, rel)
			if err != nil {
				return nil, err
			}
			if !seen[abs] {
				seen[abs] = true
				out = append(out, abs)
			}
		}
	}
	return out, nil
}

// resolveNativeLibrary returns the local file path of lib, verifying SHA-256
// and downloading into the user cache when lib declares a remote URL.
func resolveNativeLibrary(ctx context.Context, lib resolvedNativeLibrary) (string, error) {
	if lib.spec.Path != "" {
		src, err := resolvePackageFile(lib.dir, lib.spec.Path)
		if err != nil {
			return "", fmt.Errorf("%s (%s): %w", lib.pkg, lib.spec.Name, err)
		}
		if lib.spec.SHA256 != "" {
			data, err := os.ReadFile(src)
			if err != nil {
				return "", fmt.Errorf("%s (%s): %w", lib.pkg, lib.spec.Name, err)
			}
			if got := sha256Hex(data); got != lib.spec.SHA256 {
				return "", fmt.Errorf("%s (%s): %s has SHA-256 %s, want %s", lib.pkg, lib.spec.Name, lib.spec.Path, got, lib.spec.SHA256)
			}
		}
		return src, nil
	}
	return fetchNativeLibrary(ctx, lib)
}

func fetchNativeLibrary(ctx context.Context, lib resolvedNativeLibrary) (string, error) {
	nativeFetching.Lock()
	defer nativeFetching.Unlock()
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(cache, "nsgo", "natives", lib.spec.SHA256)
	dst := filepath.Join(dir, lib.spec.Name)
	if data, err := os.ReadFile(dst); err == nil && sha256Hex(data) == lib.spec.SHA256 {
		return dst, nil
	}
	stage(ctx, "downloading %s", lib.spec.Name)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, lib.spec.URL, nil)
	if err != nil {
		return "", fmt.Errorf("%s (%s): %w", lib.pkg, lib.spec.Name, err)
	}
	client := nativeHTTPClient
	if client == nil {
		client = defaultNativeHTTPClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("%s (%s): %w", lib.pkg, lib.spec.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s (%s): %s answered %s", lib.pkg, lib.spec.Name, lib.spec.URL, resp.Status)
	}
	if resp.Request != nil && resp.Request.URL != nil && resp.Request.URL.Scheme != "https" {
		return "", fmt.Errorf("%s (%s): %s redirected to non-https URL %s", lib.pkg, lib.spec.Name, lib.spec.URL, resp.Request.URL.String())
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("%s (%s): %w", lib.pkg, lib.spec.Name, err)
	}
	if got := sha256Hex(data); got != lib.spec.SHA256 {
		return "", fmt.Errorf("%s (%s): %s has SHA-256 %s, want %s", lib.pkg, lib.spec.Name, lib.spec.URL, got, lib.spec.SHA256)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, ".download-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return "", err
	}
	return dst, nil
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// checkNoFileCollision refuses to overwrite an existing file in dir (checked
// case-insensitively so cross-compiles catch Windows and macOS collisions).
func checkNoFileCollision(dir, name, pkg string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if strings.EqualFold(entry.Name(), name) {
			return fmt.Errorf("%s: native library %q conflicts with existing file %q in package", pkg, name, entry.Name())
		}
	}
	return nil
}

// isMacNativeLibrary reports whether path is a dynamic library placed in a
// macOS app's Contents/Frameworks that Lipo and codesign process.
func isMacNativeLibrary(path string) bool {
	return strings.HasSuffix(path, ".dylib") || strings.HasSuffix(path, ".so")
}

// macFrameworkLibraries returns the dynamic libraries in app's
// Contents/Frameworks in deterministic sorted order.
func macFrameworkLibraries(app string) []string {
	entries, err := os.ReadDir(filepath.Join(app, "Contents", "Frameworks"))
	if err != nil {
		return nil
	}
	var libs []string
	for _, entry := range entries {
		if entry.IsDir() || !isMacNativeLibrary(entry.Name()) {
			continue
		}
		libs = append(libs, filepath.Join(app, "Contents", "Frameworks", entry.Name()))
	}
	slices.Sort(libs)
	return libs
}

// addMacLibraries puts the native libraries the app links for arch into
// its Contents/Frameworks, signed ad hoc as the app is; the app's own
// signature then seals them.
func addMacLibraries(ctx context.Context, p *project, app, arch string) error {
	env := []string{"GOOS=darwin", "GOARCH=" + arch, "CGO_ENABLED=0"}
	pkgs, err := linkedPackages(ctx, p, env)
	if err != nil {
		return err
	}
	frameworks := filepath.Join(app, "Contents", "Frameworks")
	if dir := findLinkedDir(pkgs, ghosttyPackage); dir != "" {
		dst := filepath.Join(frameworks, "libghostty-vt.dylib")
		if err := copyLibrary(filepath.Join(dir, "lib", "darwin-"+arch, "libghostty-vt.dylib"), dst); err != nil {
			return err
		}
		if err := run(ctx, "codesign", "--force", "--sign", "-", dst); err != nil {
			return err
		}
	}
	libs, err := discoverNativeLibraries(pkgs, "macos_"+goArchName(arch))
	if err != nil {
		return err
	}
	for _, lib := range libs {
		if err := checkNoFileCollision(frameworks, lib.spec.Name, lib.pkg); err != nil {
			return err
		}
		src, err := resolveNativeLibrary(ctx, lib)
		if err != nil {
			return err
		}
		dst := filepath.Join(frameworks, lib.spec.Name)
		if err := copyLibrary(src, dst); err != nil {
			return err
		}
		if err := run(ctx, "codesign", "--force", "--sign", "-", dst); err != nil {
			return err
		}
	}
	return nil
}

// addWindowsLibraries puts third-party native libraries the app links for arch
// beside its Windows executable.
func addWindowsLibraries(ctx context.Context, p *project, dir, arch string) error {
	env := []string{"GOOS=windows", "GOARCH=" + arch, "CGO_ENABLED=0"}
	pkgs, err := linkedPackages(ctx, p, env)
	if err != nil {
		return err
	}
	libs, err := discoverNativeLibraries(pkgs, "windows_"+goArchName(arch))
	if err != nil {
		return err
	}
	for _, lib := range libs {
		if err := checkNoFileCollision(dir, lib.spec.Name, lib.pkg); err != nil {
			return err
		}
		src, err := resolveNativeLibrary(ctx, lib)
		if err != nil {
			return err
		}
		if err := copyLibrary(src, filepath.Join(dir, lib.spec.Name)); err != nil {
			return err
		}
	}
	return nil
}

// addLinuxLibraries puts third-party native libraries the app links for arch
// beside its Linux executable.
func addLinuxLibraries(ctx context.Context, p *project, dir, arch string) error {
	env := []string{"GOOS=linux", "GOARCH=" + arch, "CGO_ENABLED=0"}
	pkgs, err := linkedPackages(ctx, p, env)
	if err != nil {
		return err
	}
	libs, err := discoverNativeLibraries(pkgs, "linux_"+goArchName(arch))
	if err != nil {
		return err
	}
	for _, lib := range libs {
		if err := checkNoFileCollision(dir, lib.spec.Name, lib.pkg); err != nil {
			return err
		}
		src, err := resolveNativeLibrary(ctx, lib)
		if err != nil {
			return err
		}
		if err := copyLibrary(src, filepath.Join(dir, lib.spec.Name)); err != nil {
			return err
		}
	}
	return nil
}

// addAndroidLibraries returns APK/AAB lib/<abi>/<name> files for third-party
// native libraries the app links for goarch.
func addAndroidLibraries(ctx context.Context, p *project, env []string, goarch, abi string) ([]apk.File, error) {
	pkgs, err := linkedPackages(ctx, p, env)
	if err != nil {
		return nil, err
	}
	libs, err := discoverNativeLibraries(pkgs, "android_"+goArchName(goarch))
	if err != nil {
		return nil, err
	}
	var files []apk.File
	for _, lib := range libs {
		if strings.EqualFold(lib.spec.Name, "libnsgo.so") {
			return nil, fmt.Errorf("%s: native library %q conflicts with NSGo's Android runtime binary", lib.pkg, lib.spec.Name)
		}
		src, err := resolveNativeLibrary(ctx, lib)
		if err != nil {
			return nil, err
		}
		data, err := os.ReadFile(src)
		if err != nil {
			return nil, err
		}
		files = append(files, apk.File{Name: "lib/" + abi + "/" + lib.spec.Name, Data: data})
	}
	return files, nil
}

// addIOSFrameworks puts the native libraries the app links for sys into its
// Frameworks as frameworks: signed ad hoc for the simulator, left for the
// app's signing on devices (signFrameworks).
func addIOSFrameworks(ctx context.Context, p *project, sys *appleOS, app string, env []string, simulator bool) error {
	pkgs, err := linkedPackages(ctx, p, env)
	if err != nil {
		return err
	}
	if dir := findLinkedDir(pkgs, ghosttyPackage); dir != "" {
		build := sys.target + "-arm64"
		if simulator {
			build = sys.target + "simulator-arm64"
		}
		lib := filepath.Join(dir, "lib", build, "libghostty-vt.dylib")
		if _, err := os.Stat(lib); err != nil && sys != iOS {
			// tools/libghostty builds no tvOS library yet; the terminal then
			// reports it missing.
			detailIn(ctx, "no libghostty-vt for %s: the terminal will not start", sys.name)
		} else if err := writeIOSNativeFramework(ctx, p, sys, app, "GhosttyVT", p.ID+".ghosttyvt", lib, simulator); err != nil {
			return err
		}
	}
	target := sys.target
	if simulator {
		target += "_simulator"
	}
	libs, err := discoverNativeLibraries(pkgs, target)
	if err != nil {
		return err
	}
	frameworksDir := filepath.Join(app, "Frameworks")
	for _, lib := range libs {
		fwName, err := nativeFrameworkName(lib.spec)
		if err != nil {
			return fmt.Errorf("%s: %w", lib.pkg, err)
		}
		if err := checkNoFileCollision(frameworksDir, fwName+".framework", lib.pkg); err != nil {
			return err
		}
		src, err := resolveNativeLibrary(ctx, lib)
		if err != nil {
			return err
		}
		bundleID := p.ID + "." + sanitizeBundleIDComponent(fwName)
		if err := writeIOSNativeFramework(ctx, p, sys, app, fwName, bundleID, src, simulator); err != nil {
			return err
		}
	}
	return nil
}

func writeIOSNativeFramework(ctx context.Context, p *project, sys *appleOS, app, fwName, bundleID, src string, simulator bool) error {
	fwDirName := fwName + ".framework"
	fw := filepath.Join(app, "Frameworks", fwDirName)
	binary := filepath.Join(fw, fwName)
	if err := copyLibrary(src, binary); err != nil {
		return err
	}
	if err := run(ctx, "install_name_tool", "-id", "@rpath/"+fwDirName+"/"+fwName, binary); err != nil {
		return err
	}
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleExecutable</key><string>` + xmlText(fwName) + `</string>
	<key>CFBundleIdentifier</key><string>` + xmlText(bundleID) + `</string>
	<key>CFBundleName</key><string>` + xmlText(fwName) + `</string>
	<key>CFBundlePackageType</key><string>FMWK</string>
	<key>CFBundleShortVersionString</key><string>1.0</string>
	<key>CFBundleVersion</key><string>1</string>
	<key>CFBundleSupportedPlatforms</key><array><string>` + sys.platformName(simulator) + `</string></array>
	<key>MinimumOSVersion</key><string>` + sys.minimum + `</string>
</dict>
</plist>
`
	if err := os.WriteFile(filepath.Join(fw, "Info.plist"), []byte(plist), 0o644); err != nil {
		return err
	}
	if simulator {
		return run(ctx, "codesign", "--force", "--sign", "-", fw)
	}
	return nil
}

// signFrameworks signs the frameworks of an iOS app with identity, before
// the app is, which seals them.
func signFrameworks(ctx context.Context, app, identity string) error {
	fws, _ := filepath.Glob(filepath.Join(app, "Frameworks", "*.framework"))
	for _, fw := range fws {
		if err := run(ctx, "codesign", "--force", "--sign", identity, fw); err != nil {
			return err
		}
	}
	return nil
}

// copyLibrary copies a native library to dst, making its directory.
func copyLibrary(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o755)
}
