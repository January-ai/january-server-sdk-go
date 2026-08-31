package january_test

import (
	"archive/zip"
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Builds a real Go proxy module zip and consumes it without a local replace.
func TestModuleDistribution(t *testing.T) {
	dir := t.TempDir()
	const module = "github.com/January-ai/january-server-sdk-go"
	const version = "v0.0.0"
	proxy := filepath.Join(dir, "proxy")
	versionDir := filepath.Join(proxy, "github.com/!january-ai/january-server-sdk-go/@v")
	if err := os.MkdirAll(versionDir, 0755); err != nil {
		t.Fatal(err)
	}
	write := func(path string, data []byte) {
		t.Helper()
		if err := os.WriteFile(path, data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	read := func(path string) []byte {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	add := func(path string) {
		t.Helper()
		entry, err := zw.Create(module + "@" + version + "/" + filepath.ToSlash(path))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(read(path)); err != nil {
			t.Fatal(err)
		}
	}
	add("go.mod")
	add("README.md")
	if err := filepath.WalkDir("january", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			add(path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(versionDir, version+".zip"), archive.Bytes())
	write(filepath.Join(versionDir, version+".mod"), read("go.mod"))
	write(filepath.Join(versionDir, version+".info"), []byte(`{"Version":"v0.0.0","Time":"2026-08-30T00:00:00Z"}`))
	write(filepath.Join(versionDir, "list"), []byte(version+"\n"))
	consumer := filepath.Join(dir, "consumer")
	if err := os.Mkdir(consumer, 0755); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(consumer, "go.mod"), []byte("module example.com/consumer\n\ngo 1.26\n\nrequire "+module+" "+version+"\n"))
	write(filepath.Join(consumer, "main.go"), read("testdata/consumer/main.go"))
	cmd := exec.Command("go", "run", "-mod=mod", ".")
	cmd.Dir = consumer
	// Inherit build infrastructure only, never real credentials or proxy settings.
	cmd.Env = []string{"GOWORK=off", "GOSUMDB=off", "GOTOOLCHAIN=local", "GOFLAGS=-modcacherw", "GOPROXY=file://" + proxy, "GOMODCACHE=" + filepath.Join(dir, "cache")}
	for _, name := range []string{"PATH", "HOME", "TMPDIR", "GOCACHE", "GOPATH", "GOROOT"} {
		if value, ok := os.LookupEnv(name); ok {
			cmd.Env = append(cmd.Env, name+"="+value)
		}
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("installed consumer failed: %v\n%s", err, output)
	}
	t.Log(string(output))
}
