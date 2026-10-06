package run

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestPackageManagerSelection(t *testing.T) {
	t.Run("package.json takes precedence over stale lockfiles", func(t *testing.T) {
		clone := t.TempDir()
		write := func(name, text string) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(clone, name), []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		write("package.json", `{"packageManager":"pnpm@9.0.0",`+
			`"scripts":{"test":"vitest","typecheck":"tsc"},"devDependencies":{"vitest":"1"}}`)
		write("package-lock.json", "stale")
		_, ok := readPackage(clone)
		if !ok {
			t.Fatal("package.json was not read")
		}
		if test, typecheck := suite(clone, "", ""); test != "pnpm run test" || typecheck != "pnpm run typecheck" {
			t.Errorf("suite = %q, %q; want pnpm scripts", test, typecheck)
		}
		if got, want := jsRunner(clone), []string{"pnpm", "exec", "vitest", "run"}; !reflect.DeepEqual(got, want) {
			t.Errorf("jsRunner = %v, want %v", got, want)
		}
	})

	t.Run("lockfile precedence is stable", func(t *testing.T) {
		clone := t.TempDir()
		for _, name := range []string{"package-lock.json", "yarn.lock", "pnpm-lock.yaml"} {
			if err := os.WriteFile(filepath.Join(clone, name), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if got := packageManager(clone, packageJSON{}); got != "pnpm" {
			t.Errorf("packageManager = %q, want pnpm", got)
		}
	})
}
