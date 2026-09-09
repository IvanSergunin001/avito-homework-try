package domain

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	// PartnerNameMaxLength — предел длины названия заведения, объявленный админской
	// спецификацией.
	//
	// Длина считается в знаках, а не в байтах: в названиях кириллица, и байтовый предел
	// обрезал бы русское название вдвое раньше обещанного, а `maxLength` в спецификации
	// и `length()` в CHECK базы считают именно знаки.
	//
	// Экспортирован ради сторожевого теста: кодогенератор `maxLength` в тип не переносит,
	// и правка спецификации разошлась бы с кодом молча
	// (см. TestRestaurantLimitsMatchTheContract).
	PartnerNameMaxLength = 200

	// BaseURLMaxLength — предел длины адреса интеграции, объявленный той же
	// спецификацией и тем же CHECK базы. Считается так же в знаках: в адресе бывает
	// IDN-хост и кириллический путь.
	BaseURLMaxLength = 500

	// contractPrefix — префикс партнёрского контракта в коробке заведения: к адресу
	// интеграции площадка приписывает пути, начинающиеся с него. Тот же префикс объявлен
	// адаптером коробки (`internal/restaurant/adapters/http`.PartnerAPIPrefix); ссылаться
	// отсюда на коробку нельзя — это другой ограниченный контекст, и площадка знает о нём
	// ровно то, что написано в контракте.
	contractPrefix = "/api/kitchen/v1/restaurant"

	// partnerKeyPrefix — префикс выдаваемого ключа. Секретности он не добавляет, зато по
	// нему видно, что за строка попала в тикет или в лог: ключ заведения, а не пароль от
	// базы, — и понятно, что именно нужно отозвать.
	partnerKeyPrefix = "pk_"
)

// Partner — заведение глазами площадки как сторона интеграции: по какому адресу к нему
// стучаться и подключено ли оно сейчас.
//
// Это не то же самое, что Restaurant: покупателю площадка показывает название заведения,
// а реквизиты интеграции ему не показывает никогда. Один и тот же ряд в хранилище смотрит
// в две стороны, и смешивать эти два взгляда в одном типе значило бы возить адрес
// коробки в витрину, откуда он рано или поздно уехал бы наружу.
type Partner struct {
	// ID — идентификатор заведения на площадке, общий для витрины и интеграции.
	ID uuid.UUID
	// Name — название заведения, как его видит покупатель.
	Name string
	// BaseURL — адрес коробки заведения: к нему площадка добавляет пути партнёрского
	// контракта. Хранится без завершающего слэша, чтобы склейка не давала двойного.
	BaseURL string
	// IsActive — заведение подключено: его видно на витрине и его меню синхронизируется.
	IsActive bool
	// CreatedAt и UpdatedAt — когда заведение подключили и когда правили его реквизиты.
	// Их проставляет хранилище, а не эта область: «сейчас» у базы одно на все службы,
	// а у процесса — своё, и по двум разным часам порядок правок не восстанавливается.
	// Поэтому NewPartner их не заполняет — они приезжают из хранилища вместе с рядом.
	CreatedAt time.Time
	UpdatedAt time.Time
}

// NewPartner собирает заведение, проверяя реквизиты, с которыми его подключают.
//
// Проверка живёт в предметной области, а не в обработчике: пределы объявлены в
// спецификации, но кодогенератор их не сверяет, а правило «у заведения есть название и
// адрес, по которому до него можно дойти» одинаково верно и при регистрации, и при
// переезде.
func NewPartner(id uuid.UUID, name, baseURL string, isActive bool) (Partner, error) {
	// Пробелы по краям — след ввода, а не часть названия: заведение «  » названия не имеет.
	name = strings.TrimSpace(name)

	if utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > PartnerNameMaxLength {
		return Partner{}, InvalidPartnerError{
			Reason: fmt.Sprintf("название: от 1 до %d знаков", PartnerNameMaxLength),
		}
	}

	address, err := integrationAddress(baseURL)
	if err != nil {
		return Partner{}, err
	}

	partner := Partner{ID: id, Name: name, BaseURL: address, IsActive: isActive}

	// Адрес проверяется не формой, а результатом: к нему приписываются пути контракта, и
	// годен он ровно тогда, когда каждый из них после склейки остаётся тем адресом, по
	// которому заведение обещало отвечать. Сделать это при подключении дешевле, чем
	// узнать о неудачной склейке в момент первого заказа.
	if _, err := partner.Endpoints(); err != nil {
		return Partner{}, err
	}

	return partner, nil
}

// integrationAddress проверяет адрес интеграции и приводит его к виду, к которому площадка
// приписывает пути контракта.
//
// Требования ровно те, без которых обращение к заведению не состоится: абсолютный адрес по
// http или https и хост, который можно резолвить. Всё остальное — порт, префикс пути,
// параметры запроса, учётные данные — заведение вправе задать, и площадка их сохраняет:
// это его вход, а не её. Раньше такие адреса отвергались, потому что путь контракта
// приписывался к адресу строкой и на `https://host?` давал `https://host?/api/...`;
// теперь склейка идёт разбором (см. Partner.Endpoints), и запрещать заведению настраивать
// свой вход больше не за что.
//
// Цена этой широты названа прямо: адрес с учётными данными (`https://user:pass@host`)
// ложится в `base_url` открытым текстом и уедет в лог медленного запроса. Хранить его
// хешем нельзя — по нему нужно ходить; кому такой адрес нужен, тот берёт эту цену на себя
// осознанно (см. раздел упрощений).
func integrationAddress(baseURL string) (string, error) {
	baseURL = strings.TrimSpace(baseURL)

	if utf8.RuneCountInString(baseURL) > BaseURLMaxLength {
		return "", InvalidPartnerError{
			Reason: fmt.Sprintf("адрес интеграции: не длиннее %d знаков", BaseURLMaxLength),
		}
	}

	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", InvalidPartnerError{Reason: "адрес интеграции: " + err.Error()}
	}

	switch {
	case parsed.Scheme != "http" && parsed.Scheme != "https":
		return "", InvalidPartnerError{
			Reason: "адрес интеграции: абсолютный адрес по схеме http или https",
		}
	case parsed.Host == "":
		return "", InvalidPartnerError{Reason: "адрес интеграции: не указан хост"}
	}

	// Завершающий слэш снимается с пути, а не со всей строки: у адреса с параметрами
	// запроса слэш стоит не в конце, и обрезка строки его бы не сняла. Снимается он один
	// раз здесь, а не при каждой склейке, — иначе один и тот же адрес, записанный со
	// слэшем и без, давал бы два разных пути к одному заведению.
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawPath = ""

	return parsed.String(), nil
}

// ContractOperation — операция партнёрского контракта, которую площадка вызывает у
// заведения: то, за чем она ходит в коробку. Операции самой площадки (`catalog/refresh`,
// callback статуса) сюда не входят — по ним ходит заведение, и адрес у них площадкин.
type ContractOperation struct {
	// Name — `operationId` операции в контракте: по нему заведение находит её в
	// спецификации.
	Name string
	// Method — метод запроса.
	Method string
	// Path — путь контракта, который приписывается к адресу интеграции. Сегмент в
	// фигурных скобках — имя параметра, а не его значение.
	Path string
}

// contractOperations — все обращения площадки к коробке заведения. Перечень повторяет
// операции контракта с тегом `restaurant`; расходиться с ним ему не даёт сторожевой тест
// по собранной спецификации (TestContractOperationsMatchTheContract).
var contractOperations = []ContractOperation{
	{Name: "listMenu", Method: "GET", Path: contractPrefix + "/menu"},
	{Name: "acceptOrder", Method: "POST", Path: contractPrefix + "/orders"},
	{Name: "cancelAcceptedOrder", Method: "POST", Path: contractPrefix + "/orders/{orderId}/cancel"},
}

// ContractOperations — обращения площадки к коробке заведения, все до одного.
//
// Список отдаётся копией: перечень объявлен здесь, и звонящий, дописавший в него свою
// строку, менял бы контракт мимо спецификации.
func ContractOperations() []ContractOperation {
	return slices.Clone(contractOperations)
}

// Endpoint — адрес одной операции контракта у конкретного заведения: то, куда площадка
// пошлёт запрос.
type Endpoint struct {
	// Operation — `operationId` операции в контракте.
	Operation string
	// Method — метод запроса.
	Method string
	// URL — полный адрес операции: адрес интеграции с приписанным путём контракта.
	URL string
}

// Endpoints строит адреса всех операций контракта для этого заведения.
//
// Это и есть склейка, ради которой адрес интеграции хранится: путь контракта
// приписывается разбором адреса, а не сложением строк. Разбор снимает целый класс
// поломок сложения — двойной слэш, путь, уехавший в строку запроса, потерянный префикс, —
// и позволяет заведению задавать свой вход как ему удобно: с портом, с префиксом пути,
// с параметром запроса.
//
// Ошибку метод возвращает не «на всякий случай»: результат склейки проверяется, и адрес,
// у которого после приписывания пути уехали схема, хост или сам путь, заведению не
// годится. Площадка узнаёт об этом при подключении (NewPartner зовёт этот метод), а не
// в момент первого заказа.
func (p Partner) Endpoints() ([]Endpoint, error) {
	endpoints := make([]Endpoint, 0, len(contractOperations))

	for _, operation := range contractOperations {
		address, err := p.endpoint(operation.Path)
		if err != nil {
			return nil, err
		}

		endpoints = append(endpoints, Endpoint{
			Operation: operation.Name,
			Method:    operation.Method,
			URL:       address,
		})
	}

	return endpoints, nil
}

// endpoint приписывает к адресу интеграции один путь контракта.
func (p Partner) endpoint(contractPath string) (string, error) {
	base, err := url.Parse(p.BaseURL)
	if err != nil {
		return "", InvalidPartnerError{Reason: "адрес интеграции: " + err.Error()}
	}

	joined := base.JoinPath(contractPath)

	// У адреса без пути (`https://baton.example.org`) JoinPath отдаёт путь относительным:
	// он считает относительным сам адрес, потому что его путь пуст. На собранной строке
	// это не видно — `String` дописывает слэш перед путём, — но сверять склейку нужно с
	// тем же абсолютным путём, какой уедет в запрос.
	joinedPath := joined.Path
	if !strings.HasPrefix(joinedPath, "/") {
		joinedPath = "/" + joinedPath
	}

	switch {
	case joined.Scheme != base.Scheme || joined.Host != base.Host:
		return "", InvalidPartnerError{
			Reason: fmt.Sprintf(
				"адрес интеграции: после приписывания пути %s запрос ушёл бы не на тот хост",
				contractPath),
		}
	case !strings.HasSuffix(joinedPath, contractPath):
		return "", InvalidPartnerError{
			Reason: fmt.Sprintf(
				"адрес интеграции: путь %s после приписывания превратился в %s — заведение по нему не ответит",
				contractPath, joinedPath),
		}
	}

	// Фигурные скобки возвращаются на место: `String` экранирует их как обычные знаки
	// пути, а здесь в них имя параметра — то, что заведение должно узнать в своём
	// контракте, а не `%7BorderId%7D`.
	return templateBraces.Replace(joined.String()), nil
}

// templateBraces возвращает в собранный адрес фигурные скобки имени параметра.
var templateBraces = strings.NewReplacer("%7B", "{", "%7D", "}")

// NewPartnerKey выдаёт заведению партнёрский ключ.
//
// Ключ придумывает площадка, а не заведение: это её секрет, по которому она узнаёт
// звонящего, и «удобный» ключ, придуманный человеком, подбирается перебором. 26 знаков
// base32 из криптографического источника — это около 130 бит случайности.
func NewPartnerKey() string {
	return partnerKeyPrefix + rand.Text()
}

// HashPartnerKey считает хеш ключа — то единственное, что площадка о ключе хранит.
// Открытый ключ есть только у заведения: утёкшая копия базы не даёт возможности звонить
// от имени заведения, а потерянный ключ не восстанавливается, а перевыпускается.
//
// SHA-256, а не bcrypt или argon2, и без соли — сознательно. Заведение опознаётся поиском
// по хешу, то есть хеш обязан быть детерминированным: с солью на строку пришлось бы
// перебирать всю таблицу и считать медленный хеш на каждой строке. Медленный хеш защищает
// пароли, которые придумал человек; здесь секрет — случайные 130 бит, и перебирать его
// бессмысленно с любой скоростью хеширования.
func HashPartnerKey(key string) string {
	sum := sha256.Sum256([]byte(key))

	return hex.EncodeToString(sum[:])
}

// InvalidPartnerError — заведение описали так, что подключить его нельзя. Причина —
// текстом: её читает администратор площадки, который в состоянии исправить запрос,
// поэтому машиночитаемой ветки поведения у неё нет.
type InvalidPartnerError struct {
	// Reason — что именно не так, словами для администратора.
	Reason string
}

// Error возвращает причину отказа.
func (e InvalidPartnerError) Error() string {
	return e.Reason
}
