// Команда migrationjournal печатает имя журнала применённых миграций одного контекста.
//
// Имя объявлено Go-константой в пакете миграций контекста (`VersionTable`), и тестовая
// оснастка берёт его оттуда. Шагу запуска то же имя нужно строкой — флагом `goose -table`,
// — а Dockerfile до Go-констант не дотягивается. Эта команда и есть мост между ними:
// имя журнала закреплено в одном месте, и тесты не могут разъехаться с шагом запуска по
// разным журналам незаметно.
//
// В бинари служб команда не попадает: её запускает только сборка образа миграций.
package main

import (
	"fmt"
	"os"

	platformmigrations "github.com/IvanSergunin001/Test157/internal/platform/migrations"
	boxmigrations "github.com/IvanSergunin001/Test157/internal/restaurant/migrations"
)

// journals — журнал каждого контекста под тем же именем, под каким контекст известен
// шагу запуска: каталогом миграций (`/migrations/<контекст>`) и значением `context=`
// у `make migration`.
var journals = map[string]string{
	"platform":      platformmigrations.VersionTable,
	"platform_seed": platformmigrations.SeedVersionTable,
	"restaurant":    boxmigrations.VersionTable,
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "нужен контекст: migrationjournal platform|platform_seed|restaurant")
		os.Exit(2)
	}

	journal, known := journals[os.Args[1]]
	if !known {
		fmt.Fprintf(os.Stderr, "неизвестный контекст %q: есть platform, platform_seed и restaurant\n", os.Args[1])
		os.Exit(2)
	}

	fmt.Println(journal)
}
