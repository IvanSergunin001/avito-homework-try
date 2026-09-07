package testdb_test

import (
	"os/exec"
	"strings"
	"testing"
)

// Миграции накатывает отдельный шаг запуска, а не служба на старте: службе, которая
// мигрирует сама, нужны права на изменение схемы в бою, а при нескольких экземплярах —
// ещё и договорённость, кто накатывает первым. Проверяем это по зависимостям бинарей:
// инструмента миграций там быть не должно.
func TestServiceBinariesDoNotCarryTheMigrationTool(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("тулчейна Go нет под рукой — зависимости бинарей проверить нечем")
	}

	for _, service := range []string{
		"github.com/IvanSergunin001/Test157/cmd/platform",
		"github.com/IvanSergunin001/Test157/cmd/restaurant",
	} {
		out, err := exec.CommandContext(t.Context(), "go", "list", "-deps", service).Output()
		if err != nil {
			t.Fatalf("перечислить зависимости %s: %v", service, err)
		}

		for _, dependency := range strings.Fields(string(out)) {
			if strings.Contains(dependency, "goose") {
				t.Errorf("%s тянет инструмент миграций (%s), а мигрировать должен отдельный шаг запуска",
					service, dependency)
			}
		}
	}
}
