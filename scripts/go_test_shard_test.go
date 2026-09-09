package main

import (
	"os/exec"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestGoTestShardsPartitionAllPackages(t *testing.T) {
	root := repoRootForTest(t)
	modulePath := runGoTestShardCommand(t, root, "go", "list", "-m")
	all := outputLines(runGoTestShardCommand(
		t,
		root,
		"go",
		"list",
		"-tags",
		"goolm,stdjson",
		"-f",
		"{{.ImportPath}}",
		"./...",
	))
	slow := outputLines(runGoTestShardCommand(t, root, "bash", "./scripts/run-go-test-shard.sh", "--list", "slow"))
	remaining := outputLines(runGoTestShardCommand(
		t,
		root,
		"bash",
		"./scripts/run-go-test-shard.sh",
		"--list",
		"remaining",
	))

	if len(slow) == 0 || len(remaining) == 0 {
		t.Fatalf("empty shard: slow=%d remaining=%d", len(slow), len(remaining))
	}
	wantSlow := []string{
		modulePath + "/pkg/agent",
		modulePath + "/web/backend/api",
	}
	for _, packagePath := range wantSlow {
		if !containsString(slow, packagePath) {
			t.Errorf("slow shard does not contain %s", packagePath)
		}
	}

	combined := append(append([]string(nil), slow...), remaining...)
	sort.Strings(all)
	sort.Strings(combined)
	if !reflect.DeepEqual(combined, all) {
		t.Fatalf("combined shards do not equal tagged package list\ncombined=%q\nall=%q", combined, all)
	}
	for index := 1; index < len(combined); index++ {
		if combined[index] == combined[index-1] {
			t.Fatalf("package appears in multiple shards: %s", combined[index])
		}
	}
}

func TestGoTestShardRejectsInvalidArguments(t *testing.T) {
	root := repoRootForTest(t)
	for _, arguments := range [][]string{
		nil,
		{"unknown"},
		{"slow", "remaining"},
		{"--list"},
	} {
		command := exec.Command("bash", append([]string{"./scripts/run-go-test-shard.sh"}, arguments...)...)
		command.Dir = root
		if output, err := command.CombinedOutput(); err == nil {
			t.Fatalf("arguments %q succeeded with output %q", arguments, output)
		}
	}
}

func runGoTestShardCommand(t *testing.T, directory, name string, arguments ...string) string {
	t.Helper()
	command := exec.Command(name, arguments...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("run %s %q: %v\n%s", name, arguments, err, output)
	}
	return strings.TrimSpace(string(output))
}

func outputLines(output string) []string {
	if output == "" {
		return nil
	}
	return strings.Split(output, "\n")
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
