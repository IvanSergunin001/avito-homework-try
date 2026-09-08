// Сквозной тест: система поднимается тем же `docker compose`, что и в README, и проходит
// бизнес-сценарий целиком — от меню, заведённого оператором в коробке, до доставленного
// заказа и до заказа, отменённого покупателем.
//
// Подмен здесь нет ни одной. Остальные тесты идут по швам служб: там за границей стоит
// HTTP-заглушка, а служба поднимается в процессе теста. Здесь не поднимается ничего —
// система поднимается сама, как у ревьюера на чистой машине: собранные образы, отдельный
// шаг миграций, демонстрационное заведение из seed-миграции, обе службы в сети Compose и
// настоящий PostgreSQL. Тест ходит в них снаружи по HTTP: покупателем на клиентской
// поверхности, оператором на его рабочем месте, заведением на партнёрской.
//
// Отсюда и его роль: он не проверяет правила предметной области — их дешевле и точнее
// перебирать по швам и в домене. Он отвечает на единственный вопрос, на который те
// ответить не могут: собирается ли всё это вместе и работает ли одной командой.
//
// В этом файле — оснастка: запуск и остановка системы, адреса поднятых служб и HTTP.
// Сам сценарий — в `scenario_test.go`.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
)

const (
	// project — имя проекта Compose, под которым живёт система теста. Своё, а не то,
	// что записано в `compose.yaml`: тест не должен ни погасить, ни переиспользовать
	// стенд, поднятый разработчиком через `make up`.
	project = "avito-kitchen-e2e"

	// composeFile — файл запуска, тот же самый, что описан в README и что поднимает
	// `make up`. Проверяется именно он: тест, поднимающий систему собственным описанием,
	// доказывал бы работоспособность этого описания, а не системы.
	composeFile = "../compose.yaml"
	// composeOverride — надстройка со случайными портами хоста, чтобы прогон не спорил
	// за порты с уже поднятым стендом. Почему только порты — в самом файле.
	composeOverride = "compose.e2e.yaml"
)

const (
	// upTimeout — сколько тест ждёт поднятия системы. С холодным кешем сюда попадают
	// сборка обеих служб, образа миграций и загрузка PostgreSQL, поэтому предел не
	// «секунды на запуск», а «минуты на сборку».
	upTimeout = 10 * time.Minute
	// downTimeout — сколько тест ждёт остановки: система за собой убирает всегда, даже
	// когда сценарий упал на первом же шаге.
	downTimeout = 2 * time.Minute
	// commandTimeout — предел для коротких команд Compose: спросить порт, забрать логи.
	commandTimeout = time.Minute

	// requestTimeout — сколько тест ждёт ответа поднятой службы. Он больше пределов
	// самих служб: тест не проверяет их таймауты, он не должен зависать навсегда.
	requestTimeout = 30 * time.Second
	// settleTimeout — сколько тест ждёт того, что случается не в ответ на его запрос:
	// статуса, который коробка развозит фоновой доставкой. Развозит она его раз в
	// секунду (`RESTAURANT_NOTIFY_INTERVAL`), но ждать приходится ещё и сеть, и запись.
	settleTimeout = 30 * time.Second
	// pollInterval — как часто тест переспрашивает систему, пока ждёт.
	pollInterval = 200 * time.Millisecond
)

// stack — система, поднятая для теста: обе службы, база и накатанные миграции.
type stack struct {
	// platform — адрес площадки, опубликованный Compose на хосте.
	platform string
	// box — адрес коробки заведения там же.
	box string
	// client — клиент, которым тест ходит в обе службы.
	client *http.Client
}

// start поднимает систему одной командой и возвращает адреса поднятых служб. Готовить
// окружение перед прогоном не нужно: нужен только Docker — тот же самый, без которого не
// работают и остальные тесты.
func start(t *testing.T) *stack {
	t.Helper()

	requireDocker(t)

	// Остановка регистрируется до запуска: `up` может успеть создать половину системы и
	// упасть на второй, и эту половину тоже надо убрать за собой.
	t.Cleanup(func() { stop(t) })

	compose(t, upTimeout, "up", "--build", "--detach", "--wait", "platform", "restaurant")

	return &stack{
		platform: published(t, "platform", "8080"),
		box:      published(t, "restaurant", "8081"),
		client:   &http.Client{Timeout: requestTimeout},
	}
}

// requireDocker убеждается, что система есть чем поднять.
//
// Пропустить тест здесь было бы хуже, чем упасть: единственное доказательство того, что
// система собирается и работает целиком, молча превратилось бы в зелёный прогон.
func requireDocker(t *testing.T) {
	t.Helper()

	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("сквозной тест поднимает систему тем же `docker compose`, что и README, "+
			"а `docker` не найден: %v", err)
	}
}

// stop гасит систему вместе с данными базы. Перед этим — логи обеих служб, если сценарий
// упал: разбирать падение сквозного теста по одному лишь коду ответа нечем, а после
// остановки логов уже не спросить.
//
// За логами тест идёт помощником, который не останавливает прогон: система могла не
// подняться вовсе, и неудача с логами не должна подменить собой сообщение о том, почему
// она не поднялась.
func stop(t *testing.T) {
	t.Helper()

	if t.Failed() {
		logs, err := runCompose(commandTimeout, "logs", "--no-color", "platform", "restaurant")
		if err != nil {
			logs = fmt.Sprintf("логов нет (%v)", err)
		}
		t.Logf("логи системы:\n%s", logs)
	}

	compose(t, downTimeout, "down", "--volumes", "--remove-orphans")
}

// published спрашивает у Compose, на каком порту хоста опубликована служба, и собирает
// адрес, по которому её видно из теста.
func published(t *testing.T, service, port string) string {
	t.Helper()

	mapping := strings.TrimSpace(compose(t, commandTimeout, "port", service, port))
	// В выдаче может быть две строки — IPv4 и IPv6; берётся первая, дальше от неё нужен
	// только номер порта.
	mapping, _, _ = strings.Cut(mapping, "\n")

	index := strings.LastIndex(mapping, ":")
	if index < 0 {
		t.Fatalf("Compose назвал порт службы %s как %q, ожидали `адрес:порт`", service, mapping)
	}

	return fmt.Sprintf("http://%s:%s", daemonHost(t), mapping[index+1:])
}

// daemonHost отдаёт адрес, по которому из теста видно порты, опубликованные Compose.
//
// Публикуются они на хосте демона Docker, а тест может идти и на самом хосте, и внутри
// контейнера с проброшенным сокетом (так его запускает `make test`). Разбирается с этим
// та же библиотека, которой оснастка тестовой базы поднимает PostgreSQL: адрес хоста
// демона — её вопрос, и отвечать на него второй раз своими руками значило бы завести
// второй ответ, расходящийся с первым.
func daemonHost(t *testing.T) string {
	t.Helper()

	provider, err := testcontainers.NewDockerProvider()
	if err != nil {
		t.Fatalf("узнать хост Docker: %v", err)
	}
	defer func() { _ = provider.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()

	host, err := provider.DaemonHost(ctx)
	if err != nil {
		t.Fatalf("узнать хост Docker: %v", err)
	}

	return host
}

// compose выполняет команду Compose и возвращает то, что она написала. Неудача
// останавливает тест вместе с выводом команды: сообщение Compose о том, почему система не
// поднялась, и есть результат теста.
func compose(t *testing.T, timeout time.Duration, args ...string) string {
	t.Helper()

	output, err := runCompose(timeout, args...)
	if err != nil {
		t.Fatalf("docker compose %s: %v\n%s", strings.Join(args, " "), err, output)
	}

	return output
}

// runCompose выполняет команду Compose над той же парой файлов и под тем же именем
// проекта, что и всё остальное в этом тесте.
func runCompose(timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	full := append([]string{"compose", "-f", composeFile, "-f", composeOverride, "-p", project}, args...)

	output, err := exec.CommandContext(ctx, "docker", full...).CombinedOutput()

	return string(output), err
}

// response — ответ службы, прочитанный целиком.
type response struct {
	Status      int
	ContentType string
	Body        []byte
}

// get выполняет запрос к поднятой службе.
func (s *stack) get(t *testing.T, url string, headers http.Header) response {
	t.Helper()

	return s.do(t, http.MethodGet, url, nil, headers)
}

// post отправляет службе тело в JSON.
func (s *stack) post(t *testing.T, url string, body any, headers http.Header) response {
	t.Helper()

	if body == nil {
		return s.do(t, http.MethodPost, url, nil, headers)
	}

	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("собрать тело запроса %s: %v", url, err)
	}

	return s.do(t, http.MethodPost, url, encoded, headers)
}

// do выполняет запрос к поднятой службе и читает ответ целиком.
func (s *stack) do(t *testing.T, method, url string, body []byte, headers http.Header) response {
	t.Helper()

	var payload io.Reader
	if body != nil {
		payload = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(t.Context(), method, url, payload)
	if err != nil {
		t.Fatalf("собрать запрос %s %s: %v", method, url, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for name, values := range headers {
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}

	resp, err := s.client.Do(req)
	if err != nil {
		t.Fatalf("запрос %s %s: %v", method, url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	read, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("прочитать ответ %s %s: %v", method, url, err)
	}

	return response{Status: resp.StatusCode, ContentType: resp.Header.Get("Content-Type"), Body: read}
}

// decode разбирает успешный ответ, ожидая обещанный контрактом код и тип содержимого.
func decode(t *testing.T, resp response, status int, target any) {
	t.Helper()

	if resp.Status != status {
		t.Fatalf("служба ответила %d, ожидали %d: %s", resp.Status, status, resp.Body)
	}
	if resp.ContentType != "application/json" {
		t.Errorf("тип содержимого %q, ожидали application/json", resp.ContentType)
	}
	if err := json.Unmarshal(resp.Body, target); err != nil {
		t.Fatalf("разобрать ответ: %v", err)
	}
}

// eventually ждёт, пока система придёт в описанное состояние, и останавливает тест, если
// она к сроку в него не пришла.
//
// Ожидание здесь — не небрежность, а суть проверяемого: коробка сообщает площадке о смене
// статуса фоновой доставкой, и мгновенным этот путь не бывает. Ждать нечего только в тех
// проверках, где ответ приходит на сам запрос теста, — там ожидания и нет.
func eventually(t *testing.T, rule string, reached func() bool) {
	t.Helper()

	deadline := time.Now().Add(settleTimeout)
	for {
		if reached() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("за %s не дождались: %s", settleTimeout, rule)
		}
		time.Sleep(pollInterval)
	}
}
