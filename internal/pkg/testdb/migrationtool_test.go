package testdb_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Тесты этого файла лежат, возможно, не в своём пакете: они про инструмент миграций —
// про то, что его не должно быть в бинарях служб, и про то, на какой версии он закреплён,
// — а не про оснастку базы; `testdb` им нужен как место, где живут вещи вокруг миграций.
// Проверок теперь две, и с третьей они переедут в отдельный пакет архитектурных тестов.

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

// Версия goose закреплена в репозитории дважды: CLI ставится в образ по аргументу
// `GOOSE_VERSION` в Dockerfile, библиотеку берёт go.mod. Разъехаться им нельзя — шаг
// запуска и тесты накатывают одни и те же миграции, и разные версии goose означали бы,
// что накатывают они их по-разному. Рядом с аргументом об этом сказано словами, но слова
// не держат — держит этот тест.
func TestTheMigrationToolIsPinnedToTheSameVersionAsItsLibrary(t *testing.T) {
	t.Parallel()

	cli := pinnedVersion(t, "Dockerfile", `(?m)^ARG GOOSE_VERSION=(\S+)\s*$`)
	library := pinnedVersion(t, "go.mod", `(?m)^\s*github\.com/pressly/goose/v3 (\S+)\s*$`)

	if cli != library {
		t.Errorf("goose CLI закреплён на %s (Dockerfile), библиотека — на %s (go.mod): "+
			"шаг запуска и тесты накатят миграции разными версиями", cli, library)
	}
}

// pinnedVersion вынимает версию из файла в корне репозитория. Корень — на три каталога
// выше: пакет лежит в internal/pkg/testdb.
func pinnedVersion(t *testing.T, name, pattern string) string {
	t.Helper()

	path := filepath.Join("..", "..", "..", name)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("прочитать %s: %v", name, err)
	}

	found := regexp.MustCompile(pattern).FindSubmatch(content)
	if found == nil {
		t.Fatalf("в %s нет версии goose по образцу %s: сторож потерял то, что сторожил", name, pattern)
	}

	return string(found[1])
}
