package application

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/IvanSergunin001/Test157/internal/platform/domain"
)

const (
	// catalogPageSize — сколько позиций площадка просит у заведения за один раз. Столько
	// же объявлено потолком страницы в партнёрском контракте: меню отдаётся страницами,
	// чтобы большой каталог не приходилось передавать одним ответом.
	catalogPageSize = 500

	// maxCatalogItems — потолок на число позиций в одной выгрузке. Он не про размер
	// разумного меню — тысячи позиций у заведения не бывает, — а про то, что выгрузка
	// приходит от сторонней системы: без потолка заведение, отдающее страницы бесконечно,
	// уводит площадку в бесконечный цикл, а не просто перегружает её каталог.
	//
	// Упереться в потолок — повод для разговора с заведением, а не для тихого обрезания
	// меню: превышение отменяет синхронизацию целиком.
	maxCatalogItems = 5_000
)

// MenuBatch — страница меню, приехавшая от заведения.
type MenuBatch struct {
	// Items — позиции этой страницы, уже проверенные предметной областью.
	Items []domain.CatalogItem
	// Total — сколько позиций в меню заведения всего: по нему площадка понимает, что
	// дочитала выгрузку до конца, а не остановилась на середине.
	Total int64
}

// MenuSource — меню заведения по ту сторону партнёрского контракта.
//
// Порт описан страницами, а не «отдай всё меню»: страничность — часть контракта, и
// прятать её за портом значило бы делать вид, что меню приезжает одним куском, тогда как
// именно на границах страниц синхронизация и обрывается.
type MenuSource interface {
	// Page забирает страницу меню заведения, живущего по адресу baseURL.
	//
	// Реализация обязана ограничивать себя сама: таймаут запроса, потолок размера ответа
	// и потоковый разбор — это защита от стороннего заведения, и она не может зависеть от
	// того, вспомнит ли о ней сценарий.
	Page(ctx context.Context, baseURL string, page domain.Page) (MenuBatch, error)
}

// CatalogWriter — применение выгрузки к каталогу площадки.
type CatalogWriter interface {
	// Replace применяет выгрузку заведения целиком: обновляет позиции по паре «заведение +
	// внешний идентификатор», деактивирует те, которых в выгрузке не оказалось, и
	// отмечает время синхронизации.
	//
	// Всё или ничего: применить половину выгрузки нельзя, иначе на витрине оказалось бы
	// меню, которого нет ни у заведения, ни у площадки до синхронизации.
	Replace(ctx context.Context, restaurant uuid.UUID, items []domain.CatalogItem, syncedAt time.Time) error
}

// CatalogSync — синхронизация каталога: площадка сама забирает меню у заведения и
// применяет его к витрине.
//
// Модель pull, а не push от заведения: тогда площадка распоряжается нагрузкой на себя,
// умеет повторить неудачную выгрузку и не зависит от того, реализовало ли заведение
// доставку изменений. Цена — задержка между правкой в заведении и витриной, и ровно её
// снимает внеочередное обновление: заведение просит забрать меню сейчас.
type CatalogSync struct {
	partners Partners
	source   MenuSource
	catalog  CatalogWriter
	log      *slog.Logger
}

// NewCatalogSync собирает синхронизацию над хранилищем заведений, источником меню и
// каталогом площадки.
func NewCatalogSync(partners Partners, source MenuSource, catalog CatalogWriter, log *slog.Logger) *CatalogSync {
	return &CatalogSync{partners: partners, source: source, catalog: catalog, log: log}
}

// Refresh забирает меню заведения и применяет его к каталогу площадки.
//
// Сначала выгрузка читается целиком и только потом применяется. Держать транзакцию
// открытой, пока площадка ходит по страницам в чужую систему, нельзя: подключение к базе
// оказалось бы занято на время сетевого обмена с тем, кто не обязан отвечать быстро.
// Отсюда же требование потолка на число позиций — прочитанная выгрузка живёт в памяти.
//
// Любая ошибка по дороге отменяет синхронизацию целиком: каталога она не касается вовсе,
// и заведение остаётся на витрине с прежним меню. Частично применённое меню хуже
// устаревшего — устаревшее хотя бы непротиворечиво.
func (s *CatalogSync) Refresh(ctx context.Context, partner domain.Partner) error {
	items, err := s.pull(ctx, partner.BaseURL)
	if err != nil {
		return fmt.Errorf("забрать меню заведения %s: %w", partner.ID, err)
	}

	if err := s.catalog.Replace(ctx, partner.ID, items, time.Now()); err != nil {
		return fmt.Errorf("применить меню заведения %s: %w", partner.ID, err)
	}

	return nil
}

// RefreshAll обходит все работающие заведения площадки.
//
// Неудача одного заведения не прекращает обход: заведения независимы, и сломанная
// выгрузка одного не повод оставить остальные с позавчерашним меню. Наружу такая неудача
// не поднимается — поднимать её некуда, обход никто не ждёт, — но в лог она попадает
// обязательно: молчаливо не обновляющийся каталог обнаруживается по жалобе покупателя.
func (s *CatalogSync) RefreshAll(ctx context.Context) error {
	partners, err := s.partners.ListActive(ctx)
	if err != nil {
		return fmt.Errorf("перечислить работающие заведения: %w", err)
	}

	for _, partner := range partners {
		if err := s.Refresh(ctx, partner); err != nil {
			s.log.ErrorContext(ctx, "catalog sync failed",
				slog.String("restaurant_id", partner.ID.String()), slog.Any("error", err))
		}
	}

	return nil
}

// RunPeriodically забирает меню у всех работающих заведений каждые every, пока жив ctx.
//
// Это вся фоновая синхронизация целиком: тонкая обёртка над Refresh, который доступен
// снаружи партнёрской ручкой внеочередного обновления. Своего шва она не заводит — и
// проверять её ожиданием тика незачем: тик не добавляет к сценарию ни одного правила,
// а тест, который спит в надежде на срабатывание таймера, проверяет планировщик Go.
func (s *CatalogSync) RunPeriodically(ctx context.Context, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.RefreshAll(ctx); err != nil {
				s.log.ErrorContext(ctx, "scheduled catalog sync failed", slog.Any("error", err))
			}
		}
	}
}

// pull читает меню заведения страницу за страницей.
//
// Обход не доверяет заведению ни в чём: ни в том, что оно когда-нибудь отдаст пустую
// страницу, ни в том, что общее число позиций правда, ни в том, что внешние
// идентификаторы не повторятся. Каждое из этих допущений, оказавшись ложным, стоило бы
// площадке либо бесконечного цикла, либо каталога, в котором позиция значит не то, что
// значила вчера.
func (s *CatalogSync) pull(ctx context.Context, baseURL string) ([]domain.CatalogItem, error) {
	var (
		items = make([]domain.CatalogItem, 0, catalogPageSize)
		seen  = make(map[string]struct{}, catalogPageSize)
	)

	for {
		batch, err := s.source.Page(ctx, baseURL, domain.Page{Limit: catalogPageSize, Offset: len(items)})
		if err != nil {
			return nil, err
		}

		// Пустая страница — конец выгрузки, чем бы ни было объявленное общее число:
		// заведение, у которого позиций «больше», но отдавать их нечем, иначе крутило бы
		// площадку по одному и тому же смещению вечно.
		if len(batch.Items) == 0 {
			return items, nil
		}

		for _, item := range batch.Items {
			if _, repeated := seen[item.ExternalID]; repeated {
				return nil, domain.BrokenMenuError{
					Reason: fmt.Sprintf("внешний идентификатор %q встречается в выгрузке дважды", item.ExternalID),
				}
			}
			seen[item.ExternalID] = struct{}{}
			items = append(items, item)
		}

		if len(items) > maxCatalogItems {
			return nil, domain.BrokenMenuError{
				Reason: fmt.Sprintf("в выгрузке больше %d позиций", maxCatalogItems),
			}
		}
		if int64(len(items)) >= batch.Total {
			return items, nil
		}
	}
}
