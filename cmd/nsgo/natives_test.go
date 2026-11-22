package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/licenseclassifier/v2/assets"
)

func isolateCacheEnv(t *testing.T) {
	t.Helper()
	cacheRoot := t.TempDir()
	t.Setenv("HOME", cacheRoot)
	t.Setenv("XDG_CACHE_HOME", cacheRoot)
	t.Setenv("LocalAppData", cacheRoot)
}

func withNativeHTTPClient(t *testing.T, client *http.Client) {
	t.Helper()
	prev := nativeHTTPClient
	nativeHTTPClient = client
	t.Cleanup(func() { nativeHTTPClient = prev })
}

func writeTestFiles(t *testing.T, files map[string][]byte) {
	t.Helper()
	for name, data := range files {
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestNativeManifestValid(t *testing.T) {
	dir := t.TempDir()
	libBytes := []byte("fake-windows-dll")
	libHash := sha256Hex(libBytes)
	writeTestFiles(t, map[string][]byte{
		filepath.Join(dir, "third_party", "LICENSE.txt"):  []byte("MIT"),
		filepath.Join(dir, "third_party", "NOTICE.txt"):   []byte("Notice"),
		filepath.Join(dir, "lib", "win-x64", "audio.dll"): libBytes,
	})
	yamlText := `
version: 1
licenses:
  - third_party/LICENSE.txt
targets:
  macos_arm64:
    - name: libaudio.dylib
      url: https://example.com/releases/libaudio-arm64.dylib
      sha256: ` + strings.ToUpper(libHash) + `
      licenses:
        - third_party/NOTICE.txt
  windows_x64:
    - name: audio.dll
      path: lib/win-x64/audio.dll
      sha256: ` + libHash + `
  ios:
    - name: libaudio.dylib
      url: https://example.com/releases/libaudio-ios.dylib
      sha256: ` + libHash + `
      framework: AudioEngine
`
	m, err := parseNativeManifest([]byte(yamlText), dir)
	if err != nil {
		t.Fatalf("parseNativeManifest failed: %v", err)
	}
	if m.Version != 1 {
		t.Fatalf("version = %d, want 1", m.Version)
	}
	if got := m.Targets["macos_arm64"][0].SHA256; got != libHash {
		t.Errorf("sha256 normalized = %q, want %q", got, libHash)
	}
	if got := m.Targets["ios"][0].Framework; got != "AudioEngine" {
		t.Errorf("framework = %q, want AudioEngine", got)
	}
}

func TestNativeManifestErrors(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.bin")
	libBytes := []byte("local-lib")
	libHash := sha256Hex(libBytes)
	writeTestFiles(t, map[string][]byte{
		outside:                           []byte("secret"),
		filepath.Join(dir, "ok.dylib"):    libBytes,
		filepath.Join(dir, "LICENSE.txt"): []byte("MIT"),
	})
	linkPath := filepath.Join(dir, "escape.dylib")
	if err := os.Symlink(outside, linkPath); err != nil {
		t.Fatal(err)
	}
	linkLicense := filepath.Join(dir, "escape-license.txt")
	if err := os.Symlink(outside, linkLicense); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		yaml string
		want string
	}{
		{
			name: "unknown top-level field",
			yaml: "version: 1\nextra: true\ntargets:\n  macos_arm64:\n    - name: liba.dylib\n      path: ok.dylib\n",
			want: "field extra not found",
		},
		{
			name: "unknown library field",
			yaml: "version: 1\ntargets:\n  macos_arm64:\n    - name: liba.dylib\n      path: ok.dylib\n      bogus: 1\n",
			want: "field bogus not found",
		},
		{
			name: "missing version",
			yaml: "targets:\n  macos_arm64:\n    - name: liba.dylib\n      path: ok.dylib\n",
			want: "unsupported version 0",
		},
		{
			name: "bad version",
			yaml: "version: 2\ntargets:\n  macos_arm64:\n    - name: liba.dylib\n      path: ok.dylib\n",
			want: "unsupported version 2",
		},
		{
			name: "multiple YAML documents",
			yaml: "version: 1\ntargets:\n  macos_arm64:\n    - name: liba.dylib\n      path: ok.dylib\n---\nversion: 1\n",
			want: "multiple YAML documents",
		},
		{
			name: "empty targets",
			yaml: "version: 1\ntargets: {}\n",
			want: "at least one target",
		},
		{
			name: "macos_universal rejected with hint",
			yaml: "version: 1\ntargets:\n  macos_universal:\n    - name: liba.dylib\n      path: ok.dylib\n",
			want: "declare macos_arm64 and macos_x64",
		},
		{
			name: "unknown target",
			yaml: "version: 1\ntargets:\n  darwin_arm64:\n    - name: liba.dylib\n      path: ok.dylib\n",
			want: "unknown target",
		},
		{
			name: "empty library list for target",
			yaml: "version: 1\ntargets:\n  macos_arm64: []\n",
			want: "at least one library",
		},
		{
			name: "neither url nor path",
			yaml: "version: 1\ntargets:\n  macos_arm64:\n    - name: liba.dylib\n",
			want: "set either url or path",
		},
		{
			name: "both url and path",
			yaml: "version: 1\ntargets:\n  macos_arm64:\n    - name: liba.dylib\n      url: https://example.com/liba.dylib\n      path: ok.dylib\n      sha256: " + libHash + "\n",
			want: "not both",
		},
		{
			name: "non-https url",
			yaml: "version: 1\ntargets:\n  macos_arm64:\n    - name: liba.dylib\n      url: http://example.com/liba.dylib\n      sha256: " + libHash + "\n",
			want: "must be an https URL",
		},
		{
			name: "url without sha256",
			yaml: "version: 1\ntargets:\n  macos_arm64:\n    - name: liba.dylib\n      url: https://example.com/liba.dylib\n",
			want: "sha256 is required",
		},
		{
			name: "bad sha256 length",
			yaml: "version: 1\ntargets:\n  macos_arm64:\n    - name: liba.dylib\n      url: https://example.com/liba.dylib\n      sha256: deadbeef\n",
			want: "expected 64 hex characters",
		},
		{
			name: "bad sha256 non-hex on local path",
			yaml: "version: 1\ntargets:\n  macos_arm64:\n    - name: liba.dylib\n      path: ok.dylib\n      sha256: " + strings.Repeat("z", 64) + "\n",
			want: "64 hex characters",
		},
		{
			name: "path traversal in library name",
			yaml: "version: 1\ntargets:\n  macos_arm64:\n    - name: ../liba.dylib\n      path: ok.dylib\n",
			want: "without path separators",
		},
		{
			name: "slash in library name",
			yaml: "version: 1\ntargets:\n  macos_arm64:\n    - name: sub/liba.dylib\n      path: ok.dylib\n",
			want: "without path separators",
		},
		{
			name: "path traversal in local path",
			yaml: "version: 1\ntargets:\n  macos_arm64:\n    - name: liba.dylib\n      path: ../outside.bin\n",
			want: "stay inside the package directory",
		},
		{
			name: "absolute local path",
			yaml: "version: 1\ntargets:\n  macos_arm64:\n    - name: liba.dylib\n      path: /etc/passwd\n",
			want: "must be relative",
		},
		{
			name: "symlink escape in local path",
			yaml: "version: 1\ntargets:\n  macos_arm64:\n    - name: liba.dylib\n      path: escape.dylib\n",
			want: "escapes the package directory",
		},
		{
			name: "path traversal in top-level license",
			yaml: "version: 1\nlicenses: [../LICENSE]\ntargets:\n  macos_arm64:\n    - name: liba.dylib\n      path: ok.dylib\n",
			want: "stay inside the package directory",
		},
		{
			name: "symlink escape in entry license",
			yaml: "version: 1\ntargets:\n  macos_arm64:\n    - name: liba.dylib\n      path: ok.dylib\n      licenses: [escape-license.txt]\n",
			want: "escapes the package directory",
		},
		{
			name: "duplicate library name case-insensitive",
			yaml: "version: 1\ntargets:\n  windows_x64:\n    - name: Audio.dll\n      path: ok.dylib\n    - name: audio.dll\n      path: ok.dylib\n",
			want: "duplicate library name",
		},
		{
			name: "framework on desktop target",
			yaml: "version: 1\ntargets:\n  macos_arm64:\n    - name: liba.dylib\n      path: ok.dylib\n      framework: Foo\n",
			want: "only valid on iOS and tvOS",
		},
		{
			name: "duplicate framework on ios",
			yaml: "version: 1\ntargets:\n  ios:\n    - name: liba.dylib\n      path: ok.dylib\n      framework: Same\n    - name: libb.dylib\n      path: ok.dylib\n      framework: same\n",
			want: "duplicate framework name",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseNativeManifest([]byte(tc.yaml), dir)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("parseNativeManifest err = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestNativeDiscoverFromGoList(t *testing.T) {
	t.Setenv("GOWORK", "off")
	t.Setenv("GOFLAGS", "")
	root := t.TempDir()
	appDir := filepath.Join(root, "app")
	pkgADir := filepath.Join(root, "depa")
	pkgBDir := filepath.Join(root, "depb")

	writeTestFiles(t, map[string][]byte{
		filepath.Join(appDir, "go.mod"):   []byte("module go.example/app\ngo 1.27.4\nrequire (\n\tgo.example/depa v0.0.0\n\tgo.example/depb v0.0.0\n)\nreplace (\n\tgo.example/depa => ../depa\n\tgo.example/depb => ../depb\n)\n"),
		filepath.Join(appDir, "main.go"):  []byte("package main\nimport _ \"go.example/depa\"\nfunc main() {}\n"),
		filepath.Join(appDir, "extra.go"): []byte("//go:build withdepb\n\npackage main\nimport _ \"go.example/depb\"\n"),

		filepath.Join(pkgADir, "go.mod"):           []byte("module go.example/depa\ngo 1.27.4\n"),
		filepath.Join(pkgADir, "a.go"):             []byte("package depa\n"),
		filepath.Join(pkgADir, "lib", "liba.so"):   []byte("a-so"),
		filepath.Join(pkgADir, nativeManifestName): []byte("version: 1\ntargets:\n  linux_x64:\n    - name: liba.so\n      path: lib/liba.so\n"),

		filepath.Join(pkgBDir, "go.mod"):           []byte("module go.example/depb\ngo 1.27.4\n"),
		filepath.Join(pkgBDir, "b.go"):             []byte("package depb\n"),
		filepath.Join(pkgBDir, "lib", "libb.so"):   []byte("b-so"),
		filepath.Join(pkgBDir, nativeManifestName): []byte("version: 1\ntargets:\n  linux_x64:\n    - name: libb.so\n      path: lib/libb.so\n"),
	})

	p := &project{Dir: appDir}
	env := []string{"GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0"}
	pkgs, err := linkedPackages(t.Context(), p, env)
	if err != nil {
		t.Fatal(err)
	}
	libs, err := discoverNativeLibraries(pkgs, "linux_x64")
	if err != nil {
		t.Fatal(err)
	}
	if len(libs) != 1 || libs[0].spec.Name != "liba.so" {
		t.Fatalf("without build tag: libs = %+v, want [liba.so]", libs)
	}

	p.Build.Tags = []string{"withdepb"}
	pkgs, err = linkedPackages(t.Context(), p, env)
	if err != nil {
		t.Fatal(err)
	}
	libs, err = discoverNativeLibraries(pkgs, "linux_x64")
	if err != nil {
		t.Fatal(err)
	}
	if len(libs) != 2 || libs[0].spec.Name != "liba.so" || libs[1].spec.Name != "libb.so" {
		t.Fatalf("with build tag: libs = %+v, want [liba.so, libb.so] in deterministic order", libs)
	}

	// Cross-package duplicate library filename collision.
	writeTestFiles(t, map[string][]byte{
		filepath.Join(pkgBDir, nativeManifestName): []byte("version: 1\ntargets:\n  linux_x64:\n    - name: LIBA.SO\n      path: lib/libb.so\n"),
	})
	if _, err := discoverNativeLibraries(pkgs, "linux_x64"); err == nil ||
		!strings.Contains(err.Error(), "go.example/depa") || !strings.Contains(err.Error(), "go.example/depb") {
		t.Fatalf("cross-package duplicate err = %v, want error naming both packages", err)
	}
}

func TestNativeResolveRemoteCacheAndSHA256(t *testing.T) {
	isolateCacheEnv(t)
	payload := []byte("real-native-library-bytes")
	wantHash := sha256Hex(payload)

	var hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/libfoo.dylib":
			hits.Add(1)
			w.Write(payload)
		case "/bad.dylib":
			w.Write([]byte("corrupted-payload"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	withNativeHTTPClient(t, srv.Client())

	lib := resolvedNativeLibrary{
		pkg:    "go.example/foo",
		target: "macos_arm64",
		spec: nativeLibrarySpec{
			Name:   "libfoo.dylib",
			URL:    srv.URL + "/libfoo.dylib",
			SHA256: wantHash,
		},
	}

	// First fetch downloads and caches.
	cachedPath, err := resolveNativeLibrary(t.Context(), lib)
	if err != nil {
		t.Fatalf("first resolveNativeLibrary failed: %v", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("server hits = %d, want 1", hits.Load())
	}
	gotBytes, err := os.ReadFile(cachedPath)
	if err != nil || string(gotBytes) != string(payload) {
		t.Fatalf("cached file = %q, %v; want %q", gotBytes, err, payload)
	}

	// Second fetch hits cache without network request.
	cachedPath2, err := resolveNativeLibrary(t.Context(), lib)
	if err != nil {
		t.Fatalf("second resolveNativeLibrary failed: %v", err)
	}
	if cachedPath2 != cachedPath || hits.Load() != 1 {
		t.Fatalf("cache miss: path=%q hits=%d, want %q and 1 hit", cachedPath2, hits.Load(), cachedPath)
	}

	// Corrupted cached file is detected and re-downloaded.
	if err := os.WriteFile(cachedPath, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveNativeLibrary(t.Context(), lib); err != nil {
		t.Fatalf("re-fetch after cache corruption failed: %v", err)
	}
	if hits.Load() != 2 {
		t.Fatalf("server hits after cache corruption = %d, want 2", hits.Load())
	}

	// SHA-256 mismatch fails and does not cache.
	badLib := resolvedNativeLibrary{
		pkg:    "go.example/foo",
		target: "macos_arm64",
		spec: nativeLibrarySpec{
			Name:   "bad.dylib",
			URL:    srv.URL + "/bad.dylib",
			SHA256: strings.Repeat("a", 64),
		},
	}
	if _, err := resolveNativeLibrary(t.Context(), badLib); err == nil || !strings.Contains(err.Error(), "has SHA-256") {
		t.Fatalf("mismatch err = %v, want SHA-256 mismatch error", err)
	}
	cache, _ := os.UserCacheDir()
	if _, err := os.Stat(filepath.Join(cache, "nsgo", "natives", badLib.spec.SHA256, "bad.dylib")); !os.IsNotExist(err) {
		t.Fatalf("mismatched download should not be cached, stat err = %v", err)
	}

	// Redirect from HTTPS to HTTP is refused.
	httpTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(payload)
	}))
	defer httpTarget.Close()
	redirectSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, httpTarget.URL+"/libfoo.dylib", http.StatusFound)
	}))
	defer redirectSrv.Close()
	client := redirectSrv.Client()
	client.CheckRedirect = defaultNativeHTTPClient.CheckRedirect
	withNativeHTTPClient(t, client)
	redirLib := resolvedNativeLibrary{
		pkg:    "go.example/foo",
		target: "macos_arm64",
		spec: nativeLibrarySpec{
			Name:   "redir.dylib",
			URL:    redirectSrv.URL + "/redir.dylib",
			SHA256: wantHash,
		},
	}
	if _, err := resolveNativeLibrary(t.Context(), redirLib); err == nil || !strings.Contains(err.Error(), "non-https") {
		t.Fatalf("https->http redirect err = %v, want non-https refusal", err)
	}
}

func TestNativeResolveLocalPath(t *testing.T) {
	dir := t.TempDir()
	data := []byte("local-shared-lib")
	writeTestFiles(t, map[string][]byte{
		filepath.Join(dir, "libs", "liblocal.so"): data,
	})
	lib := resolvedNativeLibrary{
		pkg:    "go.example/local",
		dir:    dir,
		target: "linux_x64",
		spec: nativeLibrarySpec{
			Name:   "liblocal.so",
			Path:   "libs/liblocal.so",
			SHA256: sha256Hex(data),
		},
	}
	got, err := resolveNativeLibrary(t.Context(), lib)
	if err != nil {
		t.Fatalf("local resolve with matching sha256 failed: %v", err)
	}
	if bytes, err := os.ReadFile(got); err != nil || string(bytes) != string(data) {
		t.Fatalf("local file bytes = %q, %v", bytes, err)
	}

	// Omitted SHA-256 on local file succeeds.
	lib.spec.SHA256 = ""
	if _, err := resolveNativeLibrary(t.Context(), lib); err != nil {
		t.Fatalf("local resolve without sha256 failed: %v", err)
	}

	// Mismatched SHA-256 on local file fails.
	lib.spec.SHA256 = strings.Repeat("0", 64)
	if _, err := resolveNativeLibrary(t.Context(), lib); err == nil || !strings.Contains(err.Error(), "has SHA-256") {
		t.Fatalf("local mismatch err = %v, want SHA-256 mismatch error", err)
	}
}

func TestNativeStageAcrossTargetsAndUniversalLipo(t *testing.T) {
	t.Setenv("GOWORK", "off")
	t.Setenv("GOFLAGS", "")
	isolateCacheEnv(t)

	armDylib := []byte("arm64-dylib-slice")
	x64Dylib := []byte("x64-dylib-slice")
	universalDylib := []byte("universal-already-dylib")
	winDLL := []byte("windows-dll-bytes")
	linuxSO := []byte("linux-so-bytes")
	androidSO := []byte("android-so-bytes")

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/libremote-arm64.dylib":
			w.Write(armDylib)
		case "/libremote-x64.dylib":
			w.Write(x64Dylib)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	withNativeHTTPClient(t, srv.Client())

	root := t.TempDir()
	appDir := filepath.Join(root, "app")
	depDir := filepath.Join(root, "dep")
	manifest := `version: 1
targets:
  macos_arm64:
    - name: libremote.dylib
      url: ` + srv.URL + `/libremote-arm64.dylib
      sha256: ` + sha256Hex(armDylib) + `
    - name: libuni.dylib
      path: native/libuni.dylib
      sha256: ` + sha256Hex(universalDylib) + `
  macos_x64:
    - name: libremote.dylib
      url: ` + srv.URL + `/libremote-x64.dylib
      sha256: ` + sha256Hex(x64Dylib) + `
    - name: libuni.dylib
      path: native/libuni.dylib
      sha256: ` + sha256Hex(universalDylib) + `
  windows_x64:
    - name: engine.dll
      path: native/engine.dll
      sha256: ` + sha256Hex(winDLL) + `
  linux_x64:
    - name: libengine.so
      path: native/libengine.so
      sha256: ` + sha256Hex(linuxSO) + `
  android_arm64:
    - name: libengine.so
      path: native/android-libengine.so
      sha256: ` + sha256Hex(androidSO) + `
`
	writeTestFiles(t, map[string][]byte{
		filepath.Join(appDir, "go.mod"):                         []byte("module go.example/app\ngo 1.27.4\nrequire go.example/dep v0.0.0\nreplace go.example/dep => ../dep\n"),
		filepath.Join(appDir, "main.go"):                        []byte("package main\nimport _ \"go.example/dep\"\nfunc main() {}\n"),
		filepath.Join(depDir, "go.mod"):                         []byte("module go.example/dep\ngo 1.27.4\n"),
		filepath.Join(depDir, "dep.go"):                         []byte("package dep\n"),
		filepath.Join(depDir, "native", "libuni.dylib"):         universalDylib,
		filepath.Join(depDir, "native", "engine.dll"):           winDLL,
		filepath.Join(depDir, "native", "libengine.so"):         linuxSO,
		filepath.Join(depDir, "native", "android-libengine.so"): androidSO,
		filepath.Join(depDir, nativeManifestName):               []byte(manifest),
	})

	p := &project{Dir: appDir, Name: "Fixture", ID: "dev.example.fixture"}

	// macOS arm64 & x64 staging into Contents/Frameworks + ad hoc codesign.
	armApp := filepath.Join(t.TempDir(), "Fixture.app")
	x64App := filepath.Join(t.TempDir(), "Fixture.app")
	var signed []string
	ctx := withProcessRunner(t, func(_ context.Context, cmd processCommand) ([]byte, error) {
		switch cmd.name {
		case "go":
			return nil, nil
		case "codesign":
			signed = append(signed, cmd.args[len(cmd.args)-1])
			return nil, nil
		case "lipo":
			return nil, nil
		default:
			t.Fatalf("unexpected command %s %q", cmd.name, cmd.args)
			return nil, nil
		}
	})

	if err := addMacLibraries(ctx, p, armApp, "arm64"); err != nil {
		t.Fatalf("addMacLibraries arm64: %v", err)
	}
	if err := addMacLibraries(ctx, p, x64App, "amd64"); err != nil {
		t.Fatalf("addMacLibraries amd64: %v", err)
	}
	wantSigned := []string{
		filepath.Join(armApp, "Contents", "Frameworks", "libremote.dylib"),
		filepath.Join(armApp, "Contents", "Frameworks", "libuni.dylib"),
		filepath.Join(x64App, "Contents", "Frameworks", "libremote.dylib"),
		filepath.Join(x64App, "Contents", "Frameworks", "libuni.dylib"),
	}
	if !slices.Equal(signed, wantSigned) {
		t.Fatalf("codesigned paths = %q, want %q", signed, wantSigned)
	}
	if data, err := os.ReadFile(filepath.Join(armApp, "Contents", "Frameworks", "libremote.dylib")); err != nil || string(data) != string(armDylib) {
		t.Fatalf("arm64 libremote.dylib = %q, %v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(x64App, "Contents", "Frameworks", "libremote.dylib")); err != nil || string(data) != string(x64Dylib) {
		t.Fatalf("x64 libremote.dylib = %q, %v", data, err)
	}

	// Universal macOS: lipoMacBinaries joins differing dylibs and skips identical ones.
	var lipoCalls [][]string
	lipoCtx := withProcessRunner(t, func(_ context.Context, cmd processCommand) ([]byte, error) {
		if cmd.name != "lipo" {
			t.Fatalf("unexpected command %s %q", cmd.name, cmd.args)
		}
		lipoCalls = append(lipoCalls, cmd.args)
		return nil, nil
	})
	if err := lipoMacBinaries(lipoCtx, armApp, x64App); err != nil {
		t.Fatalf("lipoMacBinaries: %v", err)
	}
	wantLipo := []string{
		"-create",
		filepath.Join(armApp, "Contents", "Frameworks", "libremote.dylib"),
		filepath.Join(x64App, "Contents", "Frameworks", "libremote.dylib"),
		"-output",
		filepath.Join(armApp, "Contents", "Frameworks", "libremote.dylib"),
	}
	if len(lipoCalls) != 1 || !slices.Equal(lipoCalls[0], wantLipo) {
		t.Fatalf("lipo calls = %q, want [%q]", lipoCalls, wantLipo)
	}

	// signMacApp signs all staged libraries before signing the app bundle.
	p.Sign.Identity = "Developer ID Application: Test"
	var signOrder []string
	signCtx := withProcessRunner(t, func(_ context.Context, cmd processCommand) ([]byte, error) {
		if cmd.name != "codesign" {
			t.Fatalf("unexpected command %s %q", cmd.name, cmd.args)
		}
		signOrder = append(signOrder, cmd.args[len(cmd.args)-1])
		return nil, nil
	})
	if err := signMacApp(signCtx, p, armApp); err != nil {
		t.Fatalf("signMacApp: %v", err)
	}
	wantOrder := []string{
		filepath.Join(armApp, "Contents", "Frameworks", "libremote.dylib"),
		filepath.Join(armApp, "Contents", "Frameworks", "libuni.dylib"),
		armApp,
	}
	if !slices.Equal(signOrder, wantOrder) {
		t.Fatalf("signMacApp order = %q, want %q", signOrder, wantOrder)
	}

	// Windows staging beside executable + collision check.
	winDir := t.TempDir()
	if err := addWindowsLibraries(t.Context(), p, winDir, "amd64"); err != nil {
		t.Fatalf("addWindowsLibraries: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(winDir, "engine.dll")); err != nil || string(data) != string(winDLL) {
		t.Fatalf("windows engine.dll = %q, %v", data, err)
	}
	if err := addWindowsLibraries(t.Context(), p, winDir, "amd64"); err == nil || !strings.Contains(err.Error(), "conflicts with existing file") {
		t.Fatalf("windows collision err = %v, want conflict error", err)
	}

	// Linux staging beside executable with executable bit + collision check.
	linuxDir := t.TempDir()
	if err := addLinuxLibraries(t.Context(), p, linuxDir, "amd64"); err != nil {
		t.Fatalf("addLinuxLibraries: %v", err)
	}
	soPath := filepath.Join(linuxDir, "libengine.so")
	info, err := os.Stat(soPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0o111 == 0 {
		t.Errorf("linux libengine.so mode = %o, want executable", info.Mode())
	}
	if data, err := os.ReadFile(soPath); err != nil || string(data) != string(linuxSO) {
		t.Fatalf("linux libengine.so = %q, %v", data, err)
	}
	if err := addLinuxLibraries(t.Context(), p, linuxDir, "amd64"); err == nil || !strings.Contains(err.Error(), "conflicts with existing file") {
		t.Fatalf("linux collision err = %v, want conflict error", err)
	}

	// Android staging returns lib/<abi>/<name>.
	apkFiles, err := addAndroidLibraries(t.Context(), p, []string{"GOOS=android", "GOARCH=arm64", "CGO_ENABLED=0"}, "arm64", "arm64-v8a")
	if err != nil {
		t.Fatalf("addAndroidLibraries: %v", err)
	}
	if len(apkFiles) != 1 || apkFiles[0].Name != "lib/arm64-v8a/libengine.so" || string(apkFiles[0].Data) != string(androidSO) {
		t.Fatalf("android files = %+v, want lib/arm64-v8a/libengine.so", apkFiles)
	}
}

func TestNativeStageIOSFrameworkAndAndroidCollision(t *testing.T) {
	t.Setenv("GOWORK", "off")
	t.Setenv("GOFLAGS", "")
	root := t.TempDir()
	appDir := filepath.Join(root, "app")
	depDir := filepath.Join(root, "dep")
	iosBin := []byte("ios-dylib-bytes")
	writeTestFiles(t, map[string][]byte{
		filepath.Join(appDir, "go.mod"):                    []byte("module go.example/app\ngo 1.27.4\nrequire go.example/dep v0.0.0\nreplace go.example/dep => ../dep\n"),
		filepath.Join(appDir, "main.go"):                   []byte("package main\nimport _ \"go.example/dep\"\nfunc main() {}\n"),
		filepath.Join(depDir, "go.mod"):                    []byte("module go.example/dep\ngo 1.27.4\n"),
		filepath.Join(depDir, "dep.go"):                    []byte("package dep\n"),
		filepath.Join(depDir, "native", "libengine.dylib"): iosBin,
		filepath.Join(depDir, nativeManifestName): []byte(`version: 1
targets:
  ios_simulator:
    - name: libengine.dylib
      path: native/libengine.dylib
      framework: EngineKit
  android_arm64:
    - name: libnsgo.so
      path: native/libengine.dylib
`),
	})

	p := &project{Dir: appDir, Name: "Fixture", ID: "dev.example.fixture"}
	iosApp := filepath.Join(t.TempDir(), "Fixture.app")
	var commands []string
	ctx := withProcessRunner(t, func(_ context.Context, cmd processCommand) ([]byte, error) {
		switch cmd.name {
		case "go":
			return nil, nil
		case "install_name_tool", "codesign":
			commands = append(commands, cmd.name+" "+strings.Join(cmd.args, " "))
			return nil, nil
		default:
			t.Fatalf("unexpected command %s %q", cmd.name, cmd.args)
			return nil, nil
		}
	})
	if err := addIOSFrameworks(ctx, p, iOS, iosApp, []string{"GOOS=darwin", "GOARCH=arm64", "CGO_ENABLED=0"}, true); err != nil {
		t.Fatalf("addIOSFrameworks: %v", err)
	}
	fwDir := filepath.Join(iosApp, "Frameworks", "EngineKit.framework")
	if data, err := os.ReadFile(filepath.Join(fwDir, "EngineKit")); err != nil || string(data) != string(iosBin) {
		t.Fatalf("iOS framework binary = %q, %v", data, err)
	}
	if plist, err := os.ReadFile(filepath.Join(fwDir, "Info.plist")); err != nil || !strings.Contains(string(plist), "dev.example.fixture.enginekit") {
		t.Fatalf("iOS framework Info.plist = %q, %v", plist, err)
	}
	if len(commands) != 2 || !strings.HasPrefix(commands[0], "install_name_tool -id @rpath/EngineKit.framework/EngineKit ") || !strings.HasPrefix(commands[1], "codesign --force --sign - ") {
		t.Fatalf("iOS framework commands = %q", commands)
	}

	if _, err := addAndroidLibraries(t.Context(), p, []string{"GOOS=android", "GOARCH=arm64", "CGO_ENABLED=0"}, "arm64", "arm64-v8a"); err == nil ||
		!strings.Contains(err.Error(), "conflicts with NSGo's Android runtime binary") {
		t.Fatalf("android libnsgo.so collision err = %v", err)
	}
}

func TestNativeLibraryLicenseNotices(t *testing.T) {
	t.Setenv("GOWORK", "off")
	t.Setenv("GOFLAGS", "")

	mitText, err := assets.ReadLicenseFile("License/MIT/pristine.txt")
	if err != nil {
		t.Fatal(err)
	}
	mplText, err := assets.ReadLicenseFile("License/MPL-2.0/pristine.txt")
	if err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	appDir := filepath.Join(root, "app")
	depDir := filepath.Join(root, "dep")
	noticeText := "Third-party native library attribution notice for libengine.\n"
	writeTestFiles(t, map[string][]byte{
		filepath.Join(appDir, "go.mod"):  []byte("module go.example/app\ngo 1.27.4\nrequire go.example/dep v0.0.0\nreplace go.example/dep => ../dep\n"),
		filepath.Join(appDir, "main.go"): []byte("package main\nimport _ \"go.example/dep\"\nfunc main() {}\n"),

		filepath.Join(depDir, "go.mod"):                               []byte("module go.example/dep\ngo 1.27.4\n"),
		filepath.Join(depDir, "dep.go"):                               []byte("package dep\n"),
		filepath.Join(depDir, "LICENSE"):                              mitText,
		filepath.Join(depDir, "native", "libengine.dylib"):            []byte("dylib"),
		filepath.Join(depDir, "third_party", "libengine-NOTICE.txt"):  []byte(noticeText),
		filepath.Join(depDir, "third_party", "libengine-LICENSE.txt"): mplText,
		filepath.Join(depDir, nativeManifestName): []byte(`version: 1
licenses:
  - LICENSE
  - third_party/libengine-NOTICE.txt
targets:
  macos_arm64:
    - name: libengine.dylib
      path: native/libengine.dylib
      licenses:
        - third_party/libengine-LICENSE.txt
  macos_x64:
    - name: libengine.dylib
      path: native/libengine.dylib
      licenses:
        - third_party/libengine-LICENSE.txt
`),
	})

	// Copyleft policy catches MPL-2.0 declared on the native library.
	p := &project{Dir: appDir, Licenses: licensePolicy{Copyleft: "error"}}
	if _, err := p.licenseNoticesFile(t.Context(), []string{"GOOS=darwin", "GOARCH=arm64", "CGO_ENABLED=0"}, nil, nil, ""); err == nil ||
		!strings.Contains(err.Error(), "copyleft license text (MPL-2.0)") {
		t.Fatalf("copyleft native license err = %v, want MPL-2.0 error", err)
	}

	// With copyleft=warn and universal macOS noticeArchitectures, notices include
	// both declared files once, without leaking the local directory path.
	p.Licenses.Copyleft = "warn"
	p.noticeArchitectures = []string{"arm64", "amd64"}
	if _, err := p.licenseNoticesFile(t.Context(), []string{"GOOS=darwin", "CGO_ENABLED=0"}, nil, nil, ""); err != nil {
		t.Fatalf("universal notices failed: %v", err)
	}
	for _, want := range []string{
		"third_party/libengine-NOTICE.txt",
		noticeText,
		"third_party/libengine-LICENSE.txt",
		string(mplText),
	} {
		if !strings.Contains(p.thirdPartyNotices, want) {
			t.Errorf("thirdPartyNotices missing %q", want)
		}
	}
	if strings.Count(p.thirdPartyNotices, noticeText) != 1 {
		t.Errorf("notice text appeared %d times, want 1", strings.Count(p.thirdPartyNotices, noticeText))
	}
	_, depSection, ok := strings.Cut(p.thirdPartyNotices, "go.example/dep")
	if !ok {
		t.Fatal("thirdPartyNotices missing go.example/dep section")
	}
	if strings.Count(depSection, "\nLICENSE\n") != 1 {
		t.Errorf("dep LICENSE appeared %d times, want 1", strings.Count(depSection, "\nLICENSE\n"))
	}
	if strings.Contains(p.thirdPartyNotices, root) {
		t.Fatal("thirdPartyNotices leaked local replacement directory path")
	}

	// Unrecognized license declared on a native library fails until allowed.
	writeTestFiles(t, map[string][]byte{
		filepath.Join(depDir, "third_party", "libengine-LICENSE.txt"): []byte("Proprietary native library license text.\n"),
	})
	p.Licenses = licensePolicy{}
	if _, err := p.licenseNoticesFile(t.Context(), []string{"GOOS=darwin", "GOARCH=arm64", "CGO_ENABLED=0"}, nil, nil, ""); err == nil ||
		!strings.Contains(err.Error(), "unrecognized license text in third_party/libengine-LICENSE.txt") {
		t.Fatalf("unrecognized native license err = %v, want unrecognized license error", err)
	}
	p.Licenses.Allow = []string{"go.example/dep"}
	if _, err := p.licenseNoticesFile(t.Context(), []string{"GOOS=darwin", "GOARCH=arm64", "CGO_ENABLED=0"}, nil, nil, ""); err != nil {
		t.Fatalf("allowed native license failed: %v", err)
	}

	// Web target ships no native library from this manifest, so its native
	// library license files are not bundled.
	p.noticeArchitectures = nil
	p.Licenses = licensePolicy{Copyleft: "error"}
	if _, err := p.licenseNoticesFile(t.Context(), []string{"GOOS=js", "GOARCH=wasm"}, nil, nil, ""); err != nil {
		t.Fatalf("web notices failed: %v", err)
	}
	if strings.Contains(p.thirdPartyNotices, "third_party/libengine-LICENSE.txt") || strings.Contains(p.thirdPartyNotices, noticeText) {
		t.Fatal("web build should not include macOS native library licenses")
	}
}
